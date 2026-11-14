package probe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	homerun "github.com/stuttgart-things/homerun-library/v4"
)

func TestFingerprint(t *testing.T) {
	sum := sha256.Sum256([]byte("endpoints|c1|Certificate|argocd/argo.example.com|expiry"))
	want := hex.EncodeToString(sum[:])[:16]
	if got := Fingerprint("c1", "Certificate", "argocd", "argo.example.com", "expiry"); got != want {
		t.Errorf("Fingerprint = %s, want %s", got, want)
	}
	if Fingerprint("c1", "Endpoint", "", "x", "dns") == Fingerprint("c2", "Endpoint", "", "x", "dns") {
		t.Error("fingerprint must differ per cluster")
	}
}

func TestPayload(t *testing.T) {
	p := Payload("c1", nil)
	if p["format"] != Format || p["source"] != Source || p["target"] != "c1" || p["count"] != 0 {
		t.Errorf("payload = %v", p)
	}
	if items, ok := p["items"].([]Finding); !ok || items == nil || len(items) != 0 {
		t.Errorf("items = %#v, want empty list", p["items"])
	}
	if _, ok := p["truncated"]; ok {
		t.Error("truncated set on a small payload")
	}

	// Worst first.
	p = Payload("c1", []Finding{{Name: "a", Severity: SeverityWarning}, {Name: "b", Severity: SeverityError}})
	items := p["items"].([]Finding)
	if items[0].Name != "b" {
		t.Errorf("items = %+v, want error first", items)
	}
	if Worst(items) != SeverityError || Worst(nil) != SeveritySuccess {
		t.Error("Worst")
	}
}

func TestPayloadTruncated(t *testing.T) {
	long := strings.Repeat("x", 1000)
	var fs []Finding
	for i := range 400 {
		sev := SeverityWarning
		if i%100 == 0 {
			sev = SeverityError
		}
		fs = append(fs, Finding{Name: long, Severity: sev, Texts: []string{long}})
	}
	p := Payload("c1", fs)
	if size(p) > homerun.MaxPayloadSize {
		t.Fatalf("payload %d bytes", size(p))
	}
	items := p["items"].([]Finding)
	if p["count"] != 400 || p["truncated"] != 400-len(items) || len(items) == 0 {
		t.Errorf("count %v truncated %v kept %d", p["count"], p["truncated"], len(items))
	}
	for i := range 4 {
		if items[i].Severity != SeverityError {
			t.Errorf("item %d = %s, want the errors first", i, items[i].Severity)
		}
	}
	var msg homerun.Message
	if err := msg.SetPayload(p); err != nil {
		t.Errorf("SetPayload: %v", err)
	}
}

func newProber() *Prober {
	return &Prober{Cluster: "c1", Client: NewHTTPClient(""), Timeout: 2 * time.Second, WarnDays: 14, ErrorDays: 3}
}

func targetFor(t *testing.T, rawURL string) Target {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return Target{URL: rawURL, Host: u.Hostname(), Source: SourceIngress, Namespace: "web", Name: "shop"}
}

func checks(fs []Finding) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Kind+"/"+f.Check+"/"+f.Severity)
	}
	return out
}

func TestProbeHTTPStatus(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	p := newProber()
	tgt := targetFor(t, srv.URL+"/")

	r := p.Probe(context.Background(), tgt)
	if !r.Success || len(r.Findings) != 0 || r.StatusCode != 200 || !r.CertNotAfter.IsZero() {
		t.Errorf("200: %+v", r)
	}

	status = http.StatusFound // redirects are not followed, 3xx is fine
	if r = p.Probe(context.Background(), tgt); !r.Success || r.StatusCode != 302 {
		t.Errorf("302: %+v", r)
	}

	tgt.ExpectedStatus = []int{200}
	r = p.Probe(context.Background(), tgt)
	if r.Success || len(r.Findings) != 1 || r.Findings[0].Check != CheckStatus {
		t.Fatalf("302 with expected 200: %+v", r)
	}
	f := r.Findings[0]
	if f.Kind != KindEndpoint || f.Name != "127.0.0.1" || f.Namespace != "web" || f.Parent != "Ingress/web/shop" ||
		f.Fingerprint != Fingerprint("c1", KindEndpoint, "web", "127.0.0.1", CheckStatus) ||
		!strings.Contains(f.Texts[0], "unexpected status 302 (expected 200)") {
		t.Errorf("finding = %+v", f)
	}

	tgt.ExpectedStatus = nil
	status = http.StatusServiceUnavailable
	r = p.Probe(context.Background(), tgt)
	if r.Success || len(r.Findings) != 1 || !strings.Contains(r.Findings[0].Texts[0], "below 500") {
		t.Errorf("503: %+v", r)
	}

	// A login or an API root answering 401/403/404 is up.
	for _, status = range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		if r = p.Probe(context.Background(), tgt); !r.Success {
			t.Errorf("%d counted as down: %+v", status, r)
		}
	}
}

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProbeDNSError(t *testing.T) {
	p := newProber()
	p.Client = &http.Client{Transport: rtFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "gone.example.com", IsNotFound: true}}
	})}
	tgt := Target{URL: "https://gone.example.com/", Host: "gone.example.com", Source: SourceTarget, Name: "gone"}
	r := p.Probe(context.Background(), tgt)
	if r.Success || len(r.Findings) != 1 {
		t.Fatalf("result = %+v", r)
	}
	f := r.Findings[0]
	if f.Check != CheckDNS || f.Name != "gone" || f.Namespace != "" || f.Parent != "" ||
		f.Texts[0] != "DNS lookup of gone.example.com failed: no such host" {
		t.Errorf("finding = %+v", f)
	}
}

func TestProbeConnectError(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	r := newProber().Probe(context.Background(), targetFor(t, "http://"+addr+"/"))
	if got := checks(r.Findings); r.Success || len(got) != 1 || got[0] != "Endpoint/connect/error" {
		t.Errorf("result = %+v", r)
	}
	if strings.HasPrefix(r.Findings[0].Texts[0], "GET "+"http://"+addr+"/ failed: Get") {
		t.Errorf("text repeats the URL: %s", r.Findings[0].Texts[0])
	}
}

func TestProbeTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	p := newProber()
	p.Timeout = 50 * time.Millisecond
	r := p.Probe(context.Background(), targetFor(t, srv.URL))
	if r.Success || len(r.Findings) != 1 || r.Findings[0].Check != CheckConnect || !strings.Contains(r.Findings[0].Texts[0], "timed out") {
		t.Errorf("result = %+v", r)
	}
}

func TestProbeTLSHandshakeError(t *testing.T) {
	// A plain HTTP server spoken to with https fails the handshake.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	r := newProber().Probe(context.Background(), targetFor(t, strings.Replace(srv.URL, "http://", "https://", 1)))
	if r.Success || len(r.Findings) != 1 || r.Findings[0].Check != CheckTLS {
		t.Errorf("result = %+v", r)
	}
}

func tlsServer(t *testing.T) (*httptest.Server, *x509.Certificate) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(srv.Close)
	return srv, srv.Certificate()
}

func TestProbeCertificate(t *testing.T) {
	srv, cert := tlsServer(t)
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	tgt := targetFor(t, srv.URL)

	for _, tc := range []struct {
		name    string
		now     time.Time
		want    []string
		success bool
		text    string
	}{
		{"valid", cert.NotAfter.Add(-100 * 24 * time.Hour), nil, true, ""},
		{"warn", cert.NotAfter.Add(-9*24*time.Hour - time.Hour), []string{"Certificate/expiry/warning"}, true, "certificate expires in 9 days (" + cert.NotAfter.UTC().Format(time.DateOnly) + ")"},
		{"error", cert.NotAfter.Add(-36 * time.Hour), []string{"Certificate/expiry/error"}, true, "certificate expires in 1 days"},
		{"expired", cert.NotAfter.Add(50 * time.Hour), []string{"Certificate/expiry/error"}, false, "certificate expired 2 days ago"},
	} {
		p := newProber()
		p.Roots = roots
		p.Now = func() time.Time { return tc.now }
		r := p.Probe(context.Background(), tgt)
		got := checks(r.Findings)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") || r.Success != tc.success {
			t.Errorf("%s: findings %v success %v", tc.name, got, r.Success)
			continue
		}
		if !r.CertNotAfter.Equal(cert.NotAfter) || r.CertIssuer == "" {
			t.Errorf("%s: cert %v %q", tc.name, r.CertNotAfter, r.CertIssuer)
		}
		if tc.text != "" && !strings.HasPrefix(r.Findings[0].Texts[0], tc.text) {
			t.Errorf("%s: text %q, want %q", tc.name, r.Findings[0].Texts[0], tc.text)
		}
	}
	// The fingerprint stays while the days count down.
	p := newProber()
	p.Roots = roots
	p.Now = func() time.Time { return cert.NotAfter.Add(-10 * 24 * time.Hour) }
	a := p.Probe(context.Background(), tgt).Findings[0]
	p.Now = func() time.Time { return cert.NotAfter.Add(-5 * 24 * time.Hour) }
	b := p.Probe(context.Background(), tgt).Findings[0]
	if a.Fingerprint != b.Fingerprint || a.Texts[0] == b.Texts[0] {
		t.Errorf("fingerprints %s %s texts %q %q", a.Fingerprint, b.Fingerprint, a.Texts[0], b.Texts[0])
	}
}

func TestProbeUntrustedCertificate(t *testing.T) {
	srv, _ := tlsServer(t)
	tgt := targetFor(t, srv.URL)
	p := newProber()
	p.Roots = x509.NewCertPool() // the test CA is not in it
	r := p.Probe(context.Background(), tgt)
	if r.Success || strings.Join(checks(r.Findings), ",") != "Endpoint/tls/error" ||
		!strings.Contains(r.Findings[0].Texts[0], "certificate not trusted") {
		t.Errorf("untrusted: %+v", r)
	}
	tgt.InsecureSkipVerify = true
	if r = p.Probe(context.Background(), tgt); !r.Success || len(r.Findings) != 0 {
		t.Errorf("insecureSkipVerify: %+v", r)
	}
}

// TestProbeShortLivedCertWrongHost serves a self-signed certificate for
// another host that expires in two days.
func TestProbeShortLivedCertWrongHost(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "other.example"},
		DNSNames:              []string{"other.example"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(2*24*time.Hour + time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	defer srv.Close()

	p := newProber()
	p.Roots = x509.NewCertPool()
	p.Roots.AddCert(leaf)
	r := p.Probe(context.Background(), targetFor(t, srv.URL))
	got := strings.Join(checks(r.Findings), ",")
	if got != "Certificate/expiry/error,Certificate/san/error" || r.Success {
		t.Errorf("findings %s success %v", got, r.Success)
	}
	if !strings.Contains(r.Findings[1].Texts[0], "not valid for 127.0.0.1") {
		t.Errorf("san text %q", r.Findings[1].Texts[0])
	}
}

func TestClassifyTLSAlert(t *testing.T) {
	check, _ := classify(&url.Error{Op: "Get", URL: "https://x", Err: tls.AlertError(40)}, Target{Host: "x"}, time.Second)
	if check != CheckTLS {
		t.Errorf("check = %s", check)
	}
	check, _ = classify(errors.New("boom"), Target{Host: "x"}, time.Second)
	if check != CheckConnect {
		t.Errorf("check = %s", check)
	}
}
