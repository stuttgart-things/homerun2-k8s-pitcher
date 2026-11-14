package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/inventory"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/summary"
)

// gvrForKind maps collector kind names to their GVR.
var gvrForKind = map[string]schema.GroupVersionResource{
	"Node":  {Group: "", Version: "v1", Resource: "nodes"},
	"Pod":   {Group: "", Version: "v1", Resource: "pods"},
	"Event": {Group: "", Version: "v1", Resource: "events"},
}

// Collector periodically gathers resource snapshots and pitches them.
type Collector struct {
	client      dynamic.Interface
	pitcher     pitcher.K8sPitcher
	specs       []profile.CollectorSpec
	clusterName string
	// ServerVersion returns the cluster's gitVersion for the inventory
	// (#108); nil leaves it out.
	ServerVersion func() (string, error)
}

// KindInventory is the built-in collector kind of the inventory snapshot.
const KindInventory = "Inventory"

// New creates a Collector from profile specs.
func New(client dynamic.Interface, p pitcher.K8sPitcher, specs []profile.CollectorSpec, clusterName string) *Collector {
	return &Collector{
		client:      client,
		pitcher:     p,
		specs:       specs,
		clusterName: clusterName,
	}
}

// Start launches a goroutine per collector spec. Blocks until ctx is cancelled.
func (c *Collector) Start(ctx context.Context) {
	var wg sync.WaitGroup

	for _, spec := range c.specs {
		wg.Add(1)
		go func(s profile.CollectorSpec) {
			defer wg.Done()
			c.run(ctx, s)
		}(spec)
	}

	wg.Wait()
}

// gvrFor is the GVR of a spec: group/version/resource if set, else the
// built-in kind.
func gvrFor(spec profile.CollectorSpec) (schema.GroupVersionResource, bool) {
	if spec.Resource != "" {
		return schema.GroupVersionResource{Group: spec.Group, Version: spec.Version, Resource: spec.Resource}, true
	}
	gvr, ok := gvrForKind[spec.Kind]
	return gvr, ok
}

func (c *Collector) run(ctx context.Context, spec profile.CollectorSpec) {
	if spec.Kind == KindInventory {
		c.every(ctx, spec, func() { c.pitchInventory(ctx, spec) })
		return
	}
	gvr, ok := gvrFor(spec)
	if !ok {
		slog.Error("unknown collector kind", "kind", spec.Kind)
		return
	}

	slog.Info("collector started",
		"kind", spec.Kind,
		"resource", gvr.Resource,
		"mode", spec.Mode,
		"namespace", spec.Namespace,
		"interval", spec.Interval,
	)

	c.every(ctx, spec, func() { c.collect(ctx, spec, gvr) })
}

// every runs fn once immediately, then on the spec's interval.
func (c *Collector) every(ctx context.Context, spec profile.CollectorSpec, fn func()) {
	ticker := time.NewTicker(spec.Interval)
	defer ticker.Stop()
	fn()
	for {
		select {
		case <-ctx.Done():
			slog.Info("collector stopped", "kind", spec.Kind)
			return
		case <-ticker.C:
			fn()
		}
	}
}

// pitchInventory pitches the inventory snapshot (#108) as system
// inventory-<cluster> unless the spec names one.
func (c *Collector) pitchInventory(ctx context.Context, spec profile.CollectorSpec) {
	version := ""
	if c.ServerVersion != nil {
		v, err := c.ServerVersion()
		if err != nil {
			slog.Warn("inventory: server version", "error", err)
		}
		version = v
	}
	inv, err := inventory.Gather(ctx, c.client, c.clusterName, version)
	if err != nil {
		slog.Error("inventory failed", "error", err)
		return
	}
	inv.Fit(homerun.MaxPayloadSize)
	payload, err := toMap(inv)
	if err != nil {
		slog.Error("inventory marshal failed", "error", err)
		return
	}
	system := spec.System
	if system == "" {
		system = "inventory-" + c.clusterName
	}
	title, text := inv.Summary()
	event := pitcher.K8sEvent{
		Kind:        KindInventory,
		EventType:   "snapshot",
		Title:       title,
		Severity:    "info",
		Summary:     text,
		Payload:     payload,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Cluster:     c.clusterName,
		Subsystem:   system,
		Unthrottled: !spec.Throttled(),
	}
	if err := c.pitcher.Pitch(event); err != nil {
		slog.Error("inventory pitch failed", "error", err)
		return
	}
	slog.Info("inventory pitched", "system", system, "nodes", len(inv.Nodes), "images", inv.Count, "releases", len(inv.Releases))
}

func (c *Collector) collect(ctx context.Context, spec profile.CollectorSpec, gvr schema.GroupVersionResource) {
	ns := profile.ResolveNamespace(spec.Namespace)

	list, err := c.client.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		// Not served (yet), e.g. a CRD that is not installed: no error in
		// every interval, the next run tries again (#106).
		slog.Debug("collector resource not served", "resource", gvr.Resource)
		return
	}
	if err != nil {
		slog.Error("collector list failed",
			"kind", spec.Kind,
			"namespace", spec.Namespace,
			"error", err,
		)
		return
	}

	slog.Debug("collector snapshot",
		"kind", spec.Kind,
		"resource", gvr.Resource,
		"namespace", spec.Namespace,
		"count", len(list.Items),
	)

	if spec.Mode == profile.CollectorSnapshot {
		c.pitchSnapshot(spec, gvr, list.Items)
		return
	}

	for _, item := range list.Items {
		obj, err := toMap(item.Object)
		if err != nil {
			slog.Error("collector marshal failed", "kind", spec.Kind, "name", item.GetName(), "error", err)
			continue
		}

		event := pitcher.K8sEvent{
			Kind:        spec.Kind,
			EventType:   "snapshot",
			Namespace:   item.GetNamespace(),
			Name:        item.GetName(),
			Object:      obj,
			Summary:     summary.SummarizeText(spec.Kind, obj),
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			Cluster:     c.clusterName,
			Subsystem:   spec.System,
			Unthrottled: !spec.Throttled(),
		}

		if err := c.pitcher.Pitch(event); err != nil {
			slog.Error("collector pitch failed",
				"kind", spec.Kind,
				"name", item.GetName(),
				"error", err,
			)
		}
	}
}

// pitchSnapshot pitches one message for all objects of a run (#103). The
// payload carries every object (stripped like includeObject) with its
// K8sGPT fingerprint; it is trimmed to fit homerun-library's limit.
func (c *Collector) pitchSnapshot(spec profile.CollectorSpec, gvr schema.GroupVersionResource, list []unstructured.Unstructured) {
	objs := make([]map[string]any, 0, len(list))
	items := make([]any, 0, len(list))
	kind := spec.Kind
	for _, item := range list {
		obj, err := toMap(item.Object)
		if err != nil {
			slog.Error("collector marshal failed", "resource", gvr.Resource, "name", item.GetName(), "error", err)
			continue
		}
		if kind == "" {
			kind = item.GetKind()
		}
		objs = append(objs, obj)
		entry := map[string]any{"object": pitcher.PayloadOf(obj)}
		if fp, ok := summary.Fingerprint(c.clusterName, item.GetKind(), obj); ok {
			entry["fingerprint"] = fp
		}
		items = append(items, entry)
	}
	if kind == "" {
		kind = gvr.Resource
	}

	payload := map[string]any{
		"cluster":   c.clusterName,
		"group":     gvr.Group,
		"version":   gvr.Version,
		"resource":  gvr.Resource,
		"namespace": spec.Namespace,
		"count":     len(items),
		"items":     items,
	}
	fitPayload(payload)

	title, text := summary.SnapshotHeader(c.clusterName, gvr.Resource, objs)
	event := pitcher.K8sEvent{
		Kind:        kind,
		EventType:   "snapshot",
		Title:       title,
		Severity:    "info",
		Summary:     text,
		Payload:     payload,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Cluster:     c.clusterName,
		Subsystem:   spec.System,
		Unthrottled: !spec.Throttled(),
	}
	if err := c.pitcher.Pitch(event); err != nil {
		slog.Error("collector snapshot pitch failed", "resource", gvr.Resource, "error", err)
	}
}

// fitPayload keeps a snapshot payload under homerun-library's MaxPayloadSize:
// first every object shrinks to apiVersion, kind, name, namespace and spec,
// then items are cut from the end. truncated says so; count stays the total.
func fitPayload(payload map[string]any) {
	if size(payload) <= homerun.MaxPayloadSize {
		return
	}
	items, _ := payload["items"].([]any)
	for _, it := range items {
		entry, _ := it.(map[string]any)
		obj, _ := entry["object"].(map[string]any)
		meta, _ := obj["metadata"].(map[string]any)
		entry["object"] = map[string]any{
			"apiVersion": obj["apiVersion"],
			"kind":       obj["kind"],
			"metadata":   map[string]any{"name": meta["name"], "namespace": meta["namespace"]},
			"spec":       obj["spec"],
		}
	}
	payload["trimmed"] = true
	for size(payload) > homerun.MaxPayloadSize && len(items) > 0 {
		items = items[:len(items)*9/10]
		payload["items"] = items
		payload["truncated"] = true
	}
	slog.Warn("snapshot payload trimmed to fit", "resource", payload["resource"],
		"count", payload["count"], "kept", len(items))
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// toMap converts an unstructured object to map[string]any via JSON round-trip.
func toMap(obj any) (map[string]any, error) {
	data, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("marshaling object: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("unmarshaling object: %w", err)
	}
	return m, nil
}

// SupportedKinds returns the list of supported collector kinds.
func SupportedKinds() []string {
	kinds := make([]string, 0, len(gvrForKind))
	for k := range gvrForKind {
		kinds = append(kinds, k)
	}
	return kinds
}

// IsSupported checks if a collector kind is supported.
func IsSupported(kind string) bool {
	// Case-insensitive match
	for k := range gvrForKind {
		if strings.EqualFold(k, kind) {
			return true
		}
	}
	return false
}
