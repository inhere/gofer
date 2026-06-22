// Package metrics is the gofer Prometheus instrumentation (E16, design §6). It
// owns a PRIVATE registry (not the global default) so the collector set is
// explicit, test-isolated and free of any unrelated process-wide registrations.
//
// The job package must NOT depend on prometheus (design §6, hard constraint): it
// records lifecycle counters through the narrow job.MetricsSink interface, which
// *Metrics implements here. Every record method is nil-safe — a nil *Metrics is a
// no-op — so non-serve callers (tests, mcp) need no wiring.
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the collectors and the private registry. A nil *Metrics makes
// every record method a no-op (so non-serve callers/tests need no wiring) and is
// the "metrics disabled" sentinel everywhere it is injected.
type Metrics struct {
	reg *prometheus.Registry

	httpRequests  *prometheus.CounterVec   // {method,route,status}
	httpDuration  *prometheus.HistogramVec // {method,route}
	jobsSubmitted *prometheus.CounterVec   // {caller,project,agent,runner}
	jobsTerminal  *prometheus.CounterVec   // {status,caller,project}
	jobDuration   *prometheus.HistogramVec // {agent,runner,status}

	workflowsTerminal *prometheus.CounterVec // {status} (P4/T4.3)
	workflowDuration  prometheus.Histogram   // submit→terminal seconds (P4/T4.3)
}

// New builds a Metrics with a fresh private registry pre-loaded with the Go
// runtime + process collectors (goroutines / GC / memory / fds) and the five
// gofer collector vecs. It panics on a duplicate registration (MustRegister),
// which only happens on a programming error since the registry is private.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	m := &Metrics{reg: reg}
	m.httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gofer_http_requests_total",
		Help: "HTTP requests by method/route/status",
	}, []string{"method", "route", "status"})
	m.httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gofer_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "route"})
	m.jobsSubmitted = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gofer_jobs_submitted_total",
		Help: "Jobs submitted",
	}, []string{"caller", "project", "agent", "runner"})
	m.jobsTerminal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gofer_jobs_terminal_total",
		Help: "Jobs reaching a terminal state",
	}, []string{"status", "caller", "project"})
	m.jobDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gofer_job_duration_seconds",
		Help:    "Job submit→terminal duration in seconds (incl. queue wait)",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800},
	}, []string{"agent", "runner", "status"})
	// P4/T4.3: workflow-level terminal counter + duration histogram (design §9). A
	// workflow runs longer than a single job (chained steps), so the buckets reach
	// further (up to 1h) than the job histogram.
	m.workflowsTerminal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "gofer_workflows_terminal_total",
		Help: "Workflows (job chains) reaching a terminal state",
	}, []string{"status"})
	m.workflowDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "gofer_workflow_duration_seconds",
		Help:    "Workflow submit→terminal duration in seconds (whole chain)",
		Buckets: []float64{5, 15, 30, 60, 120, 300, 600, 1800, 3600},
	})
	m.reg.MustRegister(m.httpRequests, m.httpDuration, m.jobsSubmitted, m.jobsTerminal, m.jobDuration,
		m.workflowsTerminal, m.workflowDuration)
	return m
}

// Handler returns an http.Handler serving the registry in the Prometheus text
// exposition format. It reads the private registry only (never the global
// default), so the scrape output is exactly the collectors registered here.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// JobSubmitted records one job submission (job.MetricsSink). nil-safe. An empty
// caller (allow_empty_token direct submit) is labelled "anon" so the label is
// never the empty string.
func (m *Metrics) JobSubmitted(caller, project, agent, runner string) {
	if m == nil {
		return
	}
	m.jobsSubmitted.WithLabelValues(orAnon(caller), project, agent, runner).Inc()
}

// JobTerminal records one job reaching a terminal state plus its end-to-end
// duration (job.MetricsSink). nil-safe. durationSec is submit→terminal (incl.
// queue wait), per design §6.3.
func (m *Metrics) JobTerminal(status, caller, project, agent, runner string, durationSec float64) {
	if m == nil {
		return
	}
	m.jobsTerminal.WithLabelValues(status, orAnon(caller), project).Inc()
	m.jobDuration.WithLabelValues(agent, runner, status).Observe(durationSec)
}

// WorkflowTerminal records one workflow reaching a terminal state plus its whole-chain
// submit→terminal duration (job.MetricsSink, P4/T4.3). nil-safe. status is the workflow
// terminal status (done/failed/cancelled).
func (m *Metrics) WorkflowTerminal(status string, durationSec float64) {
	if m == nil {
		return
	}
	m.workflowsTerminal.WithLabelValues(status).Inc()
	m.workflowDuration.Observe(durationSec)
}

// ObserveHTTP records one HTTP request's count + duration. nil-safe. route must
// be a bounded route TEMPLATE (e.g. /v1/jobs/:id), never a raw path — the caller
// (httpapi.metricsMiddleware) is responsible for the cardinality guard.
func (m *Metrics) ObserveHTTP(method, route, status string, sec float64) {
	if m == nil {
		return
	}
	m.httpRequests.WithLabelValues(method, route, status).Inc()
	m.httpDuration.WithLabelValues(method, route).Observe(sec)
}

// RegisterRuntimeGauges registers the in-flight / queued / workers gauges as
// GaugeFuncs: each is evaluated at scrape time by reading live in-memory state
// (design §6.4), so there is NO periodic loop and no increment/decrement
// bookkeeping to keep consistent. nil-safe. stats returns (inflight, queued,
// running); workers returns (connected, inFlight).
func (m *Metrics) RegisterRuntimeGauges(stats func() (inflight, queued, running int), workers func() (connected, inFlight int)) {
	if m == nil {
		return
	}
	m.reg.MustRegister(
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gofer_jobs_in_flight",
			Help: "Live jobs (queued+running+pending)",
		}, func() float64 { i, _, _ := stats(); return float64(i) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gofer_jobs_queued",
			Help: "Jobs waiting for a concurrency slot",
		}, func() float64 { _, q, _ := stats(); return float64(q) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gofer_jobs_running",
			Help: "Jobs currently running",
		}, func() float64 { _, _, r := stats(); return float64(r) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gofer_workers_connected",
			Help: "Connected ws-workers",
		}, func() float64 { c, _ := workers(); return float64(c) }),
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "gofer_worker_in_flight",
			Help: "Jobs in flight across connected ws-workers",
		}, func() float64 { _, f := workers(); return float64(f) }),
	)
}

// orAnon maps an empty caller id (allow_empty_token pass-through) to "anon" so a
// metric label is never the empty string.
func orAnon(caller string) string {
	if caller == "" {
		return "anon"
	}
	return caller
}
