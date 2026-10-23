package profile

import (
	"fmt"
	"os"
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
	if len(p.Spec.Collectors) == 0 && len(p.Spec.Informers) == 0 && !p.Spec.Webhook.Enabled {
		return fmt.Errorf("at least one collector, informer or an enabled webhook must be defined")
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
		case "", UpdatesConditions, UpdatesGeneration, UpdatesAll:
		default:
			return fmt.Errorf("spec.informers[%d].updates must be conditions, generation or all (got %q)", i, inf.Updates)
		}
		switch strings.ToLower(inf.DeleteSeverity) {
		case "", "info", "success", "warning", "error", "critical":
		default:
			return fmt.Errorf("spec.informers[%d].deleteSeverity must be info, success, warning, error or critical (got %q)", i, inf.DeleteSeverity)
		}
		if err := validateEventFilter(inf); err != nil {
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
		if col.Kind == "" {
			return fmt.Errorf("spec.collectors[%d].kind is required", i)
		}
		if col.Interval <= 0 {
			return fmt.Errorf("spec.collectors[%d].interval must be positive", i)
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

func applyDefaults(p *K8sPitcherProfile) {
	if p.Spec.Redis.Port == "" {
		p.Spec.Redis.Port = "6379"
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

// SystemFor returns spec.system, or k8s-<cluster>.
func (p *K8sPitcherProfile) SystemFor(cluster string) string {
	if p.Spec.System != "" {
		return p.Spec.System
	}
	return "k8s-" + cluster
}
