// Package throttle keeps the pitcher from flooding its target (#87): a
// cooldown per problem and a rate limit over every message.
package throttle

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// Pitcher wraps a K8sPitcher. An event with a Key that was pitched less than
// cooldown ago is dropped and counted; the first one after the cooldown is
// pitched with that count. Above max events per window everything is dropped
// and, once the window is over, a single "N events suppressed" warning is
// pitched instead.
type Pitcher struct {
	next     pitcher.K8sPitcher
	cooldown time.Duration
	max      int
	per      time.Duration
	cluster  string
	// now is the clock, time.Now outside of tests.
	now func() time.Time

	mu         sync.Mutex
	seen       map[string]*seen
	windowEnd  time.Time
	sent       int
	suppressed int
}

type seen struct {
	last    time.Time
	repeats int
}

// New wraps next with the cooldown (0 is off) and the rate limit (nil is
// off).
func New(next pitcher.K8sPitcher, cooldown time.Duration, rl *profile.RateLimit, cluster string) *Pitcher {
	p := &Pitcher{
		next:     next,
		cooldown: cooldown,
		cluster:  cluster,
		now:      time.Now,
		seen:     map[string]*seen{},
	}
	if rl != nil {
		p.max, p.per = rl.Max, rl.Per
	}
	return p
}

func (p *Pitcher) HealthCheck(ctx context.Context) error {
	return p.next.HealthCheck(ctx)
}

// Pitch passes event on unless the cooldown or the rate limit drops it.
func (p *Pitcher) Pitch(event pitcher.K8sEvent) error {
	p.mu.Lock()
	now := p.now()
	summary := p.closeWindow(now)
	pass := p.admit(&event, now)
	p.mu.Unlock()

	if summary != nil {
		if err := p.next.Pitch(*summary); err != nil {
			slog.Error("suppressed summary pitch failed", "error", err)
		}
	}
	if !pass {
		return nil
	}
	return p.next.Pitch(event)
}

// admit applies the cooldown, then the rate limit. The cooldown only
// starts when the event is really pitched, so a problem dropped by the rate
// limit is not silenced for the whole cooldown.
func (p *Pitcher) admit(event *pitcher.K8sEvent, now time.Time) bool {
	var s *seen
	if p.cooldown > 0 && event.Key != "" {
		s = p.seen[event.Key]
		if s != nil && now.Sub(s.last) < p.cooldown {
			s.repeats++
			slog.Debug("event dropped by cooldown", "key", event.Key, "repeats", s.repeats)
			return false
		}
	}
	if p.max > 0 {
		if p.sent >= p.max {
			p.suppressed++
			slog.Debug("event dropped by rate limit", "kind", event.Kind, "name", event.Name)
			return false
		}
		p.sent++
	}
	if p.cooldown > 0 && event.Key != "" {
		if s != nil && s.repeats > 0 {
			event.Summary += fmt.Sprintf("\nRepeats: %d since %s", s.repeats, s.last.UTC().Format(time.RFC3339))
		}
		p.seen[event.Key] = &seen{last: now}
	}
	return true
}

// closeWindow starts a new rate limit window once the current one is over
// and returns the summary of what it suppressed, if anything. It also
// forgets keys whose cooldown is long over.
func (p *Pitcher) closeWindow(now time.Time) *pitcher.K8sEvent {
	for k, s := range p.seen {
		// Twice the cooldown: a repeat count is still reported shortly
		// after the cooldown.
		if now.Sub(s.last) >= 2*p.cooldown {
			delete(p.seen, k)
		}
	}
	if p.max == 0 {
		return nil
	}
	if p.windowEnd.IsZero() {
		p.windowEnd = now.Add(p.per)
		return nil
	}
	if now.Before(p.windowEnd) {
		return nil
	}
	n := p.suppressed
	p.sent, p.suppressed = 0, 0
	p.windowEnd = now.Add(p.per)
	if n == 0 {
		return nil
	}
	return &pitcher.K8sEvent{
		Kind:      "k8s-pitcher",
		EventType: "suppressed",
		Title:     fmt.Sprintf("k8s-pitcher: %d events suppressed", n),
		Summary: fmt.Sprintf("%d events above the rate limit of %d per %s were dropped on cluster %s.",
			n, p.max, p.per, p.cluster),
		Severity:  "warning",
		Timestamp: now.UTC().Format(time.RFC3339),
		Cluster:   p.cluster,
	}
}

// Flush pitches the summary of a finished window even when no further event
// arrives to close it.
func (p *Pitcher) Flush() {
	p.mu.Lock()
	summary := p.closeWindow(p.now())
	p.mu.Unlock()
	if summary != nil {
		if err := p.next.Pitch(*summary); err != nil {
			slog.Error("suppressed summary pitch failed", "error", err)
		}
	}
}

// Run calls Flush every interval until ctx is cancelled.
func (p *Pitcher) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Flush()
		}
	}
}
