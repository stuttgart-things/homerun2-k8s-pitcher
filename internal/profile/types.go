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
	// Cooldown drops a repeat of the same Kubernetes Event (involved object
	// and reason) or the same condition flip within this window; the next
	// one after it carries the number of repeats. 0 (default) is off.
	Cooldown time.Duration `yaml:"cooldown"`
	// RateLimit caps the messages of all sources; off if unset.
	RateLimit *RateLimit `yaml:"rateLimit"`
}

// RateLimit allows Max messages per window. Messages above it are dropped
// and, once per window, replaced by a single "N events suppressed" warning.
type RateLimit struct {
	Max int           `yaml:"max"`
	Per time.Duration `yaml:"per"`
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
	// Kind is one of the built-in kinds (Node, Pod, Event, or Inventory: one
	// snapshot of versions, images and releases, #108). Instead, any resource
	// can be named by group/version/resource (#103).
	Kind      string        `yaml:"kind"`
	Group     string        `yaml:"group"`
	Version   string        `yaml:"version"`
	Resource  string        `yaml:"resource"`
	Namespace string        `yaml:"namespace"`
	Interval  time.Duration `yaml:"interval"`
	// Mode is items (default: one message per object) or snapshot (one
	// message per run, the objects as its payload, #103).
	Mode string `yaml:"mode"`
	// Throttle false bypasses spec.cooldown and spec.rateLimit (#102).
	Throttle *bool `yaml:"throttle"`
	// System overrides spec.system for this collector's messages (#106).
	System string `yaml:"system"`
}

// Collector modes.
const (
	CollectorItems    = "items"
	CollectorSnapshot = "snapshot"
)

// Throttled reports whether the collector's events go through cooldown and
// rate limit (default true).
func (c CollectorSpec) Throttled() bool { return c.Throttle == nil || *c.Throttle }

// Update filters of an informer.
const (
	// UpdatesConditions pitches a generation change or a flipped Ready or
	// Synced condition (default).
	UpdatesConditions = "conditions"
	// UpdatesFlips pitches only a flipped Ready or Synced condition, no
	// generation change (#93).
	UpdatesFlips = "flips"
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
	// System overrides spec.system for this informer's messages (#106), e.g.
	// k8sgpt-<cluster> for findings the central omni-pitcher routes to the
	// archive while the profile's other informers keep k8s-<cluster>.
	System string `yaml:"system"`
	// Throttle false bypasses spec.cooldown and spec.rateLimit for this
	// informer (#102): for archive-only data, where a dropped event is lost
	// data, not a quieter channel. Default true.
	Throttle *bool `yaml:"throttle"`
	// IncludeObject sends the object along as the message payload (nested
	// JSON in the Redis document, without metadata.managedFields).
	IncludeObject bool `yaml:"includeObject"`
	// Types, Reasons and ExcludeReasons filter Kubernetes Events (resource
	// events only): Types by type (Normal, Warning), Reasons as an allowlist
	// and ExcludeReasons as a denylist of reasons. Unset passes every Event.
	Types          []string `yaml:"types"`
	Reasons        []string `yaml:"reasons"`
	ExcludeReasons []string `yaml:"excludeReasons"`
	// IgnoreStatuses, IgnoreReasons and MinDuration filter condition flips
	// (updates: conditions only, #89). A flip to an ignored status or
	// reason is not pitched, and the next flip is compared against the last
	// reported state. A failure (a condition that is not True) is pitched
	// only once it has lasted MinDuration; its recovery only if the failure
	// was pitched. Unset keeps the plain flip behaviour.
	IgnoreStatuses []string      `yaml:"ignoreStatuses"`
	IgnoreReasons  []string      `yaml:"ignoreReasons"`
	MinDuration    time.Duration `yaml:"minDuration"`
}

// Throttled reports whether the informer's events go through cooldown and
// rate limit (default true).
func (s InformerSpec) Throttled() bool { return s.Throttle == nil || *s.Throttle }

// HasConditionFilter reports whether the informer filters condition flips.
func (s InformerSpec) HasConditionFilter() bool {
	return len(s.IgnoreStatuses) > 0 || len(s.IgnoreReasons) > 0 || s.MinDuration != 0
}

// WatchesFlips reports whether the informer pitches condition flips
// (updates: conditions or flips).
func (s InformerSpec) WatchesFlips() bool {
	return s.Updates == UpdatesConditions || s.Updates == UpdatesFlips
}

// HasEventFilter reports whether the informer filters Kubernetes Events.
func (s InformerSpec) HasEventFilter() bool {
	return len(s.Types) > 0 || len(s.Reasons) > 0 || len(s.ExcludeReasons) > 0
}
