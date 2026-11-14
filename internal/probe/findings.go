package probe

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	homerun "github.com/stuttgart-things/homerun-library/v4"
)

// Format and Source of the findings/v1 payload (contract of
// stuttgart-things/stuttgart-things#4556).
const (
	Format = "findings/v1"
	Source = "endpoints"
)

// Finding kinds.
const (
	KindEndpoint    = "Endpoint"
	KindCertificate = "Certificate"
)

// Checks: the last part of a fingerprint. A finding keeps its check (and so
// its fingerprint) while the problem stays the same.
const (
	CheckDNS     = "dns"     // host does not resolve
	CheckConnect = "connect" // refused, timeout, reset
	CheckTLS     = "tls"     // handshake failed or chain not trusted
	CheckStatus  = "status"  // unexpected HTTP status
	CheckExpiry  = "expiry"  // certificate expires soon or expired
	CheckSAN     = "san"     // host not covered by the certificate
)

// Severities of findings and messages.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityError    = "error"
	SeverityCritical = "critical"
	SeveritySuccess  = "success"
)

// Finding is one item of a findings/v1 payload.
type Finding struct {
	Fingerprint string   `json:"fingerprint"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	Namespace   string   `json:"namespace,omitempty"`
	Parent      string   `json:"parent,omitempty"`
	Severity    string   `json:"severity"`
	Texts       []string `json:"texts"`
	// Check is not part of the payload, only of the fingerprint.
	Check string `json:"-"`
}

// Fingerprint is the first 16 hex characters of sha256 over
// source|target|kind|namespace/name|check. Texts are not part of it, so it
// stays stable while the problem stays the same.
func Fingerprint(target, kind, namespace, name, check string) string {
	sum := sha256.Sum256([]byte(Source + "|" + target + "|" + kind + "|" + namespace + "/" + name + "|" + check))
	return hex.EncodeToString(sum[:])[:16]
}

// rank orders severities; success and unknown values rank lowest.
func rank(sev string) int {
	switch sev {
	case SeverityInfo:
		return 1
	case SeverityWarning:
		return 2
	case SeverityError:
		return 3
	case SeverityCritical:
		return 4
	default:
		return 0
	}
}

// Worst returns the worst severity of findings, success if there are none.
func Worst(findings []Finding) string {
	worst := SeveritySuccess
	for _, f := range findings {
		if rank(f.Severity) > rank(worst) {
			worst = f.Severity
		}
	}
	return worst
}

// sortWorstFirst orders findings by severity (worst first), then by kind,
// namespace, name and check, so a truncated payload keeps the worst ones.
func sortWorstFirst(findings []Finding) {
	slices.SortStableFunc(findings, func(a, b Finding) int {
		if d := rank(b.Severity) - rank(a.Severity); d != 0 {
			return d
		}
		for _, c := range [][2]string{{a.Kind, b.Kind}, {a.Namespace, b.Namespace}, {a.Name, b.Name}, {a.Check, b.Check}} {
			if c[0] != c[1] {
				if c[0] < c[1] {
					return -1
				}
				return 1
			}
		}
		return 0
	})
}

// Payload builds a findings/v1 payload for target (the cluster). items are
// sorted worst first; if they do not fit homerun-library's MaxPayloadSize,
// the tail is cut and truncated holds the number of omitted items. count
// is the number of all findings.
func Payload(target string, findings []Finding) map[string]any {
	items := slices.Clone(findings)
	if items == nil {
		items = []Finding{}
	}
	sortWorstFirst(items)
	payload := map[string]any{
		"format": Format,
		"source": Source,
		"target": target,
		"count":  len(items),
		"items":  items,
	}
	if size(payload) <= homerun.MaxPayloadSize {
		return payload
	}
	// Binary search for the most items that fit.
	lo, hi := 0, len(items)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		payload["items"] = items[:mid]
		payload["truncated"] = len(items) - mid
		if size(payload) <= homerun.MaxPayloadSize {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	payload["items"] = items[:lo]
	payload["truncated"] = len(items) - lo
	return payload
}

func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}
