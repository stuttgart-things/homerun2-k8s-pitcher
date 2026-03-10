package profile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	yaml := `
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: test-cluster
spec:
  redis:
    addr: redis.example.com
    port: "6379"
    stream: k8s-events
    password: changeme
  auth:
    token: mytoken
  collectors:
    - kind: Node
      interval: 60s
    - kind: Pod
      namespace: "*"
      interval: 30s
  informers:
    - group: ""
      version: v1
      resource: pods
      namespace: "*"
      events: [add, update, delete]
    - group: apps
      version: v1
      resource: deployments
      namespace: homerun2
      events: [add, update]
`
	path := writeTempFile(t, yaml)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if p.Metadata.Name != "test-cluster" {
		t.Errorf("metadata.name = %q, want %q", p.Metadata.Name, "test-cluster")
	}
	if p.Spec.Redis.Addr != "redis.example.com" {
		t.Errorf("redis.addr = %q, want %q", p.Spec.Redis.Addr, "redis.example.com")
	}
	if p.Spec.Redis.Stream != "k8s-events" {
		t.Errorf("redis.stream = %q, want %q", p.Spec.Redis.Stream, "k8s-events")
	}
	if p.Spec.Redis.Password != "changeme" {
		t.Errorf("redis.password = %q, want %q", p.Spec.Redis.Password, "changeme")
	}
	if p.Spec.Auth.Token != "mytoken" {
		t.Errorf("auth.token = %q, want %q", p.Spec.Auth.Token, "mytoken")
	}
	if len(p.Spec.Collectors) != 2 {
		t.Fatalf("collectors count = %d, want 2", len(p.Spec.Collectors))
	}
	if p.Spec.Collectors[0].Kind != "Node" {
		t.Errorf("collectors[0].kind = %q, want %q", p.Spec.Collectors[0].Kind, "Node")
	}
	if p.Spec.Collectors[0].Interval != 60*time.Second {
		t.Errorf("collectors[0].interval = %v, want 60s", p.Spec.Collectors[0].Interval)
	}
	if p.Spec.Collectors[1].Namespace != "*" {
		t.Errorf("collectors[1].namespace = %q, want %q", p.Spec.Collectors[1].Namespace, "*")
	}
	if len(p.Spec.Informers) != 2 {
		t.Fatalf("informers count = %d, want 2", len(p.Spec.Informers))
	}
	if p.Spec.Informers[0].Resource != "pods" {
		t.Errorf("informers[0].resource = %q, want %q", p.Spec.Informers[0].Resource, "pods")
	}
	if p.Spec.Informers[1].Namespace != "homerun2" {
		t.Errorf("informers[1].namespace = %q, want %q", p.Spec.Informers[1].Namespace, "homerun2")
	}
}

func TestLoadWithSecretRefs(t *testing.T) {
	yaml := `
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: prod-cluster
spec:
  redis:
    addr: redis.prod.svc
    stream: k8s-events
    passwordFrom:
      secretKeyRef:
        name: redis-credentials
        namespace: homerun2
        key: password
  auth:
    tokenFrom:
      secretKeyRef:
        name: pitcher-auth
        namespace: homerun2
        key: token
  collectors:
    - kind: Event
      namespace: "*"
      interval: 15s
`
	path := writeTempFile(t, yaml)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if p.Spec.Redis.PasswordFrom == nil {
		t.Fatal("redis.passwordFrom is nil")
	}
	if p.Spec.Redis.PasswordFrom.SecretKeyRef.Name != "redis-credentials" {
		t.Errorf("passwordFrom.name = %q, want %q", p.Spec.Redis.PasswordFrom.SecretKeyRef.Name, "redis-credentials")
	}
	if p.Spec.Auth.TokenFrom == nil {
		t.Fatal("auth.tokenFrom is nil")
	}
	if p.Spec.Auth.TokenFrom.SecretKeyRef.Key != "token" {
		t.Errorf("tokenFrom.key = %q, want %q", p.Spec.Auth.TokenFrom.SecretKeyRef.Key, "token")
	}
	// Default port applied
	if p.Spec.Redis.Port != "6379" {
		t.Errorf("redis.port = %q, want %q (default)", p.Spec.Redis.Port, "6379")
	}
}

func TestLoadDefaultNamespace(t *testing.T) {
	yaml := `
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: test
spec:
  redis:
    addr: localhost
    stream: test
  informers:
    - group: ""
      version: v1
      resource: nodes
      events: [add]
`
	path := writeTempFile(t, yaml)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if p.Spec.Informers[0].Namespace != "*" {
		t.Errorf("informers[0].namespace = %q, want %q (default)", p.Spec.Informers[0].Namespace, "*")
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "missing both redis and pitcher addr",
			yaml: `
spec:
  redis:
    stream: test
  collectors:
    - kind: Node
      interval: 10s
`,
		},
		{
			name: "missing redis stream",
			yaml: `
spec:
  redis:
    addr: localhost
  collectors:
    - kind: Node
      interval: 10s
`,
		},
		{
			name: "redis and pitcher mutually exclusive",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
  pitcher:
    addr: https://pitcher.example.com
  collectors:
    - kind: Node
      interval: 10s
`,
		},
		{
			name: "no collectors or informers",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
`,
		},
		{
			name: "informer missing version",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
  informers:
    - resource: pods
      events: [add]
`,
		},
		{
			name: "informer missing events",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
  informers:
    - version: v1
      resource: pods
`,
		},
		{
			name: "collector missing kind",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
  collectors:
    - interval: 10s
`,
		},
		{
			name: "collector zero interval",
			yaml: `
spec:
  redis:
    addr: localhost
    stream: test
  collectors:
    - kind: Node
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempFile(t, tt.yaml)
			_, err := Load(path)
			if err == nil {
				t.Error("Load() expected error, got nil")
			}
		})
	}
}

func TestLoadPitcherProfile(t *testing.T) {
	yaml := `
apiVersion: homerun2.sthings.io/v1alpha1
kind: K8sPitcherProfile
metadata:
  name: pitcher-cluster
spec:
  pitcher:
    addr: https://pitcher.example.com
    insecure: true
  auth:
    token: mytoken
  collectors:
    - kind: Node
      interval: 60s
  informers:
    - group: ""
      version: v1
      resource: pods
      namespace: "*"
      events: [add, update, delete]
`
	path := writeTempFile(t, yaml)
	p, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if p.Spec.Pitcher.Addr != "https://pitcher.example.com" {
		t.Errorf("pitcher.addr = %q, want %q", p.Spec.Pitcher.Addr, "https://pitcher.example.com")
	}
	if !p.Spec.Pitcher.Insecure {
		t.Error("pitcher.insecure = false, want true")
	}
	if p.Spec.Auth.Token != "mytoken" {
		t.Errorf("auth.token = %q, want %q", p.Spec.Auth.Token, "mytoken")
	}
	if p.Spec.Redis.Addr != "" {
		t.Errorf("redis.addr should be empty, got %q", p.Spec.Redis.Addr)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
