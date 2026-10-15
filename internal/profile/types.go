package profile

import "time"

// K8sPitcherProfile defines what resources to watch/collect and where to pitch events.
type K8sPitcherProfile struct {
	APIVersion string   `yaml:"apiVersion"`
	Kind       string   `yaml:"kind"`
	Metadata   Metadata `yaml:"metadata"`
	Spec       Spec     `yaml:"spec"`
}

type Metadata struct {
	Name string `yaml:"name"`
}

type Spec struct {
	// Cluster names the watched cluster in messages; default: the kubeconfig
	// context, or "in-cluster".
	Cluster string `yaml:"cluster"`
	// System is the homerun system of every message; default k8s-<cluster>.
	// Flux webhook events keep the system flux.
	System     string          `yaml:"system"`
	Redis      RedisConfig     `yaml:"redis"`
	Pitcher    PitcherConfig   `yaml:"pitcher"`
	Auth       AuthConfig      `yaml:"auth"`
	Webhook    WebhookConfig   `yaml:"webhook"`
	Collectors []CollectorSpec `yaml:"collectors"`
	Informers  []InformerSpec  `yaml:"informers"`
}

type WebhookConfig struct {
	Enabled bool   `yaml:"enabled"`
	Port    string `yaml:"port"`
	HMACKey string `yaml:"hmacKey"`
}

type PitcherConfig struct {
	Addr     string `yaml:"addr"`
	Insecure bool   `yaml:"insecure"`
}

type RedisConfig struct {
	Addr         string        `yaml:"addr"`
	Port         string        `yaml:"port"`
	Stream       string        `yaml:"stream"`
	Password     string        `yaml:"password"`
	PasswordFrom *SecretKeyRef `yaml:"passwordFrom"`
}

type AuthConfig struct {
	Token     string        `yaml:"token"`
	TokenFrom *SecretKeyRef `yaml:"tokenFrom"`
}

type SecretKeyRef struct {
	SecretKeyRef SecretRef `yaml:"secretKeyRef"`
}

type SecretRef struct {
	Name      string `yaml:"name"`
	Namespace string `yaml:"namespace"`
	Key       string `yaml:"key"`
}

type CollectorSpec struct {
	Kind      string        `yaml:"kind"`
	Namespace string        `yaml:"namespace"`
	Interval  time.Duration `yaml:"interval"`
}

// Update filters of an informer.
const (
	// UpdatesConditions pitches a generation change or a flipped Ready or
	// Synced condition (default).
	UpdatesConditions = "conditions"
	// UpdatesGeneration pitches only a metadata.generation change.
	UpdatesGeneration = "generation"
	// UpdatesAll pitches every update, status-only ones included.
	UpdatesAll = "all"
)

type InformerSpec struct {
	Group     string   `yaml:"group"`
	Version   string   `yaml:"version"`
	Resource  string   `yaml:"resource"`
	Namespace string   `yaml:"namespace"`
	Events    []string `yaml:"events"`
	// PitchInitialList pitches the objects listed at start-up as adds. Off
	// by default: every restart would re-pitch the whole inventory.
	PitchInitialList bool `yaml:"pitchInitialList"`
	// Updates is conditions (default), generation or all.
	Updates string `yaml:"updates"`
	// DeleteSeverity overrides the severity of deletes (default WARNING),
	// e.g. info for claims that are deleted on purpose.
	DeleteSeverity string `yaml:"deleteSeverity"`
	// IncludeObject appends the whole object as JSON to the summary.
	IncludeObject bool `yaml:"includeObject"`
}
