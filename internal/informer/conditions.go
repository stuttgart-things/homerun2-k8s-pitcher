package informer

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// clock is the time source of the informers, real outside of tests.
type clock interface {
	Now() time.Time
	// AfterFunc calls f in its own goroutine once d has passed, unless the
	// returned timer is stopped first.
	AfterFunc(d time.Duration, f func()) timer
}

type timer interface{ Stop() bool }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

func (realClock) AfterFunc(d time.Duration, f func()) timer { return time.AfterFunc(d, f) }

// flip is a condition that changed to state.
type flip struct {
	typ   string
	state cond
}

// flipEvent titles flips e.g. "VirtualMachine/vm1 Ready=False
// (ReconcileError)" with a severity from the transition: Ready to False is
// error, Synced to False or either to Unknown warning, back to True success.
func flipEvent(u *unstructured.Unstructured, flips []flip) pitcher.K8sEvent {
	parts := make([]string, 0, len(flips))
	rank := 0
	for _, f := range flips {
		s := f.typ + "=" + f.state.status
		if f.state.reason != "" {
			s += " (" + f.state.reason + ")"
		}
		parts = append(parts, s)
		r := 1 // success
		switch {
		case f.state.status == "False" && f.typ == "Ready":
			r = 3
		case f.state.status != "True":
			r = 2
		}
		rank = max(rank, r)
	}
	return pitcher.K8sEvent{
		EventType: "update",
		Title:     fmt.Sprintf("%s/%s %s", u.GetKind(), u.GetName(), strings.Join(parts, ", ")),
		Severity:  []string{"", "success", "warning", "error"}[rank],
	}
}

// conditionTracker filters the condition flips of one informer with
// ignoreStatuses, ignoreReasons and minDuration (#89). Per object it keeps
// the last reported state of each watched condition and the start of
// failures that are not reported yet. Its state lives in memory only.
type conditionTracker struct {
	m    *Manager
	spec profile.InformerSpec
	gvr  schema.GroupVersionResource

	// mu also covers pitching, so a delayed failure and its recovery are
	// pitched in order.
	mu      sync.Mutex
	objects map[string]*objectState
	closed  bool
}

type objectState struct {
	// reported is the last reported state per condition; status "" means
	// not known yet (the condition was missing, ignored or failing when
	// first seen).
	reported map[string]cond
	// current is the last state per condition that is not ignored.
	current map[string]cond
	// failingSince marks failures that are not reported yet.
	failingSince map[string]time.Time
	// obj is the latest version, pitched by a delayed failure.
	obj   *unstructured.Unstructured
	timer timer
	due   time.Time
	// seq tells a timer that fires after it was replaced or stopped.
	seq int
}

func newConditionTracker(m *Manager, spec profile.InformerSpec, gvr schema.GroupVersionResource) *conditionTracker {
	return &conditionTracker{m: m, spec: spec, gvr: gvr, objects: map[string]*objectState{}}
}

func objectKey(u *unstructured.Unstructured) string {
	return u.GetNamespace() + "/" + u.GetName()
}

func (t *conditionTracker) ignored(c cond) bool {
	return slices.Contains(t.spec.IgnoreStatuses, c.status) ||
		(c.reason != "" && slices.Contains(t.spec.IgnoreReasons, c.reason))
}

// update handles an update of an object: recoveries of reported failures
// (and failures, without minDuration) are pitched at once, failures with
// minDuration are pitched by a timer once they have lasted that long. A
// generation change without a flip is pitched as before.
func (t *conditionTracker) update(oldU, newU *unstructured.Unstructured) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	key := objectKey(newU)
	st := t.objects[key]
	if st == nil {
		st = &objectState{
			reported:     map[string]cond{},
			current:      map[string]cond{},
			failingSince: map[string]time.Time{},
		}
		t.objects[key] = st
	}
	st.obj = newU
	now := t.m.clock.Now()

	var flips []flip
	for _, typ := range watchedConditions {
		after, ok := condition(newU, typ)
		if !ok || t.ignored(after) {
			continue
		}
		st.current[typ] = after
		rep, known := st.reported[typ]
		if !known {
			// First update of this object: the old object is the baseline.
			// With minDuration only a healthy old state is; a failure seen
			// first counts as new and has to last minDuration. Without
			// minDuration any old state that is not ignored is, as for plain
			// flips, and a condition that appears is no flip.
			before, ok := condition(oldU, typ)
			switch {
			case ok && before.status == "True":
				rep = before
			case t.spec.MinDuration <= 0 && ok && !t.ignored(before):
				rep = before
			case t.spec.MinDuration <= 0 && !ok:
				rep = after
			}
			st.reported[typ] = rep
		}
		switch {
		case after.status == rep.status:
			// Back to the reported state: a pending failure is dropped.
			delete(st.failingSince, typ)
		case after.status == "True":
			delete(st.failingSince, typ)
			st.reported[typ] = after
			if rep.status != "" {
				flips = append(flips, flip{typ, after}) // recovery of a reported failure
			}
		case t.spec.MinDuration <= 0:
			st.reported[typ] = after
			flips = append(flips, flip{typ, after})
		default:
			if _, pending := st.failingSince[typ]; !pending {
				st.failingSince[typ] = now
			}
		}
	}
	t.schedule(key, st, now)

	if len(flips) > 0 {
		t.m.pitch(t.spec, t.gvr, newU, flipEvent(newU, flips))
		return
	}
	if oldU.GetGeneration() != newU.GetGeneration() {
		t.m.pitch(t.spec, t.gvr, newU, pitcher.K8sEvent{EventType: "update", Severity: "info"})
	}
}

// schedule arms the timer of st for its earliest pending failure, or stops
// it when nothing is pending.
func (t *conditionTracker) schedule(key string, st *objectState, now time.Time) {
	var due time.Time
	for _, since := range st.failingSince {
		if d := since.Add(t.spec.MinDuration); due.IsZero() || d.Before(due) {
			due = d
		}
	}
	if st.timer != nil && due.Equal(st.due) {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
		st.timer = nil
	}
	st.seq++
	st.due = due
	if due.IsZero() {
		return
	}
	seq := st.seq
	st.timer = t.m.clock.AfterFunc(due.Sub(now), func() { t.fire(key, seq) })
}

// fire pitches the failures of an object that have lasted minDuration.
func (t *conditionTracker) fire(key string, seq int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.objects[key]
	if t.closed || st == nil || st.seq != seq {
		return
	}
	st.timer = nil
	now := t.m.clock.Now()
	var (
		flips   []flip
		longest time.Duration
		first   time.Time
	)
	for _, typ := range watchedConditions {
		since, pending := st.failingSince[typ]
		if !pending || now.Before(since.Add(t.spec.MinDuration)) {
			continue
		}
		delete(st.failingSince, typ)
		st.reported[typ] = st.current[typ]
		flips = append(flips, flip{typ, st.current[typ]})
		if d := now.Sub(since); d > longest {
			longest, first = d, since
		}
	}
	t.schedule(key, st, now)
	if len(flips) == 0 {
		return
	}
	ev := flipEvent(st.obj, flips)
	ev.Summary = fmt.Sprintf("Failing for %s (since %s)", longest.Round(time.Second), first.UTC().Format(time.RFC3339))
	t.m.pitch(t.spec, t.gvr, st.obj, ev)
}

// forget drops the state of a deleted object and stops its timer.
func (t *conditionTracker) forget(u *unstructured.Unstructured) {
	t.mu.Lock()
	defer t.mu.Unlock()
	key := objectKey(u)
	if st := t.objects[key]; st != nil && st.timer != nil {
		st.timer.Stop()
	}
	delete(t.objects, key)
}

// close stops every timer; later updates and timers do nothing.
func (t *conditionTracker) close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	for _, st := range t.objects {
		if st.timer != nil {
			st.timer.Stop()
		}
	}
	t.objects = map[string]*objectState{}
}
