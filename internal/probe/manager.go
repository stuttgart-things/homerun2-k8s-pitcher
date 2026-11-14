package probe

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/dynamic"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// workers probe targets in parallel.
const workers = 8

// Manager runs probe rounds: discover, probe, update the metrics, pitch the
// state changes of findings and, every snapshot interval, all open ones. The
// first round only seeds the state and sends the first snapshot, so a
// restart does not re-send its open problems as events.
type Manager struct {
	Prober     *Prober
	Discoverer interface {
		Targets(ctx context.Context) []Target
	}
	Pitcher pitcher.K8sPitcher
	Metrics *Metrics
	// Cluster is the findings/v1 target, System the message system.
	Cluster string
	System  string
	Spec    profile.ProbesSpec
	// Now is the clock, time.Now outside of tests.
	Now func() time.Time

	open map[string]openFinding
	// failures counts the consecutive failed rounds per target.
	failures     map[string]int
	seeded       bool
	lastSnapshot time.Time
	targets      int
}

type openFinding struct {
	Finding
	target string // Target.key()
}

// New creates a Manager for spec.
func New(client dynamic.Interface, p pitcher.K8sPitcher, spec profile.ProbesSpec, cluster, system string) *Manager {
	return &Manager{
		Prober: &Prober{
			Cluster:        cluster,
			Client:         NewHTTPClient(spec.Resolver),
			Timeout:        spec.Timeout,
			ExpectedStatus: spec.ExpectedStatus,
			WarnDays:       spec.CertWarnDays,
			ErrorDays:      spec.CertErrorDays,
		},
		Discoverer: NewDiscoverer(client, spec),
		Pitcher:    p,
		Metrics:    NewMetrics(),
		Cluster:    cluster,
		System:     system,
		Spec:       spec,
	}
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Run probes every interval until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.Spec.Interval)
	defer t.Stop()
	for {
		m.Round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Round runs one probe round.
func (m *Manager) Round(ctx context.Context) {
	targets := m.Discoverer.Targets(ctx)
	results := m.probeAll(ctx, targets)
	if ctx.Err() != nil {
		return
	}
	if m.Metrics != nil {
		m.Metrics.Set(results)
	}
	m.targets = len(targets)

	current := map[string]openFinding{}
	keys := map[string]bool{}
	failures := map[string]int{}
	for _, r := range results {
		key := r.Target.key()
		keys[key] = true
		if slices.ContainsFunc(r.Findings, func(f Finding) bool { return f.Kind == KindEndpoint }) {
			failures[key] = m.failures[key] + 1
		}
		for _, f := range r.Findings {
			// An Endpoint finding opens after failureThreshold consecutive
			// failed rounds; one already open stays open while it fails.
			// The first round seeds the state, so nothing waits there.
			if _, open := m.open[f.Fingerprint]; f.Kind == KindEndpoint && !open && m.seeded &&
				failures[key] < m.Spec.Threshold() {
				continue
			}
			current[f.Fingerprint] = openFinding{Finding: f, target: key}
		}
	}
	m.failures = failures

	if m.seeded {
		m.diff(current, keys)
	} else {
		// The first round after start sends no events: the problems found
		// are either known from before a restart or carried by the first
		// snapshot below.
		m.open = current
		m.seeded = true
	}

	now := m.now()
	if m.lastSnapshot.IsZero() || now.Sub(m.lastSnapshot) >= m.Spec.SnapshotInterval {
		if m.pitchSnapshot(now) {
			m.lastSnapshot = now
		}
	}
	slog.Debug("probe round done", "targets", len(targets), "open", len(m.open))
}

func (m *Manager) probeAll(ctx context.Context, targets []Target) []Result {
	results := make([]Result, len(targets))
	idx := make(chan int)
	var wg sync.WaitGroup
	for range min(workers, len(targets)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				results[i] = m.Prober.Probe(ctx, targets[i])
			}
		}()
	}
	for i := range targets {
		idx <- i
	}
	close(idx)
	wg.Wait()
	return results
}

// diff pitches an event per finding that opened, changed its severity or
// resolved, and makes current the open set. Findings of a target that is
// gone (Ingress deleted, opted out) are dropped without an event. A failed
// pitch keeps the old state, so the event is retried next round.
func (m *Manager) diff(current map[string]openFinding, targets map[string]bool) {
	if m.open == nil {
		m.open = map[string]openFinding{}
	}
	next := maps.Clone(current)
	for _, fp := range slices.Sorted(maps.Keys(current)) {
		f := current[fp]
		old, ok := m.open[fp]
		var err error
		switch {
		case !ok:
			err = m.pitchEvent("opened", f.Finding)
		case old.Severity != f.Severity:
			err = m.pitchEvent("changed", f.Finding)
		default:
			continue
		}
		if err != nil {
			slog.Error("probe event pitch failed", "fingerprint", fp, "error", err)
			if ok {
				next[fp] = old
			} else {
				delete(next, fp)
			}
		}
	}
	for _, fp := range slices.Sorted(maps.Keys(m.open)) {
		if _, still := current[fp]; still {
			continue
		}
		old := m.open[fp]
		if !targets[old.target] {
			slog.Debug("probe target gone, finding dropped", "fingerprint", fp, "name", old.Name)
			continue
		}
		if err := m.pitchResolved(old.Finding); err != nil {
			slog.Error("probe event pitch failed", "fingerprint", fp, "error", err)
			next[fp] = old
		}
	}
	m.open = next
}

func (m *Manager) pitchEvent(change string, f Finding) error {
	text := ""
	if len(f.Texts) > 0 {
		text = f.Texts[0]
	}
	return m.Pitcher.Pitch(m.event(change, f, f.Severity,
		fmt.Sprintf("%s %s: %s", f.Kind, f.Name, text), []Finding{f}))
}

func (m *Manager) pitchResolved(f Finding) error {
	return m.Pitcher.Pitch(m.event("resolved", f, SeveritySuccess,
		fmt.Sprintf("%s %s: resolved", f.Kind, f.Name), nil))
}

// event is the message of a finding's state change: tag fp=<fingerprint>,
// the finding as the only item (none if resolved).
func (m *Manager) event(change string, f Finding, severity, title string, items []Finding) pitcher.K8sEvent {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s on %s\n", f.Kind, f.Name, change, m.Cluster)
	if f.Namespace != "" {
		fmt.Fprintf(&b, "Namespace: %s\n", f.Namespace)
	}
	if f.Parent != "" {
		fmt.Fprintf(&b, "Parent: %s\n", f.Parent)
	}
	fmt.Fprintf(&b, "Check: %s\nSeverity: %s\n", f.Check, severity)
	if change != "resolved" {
		for _, t := range f.Texts {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}
	return pitcher.K8sEvent{
		Kind:        f.Kind,
		EventType:   change,
		Namespace:   f.Namespace,
		Name:        f.Name,
		Title:       title,
		Summary:     strings.TrimSpace(b.String()),
		Severity:    severity,
		Subsystem:   m.System,
		Payload:     Payload(m.Cluster, items),
		Fingerprint: f.Fingerprint,
		Timestamp:   m.now().UTC().Format(time.RFC3339),
		Cluster:     m.Cluster,
		Unthrottled: !m.Spec.Throttled(),
		Key:         "probe|" + f.Fingerprint + "|" + change + "|" + severity,
	}
}

// pitchSnapshot pitches all open findings; it bypasses the throttle, a
// dropped snapshot would leave the archive stale.
func (m *Manager) pitchSnapshot(now time.Time) bool {
	findings := make([]Finding, 0, len(m.open))
	for _, fp := range slices.Sorted(maps.Keys(m.open)) {
		findings = append(findings, m.open[fp].Finding)
	}
	payload := Payload(m.Cluster, findings)
	sev := Worst(findings)

	var b strings.Builder
	fmt.Fprintf(&b, "%d endpoints probed on %s, %d open findings", m.targets, m.Cluster, len(findings))
	if n, ok := payload["truncated"].(int); ok && n > 0 {
		fmt.Fprintf(&b, " (%d omitted from the payload)", n)
	}
	items, _ := payload["items"].([]Finding)
	for _, f := range items {
		text := ""
		if len(f.Texts) > 0 {
			text = ": " + f.Texts[0]
		}
		fmt.Fprintf(&b, "\n- [%s] %s %s%s", f.Severity, f.Kind, f.Name, text)
	}

	err := m.Pitcher.Pitch(pitcher.K8sEvent{
		Kind:        "Endpoints",
		EventType:   "snapshot",
		Name:        m.Cluster,
		Title:       fmt.Sprintf("endpoints %s: %d open findings", m.Cluster, len(findings)),
		Summary:     b.String(),
		Severity:    sev,
		Subsystem:   m.System,
		Payload:     payload,
		Timestamp:   now.UTC().Format(time.RFC3339),
		Cluster:     m.Cluster,
		Unthrottled: true,
	})
	if err != nil {
		slog.Error("probe snapshot pitch failed", "error", err)
		return false
	}
	return true
}
