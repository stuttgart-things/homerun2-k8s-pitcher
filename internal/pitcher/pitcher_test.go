package pitcher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

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

func TestNewHTTPK8sPitcher(t *testing.T) {
	p := NewHTTPK8sPitcher("https://pitcher.example.com", "my-token", true, "test-cluster")

	if p.Addr != "https://pitcher.example.com" {
		t.Errorf("Addr = %q, want %q", p.Addr, "https://pitcher.example.com")
	}
	if p.Token != "my-token" {
		t.Errorf("Token = %q, want %q", p.Token, "my-token")
	}
	if !p.Insecure {
		t.Error("Insecure = false, want true")
	}
	if p.System != "test-cluster" {
		t.Errorf("System = %q, want %q", p.System, "test-cluster")
	}
}

func TestHTTPK8sPitcherPitch(t *testing.T) {
	var gotBody []byte
	var gotToken string
	var gotContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	p := NewHTTPK8sPitcher(server.URL, "test-token", false, "test-cluster")

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

	if gotToken != "Bearer test-token" {
		t.Errorf("Authorization = %q, want %q", gotToken, "Bearer test-token")
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want %q", gotContentType, "application/json")
	}
	if len(gotBody) == 0 {
		t.Fatal("request body is empty")
	}

	body := string(gotBody)
	for _, want := range []string{`"author":"k8s-pitcher"`, `"system":"test-cluster"`, `"severity":"info"`} {
		if !contains(body, want) {
			t.Errorf("body missing %q\nbody: %s", want, body)
		}
	}
}

func TestHTTPK8sPitcherPitchServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	p := NewHTTPK8sPitcher(server.URL, "token", false, "test")

	event := K8sEvent{
		Kind:      "Node",
		EventType: "snapshot",
		Name:      "node-1",
		Timestamp: "2026-03-09T12:00:00Z",
		Cluster:   "test",
	}

	err := p.Pitch(event)
	if err == nil {
		t.Fatal("Pitch() expected error for 500 response, got nil")
	}
}

func TestHTTPK8sPitcherHealthCheck(t *testing.T) {
	var gotToken string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewHTTPK8sPitcher(server.URL, "hc-token", false, "test-cluster")

	if err := p.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck() error: %v", err)
	}
	if gotToken != "Bearer hc-token" {
		t.Errorf("Authorization = %q, want %q", gotToken, "Bearer hc-token")
	}
}

func TestHTTPK8sPitcherHealthCheckFail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewHTTPK8sPitcher(server.URL, "bad-token", false, "test")

	err := p.HealthCheck(context.Background())
	if err == nil {
		t.Fatal("HealthCheck() expected error for 401 response, got nil")
	}
}
