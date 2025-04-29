// Package metrics exposes Prometheus metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Metrics groups every collector of the service.
type Metrics struct {
	Registry      *prometheus.Registry
	Events        *prometheus.CounterVec
	Documents     *prometheus.CounterVec
	Batches       *prometheus.CounterVec
	BatchDuration *prometheus.HistogramVec
	Errors        *prometheus.CounterVec
	Retries       *prometheus.CounterVec
	DLQ           *prometheus.CounterVec
	Lag           *prometheus.GaugeVec
	Inflight      *prometheus.GaugeVec
	Running       *prometheus.GaugeVec
}

// New registers the collectors on a fresh registry.
func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_events_total", Help: "Change events read from sources.",
		}, []string{"task", "op"}),
		Documents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_documents_total", Help: "Documents confirmed by Meilisearch.",
		}, []string{"task", "kind"}),
		Batches: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_batches_total", Help: "Batches confirmed by Meilisearch.",
		}, []string{"task"}),
		BatchDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "cdc_batch_duration_seconds", Help: "Time from enqueue to confirmation.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12),
		}, []string{"task"}),
		Errors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_errors_total", Help: "Pipeline failures.",
		}, []string{"task"}),
		Retries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_retries_total", Help: "Retried Meilisearch operations.",
		}, []string{"task"}),
		DLQ: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "cdc_dlq_total", Help: "Documents sent to the dead letter queue.",
		}, []string{"task"}),
		Lag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cdc_lag_seconds", Help: "Delay between source commit and Meilisearch confirmation.",
		}, []string{"task"}),
		Inflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cdc_inflight_batches", Help: "Batches enqueued but not yet confirmed.",
		}, []string{"task"}),
		Running: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "cdc_task_running", Help: "1 when the task pipeline is active.",
		}, []string{"task"}),
	}
	m.Registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.Events, m.Documents, m.Batches, m.BatchDuration, m.Errors, m.Retries, m.DLQ, m.Lag, m.Inflight, m.Running)
	return m
}

// Forget removes the series of a deleted task.
func (m *Metrics) Forget(task string) {
	l := prometheus.Labels{"task": task}
	for _, v := range []interface{ DeletePartialMatch(prometheus.Labels) int }{
		m.Events, m.Documents, m.Batches, m.BatchDuration, m.Errors, m.Retries, m.DLQ, m.Lag, m.Inflight, m.Running,
	} {
		v.DeletePartialMatch(l)
	}
}
