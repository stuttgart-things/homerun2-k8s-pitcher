package pitcher

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	homerun "github.com/stuttgart-things/homerun-library/v3"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// K8sEvent represents a Kubernetes resource event to be pitched.
type K8sEvent struct {
	Kind      string         `json:"kind"`
	EventType string         `json:"eventType"` // add, update, delete, snapshot
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Object    map[string]any `json:"object"`
	Summary   string         `json:"summary,omitempty"`  // human-readable text summary
	Severity  string         `json:"severity,omitempty"` // override severity (e.g. from Flux events)
	Subsystem string         `json:"subsystem,omitempty"` // source subsystem (e.g. "flux", "kubernetes")
	Timestamp string         `json:"timestamp"`
	Cluster   string         `json:"cluster"`
}

// K8sPitcher pitches Kubernetes events to Redis Streams via homerun-library.
type K8sPitcher interface {
	Pitch(event K8sEvent) error
	HealthCheck(ctx context.Context) error
}

// RedisK8sPitcher implements K8sPitcher using Redis Streams.
type RedisK8sPitcher struct {
	RedisConfig homerun.RedisConfig
	System      string // cluster identity used as system name
}

// NewRedisK8sPitcher creates a pitcher from profile Redis config.
func NewRedisK8sPitcher(cfg profile.RedisConfig, clusterName string) *RedisK8sPitcher {
	return &RedisK8sPitcher{
		RedisConfig: homerun.RedisConfig{
			Addr:     cfg.Addr,
			Port:     cfg.Port,
			Password: cfg.Password,
			Stream:   cfg.Stream,
		},
		System: clusterName,
	}
}

func (p *RedisK8sPitcher) HealthCheck(ctx context.Context) error {
	client := redis.NewClient(&redis.Options{
		Addr:     p.RedisConfig.Addr + ":" + p.RedisConfig.Port,
		Password: p.RedisConfig.Password,
	})
	defer func() { _ = client.Close() }()

	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("redis ping failed: %w", err)
	}
	return nil
}

func (p *RedisK8sPitcher) Pitch(event K8sEvent) error {
	message := event.Summary
	if message == "" {
		objectJSON, err := json.Marshal(event.Object)
		if err != nil {
			return fmt.Errorf("marshaling object: %w", err)
		}
		message = string(objectJSON)
	}

	sev := event.Severity
	if sev == "" {
		sev = severityFor(event.EventType)
	}

	subsystem := event.Subsystem
	if subsystem == "" {
		subsystem = "kubernetes"
	}

	msg := homerun.Message{
		Title:     fmt.Sprintf("%s/%s %s", event.Kind, event.Name, event.EventType),
		Message:   message,
		Severity:  sev,
		Author:    "k8s-pitcher-" + p.System,
		Timestamp: event.Timestamp,
		System:    subsystem,
		Tags:      fmt.Sprintf("k8s,%s,%s,%s", event.Kind, event.EventType, event.Namespace),
	}

	objectID, streamID, err := homerun.EnqueueMessageInRedisStreams(msg, p.RedisConfig)
	if err != nil {
		return fmt.Errorf("enqueuing to redis stream: %w", err)
	}

	slog.Debug("event pitched",
		"kind", event.Kind,
		"name", event.Name,
		"eventType", event.EventType,
		"objectID", objectID,
		"stream", streamID,
	)
	return nil
}

func severityFor(eventType string) string {
	switch eventType {
	case "add", "update":
		return "SUCCESS"
	case "delete":
		return "WARNING"
	default:
		return "INFO"
	}
}

// HTTPK8sPitcher sends K8s events to an omni-pitcher HTTP endpoint.
type HTTPK8sPitcher struct {
	Addr     string // pitcher HTTP(S) URL
	Token    string // auth token sent via X-Auth-Token header
	Insecure bool   // skip TLS verification
	System   string // cluster identity
}

// NewHTTPK8sPitcher creates a pitcher that POSTs events to the omni-pitcher endpoint.
func NewHTTPK8sPitcher(addr, token string, insecure bool, clusterName string) *HTTPK8sPitcher {
	return &HTTPK8sPitcher{
		Addr:     addr,
		Token:    token,
		Insecure: insecure,
		System:   clusterName,
	}
}

func (p *HTTPK8sPitcher) HealthCheck(_ context.Context) error {
	msg := homerun.Message{
		Title:     "health-check",
		Message:   "k8s-pitcher health check",
		Severity:  "INFO",
		Author:    "k8s-pitcher-" + p.System,
		Timestamp: time.Now().Format(time.RFC3339),
		System:    "kubernetes",
		Tags:      "health-check",
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling health check body: %w", err)
	}

	resp, err := p.post(body)
	if err != nil {
		return fmt.Errorf("pitcher health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("pitcher health check returned status %d", resp.StatusCode)
	}
	return nil
}

func (p *HTTPK8sPitcher) Pitch(event K8sEvent) error {
	message := event.Summary
	if message == "" {
		objectJSON, err := json.Marshal(event.Object)
		if err != nil {
			return fmt.Errorf("marshaling object: %w", err)
		}
		message = string(objectJSON)
	}

	sev := event.Severity
	if sev == "" {
		sev = severityFor(event.EventType)
	}

	subsystem := event.Subsystem
	if subsystem == "" {
		subsystem = "kubernetes"
	}

	msg := homerun.Message{
		Title:     fmt.Sprintf("%s/%s %s", event.Kind, event.Name, event.EventType),
		Message:   message,
		Severity:  sev,
		Author:    "k8s-pitcher-" + p.System,
		Timestamp: event.Timestamp,
		System:    subsystem,
		Tags:      fmt.Sprintf("k8s,%s,%s,%s", event.Kind, event.EventType, event.Namespace),
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshaling message body: %w", err)
	}

	resp, err := p.post(body)
	if err != nil {
		return fmt.Errorf("sending to pitcher: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("pitcher returned status %d", resp.StatusCode)
	}

	slog.Debug("event pitched via HTTP",
		"kind", event.Kind,
		"name", event.Name,
		"eventType", event.EventType,
		"status", resp.StatusCode,
	)
	return nil
}

// post sends a JSON body to the pitcher endpoint with Bearer token auth.
func (p *HTTPK8sPitcher) post(body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", p.Addr, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)

	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: p.Insecure}, //nolint:gosec // caller-controlled
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	// Drain the body so the connection can be reused, but keep resp open for caller.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp, nil
}

// FileK8sPitcher writes K8s events as JSON lines to a file (dev/testing mode).
type FileK8sPitcher struct {
	Path string
	mu   sync.Mutex
}

func (p *FileK8sPitcher) HealthCheck(_ context.Context) error {
	return nil
}

func (p *FileK8sPitcher) Pitch(event K8sEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	f, err := os.OpenFile(p.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening pitch file: %w", err)
	}
	defer func() { _ = f.Close() }()

	entry := map[string]any{
		"timestamp": time.Now().Format(time.RFC3339),
		"event":     event,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("writing to pitch file: %w", err)
	}

	slog.Debug("event pitched to file", "kind", event.Kind, "name", event.Name, "path", p.Path)
	return nil
}
