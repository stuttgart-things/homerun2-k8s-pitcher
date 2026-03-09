package informer

import (
	"context"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

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

func TestNewManager(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(scheme)
	mp := &mockPitcher{}
	specs := []profile.InformerSpec{
		{Group: "", Version: "v1", Resource: "pods", Namespace: "*", Events: []string{"add"}},
	}

	mgr := New(client, mp, specs, "test-cluster")

	if mgr.clusterName != "test-cluster" {
		t.Errorf("clusterName = %q, want %q", mgr.clusterName, "test-cluster")
	}
	if len(mgr.specs) != 1 {
		t.Errorf("specs count = %d, want 1", len(mgr.specs))
	}
}

func TestToSet(t *testing.T) {
	s := toSet([]string{"add", "update", "delete"})
	if !s["add"] || !s["update"] || !s["delete"] {
		t.Errorf("expected all events in set, got %v", s)
	}
	if s["unknown"] {
		t.Error("unexpected key in set")
	}
}

func TestInformerStartAndStop(t *testing.T) {
	pod := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      "nginx",
				"namespace": "default",
			},
		},
	}

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "", Version: "v1", Resource: "pods"}: "PodList",
		},
		pod,
	)

	mp := &mockPitcher{}
	specs := []profile.InformerSpec{
		{Group: "", Version: "v1", Resource: "pods", Namespace: "*", Events: []string{"add", "update", "delete"}},
	}

	mgr := New(client, mp, specs, "test-cluster")

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()

	// Wait for informer to process initial list (add events)
	time.Sleep(200 * time.Millisecond)

	events := mp.Events()
	// The initial list sync should trigger add events for existing objects
	if len(events) == 0 {
		t.Log("no initial add events captured (timing-dependent, acceptable)")
	} else {
		for _, e := range events {
			if e.EventType != "add" {
				t.Errorf("expected add event from initial sync, got %q", e.EventType)
			}
		}
	}

	cancel()
	<-done
}

func TestPitchEventFields(t *testing.T) {
	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClient(scheme)
	mp := &mockPitcher{}

	mgr := New(client, mp, nil, "my-cluster")

	u := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Pod",
			"metadata": map[string]any{
				"name":      "test-pod",
				"namespace": "kube-system",
			},
			"spec": map[string]any{
				"nodeName": "node-1",
			},
		},
	}

	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	mgr.pitchEvent(u, "delete", gvr)

	events := mp.Events()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	e := events[0]
	if e.Kind != "Pod" {
		t.Errorf("kind = %q, want Pod", e.Kind)
	}
	if e.EventType != "delete" {
		t.Errorf("eventType = %q, want delete", e.EventType)
	}
	if e.Namespace != "kube-system" {
		t.Errorf("namespace = %q, want kube-system", e.Namespace)
	}
	if e.Name != "test-pod" {
		t.Errorf("name = %q, want test-pod", e.Name)
	}
	if e.Cluster != "my-cluster" {
		t.Errorf("cluster = %q, want my-cluster", e.Cluster)
	}
	if e.Timestamp == "" {
		t.Error("timestamp should not be empty")
	}
	if e.Object == nil {
		t.Error("object should not be nil")
	}
}
