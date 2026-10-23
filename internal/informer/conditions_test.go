package informer

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/tools/cache"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// fakeClock is moved by the test; due timers fire synchronously in advance.
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c       *fakeClock
	at      time.Time
	f       func()
	stopped bool
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ft := &fakeTimer{c: c, at: c.t.Add(d), f: f}
	c.timers = append(c.timers, ft)
	return ft
}

func (ft *fakeTimer) Stop() bool {
	ft.c.mu.Lock()
	defer ft.c.mu.Unlock()
	was := !ft.stopped
	ft.stopped = true
	return was
}

// advance moves the clock by d and fires the timers that are due, in order.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	end := c.t.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.Slice(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var next *fakeTimer
		for _, ft := range c.timers {
			if !ft.stopped && !ft.at.After(end) {
				next = ft
				break
			}
		}
		if next == nil {
			c.t = end
			c.mu.Unlock()
			return
		}
		next.stopped = true
		c.t = next.at
		c.mu.Unlock()
		next.f()
	}
}

// live is the number of timers that are neither fired nor stopped.
func (c *fakeClock) live() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ft := range c.timers {
		if !ft.stopped {
			n++
		}
	}
	return n
}

var fluxSpec = profile.InformerSpec{
	Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations",
	Events: []string{"update"}, Updates: profile.UpdatesConditions,
	IgnoreStatuses: []string{"Unknown"},
	IgnoreReasons:  []string{"Progressing", "DependencyNotReady", "ReconciliationInProgress"},
	MinDuration:    5 * time.Minute,
}

func ks(name, status, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1",
		"kind":       "Kustomization",
		"metadata":   map[string]any{"name": name, "namespace": "flux-system", "generation": int64(1)},
		"status":     map[string]any{"conditions": []any{c("Ready", status, reason)}},
	}}
}

// objectSim feeds the handler a sequence of states of one object, like an
// informer does.
type objectSim struct {
	h    cache.ResourceEventHandler
	last map[string]*unstructured.Unstructured
}

func (s *objectSim) set(name, status, reason string) {
	u := ks(name, status, reason)
	if old := s.last[name]; old != nil {
		s.h.OnUpdate(old, u)
	}
	s.last[name] = u
}

func newSim(t *testing.T, spec profile.InformerSpec) (*objectSim, *mockPitcher, *fakeClock, *Manager) {
	t.Helper()
	mp := &mockPitcher{}
	mgr := New(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), mp, nil, "c1")
	fc := &fakeClock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	mgr.clock = fc
	gvr := schema.GroupVersionResource{Group: spec.Group, Version: spec.Version, Resource: spec.Resource}
	sim := &objectSim{h: mgr.handlerFor(spec, gvr), last: map[string]*unstructured.Unstructured{}}
	return sim, mp, fc, mgr
}

func titles(evs []pitcher.K8sEvent) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Severity+":"+e.Title)
	}
	return strings.Join(out, " | ")
}

func TestConditionFilterBurst(t *testing.T) {
	sim, mp, fc, mgr := newSim(t, fluxSpec)
	for i := range 20 {
		sim.set(fmt.Sprint("ks", i), "True", "ReconciliationSucceeded")
	}
	// A new revision: every Kustomization goes Progressing, some wait for
	// a dependency, and all are Ready again within seconds.
	for i := range 20 {
		name := fmt.Sprint("ks", i)
		sim.set(name, "Unknown", "Progressing")
		if i%2 == 0 {
			sim.set(name, "False", "DependencyNotReady")
		}
	}
	fc.advance(3 * time.Second)
	for i := range 20 {
		sim.set(fmt.Sprint("ks", i), "True", "ReconciliationSucceeded")
	}
	fc.advance(time.Hour)
	if evs := mp.Events(); len(evs) != 0 {
		t.Errorf("burst pitched %d events: %s", len(evs), titles(evs))
	}
	if n := mgr.trackers[0].pending(); n != 0 {
		t.Errorf("%d timers left", n)
	}
}

func TestConditionFilterShortFailure(t *testing.T) {
	sim, mp, fc, _ := newSim(t, fluxSpec)
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "False", "BuildFailed")
	fc.advance(4 * time.Minute)
	sim.set("app", "Unknown", "Progressing")
	sim.set("app", "True", "ReconciliationSucceeded")
	fc.advance(time.Hour)
	if evs := mp.Events(); len(evs) != 0 {
		t.Errorf("short failure pitched %s", titles(evs))
	}
	if n := fc.live(); n != 0 {
		t.Errorf("%d timers still armed", n)
	}
}

func TestConditionFilterLastingFailure(t *testing.T) {
	sim, mp, fc, _ := newSim(t, fluxSpec)
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "False", "BuildFailed")
	// Flux retries: Progressing in between does not restart the failure.
	for range 3 {
		fc.advance(time.Minute)
		sim.set("app", "Unknown", "Progressing")
		sim.set("app", "False", "BuildFailed")
	}
	if evs := mp.Events(); len(evs) != 0 {
		t.Fatalf("pitched before minDuration: %s", titles(evs))
	}
	fc.advance(2 * time.Minute)
	evs := mp.Events()
	if got := titles(evs); got != "error:Kustomization/app Ready=False (BuildFailed)" {
		t.Fatalf("lasting failure: got %q", got)
	}
	if !strings.HasPrefix(evs[0].Summary, "Failing for 5m0s (since 2026-10-23T12:00:00Z)\n") {
		t.Errorf("summary = %q", evs[0].Summary)
	}
	if evs[0].Key == "" || evs[0].Timestamp != "2026-10-23T12:05:00Z" {
		t.Errorf("key %q, timestamp %q", evs[0].Key, evs[0].Timestamp)
	}

	// Still failing: no repeat, not even after more retries.
	fc.advance(time.Hour)
	sim.set("app", "Unknown", "Progressing")
	sim.set("app", "False", "BuildFailed")
	fc.advance(time.Hour)
	if n := len(mp.Events()); n != 1 {
		t.Fatalf("still failing: %d events", n)
	}

	// The recovery is pitched once.
	sim.set("app", "Unknown", "Progressing")
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "True", "ReconciliationSucceeded")
	if got := titles(mp.Events()[1:]); got != "success:Kustomization/app Ready=True (ReconciliationSucceeded)" {
		t.Errorf("recovery: got %q", got)
	}
}

func TestConditionFilterDeleteWhilePending(t *testing.T) {
	sim, mp, fc, mgr := newSim(t, fluxSpec)
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "False", "BuildFailed")
	if n := mgr.trackers[0].pending(); n != 1 {
		t.Fatalf("pending = %d, want 1", n)
	}
	sim.h.OnDelete(cache.DeletedFinalStateUnknown{Obj: sim.last["app"]})
	fc.advance(time.Hour)
	if evs := mp.Events(); len(evs) != 0 {
		t.Errorf("deleted object pitched %s", titles(evs))
	}
	if n := fc.live(); n != 0 {
		t.Errorf("%d timers still armed", n)
	}
}

func TestConditionFilterCloseStopsTimers(t *testing.T) {
	sim, mp, fc, mgr := newSim(t, fluxSpec)
	sim.set("a", "True", "")
	sim.set("a", "False", "BuildFailed")
	mgr.trackers[0].close()
	sim.set("a", "True", "")
	fc.advance(time.Hour)
	if n := len(mp.Events()); n != 0 || fc.live() != 0 {
		t.Errorf("after close: %d events, %d timers", n, fc.live())
	}
}

// Without minDuration a failure is pitched at once; ignored states are
// skipped and the next flip is compared against the last reported state.
func TestConditionFilterIgnoreOnly(t *testing.T) {
	spec := fluxSpec
	spec.MinDuration = 0
	sim, mp, _, _ := newSim(t, spec)
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "Unknown", "Progressing")
	sim.set("app", "True", "ReconciliationSucceeded") // True -> True: nothing
	sim.set("app", "False", "DependencyNotReady")     // ignored reason
	sim.set("app", "False", "HealthCheckFailed")      // reported
	sim.set("app", "Unknown", "Progressing")          // ignored
	sim.set("app", "False", "HealthCheckFailed")      // False -> False: nothing
	sim.set("app", "True", "ReconciliationSucceeded") // recovery
	want := "error:Kustomization/app Ready=False (HealthCheckFailed) | success:Kustomization/app Ready=True (ReconciliationSucceeded)"
	if got := titles(mp.Events()); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if strings.HasPrefix(mp.Events()[0].Summary, "Failing") {
		t.Errorf("immediate failure must not carry a duration: %q", mp.Events()[0].Summary)
	}
}

// A failure seen first (e.g. after a restart) is pitched once it lasted
// minDuration; a healthy object seen first is no recovery.
func TestConditionFilterFirstSeen(t *testing.T) {
	sim, mp, fc, _ := newSim(t, fluxSpec)
	sim.set("bad", "False", "BuildFailed")
	sim.set("bad", "False", "BuildFailed")
	sim.set("ok", "Unknown", "Progressing")
	sim.set("ok", "True", "ReconciliationSucceeded")
	fc.advance(5 * time.Minute)
	if got := titles(mp.Events()); got != "error:Kustomization/bad Ready=False (BuildFailed)" {
		t.Errorf("got %q", got)
	}
}

// Ready and Synced failing at different times are pitched separately.
func TestConditionFilterTwoConditions(t *testing.T) {
	spec := fluxSpec
	sim, mp, fc, _ := newSim(t, spec)
	obj := func(ready, synced string) *unstructured.Unstructured {
		return vm("vm1", 1, c("Ready", ready, "R"), c("Synced", synced, "S"))
	}
	h := sim.h
	h.OnUpdate(obj("True", "True"), obj("True", "False"))
	fc.advance(2 * time.Minute)
	h.OnUpdate(obj("True", "False"), obj("False", "False"))
	fc.advance(3 * time.Minute)
	fc.advance(2 * time.Minute)
	want := "warning:VsphereVM/vm1 Synced=False (S) | error:VsphereVM/vm1 Ready=False (R)"
	if got := titles(mp.Events()); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if s := mp.Events()[1].Summary; !strings.HasPrefix(s, "Failing for 5m0s (since 2026-10-23T12:02:00Z)") {
		t.Errorf("summary %q", s)
	}
}

// Without the new fields a spec gets no tracker and every flip is pitched.
func TestConditionFilterOff(t *testing.T) {
	spec := fluxSpec
	spec.IgnoreStatuses, spec.IgnoreReasons, spec.MinDuration = nil, nil, 0
	sim, mp, _, mgr := newSim(t, spec)
	if len(mgr.trackers) != 0 {
		t.Fatal("tracker without condition filter")
	}
	sim.set("app", "True", "ReconciliationSucceeded")
	sim.set("app", "Unknown", "Progressing")
	sim.set("app", "True", "ReconciliationSucceeded")
	want := "warning:Kustomization/app Ready=Unknown (Progressing) | success:Kustomization/app Ready=True (ReconciliationSucceeded)"
	if got := titles(mp.Events()); got != want {
		t.Errorf("got %q", got)
	}
}

func TestConditionFilterGeneration(t *testing.T) {
	sim, mp, _, _ := newSim(t, fluxSpec)
	old := ks("app", "True", "")
	upd := ks("app", "Unknown", "Progressing")
	upd.SetGeneration(2)
	sim.h.OnUpdate(old, upd)
	if evs := mp.Events(); len(evs) != 1 || evs[0].Severity != "info" || evs[0].Title != "" {
		t.Errorf("generation change: %+v", evs)
	}
}

// With updates: flips a generation change is not pitched; the flips still
// go through the filter (#93).
func TestConditionFilterFlipsMode(t *testing.T) {
	spec := fluxSpec
	spec.Updates = profile.UpdatesFlips
	sim, mp, fc, _ := newSim(t, spec)
	old := ks("app", "True", "")
	upd := ks("app", "True", "")
	upd.SetGeneration(2)
	sim.h.OnUpdate(old, upd)
	sim.last["app"] = upd
	sim.set("app", "False", "BuildFailed")
	if evs := mp.Events(); len(evs) != 0 {
		t.Fatalf("generation change or early failure pitched: %+v", evs)
	}
	fc.advance(5 * time.Minute)
	if evs := mp.Events(); len(evs) != 1 || evs[0].Title != "Kustomization/app Ready=False (BuildFailed)" {
		t.Errorf("lasting failure: %+v", evs)
	}
}

// pending reports the number of objects with a running timer (tests).
func (t *conditionTracker) pending() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for _, st := range t.objects {
		if st.timer != nil {
			n++
		}
	}
	return n
}

// Without minDuration the first update compares against the old object, as
// plain flips do: a failure that was already there is no flip, its recovery
// is, and a condition that appears is no flip either.
func TestConditionFilterIgnoreOnlyBaseline(t *testing.T) {
	spec := fluxSpec
	spec.MinDuration = 0
	sim, mp, _, _ := newSim(t, spec)
	sim.set("bad", "False", "BuildFailed")
	sim.set("bad", "False", "BuildFailed")
	sim.set("bad", "True", "ReconciliationSucceeded")

	noCond := ks("new", "True", "")
	noCond.Object["status"] = map[string]any{}
	sim.h.OnUpdate(noCond, ks("new", "False", "BuildFailed"))

	if got := titles(mp.Events()); got != "success:Kustomization/bad Ready=True (ReconciliationSucceeded)" {
		t.Errorf("got %q", got)
	}
}
