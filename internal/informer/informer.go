package informer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

		ns := spec.Namespace
		if ns == "*" || ns == "" {
			ns = metav1.NamespaceAll
		}

		factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(
			m.client,
			defaultResync,
			ns,
			nil,
		)

		informer := factory.ForResource(gvr).Informer()

		wantEvents := toSet(spec.Events)

		handler := cache.ResourceEventHandlerFuncs{}
		if wantEvents["add"] {
			handler.AddFunc = m.makeAddHandler(gvr)
		}
		if wantEvents["update"] {
			handler.UpdateFunc = m.makeUpdateHandler(gvr)
		}
		if wantEvents["delete"] {
			handler.DeleteFunc = m.makeDeleteHandler(gvr)
		}

		if _, err := informer.AddEventHandler(handler); err != nil {
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
		)

		go informer.Run(ctx.Done())
	}

	// Block until context is cancelled
	<-ctx.Done()
	slog.Info("informer manager stopped")
}

func (m *Manager) makeAddHandler(gvr schema.GroupVersionResource) func(obj any) {
	return func(obj any) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		m.pitchEvent(u, "add", gvr)
	}
}

func (m *Manager) makeUpdateHandler(gvr schema.GroupVersionResource) func(oldObj, newObj any) {
	return func(_, newObj any) {
		u, ok := newObj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		m.pitchEvent(u, "update", gvr)
	}
}

func (m *Manager) makeDeleteHandler(gvr schema.GroupVersionResource) func(obj any) {
	return func(obj any) {
		// Handle DeletedFinalStateUnknown (tombstone)
		if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
			obj = d.Obj
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		m.pitchEvent(u, "delete", gvr)
	}
}

func (m *Manager) pitchEvent(u *unstructured.Unstructured, eventType string, gvr schema.GroupVersionResource) {
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

	event := pitcher.K8sEvent{
		Kind:      kind,
		EventType: eventType,
		Namespace: u.GetNamespace(),
		Name:      u.GetName(),
		Object:    objMap,
		Summary:   summary.SummarizeText(kind, objMap),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Cluster:   m.clusterName,
	}

	if err := m.pitcher.Pitch(event); err != nil {
		slog.Error("informer pitch failed",
			"resource", gvr.Resource,
			"name", u.GetName(),
			"eventType", eventType,
			"error", err,
		)
	}
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
