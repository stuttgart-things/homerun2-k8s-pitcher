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

type InformerSpec struct {
	Group     string   `yaml:"group"`
	Version   string   `yaml:"version"`
	Resource  string   `yaml:"resource"`
	Namespace string   `yaml:"namespace"`
	Events    []string `yaml:"events"`
}
