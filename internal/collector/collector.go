package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

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
}

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

func (c *Collector) run(ctx context.Context, spec profile.CollectorSpec) {
	gvr, ok := gvrForKind[spec.Kind]
	if !ok {
		slog.Error("unknown collector kind", "kind", spec.Kind)
		return
	}

	slog.Info("collector started",
		"kind", spec.Kind,
		"namespace", spec.Namespace,
		"interval", spec.Interval,
	)

	ticker := time.NewTicker(spec.Interval)
	defer ticker.Stop()

	// Run once immediately, then on interval
	c.collect(ctx, spec, gvr)

	for {
		select {
		case <-ctx.Done():
			slog.Info("collector stopped", "kind", spec.Kind)
			return
		case <-ticker.C:
			c.collect(ctx, spec, gvr)
		}
	}
}

func (c *Collector) collect(ctx context.Context, spec profile.CollectorSpec, gvr schema.GroupVersionResource) {
	ns := spec.Namespace
	if ns == "*" {
		ns = ""
	}

	list, err := c.client.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
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
		"namespace", spec.Namespace,
		"count", len(list.Items),
	)

	for _, item := range list.Items {
		obj, err := toMap(item.Object)
		if err != nil {
			slog.Error("collector marshal failed", "kind", spec.Kind, "name", item.GetName(), "error", err)
			continue
		}

		event := pitcher.K8sEvent{
			Kind:      spec.Kind,
			EventType: "snapshot",
			Namespace: item.GetNamespace(),
			Name:      item.GetName(),
			Object:    obj,
			Summary:   summary.SummarizeText(spec.Kind, obj),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Cluster:   c.clusterName,
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
