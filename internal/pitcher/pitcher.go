package pitcher

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	homerun "github.com/stuttgart-things/homerun-library/v2"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/profile"
)

// K8sEvent represents a Kubernetes resource event to be pitched.
type K8sEvent struct {
	Kind      string         `json:"kind"`
	EventType string         `json:"eventType"` // add, update, delete, snapshot
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Object    map[string]any `json:"object"`
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
	objectJSON, err := json.Marshal(event.Object)
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	msg := homerun.Message{
		Title:     fmt.Sprintf("%s/%s %s", event.Kind, event.Name, event.EventType),
		Message:   string(objectJSON),
		Severity:  severityFor(event.EventType),
		Author:    "k8s-pitcher",
		Timestamp: event.Timestamp,
		System:    p.System,
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
	case "delete":
		return "warning"
	case "snapshot":
		return "info"
	default:
		return "info"
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
		Severity:  "info",
		Author:    "k8s-pitcher",
		Timestamp: time.Now().Format(time.RFC3339),
		System:    p.System,
		Tags:      "health-check",
	}

	body, err := homerun.RenderBody(homerun.HomeRunBodyData, msg)
	if err != nil {
		return fmt.Errorf("rendering health check body: %w", err)
	}

	_, resp, err := homerun.SendToHomerun(p.Addr, p.Token, []byte(body), p.Insecure)
	if err != nil {
		return fmt.Errorf("pitcher health check failed: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("pitcher health check returned status %d", resp.StatusCode)
	}
	return nil
}

func (p *HTTPK8sPitcher) Pitch(event K8sEvent) error {
	objectJSON, err := json.Marshal(event.Object)
	if err != nil {
		return fmt.Errorf("marshaling object: %w", err)
	}

	msg := homerun.Message{
		Title:     fmt.Sprintf("%s/%s %s", event.Kind, event.Name, event.EventType),
		Message:   string(objectJSON),
		Severity:  severityFor(event.EventType),
		Author:    "k8s-pitcher",
		Timestamp: event.Timestamp,
		System:    p.System,
		Tags:      fmt.Sprintf("k8s,%s,%s,%s", event.Kind, event.EventType, event.Namespace),
	}

	body, err := homerun.RenderBody(homerun.HomeRunBodyData, msg)
	if err != nil {
		return fmt.Errorf("rendering message body: %w", err)
	}

	_, resp, err := homerun.SendToHomerun(p.Addr, p.Token, []byte(body), p.Insecure)
	if err != nil {
		return fmt.Errorf("sending to pitcher: %w", err)
	}
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
