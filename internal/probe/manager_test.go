package probe

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

type recPitcher struct {
	events []pitcher.K8sEvent
	fail   bool
}

func (r *recPitcher) Pitch(e pitcher.K8sEvent) error {
	if r.fail {
		return errors.New("down")
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recPitcher) HealthCheck(context.Context) error { return nil }

func (r *recPitcher) take() []pitcher.K8sEvent {
	e := r.events
	r.events = nil
	return e
}

type staticTargets []Target

func (s *staticTargets) Targets(context.Context) []Target { return *s }

func TestManagerStateChangesAndSnapshots(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()

	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	rec := &recPitcher{}
	targets := staticTargets{targetFor(t, srv.URL+"/")}
	m := &Manager{
		Prober:     &Prober{Cluster: "c1", Client: NewHTTPClient(""), Timeout: 2 * time.Second, WarnDays: 14, ErrorDays: 3, Now: clock},
		Discoverer: &targets,
		Pitcher:    rec,
		Metrics:    NewMetrics(),
		Cluster:    "c1",
		System:     "endpoints-c1",
		Spec:       profile.ProbesSpec{SnapshotInterval: time.Hour},
		Now:        clock,
	}
	ctx := context.Background()

	// Round 1: all up, the first snapshot goes out at once.
	m.Round(ctx)
	ev := rec.take()
	if len(ev) != 1 || ev[0].EventType != "snapshot" || ev[0].Severity != SeveritySuccess || ev[0].Subsystem != "endpoints-c1" || !ev[0].Unthrottled {
		t.Fatalf("round 1: %+v", ev)
	}
	if p := ev[0].Payload; p["format"] != Format || p["target"] != "c1" || p["count"] != 0 {
		t.Errorf("snapshot payload = %v", p)
	}

	// Round 2: down once -> below the default failureThreshold of 2.
	status = http.StatusBadGateway
	now = now.Add(5 * time.Minute)
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Fatalf("round 2, first failure: %+v", ev)
	}
	// A success in between resets the count.
	status = http.StatusOK
	m.Round(ctx)
	status = http.StatusBadGateway
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Fatalf("failure after a success: %+v", ev)
	}

	// Second consecutive failure -> one opened event with the finding.
	m.Round(ctx)
	ev = rec.take()
	if len(ev) != 1 {
		t.Fatalf("round 2: %+v", ev)
	}
	e := ev[0]
	fp := Fingerprint("c1", KindEndpoint, "web", "127.0.0.1", CheckStatus)
	if e.EventType != "opened" || e.Severity != SeverityError || e.Fingerprint != fp || e.Kind != KindEndpoint ||
		e.Unthrottled || e.Key == "" || !strings.Contains(e.Title, "unexpected status 502") {
		t.Errorf("opened = %+v", e)
	}
	if items := e.Payload["items"].([]Finding); len(items) != 1 || items[0].Fingerprint != fp || items[0].Parent != "Ingress/web/shop" {
		t.Errorf("opened items = %+v", e.Payload["items"])
	}

	// Round 3: still down -> nothing.
	now = now.Add(5 * time.Minute)
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Errorf("round 3: %+v", ev)
	}

	// Round 4: up again, but the pitch fails -> retried in round 5.
	status = http.StatusOK
	rec.fail = true
	m.Round(ctx)
	rec.fail = false
	m.Round(ctx)
	ev = rec.take()
	if len(ev) != 1 || ev[0].EventType != "resolved" || ev[0].Severity != SeveritySuccess || ev[0].Fingerprint != fp {
		t.Fatalf("resolved: %+v", ev)
	}
	if items := ev[0].Payload["items"].([]Finding); len(items) != 0 {
		t.Errorf("resolved items = %+v", items)
	}

	// Down again, then an hour later: the snapshot holds the open finding.
	status = http.StatusInternalServerError
	m.Round(ctx)
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 1 || ev[0].EventType != "opened" {
		t.Fatalf("down again: %+v", ev)
	}
	now = now.Add(time.Hour)
	m.Round(ctx)
	ev = rec.take()
	if len(ev) != 1 || ev[0].EventType != "snapshot" || ev[0].Severity != SeverityError || ev[0].Payload["count"] != 1 {
		t.Fatalf("snapshot: %+v", ev)
	}
	if !strings.Contains(ev[0].Summary, "1 endpoints probed") {
		t.Errorf("snapshot summary %q", ev[0].Summary)
	}

	// The target disappears while down: its finding is dropped silently.
	targets = nil
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Errorf("target gone: %+v", ev)
	}
	if len(m.open) != 0 {
		t.Errorf("open = %v", m.open)
	}
}

// TestManagerSeedRound: a restart with an open problem sends no event, only
// the snapshot that carries it; its recovery is still reported.
func TestManagerSeedRound(t *testing.T) {
	status := http.StatusServiceUnavailable
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	rec := &recPitcher{}
	targets := staticTargets{targetFor(t, srv.URL)}
	m := &Manager{
		Prober:     &Prober{Cluster: "c1", Client: NewHTTPClient(""), Timeout: 2 * time.Second},
		Discoverer: &targets,
		Pitcher:    rec,
		Cluster:    "c1",
		Spec:       profile.ProbesSpec{SnapshotInterval: time.Hour},
	}
	ctx := context.Background()
	m.Round(ctx)
	ev := rec.take()
	if len(ev) != 1 || ev[0].EventType != "snapshot" || ev[0].Severity != SeverityError || ev[0].Payload["count"] != 1 {
		t.Fatalf("seed round: %+v", ev)
	}
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Fatalf("still down: %+v", ev)
	}
	status = http.StatusOK
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 1 || ev[0].EventType != "resolved" {
		t.Fatalf("recovered: %+v", ev)
	}
}

// TestManagerThreshold: failureThreshold delays Endpoint findings only.
func TestManagerThreshold(t *testing.T) {
	srv, cert := tlsServer(t)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	failing := false
	block := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if failing {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer block.Close()

	now := cert.NotAfter.Add(-5 * 24 * time.Hour) // certificate in the warning window
	three := 3
	rec := &recPitcher{}
	targets := staticTargets{}
	m := &Manager{
		Prober: &Prober{Cluster: "c1", Client: NewHTTPClient(""), Roots: roots, Timeout: 2 * time.Second,
			WarnDays: 14, ErrorDays: 3, Now: func() time.Time { return now }},
		Discoverer: &targets,
		Pitcher:    rec,
		Cluster:    "c1",
		Spec:       profile.ProbesSpec{SnapshotInterval: 1000 * time.Hour, FailureThreshold: &three},
		Now:        func() time.Time { return now },
	}
	ctx := context.Background()
	m.Round(ctx) // seed with no targets
	_ = rec.take()

	targets = staticTargets{targetFor(t, srv.URL), targetFor(t, block.URL)}
	targets[1].Name = "other"
	failing = true
	m.Round(ctx)
	ev := rec.take()
	if len(ev) != 1 || ev[0].Kind != KindCertificate || ev[0].EventType != "opened" {
		t.Fatalf("certificate opens at once: %+v", ev)
	}
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 0 {
		t.Fatalf("second failure of 3: %+v", ev)
	}
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 1 || ev[0].Kind != KindEndpoint || ev[0].EventType != "opened" {
		t.Fatalf("third failure: %+v", ev)
	}
	failing = false
	m.Round(ctx)
	if ev = rec.take(); len(ev) != 1 || ev[0].Kind != KindEndpoint || ev[0].EventType != "resolved" {
		t.Fatalf("first success resolves: %+v", ev)
	}
}

func TestManagerSeverityChange(t *testing.T) {
	rec := &recPitcher{}
	m := &Manager{Pitcher: rec, Cluster: "c1", Spec: profile.ProbesSpec{}}
	f := Finding{Fingerprint: "a", Kind: KindCertificate, Name: "x", Severity: SeverityWarning, Check: CheckExpiry, Texts: []string{"9 days"}}
	m.diff(map[string]openFinding{"a": {Finding: f, target: "t"}}, map[string]bool{"t": true})
	f.Texts = []string{"8 days"}
	m.diff(map[string]openFinding{"a": {Finding: f, target: "t"}}, map[string]bool{"t": true})
	f.Severity = SeverityError
	m.diff(map[string]openFinding{"a": {Finding: f, target: "t"}}, map[string]bool{"t": true})
	ev := rec.take()
	if len(ev) != 2 || ev[0].EventType != "opened" || ev[1].EventType != "changed" || ev[1].Severity != SeverityError {
		t.Errorf("events = %+v", ev)
	}
}

func TestMetrics(t *testing.T) {
	m := NewMetrics()
	notAfter := time.Unix(1900000000, 0)
	m.Set([]Result{
		{Target: Target{URL: "https://a.example.com/", Host: "a.example.com", Source: SourceIngress, Namespace: "web", Name: "shop"},
			Success: true, Duration: 120 * time.Millisecond, StatusCode: 200, CertNotAfter: notAfter},
		{Target: Target{URL: "http://b.example.com/", Host: "b.example.com", Source: SourceTarget, Name: "b"}},
	})
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{
		`probe_success{host="a.example.com",kind="Ingress",name="shop",namespace="web",target="https://a.example.com/"} 1`,
		`probe_success{host="b.example.com",kind="Target",name="b",namespace="",target="http://b.example.com/"} 0`,
		`probe_duration_seconds{host="a.example.com",kind="Ingress",name="shop",namespace="web",target="https://a.example.com/"} 0.12`,
		`probe_cert_expiry_timestamp_seconds{host="a.example.com",kind="Ingress",name="shop",namespace="web",target="https://a.example.com/"} 1.9e+09`,
		`probe_http_status_code{host="a.example.com",kind="Ingress",name="shop",namespace="web",target="https://a.example.com/"} 200`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("metrics miss %s", want)
		}
	}
	if strings.Contains(text, `probe_cert_expiry_timestamp_seconds{host="b.example.com"`) {
		t.Error("cert expiry for a plain HTTP target")
	}
}
