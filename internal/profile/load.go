package profile

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a K8sPitcherProfile from the given YAML file path.
func Load(path string) (*K8sPitcherProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading profile %s: %w", path, err)
	}

	var p K8sPitcherProfile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing profile %s: %w", path, err)
	}

	if err := validate(&p); err != nil {
		return nil, fmt.Errorf("validating profile %s: %w", path, err)
	}

	applyDefaults(&p)
	return &p, nil
}

func validate(p *K8sPitcherProfile) error {
	hasRedis := p.Spec.Redis.Addr != ""
	hasPitcher := p.Spec.Pitcher.Addr != ""

	if !hasRedis && !hasPitcher {
		return fmt.Errorf("either spec.redis.addr or spec.pitcher.addr is required")
	}
	if hasRedis && hasPitcher {
		return fmt.Errorf("spec.redis and spec.pitcher are mutually exclusive")
	}
	if hasRedis && p.Spec.Redis.Stream == "" {
		return fmt.Errorf("spec.redis.stream is required")
	}
	// The webhook server runs on its own, so a profile that only receives
	// Flux events is valid (#80).
	if len(p.Spec.Collectors) == 0 && len(p.Spec.Informers) == 0 && !p.Spec.Webhook.Enabled && !p.Spec.Probes.Enabled {
		return fmt.Errorf("at least one collector, informer, an enabled webhook or enabled probes must be defined")
	}
	for i, inf := range p.Spec.Informers {
		if inf.Version == "" {
			return fmt.Errorf("spec.informers[%d].version is required", i)
		}
		if inf.Resource == "" {
			return fmt.Errorf("spec.informers[%d].resource is required", i)
		}
		if len(inf.Events) == 0 {
			return fmt.Errorf("spec.informers[%d].events must not be empty", i)
		}
		switch inf.Updates {
		case "", UpdatesConditions, UpdatesFlips, UpdatesGeneration, UpdatesAll:
		default:
			return fmt.Errorf("spec.informers[%d].updates must be conditions, flips, generation or all (got %q)", i, inf.Updates)
		}
		switch strings.ToLower(inf.DeleteSeverity) {
		case "", "info", "success", "warning", "error", "critical":
		default:
			return fmt.Errorf("spec.informers[%d].deleteSeverity must be info, success, warning, error or critical (got %q)", i, inf.DeleteSeverity)
		}
		if err := validateEventFilter(inf); err != nil {
			return fmt.Errorf("spec.informers[%d]: %w", i, err)
		}
		if err := validateConditionFilter(inf); err != nil {
			return fmt.Errorf("spec.informers[%d]: %w", i, err)
		}
	}
	if p.Spec.Cooldown < 0 {
		return fmt.Errorf("spec.cooldown must not be negative")
	}
	if rl := p.Spec.RateLimit; rl != nil {
		if rl.Max <= 0 {
			return fmt.Errorf("spec.rateLimit.max must be positive")
		}
		if rl.Per <= 0 {
			return fmt.Errorf("spec.rateLimit.per must be positive")
		}
	}
	for i, col := range p.Spec.Collectors {
		switch {
		case col.Kind == "" && col.Resource == "":
			return fmt.Errorf("spec.collectors[%d]: kind or resource is required", i)
		case col.Kind != "" && col.Resource != "":
			return fmt.Errorf("spec.collectors[%d]: kind and resource are mutually exclusive", i)
		case col.Resource != "" && col.Version == "":
			return fmt.Errorf("spec.collectors[%d]: resource needs a version", i)
		case col.Kind == "Inventory" && col.Mode != "":
			return fmt.Errorf("spec.collectors[%d]: kind Inventory takes no mode (it is always one snapshot)", i)
		}
		if col.Mode != "" && col.Mode != CollectorItems && col.Mode != CollectorSnapshot {
			return fmt.Errorf("spec.collectors[%d].mode must be items or snapshot, got %q", i, col.Mode)
		}
		if col.Interval <= 0 {
			return fmt.Errorf("spec.collectors[%d].interval must be positive", i)
		}
	}
	if err := validateProbes(p.Spec.Probes); err != nil {
		return fmt.Errorf("spec.probes: %w", err)
	}
	return nil
}

// validateProbes checks spec.probes if enabled.
func validateProbes(pr ProbesSpec) error {
	if !pr.Enabled {
		return nil
	}
	if !pr.Discover.Ingress && !pr.Discover.HTTPRoute && len(pr.Targets) == 0 {
		return fmt.Errorf("enabled probes need discover.ingress, discover.httpRoute or targets")
	}
	if pr.Interval < 0 || pr.Timeout < 0 || pr.SnapshotInterval < 0 {
		return fmt.Errorf("interval, timeout and snapshotInterval must not be negative")
	}
	if pr.FailureThreshold != nil && *pr.FailureThreshold < 1 {
		return fmt.Errorf("failureThreshold must be at least 1 (got %d)", *pr.FailureThreshold)
	}
	if pr.CertWarnDays < 0 || pr.CertErrorDays < 0 {
		return fmt.Errorf("certWarnDays and certErrorDays must not be negative")
	}
	warn := pr.CertWarnDays
	if warn == 0 {
		warn = DefaultCertWarnDays
	}
	if pr.CertErrorDays > warn {
		return fmt.Errorf("certErrorDays (%d) must not exceed certWarnDays (%d)", pr.CertErrorDays, warn)
	}
	switch pr.Discover.HTTPRouteScheme {
	case "", "http", "https":
	default:
		return fmt.Errorf("discover.httpRouteScheme must be http or https (got %q)", pr.Discover.HTTPRouteScheme)
	}
	if pr.Resolver != "" {
		if _, _, err := net.SplitHostPort(pr.Resolver); err != nil {
			return fmt.Errorf("resolver must be host:port (got %q)", pr.Resolver)
		}
	}
	if pr.MetricsPort != "" {
		if n, err := strconv.Atoi(pr.MetricsPort); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("metricsPort must be a port number (got %q)", pr.MetricsPort)
		}
	}
	if err := validateStatus(pr.ExpectedStatus); err != nil {
		return err
	}
	for i, t := range pr.Targets {
		u, err := url.Parse(t.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return fmt.Errorf("targets[%d].url must be an http or https URL (got %q)", i, t.URL)
		}
		if err := validateStatus(t.ExpectedStatus); err != nil {
			return fmt.Errorf("targets[%d]: %w", i, err)
		}
	}
	return nil
}

// validateStatus accepts only HTTP status codes.
func validateStatus(codes []int) error {
	for _, c := range codes {
		if c < 100 || c > 599 {
			return fmt.Errorf("expectedStatus must hold HTTP status codes (got %d)", c)
		}
	}
	return nil
}

// validateEventFilter allows types, reasons and excludeReasons only on an
// informer of Kubernetes Events, and either reasons or excludeReasons.
func validateEventFilter(inf InformerSpec) error {
	if !inf.HasEventFilter() {
		return nil
	}
	if inf.Resource != "events" {
		return fmt.Errorf("types, reasons and excludeReasons are only allowed with resource events (got %q)", inf.Resource)
	}
	if len(inf.Reasons) > 0 && len(inf.ExcludeReasons) > 0 {
		return fmt.Errorf("reasons and excludeReasons are mutually exclusive")
	}
	for _, t := range inf.Types {
		if t != "Normal" && t != "Warning" {
			return fmt.Errorf("types must be Normal or Warning (got %q)", t)
		}
	}
	return nil
}

// validateConditionFilter allows ignoreStatuses, ignoreReasons and
// minDuration only on an informer with updates: conditions or flips that
// watches updates.
func validateConditionFilter(inf InformerSpec) error {
	if !inf.HasConditionFilter() {
		return nil
	}
	if inf.Updates != "" && !inf.WatchesFlips() {
		return fmt.Errorf("ignoreStatuses, ignoreReasons and minDuration are only allowed with updates: conditions or flips (got %q)", inf.Updates)
	}
	if !slices.Contains(inf.Events, "update") {
		return fmt.Errorf("ignoreStatuses, ignoreReasons and minDuration need update in events")
	}
	if inf.MinDuration < 0 {
		return fmt.Errorf("minDuration must not be negative")
	}
	for _, st := range inf.IgnoreStatuses {
		if st != "False" && st != "Unknown" {
			return fmt.Errorf("ignoreStatuses must be False or Unknown (got %q)", st)
		}
	}
	return nil
}

func applyDefaults(p *K8sPitcherProfile) {
	if p.Spec.Redis.Port == "" {
		p.Spec.Redis.Port = "6379"
	}
	if pr := &p.Spec.Probes; pr.Enabled {
		if pr.Interval == 0 {
			pr.Interval = DefaultProbeInterval
		}
		if pr.Timeout == 0 {
			pr.Timeout = DefaultProbeTimeout
		}
		if pr.SnapshotInterval == 0 {
			pr.SnapshotInterval = DefaultProbeSnapshotInterval
		}
		if pr.CertWarnDays == 0 {
			pr.CertWarnDays = DefaultCertWarnDays
		}
		if pr.CertErrorDays == 0 {
			pr.CertErrorDays = min(DefaultCertErrorDays, pr.CertWarnDays)
		}
		if pr.MetricsPort == "" {
			pr.MetricsPort = DefaultMetricsPort
		}
		if pr.Discover.HTTPRouteScheme == "" {
			pr.Discover.HTTPRouteScheme = "https"
		}
	}
	for i := range p.Spec.Informers {
		if p.Spec.Informers[i].Namespace == "" {
			p.Spec.Informers[i].Namespace = "*"
		}
		if p.Spec.Informers[i].Updates == "" {
			p.Spec.Informers[i].Updates = UpdatesConditions
		}
	}
}

// ProbeSystemFor returns spec.probes.system, or endpoints-<cluster>.
func (p *K8sPitcherProfile) ProbeSystemFor(cluster string) string {
	if p.Spec.Probes.System != "" {
		return p.Spec.Probes.System
	}
	return "endpoints-" + cluster
}

// SystemFor returns spec.system, or k8s-<cluster>.
func (p *K8sPitcherProfile) SystemFor(cluster string) string {
	if p.Spec.System != "" {
		return p.Spec.System
	}
	return "k8s-" + cluster
}
