package profile

import (
	"strings"
	"testing"
	"time"
)

func TestLoadEndpointProbesProfile(t *testing.T) {
	p, err := Load("../../profiles/endpoint-probes.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	pr := p.Spec.Probes
	if !pr.Enabled || !pr.Discover.Ingress || !pr.Discover.HTTPRoute || len(pr.Targets) != 2 {
		t.Errorf("probes = %+v", pr)
	}
	if pr.Interval != 5*time.Minute || pr.Timeout != 5*time.Second || pr.SnapshotInterval != time.Hour {
		t.Errorf("intervals = %v %v %v", pr.Interval, pr.Timeout, pr.SnapshotInterval)
	}
	if !pr.Targets[1].InsecureSkipVerify || len(pr.Targets[1].ExpectedStatus) != 1 {
		t.Errorf("target = %+v", pr.Targets[1])
	}
	if got := p.ProbeSystemFor("c1"); got != "endpoints-c1" {
		t.Errorf("ProbeSystemFor = %q", got)
	}
}

func TestProbeDefaults(t *testing.T) {
	p := &K8sPitcherProfile{Spec: Spec{
		Pitcher: PitcherConfig{Addr: "http://omni/pitch"},
		Probes:  ProbesSpec{Enabled: true, Discover: ProbeDiscover{Ingress: true}},
	}}
	if err := validate(p); err != nil {
		t.Fatalf("validate: %v", err)
	}
	applyDefaults(p)
	pr := p.Spec.Probes
	if pr.Interval != DefaultProbeInterval || pr.Timeout != DefaultProbeTimeout || pr.SnapshotInterval != DefaultProbeSnapshotInterval {
		t.Errorf("durations = %+v", pr)
	}
	if pr.CertWarnDays != 14 || pr.CertErrorDays != 3 || pr.MetricsPort != "9090" || pr.Discover.HTTPRouteScheme != "https" {
		t.Errorf("defaults = %+v", pr)
	}
	if pr.Threshold() != 2 {
		t.Errorf("Threshold() = %d, want 2", pr.Threshold())
	}
	if !pr.Throttled() {
		t.Error("probes throttled by default")
	}
	p.Spec.Probes.System = "eps"
	if got := p.ProbeSystemFor("c1"); got != "eps" {
		t.Errorf("ProbeSystemFor = %q", got)
	}
}

func TestProbeValidation(t *testing.T) {
	base := func(pr ProbesSpec) *K8sPitcherProfile {
		pr.Enabled = true
		return &K8sPitcherProfile{Spec: Spec{Pitcher: PitcherConfig{Addr: "http://omni/pitch"}, Probes: pr}}
	}
	ing := ProbeDiscover{Ingress: true}
	zero, one := 0, 1
	for _, tc := range []struct {
		name string
		pr   ProbesSpec
		want string
	}{
		{"nothing to probe", ProbesSpec{}, "need discover.ingress"},
		{"threshold 0", ProbesSpec{Discover: ing, FailureThreshold: &zero}, "failureThreshold must be at least 1"},
		{"threshold 1", ProbesSpec{Discover: ing, FailureThreshold: &one}, ""},
		{"negative interval", ProbesSpec{Discover: ing, Interval: -time.Second}, "must not be negative"},
		{"negative days", ProbesSpec{Discover: ing, CertWarnDays: -1}, "must not be negative"},
		{"error above warn", ProbesSpec{Discover: ing, CertWarnDays: 5, CertErrorDays: 7}, "must not exceed"},
		{"error above default warn", ProbesSpec{Discover: ing, CertErrorDays: 20}, "must not exceed"},
		{"scheme", ProbesSpec{Discover: ProbeDiscover{HTTPRoute: true, HTTPRouteScheme: "ftp"}}, "httpRouteScheme"},
		{"resolver", ProbesSpec{Discover: ing, Resolver: "10.96.0.10"}, "resolver"},
		{"port", ProbesSpec{Discover: ing, MetricsPort: "http"}, "metricsPort"},
		{"status", ProbesSpec{Discover: ing, ExpectedStatus: []int{42}}, "expectedStatus"},
		{"target url", ProbesSpec{Targets: []ProbeTarget{{URL: "ftp://x"}}}, "targets[0].url"},
		{"target no host", ProbesSpec{Targets: []ProbeTarget{{URL: "https://"}}}, "targets[0].url"},
		{"target status", ProbesSpec{Targets: []ProbeTarget{{URL: "https://x", ExpectedStatus: []int{1000}}}}, "targets[0]"},
		{"ok targets only", ProbesSpec{Targets: []ProbeTarget{{URL: "https://x/healthz", ExpectedStatus: []int{200}}}}, ""},
		{"ok full", ProbesSpec{Discover: ProbeDiscover{Ingress: true, HTTPRoute: true, HTTPRouteScheme: "http"}, Resolver: "10.96.0.10:53", MetricsPort: "9100", CertWarnDays: 30, CertErrorDays: 7}, ""},
	} {
		err := validate(base(tc.pr))
		if tc.want == "" && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s: err %v, want %q", tc.name, err, tc.want)
		}
	}
	// Disabled probes are not validated and do not count as a source.
	p := &K8sPitcherProfile{Spec: Spec{Pitcher: PitcherConfig{Addr: "http://omni/pitch"}, Probes: ProbesSpec{Interval: -1}}}
	if err := validate(p); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Errorf("disabled probes: err %v", err)
	}
}
