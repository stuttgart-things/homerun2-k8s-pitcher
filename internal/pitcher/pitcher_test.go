package pitcher

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

func TestFileK8sPitcher(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	p := &FileK8sPitcher{Path: path}

	if err := p.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}

	event := K8sEvent{
		Kind:      "Pod",
		EventType: "add",
		Namespace: "default",
		Name:      "nginx-abc123",
		Object:    map[string]any{"apiVersion": "v1", "kind": "Pod"},
		Timestamp: "2026-03-09T12:00:00Z",
		Cluster:   "test-cluster",
	}

	if err := p.Pitch(event); err != nil {
		t.Fatalf("Pitch() error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}

	evt, ok := entry["event"].(map[string]any)
	if !ok {
		t.Fatal("event field missing or wrong type")
	}
	if evt["kind"] != "Pod" {
		t.Errorf("kind = %v, want Pod", evt["kind"])
	}
	if evt["eventType"] != "add" {
		t.Errorf("eventType = %v, want add", evt["eventType"])
	}
	if evt["name"] != "nginx-abc123" {
		t.Errorf("name = %v, want nginx-abc123", evt["name"])
	}
}

func TestFileK8sPitcherMultiple(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.log")

	p := &FileK8sPitcher{Path: path}

	for i := 0; i < 3; i++ {
		event := K8sEvent{
			Kind:      "Node",
			EventType: "snapshot",
			Name:      "node-1",
			Timestamp: "2026-03-09T12:00:00Z",
			Cluster:   "test",
		}
		if err := p.Pitch(event); err != nil {
			t.Fatalf("Pitch() error on iteration %d: %v", i, err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}

	// Should have 3 JSON lines
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("got %d lines, want 3", lines)
	}
}

func TestSeverityFor(t *testing.T) {
	tests := []struct {
		eventType string
		want      string
	}{
		{"add", "info"},
		{"update", "info"},
		{"delete", "warning"},
		{"snapshot", "info"},
	}
	for _, tt := range tests {
		got := severityFor(tt.eventType)
		if got != tt.want {
			t.Errorf("severityFor(%q) = %q, want %q", tt.eventType, got, tt.want)
		}
	}
}

func TestNewRedisK8sPitcher(t *testing.T) {
	cfg := profile.RedisConfig{
		Addr:     "redis.example.com",
		Port:     "6380",
		Password: "secret",
		Stream:   "k8s-events",
	}

	p := NewRedisK8sPitcher(cfg, "my-cluster")

	if p.RedisConfig.Addr != "redis.example.com" {
		t.Errorf("Addr = %q, want %q", p.RedisConfig.Addr, "redis.example.com")
	}
	if p.RedisConfig.Port != "6380" {
		t.Errorf("Port = %q, want %q", p.RedisConfig.Port, "6380")
	}
	if p.RedisConfig.Password != "secret" {
		t.Errorf("Password = %q, want %q", p.RedisConfig.Password, "secret")
	}
	if p.RedisConfig.Stream != "k8s-events" {
		t.Errorf("Stream = %q, want %q", p.RedisConfig.Stream, "k8s-events")
	}
	if p.System != "my-cluster" {
		t.Errorf("System = %q, want %q", p.System, "my-cluster")
	}
}
