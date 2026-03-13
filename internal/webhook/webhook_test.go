package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
)

// mockPitcher captures pitched events for testing.
type mockPitcher struct {
	mu     sync.Mutex
	events []pitcher.K8sEvent
}

func (m *mockPitcher) Pitch(event pitcher.K8sEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockPitcher) HealthCheck(_ context.Context) error {
	return nil
}

func (m *mockPitcher) lastEvent() pitcher.K8sEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[len(m.events)-1]
}

func TestHandleFlux(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "")
	handler := srv.Handler()

	event := FluxEvent{
		InvolvedObject: InvolvedObject{
			Kind:       "Kustomization",
			Namespace:  "flux-system",
			Name:       "my-app",
			APIVersion: "kustomize.toolkit.fluxcd.io/v1",
		},
		Severity:   "info",
		Timestamp:  "2026-03-10T12:00:00Z",
		Message:    "Applied revision: main/abc123",
		Reason:     "ReconciliationSucceeded",
		Controller: "kustomize-controller",
	}

	body, _ := json.Marshal(event)
	req := httptest.NewRequest("POST", "/flux", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Gotk-Component", "kustomize-controller")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got := mock.lastEvent()
	if got.Kind != "Flux/Kustomization" {
		t.Errorf("expected Kind 'Flux/Kustomization', got %q", got.Kind)
	}
	if got.EventType != "ReconciliationSucceeded" {
		t.Errorf("expected EventType 'ReconciliationSucceeded', got %q", got.EventType)
	}
	if got.Namespace != "flux-system" {
		t.Errorf("expected Namespace 'flux-system', got %q", got.Namespace)
	}
	if got.Name != "my-app" {
		t.Errorf("expected Name 'my-app', got %q", got.Name)
	}
	if got.Cluster != "test-cluster" {
		t.Errorf("expected Cluster 'test-cluster', got %q", got.Cluster)
	}
	if !strings.Contains(got.Summary, "Kustomization flux-system/my-app") {
		t.Errorf("summary should contain resource info, got %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "Applied revision: main/abc123") {
		t.Errorf("summary should contain message, got %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "kustomize-controller") {
		t.Errorf("summary should contain controller, got %q", got.Summary)
	}
	if got.Severity != "SUCCESS" {
		t.Errorf("expected Severity 'SUCCESS', got %q", got.Severity)
	}
	if got.Subsystem != "flux" {
		t.Errorf("expected Subsystem 'flux', got %q", got.Subsystem)
	}
}

func TestHandleFlux_SeverityPassthrough(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "")
	handler := srv.Handler()

	event := FluxEvent{
		InvolvedObject: InvolvedObject{
			Kind:      "Kustomization",
			Namespace: "flux-system",
			Name:      "my-app",
		},
		Severity: "error",
		Message:  "health check failed",
		Reason:   "HealthCheckFailed",
	}

	body, _ := json.Marshal(event)
	req := httptest.NewRequest("POST", "/flux", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	got := mock.lastEvent()
	if got.Severity != "ERROR" {
		t.Errorf("expected Severity 'ERROR' (mapped from Flux 'error'), got %q", got.Severity)
	}
	if got.Subsystem != "flux" {
		t.Errorf("expected Subsystem 'flux', got %q", got.Subsystem)
	}
}

func TestHandleFlux_InvalidJSON(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "")
	handler := srv.Handler()

	req := httptest.NewRequest("POST", "/flux", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestHandleFlux_HMAC(t *testing.T) {
	key := "test-secret-key"
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", key)
	handler := srv.Handler()

	event := FluxEvent{
		InvolvedObject: InvolvedObject{
			Kind:      "GitRepository",
			Namespace: "flux-system",
			Name:      "flux-system",
		},
		Severity: "info",
		Message:  "Fetched revision: main/abc123",
		Reason:   "info",
	}

	body, _ := json.Marshal(event)

	// Compute valid HMAC
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write(body)
	sig := fmt.Sprintf("sha256=%x", mac.Sum(nil))

	req := httptest.NewRequest("POST", "/flux", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", sig)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid HMAC, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleFlux_HMAC_Missing(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "my-key")
	handler := srv.Handler()

	body := []byte(`{"message":"test"}`)
	req := httptest.NewRequest("POST", "/flux", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without signature, got %d", rec.Code)
	}
}

func TestHandleFlux_HMAC_Invalid(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "my-key")
	handler := srv.Handler()

	body := []byte(`{"message":"test"}`)
	req := httptest.NewRequest("POST", "/flux", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Signature", "sha256=badhash")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with bad signature, got %d", rec.Code)
	}
}

func TestHandleHealthz(t *testing.T) {
	mock := &mockPitcher{}
	srv := NewServer(mock, "test-cluster", "")
	handler := srv.Handler()

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestVerifySignature(t *testing.T) {
	key := []byte("secret")
	payload := []byte("hello world")

	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	sig := fmt.Sprintf("sha256=%x", mac.Sum(nil))

	if err := verifySignature(sig, payload, key); err != nil {
		t.Fatalf("expected valid signature, got error: %v", err)
	}

	if err := verifySignature("sha256=wrong", payload, key); err == nil {
		t.Fatal("expected error for wrong signature")
	}

	if err := verifySignature("invalid", payload, key); err == nil {
		t.Fatal("expected error for invalid format")
	}
}

func TestSummarizeFluxEvent(t *testing.T) {
	event := FluxEvent{
		InvolvedObject: InvolvedObject{
			Kind:       "HelmRelease",
			Namespace:  "apps",
			Name:       "redis",
			APIVersion: "helm.toolkit.fluxcd.io/v2",
		},
		Severity:   "error",
		Message:    "install retries exhausted",
		Reason:     "InstallFailed",
		Controller: "helm-controller",
		Metadata: map[string]string{
			"helm.toolkit.fluxcd.io/revision": "6.0.0",
		},
	}

	summary := summarizeFluxEvent(event, "helm-controller")

	if !strings.Contains(summary, "HelmRelease apps/redis") {
		t.Errorf("missing resource line: %s", summary)
	}
	if !strings.Contains(summary, "Severity: error") {
		t.Errorf("missing severity: %s", summary)
	}
	if !strings.Contains(summary, "Reason: InstallFailed") {
		t.Errorf("missing reason: %s", summary)
	}
	if !strings.Contains(summary, "install retries exhausted") {
		t.Errorf("missing message: %s", summary)
	}
}
