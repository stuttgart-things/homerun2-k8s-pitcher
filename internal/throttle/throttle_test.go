package throttle

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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

func (m *mockPitcher) HealthCheck(_ context.Context) error { return nil }

func (m *mockPitcher) Events() []pitcher.K8sEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]pitcher.K8sEvent(nil), m.events...)
}

// clock is a fake clock moved by the test.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTest(cooldown time.Duration, rl *profile.RateLimit) (*Pitcher, *mockPitcher, *clock) {
	mp := &mockPitcher{}
	c := &clock{t: time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)}
	p := New(mp, cooldown, rl, "c1")
	p.now = c.now
	return p, mp, c
}

func ev(name, key string) pitcher.K8sEvent {
	return pitcher.K8sEvent{Kind: "Event", Name: name, EventType: "add", Summary: "s", Key: key}
}

func TestOff(t *testing.T) {
	p, mp, _ := newTest(0, nil)
	for i := range 100 {
		_ = p.Pitch(ev(fmt.Sprint(i), "same"))
	}
	if n := len(mp.Events()); n != 100 {
		t.Errorf("pitched %d, want all 100", n)
	}
}

func TestCooldown(t *testing.T) {
	p, mp, c := newTest(time.Hour, nil)

	_ = p.Pitch(ev("a1", "pod-a|BackOff"))
	_ = p.Pitch(ev("b1", "pod-b|BackOff")) // other key
	_ = p.Pitch(ev("x", ""))               // no key: never deduplicated
	_ = p.Pitch(ev("x", ""))
	c.advance(10 * time.Minute)
	_ = p.Pitch(ev("a2", "pod-a|BackOff"))
	c.advance(10 * time.Minute)
	_ = p.Pitch(ev("a3", "pod-a|BackOff"))
	if got := names(mp.Events()); got != "a1,b1,x,x" {
		t.Fatalf("within the cooldown: pitched %s", got)
	}

	// After the cooldown the problem is pitched again, with the repeats.
	c.advance(41 * time.Minute)
	_ = p.Pitch(ev("a4", "pod-a|BackOff"))
	events := mp.Events()
	last := events[len(events)-1]
	if last.Name != "a4" || !strings.Contains(last.Summary, "Repeats: 2 since 2026-10-23T12:00:00Z") {
		t.Errorf("after the cooldown: %+v", last)
	}

	// The cooldown restarts with that pitch.
	c.advance(30 * time.Minute)
	_ = p.Pitch(ev("a5", "pod-a|BackOff"))
	if n := len(mp.Events()); n != 5 {
		t.Errorf("pitched %d, want 5", n)
	}
}

func TestRateLimit(t *testing.T) {
	p, mp, c := newTest(0, &profile.RateLimit{Max: 3, Per: 10 * time.Minute})

	for i := range 10 {
		_ = p.Pitch(ev(fmt.Sprint(i), ""))
	}
	if got := names(mp.Events()); got != "0,1,2" {
		t.Fatalf("pitched %s, want the first 3", got)
	}

	// Within the window nothing else goes out, not even the summary.
	c.advance(9 * time.Minute)
	p.Flush()
	if n := len(mp.Events()); n != 3 {
		t.Fatalf("summary before the window ended: %+v", mp.Events())
	}

	// After the window: exactly one summary, even if flushed twice.
	c.advance(time.Minute)
	p.Flush()
	p.Flush()
	events := mp.Events()
	if len(events) != 4 {
		t.Fatalf("want one summary, got %+v", events[3:])
	}
	s := events[3]
	if s.Title != "k8s-pitcher: 7 events suppressed" || s.Severity != "warning" || s.Cluster != "c1" {
		t.Errorf("summary = %+v", s)
	}

	// The new window has its full budget; the summary does not count.
	for i := range 3 {
		_ = p.Pitch(ev(fmt.Sprint("n", i), ""))
	}
	if n := len(mp.Events()); n != 7 {
		t.Errorf("new window: pitched %d, want 7", n)
	}

	// A window without drops ends without a summary.
	c.advance(10 * time.Minute)
	p.Flush()
	if n := len(mp.Events()); n != 7 {
		t.Errorf("summary without drops: %+v", mp.Events()[7:])
	}
}

// The summary goes out with the first event of the next window as well.
func TestRateLimitSummaryOnPitch(t *testing.T) {
	p, mp, c := newTest(0, &profile.RateLimit{Max: 1, Per: time.Minute})
	_ = p.Pitch(ev("a", ""))
	_ = p.Pitch(ev("b", ""))
	c.advance(time.Minute)
	_ = p.Pitch(ev("c", ""))
	if got := names(mp.Events()); got != "a,k8s-pitcher: 1 events suppressed,c" {
		t.Errorf("pitched %s", got)
	}
}

// An event dropped by the rate limit does not start its cooldown.
func TestRateLimitDoesNotStartCooldown(t *testing.T) {
	p, mp, c := newTest(time.Hour, &profile.RateLimit{Max: 1, Per: time.Minute})
	_ = p.Pitch(ev("a", "k1"))
	_ = p.Pitch(ev("b", "k2")) // rate limited
	c.advance(time.Minute)
	_ = p.Pitch(ev("b2", "k2"))
	if got := names(mp.Events()); got != "a,k8s-pitcher: 1 events suppressed,b2" {
		t.Errorf("pitched %s", got)
	}
}

func names(events []pitcher.K8sEvent) string {
	var s []string
	for _, e := range events {
		if e.Kind == "k8s-pitcher" {
			s = append(s, e.Title)
			continue
		}
		s = append(s, e.Name)
	}
	return strings.Join(s, ",")
}
