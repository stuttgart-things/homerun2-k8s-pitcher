// Package probe checks the HTTP(S) endpoints of a cluster (#109): the hosts
// of Ingress and HTTPRoute objects and explicit URLs. Every target gets an
// HTTP GET (status, latency, DNS, connect and TLS errors) and, over TLS, a
// certificate check (expiry, SAN, trust). Problems are findings/v1 items;
// their state changes and a periodic snapshot go to the pitcher, the
// results to Prometheus metrics.
package probe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Target kinds: the source of a target.
const (
	SourceIngress   = "Ingress"
	SourceHTTPRoute = "HTTPRoute"
	SourceTarget    = "Target" // explicit profile target
)

// Target is one endpoint to probe.
type Target struct {
	URL  string
	Host string
	// Source is Ingress, HTTPRoute or Target; Namespace and Name are the
	// Ingress or HTTPRoute, or the explicit target's name.
	Source             string
	Namespace          string
	Name               string
	ExpectedStatus     []int
	InsecureSkipVerify bool
}

// Parent is the object a discovered target comes from, e.g.
// HTTPRoute/argocd/argocd-server; empty for an explicit target.
func (t Target) Parent() string {
	if t.Source == SourceTarget {
		return ""
	}
	return t.Source + "/" + t.Namespace + "/" + t.Name
}

// findingName is the name of the target's findings: the host of a
// discovered target, the name of an explicit one.
func (t Target) findingName() string {
	if t.Source == SourceTarget && t.Name != "" {
		return t.Name
	}
	return t.Host
}

// findingNamespace is the namespace of the target's findings, empty for an
// explicit target.
func (t Target) findingNamespace() string {
	if t.Source == SourceTarget {
		return ""
	}
	return t.Namespace
}

// key identifies a target across rounds.
func (t Target) key() string {
	return t.Source + "|" + t.Namespace + "|" + t.Name + "|" + t.URL
}

// Result is the outcome of one probe.
type Result struct {
	Target     Target
	Success    bool
	Duration   time.Duration
	StatusCode int
	// CertNotAfter is the leaf certificate's expiry, zero without TLS.
	CertNotAfter time.Time
	CertIssuer   string
	Findings     []Finding
}

// Prober runs the checks of a target.
type Prober struct {
	// Cluster is the findings/v1 target and part of every fingerprint.
	Cluster string
	// Client sends the GET. It must not verify certificates itself (the
	// prober does, to report expiry and SAN apart from trust) and should not
	// follow redirects; NewHTTPClient builds one.
	Client *http.Client
	// Roots verifies certificate chains; nil is the system pool.
	Roots *x509.CertPool
	// Now is the clock, time.Now outside of tests.
	Now     func() time.Time
	Timeout time.Duration
	// ExpectedStatus is the default of targets without their own; empty
	// accepts any status below 500.
	ExpectedStatus []int
	WarnDays       int
	ErrorDays      int
}

// NewHTTPClient returns the probe client: no certificate verification (the
// prober verifies), no redirects, no keep-alive (every probe makes a fresh
// handshake and sees a renewed certificate). resolver (host:port) replaces
// the system DNS if set.
func NewHTTPClient(resolver string) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if resolver != "" {
		dialer.Resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, resolver)
			},
		}
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         dialer.DialContext,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the prober verifies the chain itself
			TLSHandshakeTimeout: 10 * time.Second,
			DisableKeepAlives:   true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (p *Prober) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Probe checks t once.
func (p *Prober) Probe(ctx context.Context, t Target) Result {
	res := Result{Target: t}
	if p.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.Timeout)
		defer cancel()
	}

	start := time.Now()
	resp, err := p.get(ctx, t.URL)
	res.Duration = time.Since(start)
	if err != nil {
		check, text := classify(err, t, p.Timeout)
		res.Findings = append(res.Findings, p.finding(t, KindEndpoint, check, SeverityError, text))
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	res.StatusCode = resp.StatusCode
	if !statusOK(resp.StatusCode, p.expected(t)) {
		res.Findings = append(res.Findings, p.finding(t, KindEndpoint, CheckStatus, SeverityError,
			fmt.Sprintf("unexpected status %d (expected %s)", resp.StatusCode, describeExpected(p.expected(t)))))
	}
	certFailed := false
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		var fs []Finding
		fs, certFailed = p.checkCert(t, resp.TLS.PeerCertificates, &res)
		res.Findings = append(res.Findings, fs...)
	}
	res.Success = !certFailed && !slices.ContainsFunc(res.Findings, func(f Finding) bool { return f.Kind == KindEndpoint })
	return res
}

func (p *Prober) get(ctx context.Context, rawURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "homerun2-k8s-pitcher-probe")
	client := p.Client
	if client == nil {
		client = NewHTTPClient("")
	}
	return client.Do(req)
}

func (p *Prober) expected(t Target) []int {
	if len(t.ExpectedStatus) > 0 {
		return t.ExpectedStatus
	}
	return p.ExpectedStatus
}

func statusOK(code int, expected []int) bool {
	if len(expected) == 0 {
		// A heartbeat: the server answered. 401/403 behind a login, 404 on
		// an API root are up; only 5xx is down.
		return code >= 200 && code < 500
	}
	return slices.Contains(expected, code)
}

func describeExpected(expected []int) string {
	if len(expected) == 0 {
		return "below 500"
	}
	s := make([]string, len(expected))
	for i, c := range expected {
		s[i] = fmt.Sprint(c)
	}
	return strings.Join(s, ", ")
}

// checkCert checks the leaf's expiry and SAN and, unless the target skips
// it, the chain's trust. failed is true for an expired certificate or a SAN
// mismatch: a client would refuse the connection.
func (p *Prober) checkCert(t Target, chain []*x509.Certificate, res *Result) (fs []Finding, failed bool) {
	leaf := chain[0]
	now := p.now()
	res.CertNotAfter = leaf.NotAfter
	res.CertIssuer = leaf.Issuer.String()
	date := leaf.NotAfter.UTC().Format(time.DateOnly)

	if now.After(leaf.NotAfter) {
		days := int(now.Sub(leaf.NotAfter).Hours() / 24)
		fs = append(fs, p.finding(t, KindCertificate, CheckExpiry, SeverityError,
			fmt.Sprintf("certificate expired %d days ago (%s)", days, date), "issuer: "+res.CertIssuer))
		failed = true
	} else {
		days := int(math.Floor(leaf.NotAfter.Sub(now).Hours() / 24))
		sev := ""
		switch {
		case days < p.ErrorDays:
			sev = SeverityError
		case days < p.WarnDays:
			sev = SeverityWarning
		}
		if sev != "" {
			fs = append(fs, p.finding(t, KindCertificate, CheckExpiry, sev,
				fmt.Sprintf("certificate expires in %d days (%s)", days, date), "issuer: "+res.CertIssuer))
		}
	}

	if err := leaf.VerifyHostname(t.Host); err != nil {
		fs = append(fs, p.finding(t, KindCertificate, CheckSAN, SeverityError,
			fmt.Sprintf("certificate is not valid for %s: %v", t.Host, err)))
		failed = true
	}

	if !t.InsecureSkipVerify {
		inter := x509.NewCertPool()
		for _, c := range chain[1:] {
			inter.AddCert(c)
		}
		// Expiry is reported above: verify the chain at a time the leaf is
		// valid, so an expired certificate is not also "not trusted".
		at := now
		if at.After(leaf.NotAfter) {
			at = leaf.NotAfter.Add(-time.Second)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: p.Roots, Intermediates: inter, CurrentTime: at}); err != nil {
			fs = append(fs, p.finding(t, KindEndpoint, CheckTLS, SeverityError,
				fmt.Sprintf("certificate not trusted: %v", err), "issuer: "+res.CertIssuer))
		}
	}
	return fs, failed
}

// classify maps a GET error to a check and a text.
func classify(err error, t Target, timeout time.Duration) (check, text string) {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return CheckDNS, fmt.Sprintf("DNS lookup of %s failed: %s", t.Host, dnsErr.Err)
	}
	var recErr tls.RecordHeaderError
	var alert tls.AlertError
	var verr *tls.CertificateVerificationError
	if errors.As(err, &recErr) || errors.As(err, &alert) || errors.As(err, &verr) ||
		strings.Contains(err.Error(), "tls: ") || strings.Contains(err.Error(), "HTTP response to HTTPS client") {
		return CheckTLS, fmt.Sprintf("TLS handshake with %s failed: %v", t.Host, unwrapURL(err))
	}
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return CheckConnect, fmt.Sprintf("GET %s timed out after %s", t.URL, timeout)
	}
	return CheckConnect, fmt.Sprintf("GET %s failed: %v", t.URL, unwrapURL(err))
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// unwrapURL drops the "Get <url>:" prefix of a *url.Error.
func unwrapURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

func (p *Prober) finding(t Target, kind, check, severity string, texts ...string) Finding {
	name, ns := t.findingName(), t.findingNamespace()
	return Finding{
		Fingerprint: Fingerprint(p.Cluster, kind, ns, name, check),
		Kind:        kind,
		Name:        name,
		Namespace:   ns,
		Parent:      t.Parent(),
		Severity:    severity,
		Texts:       texts,
		Check:       check,
	}
}
