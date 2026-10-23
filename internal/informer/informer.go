package informer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/tools/cache"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/summary"
)

const defaultResync = 0 // no periodic resync — rely on watch events

// Manager sets up and runs dynamic informers from profile specs.
type Manager struct {
	client      dynamic.Interface
	pitcher     pitcher.K8sPitcher
	specs       []profile.InformerSpec
	clusterName string
}

// New creates an informer Manager.
func New(client dynamic.Interface, p pitcher.K8sPitcher, specs []profile.InformerSpec, clusterName string) *Manager {
	return &Manager{
		client:      client,
		pitcher:     p,
		specs:       specs,
		clusterName: clusterName,
	}
}

// Start sets up dynamic informers for each spec and blocks until ctx is cancelled.
func (m *Manager) Start(ctx context.Context) {
	for _, spec := range m.specs {
		gvr := schema.GroupVersionResource{
			Group:    spec.Group,
			Version:  spec.Version,
			Resource: spec.Resource,
		}

		ns := profile.ResolveNamespace(spec.Namespace)

		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
			m.client,
			defaultResync,
			ns,
			nil,
		)

		informer := factory.ForResource(gvr).Informer()

		if _, err := informer.AddEventHandler(m.handlerFor(spec, gvr)); err != nil {
			slog.Error("failed to add event handler",
				"resource", gvr.Resource,
				"error", err,
			)
			continue
		}

		slog.Info("informer started",
			"group", spec.Group,
			"version", spec.Version,
			"resource", spec.Resource,
			"namespace", spec.Namespace,
			"events", spec.Events,
			"updates", spec.Updates,
			"pitchInitialList", spec.PitchInitialList,
			"types", spec.Types,
			"reasons", spec.Reasons,
			"excludeReasons", spec.ExcludeReasons,
		)

		go informer.Run(ctx.Done())
	}

	// Block until context is cancelled
	<-ctx.Done()
	slog.Info("informer manager stopped")
}

// handlerFor builds the event handler of one informer. Adds from the initial
// list are dropped unless pitchInitialList is set, so a restart does not
// re-pitch the inventory; updates pass the spec's update filter. Kubernetes
// Events pass the spec's Event filter first.
func (m *Manager) handlerFor(spec profile.InformerSpec, gvr schema.GroupVersionResource) cache.ResourceEventHandler {
	wantEvents := toSet(spec.Events)
	handler := cache.ResourceEventHandlerDetailedFuncs{}
	if wantEvents["add"] {
		handler.AddFunc = func(obj any, isInInitialList bool) {
			if isInInitialList && !spec.PitchInitialList {
				return
			}
			if u, ok := obj.(*unstructured.Unstructured); ok && eventPasses(spec, u) {
				m.pitch(spec, gvr, u, pitcher.K8sEvent{EventType: "add"})
			}
		}
	}
	if wantEvents["update"] {
		handler.UpdateFunc = func(oldObj, newObj any) {
			oldU, ok1 := oldObj.(*unstructured.Unstructured)
			newU, ok2 := newObj.(*unstructured.Unstructured)
			if !ok1 || !ok2 || !eventPasses(spec, newU) {
				return
			}
			if ev, ok := updateEvent(spec.Updates, oldU, newU); ok {
				m.pitch(spec, gvr, newU, ev)
			}
		}
	}
	if wantEvents["delete"] {
		handler.DeleteFunc = func(obj any) {
			// Handle DeletedFinalStateUnknown (tombstone)
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			if u, ok := obj.(*unstructured.Unstructured); ok && eventPasses(spec, u) {
				m.pitch(spec, gvr, u, pitcher.K8sEvent{EventType: "delete", Severity: spec.DeleteSeverity})
			}
		}
	}
	return handler
}

// eventPasses applies the spec's Event filter (#87): the Event's type must be
// in types, its reason in reasons and not in excludeReasons; an unset list
// passes everything. Without a filter every object passes.
func eventPasses(spec profile.InformerSpec, u *unstructured.Unstructured) bool {
	if !spec.HasEventFilter() {
		return true
	}
	typ, _ := u.Object["type"].(string)
	reason, _ := u.Object["reason"].(string)
	if len(spec.Types) > 0 && !slices.Contains(spec.Types, typ) {
		return false
	}
	if len(spec.Reasons) > 0 && !slices.Contains(spec.Reasons, reason) {
		return false
	}
	return !slices.Contains(spec.ExcludeReasons, reason)
}

// watchedConditions are compared between old and new object.
var watchedConditions = []string{"Ready", "Synced"}

// updateEvent decides whether an update is pitched and how. all pitches
// every update as before. generation pitches a metadata.generation change.
// conditions pitches a generation change or a flip of Ready or Synced, the
// latter titled e.g. "VirtualMachine/vm1 Ready=False (ReconcileError)" with
// a severity from the transition: Ready to False is error, Synced to False or
// either to Unknown warning, back to True success. A condition that appears
// for the first time is not a flip.
func updateEvent(mode string, oldU, newU *unstructured.Unstructured) (pitcher.K8sEvent, bool) {
	ev := pitcher.K8sEvent{EventType: "update"}
	if mode == profile.UpdatesAll {
		return ev, true
	}
	if mode == profile.UpdatesConditions {
		var flips []string
		rank := 0
		for _, t := range watchedConditions {
			before, ok := condition(oldU, t)
			after, ok2 := condition(newU, t)
			if !ok || !ok2 || before.status == after.status {
				continue
			}
			flip := t + "=" + after.status
			if after.reason != "" {
				flip += " (" + after.reason + ")"
			}
			flips = append(flips, flip)
			r := 1 // success
			switch {
			case after.status == "False" && t == "Ready":
				r = 3
			case after.status != "True":
				r = 2
			}
			rank = max(rank, r)
		}
		if len(flips) > 0 {
			ev.Title = fmt.Sprintf("%s/%s %s", newU.GetKind(), newU.GetName(), strings.Join(flips, ", "))
			ev.Severity = []string{"", "success", "warning", "error"}[rank]
			return ev, true
		}
	}
	if oldU.GetGeneration() != newU.GetGeneration() {
		ev.Severity = "info"
		return ev, true
	}
	return ev, false
}

type cond struct{ status, reason string }

func condition(u *unstructured.Unstructured, typ string) (cond, bool) {
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok || m["type"] != typ {
			continue
		}
		st, _ := m["status"].(string)
		reason, _ := m["reason"].(string)
		return cond{status: st, reason: reason}, true
	}
	return cond{}, false
}

// pitch completes ev from the object and pitches it. The body is the
// summary, plus the whole object as JSON with includeObject.
func (m *Manager) pitch(spec profile.InformerSpec, gvr schema.GroupVersionResource, u *unstructured.Unstructured, ev pitcher.K8sEvent) {
	eventType := ev.EventType
	objMap, err := toMap(u.Object)
	if err != nil {
		slog.Error("informer marshal failed",
			"resource", gvr.Resource,
			"name", u.GetName(),
			"error", err,
		)
		return
	}

	kind := u.GetKind()

	text := summary.SummarizeText(kind, objMap)
	if spec.IncludeObject {
		if data, err := json.MarshalIndent(objMap, "", "  "); err == nil {
			text += "\n" + string(data)
		}
	}

	ev.Kind = kind
	ev.Namespace = u.GetNamespace()
	ev.Name = u.GetName()
	ev.Object = objMap
	ev.Summary = text
	ev.Timestamp = time.Now().UTC().Format(time.RFC3339)
	ev.Cluster = m.clusterName
	ev.Key = m.cooldownKey(ev)

	if err := m.pitcher.Pitch(ev); err != nil {
		slog.Error("informer pitch failed",
			"resource", gvr.Resource,
			"name", u.GetName(),
			"eventType", eventType,
			"error", err,
		)
	}
}

// cooldownKey groups repeats of one problem: an added or updated Kubernetes
// Event by involved object and reason, a condition flip by object and flip.
// Everything else gets no key and is never deduplicated.
func (m *Manager) cooldownKey(ev pitcher.K8sEvent) string {
	if ev.Kind == "Event" && (ev.EventType == "add" || ev.EventType == "update") {
		reason, kind, ns, name := pitcher.EventSubject(ev.Object)
		if reason != "" && name != "" {
			return strings.Join([]string{m.clusterName, kind, ns, name, reason}, "|")
		}
		return ""
	}
	if ev.EventType == "update" && ev.Title != "" {
		return strings.Join([]string{m.clusterName, ev.Kind, ev.Namespace, ev.Name, ev.Title}, "|")
	}
	return ""
}

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

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
