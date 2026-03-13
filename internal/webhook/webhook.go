package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-k8s-pitcher/internal/pitcher"
)

// FluxEvent represents the JSON payload sent by the Flux notification controller
// generic webhook provider.
type FluxEvent struct {
	InvolvedObject InvolvedObject `json:"involvedObject"`
	Severity       string         `json:"severity"`
	Timestamp      string         `json:"timestamp"`
	Message        string         `json:"message"`
	Reason         string         `json:"reason"`
	Controller     string         `json:"reportingController"`
	Instance       string         `json:"reportingInstance"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

// InvolvedObject identifies the Flux resource that triggered the event.
type InvolvedObject struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace"`
	Name            string `json:"name"`
	UID             string `json:"uid"`
	APIVersion      string `json:"apiVersion"`
	ResourceVersion string `json:"resourceVersion"`
}

// Server handles incoming Flux notification webhook requests.
type Server struct {
	pitcher    pitcher.K8sPitcher
	cluster    string
	hmacKey    []byte // optional HMAC key for signature verification
}

// NewServer creates a webhook server that pitches Flux events.
func NewServer(p pitcher.K8sPitcher, clusterName string, hmacKey string) *Server {
	s := &Server{
		pitcher: p,
		cluster: clusterName,
	}
	if hmacKey != "" {
		s.hmacKey = []byte(hmacKey)
	}
	return s
}

// Handler returns an http.Handler with the webhook routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /flux", s.handleFlux)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return mux
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleFlux(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer func() { _ = r.Body.Close() }()

	// Verify HMAC signature if key is configured
	if s.hmacKey != nil {
		sig := r.Header.Get("X-Signature")
		if sig == "" {
			http.Error(w, "missing X-Signature header", http.StatusUnauthorized)
			return
		}
		if err := verifySignature(sig, body, s.hmacKey); err != nil {
			slog.Warn("flux webhook HMAC verification failed", "error", err)
			http.Error(w, "signature verification failed", http.StatusUnauthorized)
			return
		}
	}

	var event FluxEvent
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	k8sEvent := fluxEventToK8sEvent(event, s.cluster, r.Header.Get("Gotk-Component"))

	if err := s.pitcher.Pitch(k8sEvent); err != nil {
		slog.Error("failed to pitch flux event",
			"kind", event.InvolvedObject.Kind,
			"name", event.InvolvedObject.Name,
			"error", err,
		)
		http.Error(w, "failed to pitch event", http.StatusInternalServerError)
		return
	}

	slog.Info("flux event pitched",
		"kind", event.InvolvedObject.Kind,
		"name", event.InvolvedObject.Name,
		"severity", event.Severity,
		"reason", event.Reason,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"accepted"}`))
}

func fluxEventToK8sEvent(event FluxEvent, cluster, component string) pitcher.K8sEvent {
	summary := summarizeFluxEvent(event, component)

	ts := event.Timestamp
	if ts == "" {
		ts = time.Now().Format(time.RFC3339)
	}

	return pitcher.K8sEvent{
		Kind:      fmt.Sprintf("Flux/%s", event.InvolvedObject.Kind),
		EventType: event.Reason,
		Namespace: event.InvolvedObject.Namespace,
		Name:      event.InvolvedObject.Name,
		Summary:   summary,
		Severity:  fluxSeverityToOutcome(event.Severity),
		Subsystem: "flux",
		Timestamp: ts,
		Cluster:   cluster,
	}
}

// fluxSeverityToOutcome maps Flux notification severity to outcome-based severity.
// Flux sends "info" for successful reconciliations and "error" for failures.
func fluxSeverityToOutcome(fluxSeverity string) string {
	switch strings.ToLower(fluxSeverity) {
	case "info":
		return "SUCCESS"
	case "error":
		return "ERROR"
	default:
		return strings.ToUpper(fluxSeverity)
	}
}

func summarizeFluxEvent(event FluxEvent, component string) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s %s/%s\n", event.InvolvedObject.Kind, event.InvolvedObject.Namespace, event.InvolvedObject.Name)
	fmt.Fprintf(&b, "Severity: %s\n", event.Severity)
	fmt.Fprintf(&b, "Reason: %s\n", event.Reason)
	fmt.Fprintf(&b, "Message: %s\n", event.Message)

	if component != "" {
		fmt.Fprintf(&b, "Controller: %s\n", component)
	} else if event.Controller != "" {
		fmt.Fprintf(&b, "Controller: %s\n", event.Controller)
	}

	if event.InvolvedObject.APIVersion != "" {
		fmt.Fprintf(&b, "API: %s\n", event.InvolvedObject.APIVersion)
	}

	// Include metadata (e.g., revision info)
	for k, v := range event.Metadata {
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}

	return strings.TrimSpace(b.String())
}

// verifySignature verifies the HMAC signature from the X-Signature header.
// Format: HASH_FUNC=HASH (e.g., sha256=abc123...)
func verifySignature(sig string, payload, key []byte) error {
	parts := strings.SplitN(sig, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid signature format")
	}

	var newF func() hash.Hash
	switch parts[0] {
	case "sha224":
		newF = sha256.New224
	case "sha256":
		newF = sha256.New
	case "sha384":
		newF = sha512.New384
	case "sha512":
		newF = sha512.New
	default:
		return fmt.Errorf("unsupported hash algorithm %q", parts[0])
	}

	mac := hmac.New(newF, key)
	if _, err := mac.Write(payload); err != nil {
		return fmt.Errorf("computing HMAC: %w", err)
	}

	expected := fmt.Sprintf("%x", mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}
