package informer

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	k8stesting "k8s.io/client-go/testing"
	"sync/atomic"
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

	// Wait for informer to process the initial list
	time.Sleep(200 * time.Millisecond)

	// Without pitchInitialList the existing pod is not pitched.
	if events := mp.Events(); len(events) != 0 {
		t.Errorf("expected no events from the initial list, got %+v", events)
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
	mgr.pitch(profile.InformerSpec{}, gvr, u, pitcher.K8sEvent{EventType: "delete"})

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
	if strings.Contains(e.Summary, "nodeName") {
		t.Errorf("summary must not contain the object without includeObject: %q", e.Summary)
	}

	if e.Payload != nil {
		t.Errorf("payload without includeObject: %v", e.Payload)
	}

	// includeObject: the object goes along as payload, the summary stays text.
	mgr.pitch(profile.InformerSpec{IncludeObject: true}, gvr, u, pitcher.K8sEvent{EventType: "delete"})
	e = mp.Events()[1]
	if got, _, _ := unstructured.NestedString(e.Payload, "spec", "nodeName"); got != "node-1" {
		t.Errorf("includeObject: payload spec.nodeName = %q, want node-1", got)
	}
	if strings.Contains(e.Summary, "nodeName") {
		t.Errorf("includeObject: object must not be appended to the summary: %q", e.Summary)
	}
}

func k8sgptResult(errs ...string) *unstructured.Unstructured {
	var es []any
	for _, e := range errs {
		es = append(es, map[string]any{"text": e})
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.k8sgpt.ai/v1alpha1",
		"kind":       "Result",
		"metadata":   map[string]any{"name": "pod-defaultweb0", "namespace": "k8sgpt", "generation": int64(1)},
		"spec": map[string]any{
			"kind": "Pod", "name": "default/web-0", "parentObject": "StatefulSet/web",
			"error": es,
		},
	}}
}

// A K8sGPT Result is pitched as a finding about the object it names: added
// or changed a warning, deleted resolved, unless deleteSeverity says else.
func TestK8sGPTResultEvents(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}
	for _, tc := range []struct {
		name      string
		spec      profile.InformerSpec
		ev        pitcher.K8sEvent
		wantTitle string
		wantSev   string
	}{
		{"add", profile.InformerSpec{}, pitcher.K8sEvent{EventType: "add"}, "Pod default/web-0: 2 findings", "warning"},
		{"update", profile.InformerSpec{}, pitcher.K8sEvent{EventType: "update", Severity: "info"}, "Pod default/web-0: 2 findings", "warning"},
		{"delete", profile.InformerSpec{}, pitcher.K8sEvent{EventType: "delete"}, "Pod default/web-0: resolved", "success"},
		{"delete with deleteSeverity", profile.InformerSpec{DeleteSeverity: "info"}, pitcher.K8sEvent{EventType: "delete", Severity: "info"}, "Pod default/web-0: resolved", "info"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mp := &mockPitcher{}
			mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c")
			mgr.pitch(tc.spec, gvr, k8sgptResult("Back-off restarting failed container", "readiness probe failed"), tc.ev)

			e := mp.Events()[0]
			if e.Title != tc.wantTitle || e.Severity != tc.wantSev {
				t.Errorf("title %q severity %q, want %q %q", e.Title, e.Severity, tc.wantTitle, tc.wantSev)
			}
			if !strings.Contains(e.Summary, "  - readiness probe failed") {
				t.Errorf("summary lacks the error texts: %q", e.Summary)
			}
		})
	}
}

func vm(name string, generation int64, conds ...map[string]any) *unstructured.Unstructured {
	var cs []any
	for _, c := range conds {
		cs = append(cs, c)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "resources.stuttgart-things.com/v1alpha1",
		"kind":       "VsphereVM",
		"metadata": map[string]any{
			"name": name, "namespace": "default", "generation": generation,
		},
		"status": map[string]any{"conditions": cs},
	}}
}

func c(typ, status, reason string) map[string]any {
	return map[string]any{"type": typ, "status": status, "reason": reason}
}

func TestUpdateEvent(t *testing.T) {
	ready := c("Ready", "True", "Available")
	synced := c("Synced", "True", "ReconcileSuccess")
	tests := []struct {
		name     string
		mode     string
		old, new *unstructured.Unstructured
		want     bool
		title    string
		severity string
	}{
		{"status only", profile.UpdatesConditions, vm("vm1", 1, ready, synced), vm("vm1", 1, ready, synced), false, "", ""},
		{"status only, all", profile.UpdatesAll, vm("vm1", 1, ready), vm("vm1", 1, ready), true, "", ""},
		{"generation", profile.UpdatesConditions, vm("vm1", 1, ready), vm("vm1", 2, ready), true, "", "info"},
		{"generation, flips", profile.UpdatesFlips, vm("vm1", 1, ready), vm("vm1", 2, ready), false, "", ""},
		{"ready lost, flips", profile.UpdatesFlips, vm("vm1", 1, ready, synced), vm("vm1", 2, c("Ready", "False", "ReconcileError"), synced), true, "VsphereVM/vm1 Ready=False (ReconcileError)", "error"},
		{"generation mode ignores flips", profile.UpdatesGeneration, vm("vm1", 1, ready), vm("vm1", 1, c("Ready", "False", "X")), false, "", ""},
		{"ready lost", profile.UpdatesConditions, vm("vm1", 1, ready, synced), vm("vm1", 1, c("Ready", "False", "ReconcileError"), synced), true, "VsphereVM/vm1 Ready=False (ReconcileError)", "error"},
		{"synced lost", profile.UpdatesConditions, vm("vm1", 1, ready, synced), vm("vm1", 1, ready, c("Synced", "False", "ReconcileError")), true, "VsphereVM/vm1 Synced=False (ReconcileError)", "warning"},
		{"both lost", profile.UpdatesConditions, vm("vm1", 1, ready, synced), vm("vm1", 1, c("Ready", "Unknown", ""), c("Synced", "False", "")), true, "VsphereVM/vm1 Ready=Unknown, Synced=False", "warning"},
		{"ready again", profile.UpdatesConditions, vm("vm1", 1, c("Ready", "False", "Creating")), vm("vm1", 1, ready), true, "VsphereVM/vm1 Ready=True (Available)", "success"},
		{"condition appears", profile.UpdatesConditions, vm("vm1", 1), vm("vm1", 1, c("Ready", "False", "Creating")), false, "", ""},
		{"reason only", profile.UpdatesConditions, vm("vm1", 1, c("Ready", "False", "Creating")), vm("vm1", 1, c("Ready", "False", "ReconcileError")), false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := updateEvent(tt.mode, tt.old, tt.new)
			if ok != tt.want {
				t.Fatalf("pitched = %v, want %v", ok, tt.want)
			}
			if ev.Title != tt.title || ev.Severity != tt.severity {
				t.Errorf("got %q/%q, want %q/%q", ev.Title, ev.Severity, tt.title, tt.severity)
			}
		})
	}
}

func TestDeleteSeverity(t *testing.T) {
	mp := &mockPitcher{}
	mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c1")
	gvr := schema.GroupVersionResource{Group: "resources.stuttgart-things.com", Version: "v1alpha1", Resource: "vspherevms"}

	h := mgr.handlerFor(profile.InformerSpec{Events: []string{"delete"}, DeleteSeverity: "info"}, gvr)
	h.OnDelete(cache.DeletedFinalStateUnknown{Obj: vm("vm1", 1)})
	h.OnAdd(vm("vm2", 1), false) // add not watched

	events := mp.Events()
	if len(events) != 1 || events[0].EventType != "delete" || events[0].Severity != "info" || events[0].Name != "vm1" {
		t.Errorf("unexpected events %+v", events)
	}
}

// startInformer runs a manager with spec on a fake client holding objs and
// returns the pitcher, the client and a stop function.
func startInformer(t *testing.T, spec profile.InformerSpec, objs ...runtime.Object) (*mockPitcher, *dynamicfake.FakeDynamicClient, func()) {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: spec.Group, Version: spec.Version, Resource: spec.Resource}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "VsphereVMList"}, objs...)
	mp := &mockPitcher{}
	mgr := New(client, mp, []profile.InformerSpec{spec}, "c1")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		mgr.Start(ctx)
		close(done)
	}()
	return mp, client, func() {
		cancel()
		<-done
	}
}

func waitFor(t *testing.T, mp *mockPitcher, n int) []pitcher.K8sEvent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ev := mp.Events(); len(ev) >= n {
			return ev
		}
		time.Sleep(20 * time.Millisecond)
	}
	return mp.Events()
}

func TestInitialListNotPitched(t *testing.T) {
	spec := profile.InformerSpec{
		Group: "resources.stuttgart-things.com", Version: "v1alpha1", Resource: "vspherevms",
		Namespace: "*", Events: []string{"add", "update", "delete"}, Updates: profile.UpdatesConditions,
	}
	gvr := schema.GroupVersionResource{Group: spec.Group, Version: spec.Version, Resource: spec.Resource}
	mp, client, stop := startInformer(t, spec, vm("existing", 1))
	defer stop()

	// The existing object is not pitched; a new one is. Retry the create
	// until the informer's watch is up and sees it.
	var events []pitcher.K8sEvent
	for i := 0; i < 50 && len(events) == 0; i++ {
		name := fmt.Sprintf("new-%d", i)
		if _, err := client.Resource(gvr).Namespace("default").Create(context.Background(), vm(name, 1), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		events = waitFor(t, mp, 1)
	}
	for _, e := range events {
		if e.Name == "existing" {
			t.Errorf("object from the initial list was pitched: %+v", e)
		}
	}
	if len(events) == 0 || events[0].EventType != "add" {
		t.Fatalf("expected an add for the new object, got %+v", events)
	}

	// A status-only update is not pitched, a Ready flip is.
	n := len(mp.Events())
	upd := vm("existing", 1, c("Ready", "True", ""))
	if _, err := client.Resource(gvr).Namespace("default").Update(context.Background(), upd, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	upd = vm("existing", 1, c("Ready", "False", "ReconcileError"))
	if _, err := client.Resource(gvr).Namespace("default").Update(context.Background(), upd, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	events = waitFor(t, mp, n+1)[n:]
	if len(events) != 1 || events[0].Title != "VsphereVM/existing Ready=False (ReconcileError)" {
		t.Errorf("expected only the Ready flip, got %+v", events)
	}
}

func TestPitchInitialList(t *testing.T) {
	spec := profile.InformerSpec{
		Group: "resources.stuttgart-things.com", Version: "v1alpha1", Resource: "vspherevms",
		Namespace: "*", Events: []string{"add"}, PitchInitialList: true,
	}
	mp, _, stop := startInformer(t, spec, vm("existing", 1))
	defer stop()

	events := waitFor(t, mp, 1)
	if len(events) != 1 || events[0].Name != "existing" || events[0].EventType != "add" {
		t.Errorf("expected the initial list as add, got %+v", events)
	}
}

func k8sEvent(name, typ, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Event",
		"metadata":   map[string]any{"name": name, "namespace": "shop"},
		"type":       typ,
		"reason":     reason,
		"involvedObject": map[string]any{
			"kind": "Pod", "namespace": "shop", "name": "api-7d9",
		},
	}}
}

func TestEventFilter(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	all := []*unstructured.Unstructured{
		k8sEvent("e1", "Warning", "BackOff"),
		k8sEvent("e2", "Warning", "FailedScheduling"),
		k8sEvent("e3", "Normal", "BackOff"),
		k8sEvent("e4", "Normal", "Pulled"),
	}
	tests := []struct {
		name string
		spec profile.InformerSpec
		want []string
	}{
		{"no filter", profile.InformerSpec{}, []string{"e1", "e2", "e3", "e4"}},
		{"types", profile.InformerSpec{Types: []string{"Warning"}}, []string{"e1", "e2"}},
		{"reasons", profile.InformerSpec{Reasons: []string{"BackOff"}}, []string{"e1", "e3"}},
		{"types and reasons", profile.InformerSpec{Types: []string{"Warning"}, Reasons: []string{"BackOff"}}, []string{"e1"}},
		{"excludeReasons", profile.InformerSpec{ExcludeReasons: []string{"FailedScheduling", "Pulled"}}, []string{"e1", "e3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mp := &mockPitcher{}
			mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c1")
			tt.spec.Resource = "events"
			tt.spec.Events = []string{"add", "delete"}
			h := mgr.handlerFor(tt.spec, gvr)
			for _, e := range all {
				h.OnAdd(e, false)
			}
			var got []string
			for _, e := range mp.Events() {
				got = append(got, e.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("pitched %v, want %v", got, tt.want)
			}
			// Deletes are filtered the same way.
			n := len(mp.Events())
			h.OnDelete(all[3])
			if pitched := len(mp.Events()) > n; pitched != slices.Contains(tt.want, "e4") {
				t.Errorf("delete of e4 pitched = %v", pitched)
			}
		})
	}
}

func TestCooldownKey(t *testing.T) {
	mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), &mockPitcher{}, nil, "c1")
	obj := k8sEvent("e1", "Warning", "BackOff").Object
	for _, tc := range []struct {
		name string
		ev   pitcher.K8sEvent
		want string
	}{
		{"event add", pitcher.K8sEvent{Kind: "Event", EventType: "add", Object: obj}, "c1|Pod|shop|api-7d9|BackOff"},
		{"event delete", pitcher.K8sEvent{Kind: "Event", EventType: "delete", Object: obj}, ""},
		{"condition flip", pitcher.K8sEvent{Kind: "Node", Name: "w1", EventType: "update", Title: "Node/w1 Ready=False"}, "c1|Node||w1|Node/w1 Ready=False"},
		{"generation change", pitcher.K8sEvent{Kind: "Node", Name: "w1", EventType: "update"}, ""},
		{"plain add", pitcher.K8sEvent{Kind: "Pod", Name: "p1", EventType: "add"}, ""},
	} {
		if got := mgr.cooldownKey(tc.ev); got != tc.want {
			t.Errorf("%s: key %q, want %q", tc.name, got, tc.want)
		}
	}

	// Two Event objects about the same problem share a key.
	mp := &mockPitcher{}
	mgr = New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c1")
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "events"}
	mgr.pitch(profile.InformerSpec{}, gvr, k8sEvent("api-7d9.1", "Warning", "BackOff"), pitcher.K8sEvent{EventType: "add"})
	mgr.pitch(profile.InformerSpec{}, gvr, k8sEvent("api-7d9.2", "Warning", "BackOff"), pitcher.K8sEvent{EventType: "add"})
	if ev := mp.Events(); len(ev) != 2 || ev[0].Key == "" || ev[0].Key != ev[1].Key {
		t.Errorf("keys differ: %+v", ev)
	}
}

// throttle: false marks the informer's events, the throttle wrapper lets them
// through (#102). Default: throttled.
func TestInformerThrottleFlag(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}
	off := false
	for _, tc := range []struct {
		spec profile.InformerSpec
		want bool
	}{
		{profile.InformerSpec{}, false},
		{profile.InformerSpec{Throttle: &off}, true},
	} {
		mp := &mockPitcher{}
		mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c")
		mgr.pitch(tc.spec, gvr, k8sgptResult("x"), pitcher.K8sEvent{EventType: "add"})
		if got := mp.Events()[0].Unthrottled; got != tc.want {
			t.Errorf("throttle %v: Unthrottled = %v, want %v", tc.spec.Throttle, got, tc.want)
		}
	}
}

// A Result event carries the finding's fingerprint, any other object none.
func TestInformerFingerprint(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}
	mp := &mockPitcher{}
	mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c")
	mgr.pitch(profile.InformerSpec{}, gvr, k8sgptResult("x"), pitcher.K8sEvent{EventType: "add"})
	if fp := mp.Events()[0].Fingerprint; len(fp) != 16 {
		t.Errorf("fingerprint %q", fp)
	}
	mgr.pitch(profile.InformerSpec{}, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "p"},
	}}, pitcher.K8sEvent{EventType: "add"})
	if fp := mp.Events()[1].Fingerprint; fp != "" {
		t.Errorf("pod fingerprint %q", fp)
	}
}

// An informer on a resource the cluster does not serve waits, and starts once
// it is served (#106): here the first two checks get NotFound, then a Result
// created afterwards is pitched.
func TestInformerWaitsForUnservedResource(t *testing.T) {
	old := servedRetry
	servedRetry = 10 * time.Millisecond
	defer func() { servedRetry = old }()

	gvr := schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "ResultList"})
	var lists atomic.Int32
	client.PrependReactor("list", "results", func(k8stesting.Action) (bool, runtime.Object, error) {
		if lists.Add(1) <= 2 {
			return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "")
		}
		return false, nil, nil
	})

	mp := &mockPitcher{}
	spec := profile.InformerSpec{Group: gvr.Group, Version: gvr.Version, Resource: gvr.Resource,
		Namespace: "k8sgpt", Events: []string{"add"}, System: "k8sgpt-c"}
	mgr := New(client, mp, []profile.InformerSpec{spec}, "c")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go mgr.Start(ctx)

	deadline := time.Now().Add(5 * time.Second)
	for lists.Load() < 4 && time.Now().Before(deadline) { // 2 NotFound, the served check, the informer's list
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // let the informer sync
	if _, err := client.Resource(gvr).Namespace("k8sgpt").Create(ctx, k8sgptResult("new"), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for len(mp.Events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	events := mp.Events()
	if len(events) != 1 {
		t.Fatalf("pitched %d events after the resource became served, want 1", len(events))
	}
	if events[0].Subsystem != "k8sgpt-c" {
		t.Errorf("subsystem %q, want the informer's system", events[0].Subsystem)
	}
}

func TestInformerSystem(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "core.k8sgpt.ai", Version: "v1alpha1", Resource: "results"}
	mp := &mockPitcher{}
	mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c")
	mgr.pitch(profile.InformerSpec{System: "k8sgpt-c"}, gvr, k8sgptResult("x"), pitcher.K8sEvent{EventType: "add"})
	mgr.pitch(profile.InformerSpec{}, gvr, k8sgptResult("x"), pitcher.K8sEvent{EventType: "add"})
	e := mp.Events()
	if e[0].Subsystem != "k8sgpt-c" || e[1].Subsystem != "" {
		t.Errorf("subsystems %q %q", e[0].Subsystem, e[1].Subsystem)
	}
}
