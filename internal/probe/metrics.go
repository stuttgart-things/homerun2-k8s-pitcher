package probe

import (
	"net/http"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var labels = []string{"host", "target", "kind", "namespace", "name"}

var (
	descSuccess = prometheus.NewDesc("probe_success",
		"Whether the last probe of the endpoint succeeded (1) or not (0).", labels, nil)
	descDuration = prometheus.NewDesc("probe_duration_seconds",
		"Duration of the last probe in seconds.", labels, nil)
	descStatus = prometheus.NewDesc("probe_http_status_code",
		"HTTP status code of the last probe, 0 without response.", labels, nil)
	descCertExpiry = prometheus.NewDesc("probe_cert_expiry_timestamp_seconds",
		"Expiry of the endpoint's leaf certificate as a Unix timestamp.", labels, nil)
)

// Metrics exposes the results of the last probe round. Targets that are no
// longer probed disappear with the next round.
type Metrics struct {
	mu      sync.RWMutex
	results []Result
	reg     *prometheus.Registry
}

// NewMetrics creates the metrics with their own registry (plus Go and
// process collectors).
func NewMetrics() *Metrics {
	m := &Metrics{reg: prometheus.NewRegistry()}
	m.reg.MustRegister(m,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Set replaces the results.
func (m *Metrics) Set(results []Result) {
	m.mu.Lock()
	m.results = results
	m.mu.Unlock()
}

// Handler serves the metrics in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- descSuccess
	ch <- descDuration
	ch <- descStatus
	ch <- descCertExpiry
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, r := range m.results {
		t := r.Target
		lv := []string{t.Host, t.URL, t.Source, t.Namespace, t.Name}
		success := 0.0
		if r.Success {
			success = 1
		}
		ch <- prometheus.MustNewConstMetric(descSuccess, prometheus.GaugeValue, success, lv...)
		ch <- prometheus.MustNewConstMetric(descDuration, prometheus.GaugeValue, r.Duration.Seconds(), lv...)
		ch <- prometheus.MustNewConstMetric(descStatus, prometheus.GaugeValue, float64(r.StatusCode), lv...)
		if !r.CertNotAfter.IsZero() {
			ch <- prometheus.MustNewConstMetric(descCertExpiry, prometheus.GaugeValue, float64(r.CertNotAfter.Unix()), lv...)
		}
	}
}
