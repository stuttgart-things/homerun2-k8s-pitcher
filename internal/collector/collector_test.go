package collector

import (
	"context"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// mockPitcher records pitched events.
type mockPitcher struct {
	mu     sync.Mutex
	events []pitcher.K8sEvent
}

func (m *mockPitcher) Pitch(event pitcher.K8sEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockPitcher) HealthCheck(_ context.Context) error {
	return nil
}

func (m *mockPitcher) Events() []pitcher.K8sEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]pitcher.K8sEvent, len(m.events))
	copy(cp, m.events)
	return cp
}

func TestCollectNodes(t *testing.T) {
	node := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]any{
				"name": "node-1",
			},
		},
	}

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "", Version: "v1", Resource: "nodes"}: "NodeList",
		},
		node,
	)

	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{
		{Kind: "Node", Interval: 100 * time.Millisecond},
	}

	c := New(client, mp, specs, "test-cluster")

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	c.Start(ctx)

	events := mp.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one event, got none")
	}

	first := events[0]
	if first.Kind != "Node" {
		t.Errorf("kind = %q, want Node", first.Kind)
	}
	if first.EventType != "snapshot" {
		t.Errorf("eventType = %q, want snapshot", first.EventType)
	}
	if first.Name != "node-1" {
		t.Errorf("name = %q, want node-1", first.Name)
	}
	if first.Cluster != "test-cluster" {
		t.Errorf("cluster = %q, want test-cluster", first.Cluster)
	}
}

func TestCollectPods(t *testing.T) {
	pod1 := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      "nginx",
				"namespace": "default",
			},
		},
	}
	pod2 := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      "redis",
				"namespace": "homerun2",
			},
		},
	}

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "", Version: "v1", Resource: "pods"}: "PodList",
		},
		pod1, pod2,
	)

	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{
		{Kind: "Pod", Namespace: "*", Interval: 100 * time.Millisecond},
	}

	c := New(client, mp, specs, "test-cluster")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	c.Start(ctx)

	events := mp.Events()
	if len(events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(events))
	}

	names := map[string]bool{}
	for _, e := range events {
		names[e.Name] = true
		if e.Kind != "Pod" {
			t.Errorf("kind = %q, want Pod", e.Kind)
		}
	}
	if !names["nginx"] || !names["redis"] {
		t.Errorf("expected both nginx and redis pods, got %v", names)
	}
}

func TestCollectMultipleRuns(t *testing.T) {
	node := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Node",
			"metadata": map[string]any{
				"name": "node-1",
			},
		},
	}

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "", Version: "v1", Resource: "nodes"}: "NodeList",
		},
		node,
	)

	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{
		{Kind: "Node", Interval: 50 * time.Millisecond},
	}

	c := New(client, mp, specs, "test")

	// Run for ~160ms with 50ms interval: should get initial + ~2-3 ticks
	ctx, cancel := context.WithTimeout(context.Background(), 160*time.Millisecond)
	defer cancel()

	c.Start(ctx)

	events := mp.Events()
	if len(events) < 2 {
		t.Errorf("expected at least 2 collection runs, got %d events", len(events))
	}
}

func TestIsSupported(t *testing.T) {
	if !IsSupported("Node") {
		t.Error("Node should be supported")
	}
	if !IsSupported("Pod") {
		t.Error("Pod should be supported")
	}
	if !IsSupported("Event") {
		t.Error("Event should be supported")
	}
	if IsSupported("Deployment") {
		t.Error("Deployment should not be supported (use informers)")
	}
}

func TestCollectUnknownKind(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, nil)

	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{
		{Kind: "Unknown", Interval: 50 * time.Millisecond},
	}

	c := New(client, mp, specs, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	c.Start(ctx)

	// Unknown kind should log error and return, no events pitched
	if len(mp.Events()) != 0 {
		t.Errorf("expected 0 events for unknown kind, got %d", len(mp.Events()))
	}
}

func TestNewCollector(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, nil)
	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{{Kind: "Node", Interval: time.Second}}

	c := New(client, mp, specs, "my-cluster")

	if c.clusterName != "my-cluster" {
		t.Errorf("clusterName = %q, want %q", c.clusterName, "my-cluster")
	}
	if len(c.specs) != 1 {
		t.Errorf("specs count = %d, want 1", len(c.specs))
	}
}

// Verify that unstructured objects get the correct metadata extracted.
func TestCollectPreservesMetadata(t *testing.T) {
	event := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Event",
			"metadata": map[string]any{
				"name":              "nginx.12345",
				"namespace":         "kube-system",
				"creationTimestamp": metav1.Now().Format(time.RFC3339),
			},
			"reason":  "Pulled",
			"message": "Successfully pulled image",
		},
	}

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "", Version: "v1", Resource: "events"}: "EventList",
		},
		event,
	)

	mp := &mockPitcher{}
	specs := []profile.CollectorSpec{
		{Kind: "Event", Namespace: "*", Interval: 100 * time.Millisecond},
	}

	c := New(client, mp, specs, "test")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	c.Start(ctx)

	events := mp.Events()
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	e := events[0]
	if e.Namespace != "kube-system" {
		t.Errorf("namespace = %q, want kube-system", e.Namespace)
	}
	if e.Name != "nginx.12345" {
		t.Errorf("name = %q, want nginx.12345", e.Name)
	}
}
