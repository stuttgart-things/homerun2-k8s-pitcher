package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

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

func k8sgptResult(name string, errs ...string) *unstructured.Unstructured {
	var es []any
	for _, e := range errs {
		es = append(es, map[string]any{"text": e})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.k8sgpt.ai/v1alpha1",
		"kind":       "Result",
		"metadata": map[string]any{
			"name": name, "namespace": "k8sgpt",
			"managedFields": []any{map[string]any{"manager": "operator"}},
		},
		"spec": map[string]any{"kind": "Pod", "name": "default/" + name, "error": es},
	}}
}

var resultsGVR = schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}

func snapshotCollector(objs ...runtime.Object) (*Collector, *mockPitcher, profile.CollectorSpec) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{resultsGVR: "ResultList"}, objs...)
	mp := &mockPitcher{}
	off := false
	spec := profile.CollectorSpec{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results",
		Namespace: "k8sgpt", Interval: time.Hour, Mode: profile.CollectorSnapshot, Throttle: &off}
	return New(client, mp, []profile.CollectorSpec{spec}, "labda"), mp, spec
}

// One message per run for all Results, each with its fingerprint (#103).
func TestSnapshotResults(t *testing.T) {
	c, mp, spec := snapshotCollector(k8sgptResult("web-0", "back-off"), k8sgptResult("web-1", "oom"))
	gvr, _ := gvrFor(spec)
	c.collect(context.Background(), spec, gvr)

	events := mp.Events()
	if len(events) != 1 {
		t.Fatalf("pitched %d messages, want 1 snapshot", len(events))
	}
	e := events[0]
	if e.EventType != "snapshot" || e.Kind != "Result" || e.Title != "K8sGPT snapshot labda: 2 findings" || !e.Unthrottled || e.Severity != "info" {
		t.Errorf("event %+v", e)
	}
	if e.Payload["count"] != 2 || e.Payload["resource"] != "results" || e.Payload["cluster"] != "labda" {
		t.Errorf("payload header %v", e.Payload)
	}
	items := e.Payload["items"].([]any)
	for _, it := range items {
		entry := it.(map[string]any)
		if fp, _ := entry["fingerprint"].(string); len(fp) != 16 {
			t.Errorf("fingerprint %v", entry["fingerprint"])
		}
		meta := entry["object"].(map[string]any)["metadata"].(map[string]any)
		if _, ok := meta["managedFields"]; ok {
			t.Error("managedFields not stripped")
		}
	}
}

// No Results is a snapshot too: "0 findings" says the cluster is clean.
func TestSnapshotEmpty(t *testing.T) {
	c, mp, spec := snapshotCollector()
	gvr, _ := gvrFor(spec)
	c.collect(context.Background(), spec, gvr)
	if e := mp.Events(); len(e) != 1 || e[0].Title != "K8sGPT snapshot labda: 0 findings" {
		t.Errorf("events %+v", e)
	}
}

// An oversized snapshot is trimmed to fit homerun-library's limit.
func TestSnapshotFitsLimit(t *testing.T) {
	big := strings.Repeat("x", 4000)
	var objs []runtime.Object
	for i := range 120 {
		r := k8sgptResult(fmt.Sprintf("p%d", i), "e")
		r.Object["status"] = map[string]any{"blob": big} // dropped by trimming
		objs = append(objs, r)
	}
	c, mp, spec := snapshotCollector(objs...)
	gvr, _ := gvrFor(spec)
	c.collect(context.Background(), spec, gvr)

	p := mp.Events()[0].Payload
	raw, _ := json.Marshal(p)
	if len(raw) > homerun.MaxPayloadSize {
		t.Errorf("payload %d bytes, limit %d", len(raw), homerun.MaxPayloadSize)
	}
	if p["trimmed"] != true || p["count"] != 120 {
		t.Errorf("trimmed %v count %v", p["trimmed"], p["count"])
	}
	if p["truncated"] == true {
		t.Error("120 small specs should fit after trimming, not be cut")
	}
}

// A resource the cluster does not serve is no error and pitches nothing
// (#106); the snapshot carries the collector's system.
func TestCollectorUnservedAndSystem(t *testing.T) {
	c, mp, spec := snapshotCollector()
	c.client.(*dynamicfake.FakeDynamicClient).PrependReactor("list", "results", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(resultsGVR.GroupResource(), "")
	})
	gvr, _ := gvrFor(spec)
	c.collect(context.Background(), spec, gvr)
	if n := len(mp.Events()); n != 0 {
		t.Errorf("pitched %d for an unserved resource", n)
	}

	c2, mp2, spec2 := snapshotCollector(k8sgptResult("a", "e"))
	spec2.System = "k8sgpt-labda"
	c2.collect(context.Background(), spec2, gvr)
	if s := mp2.Events()[0].Subsystem; s != "k8sgpt-labda" {
		t.Errorf("subsystem %q", s)
	}
}

func TestInventoryCollector(t *testing.T) {
	scheme := runtime.NewScheme()
	kinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "nodes"}: "NodeList", {Version: "v1", Resource: "pods"}: "PodList",
		{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}: "HelmReleaseList",
		{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}:      "ApplicationList",
		{Group: "pkg.crossplane.io", Version: "v1", Resource: "configurations"}:    "ConfigurationList",
		{Group: "pkg.crossplane.io", Version: "v1", Resource: "providers"}:         "ProviderList",
		{Group: "pkg.crossplane.io", Version: "v1", Resource: "functions"}:         "FunctionList",
	}
	node := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "n1"}}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, kinds, node)
	mp := &mockPitcher{}
	c := New(client, mp, nil, "c1")
	c.ServerVersion = func() (string, error) { return "v1.33.2", nil }
	c.pitchInventory(context.Background(), profile.CollectorSpec{Kind: KindInventory})
	ev := mp.Events()
	if len(ev) != 1 {
		t.Fatalf("events %d", len(ev))
	}
	e := ev[0]
	if e.Subsystem != "inventory-c1" || e.EventType != "snapshot" || e.Payload["format"] != "inventory/v1" || e.Payload["version"] != "v1.33.2" {
		t.Errorf("event %+v", e)
	}
	c.pitchInventory(context.Background(), profile.CollectorSpec{Kind: KindInventory, System: "inv-x"})
	if got := mp.Events()[1].Subsystem; got != "inv-x" {
		t.Errorf("system override %s", got)
	}
}
