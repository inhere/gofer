package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
)

// newNotifyTestServer builds a server whose job service has an E14 notification
// config (one webhook subscribing the default trigger set) so a finished job
// enqueues a delivery the endpoint can return.
func newNotifyTestServer(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Token: testToken,
			Notification: &config.NotificationConfig{
				Webhooks:   []config.WebhookConfig{{URL: "https://hooks.example.com/gofer"}},
				AllowHosts: []string{"hooks.example.com"},
			},
		},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	projects := project.NewRegistry(cfg, "")
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	jobs := job.NewService(cfg, projects, agents, runners, openTestStore(t, root), nil)
	jobsEng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(jobsEng)
	return New(&cfg.Server, testToken, false, jobs, jobsEng, projects, agents, nil, nil, nil, nil)
}

// TestListDeliveries asserts GET /v1/jobs/{id}/deliveries returns the enqueued
// webhook deliveries for a finished job.
func TestListDeliveries(t *testing.T) {
	s := newNotifyTestServer(t)
	final := runExecJob(t, s, []string{"go", "version"})
	if final.Status != job.StatusDone {
		t.Fatalf("setup: status=%q, want done", final.Status)
	}

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+final.ID+"/deliveries", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deliveries status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Deliveries []jobstore.Delivery `json:"deliveries"`
	}
	decode(t, resp, &body)
	if len(body.Deliveries) != 1 {
		t.Fatalf("expected 1 delivery (job.terminal), got %d (%+v)", len(body.Deliveries), body.Deliveries)
	}
	d := body.Deliveries[0]
	if d.Status != jobstore.DeliveryPending {
		t.Errorf("delivery status=%q", d.Status)
	}
	if d.Target != "https://hooks.example.com/gofer" {
		t.Errorf("delivery target=%q", d.Target)
	}
}

// TestListDeliveriesEmpty asserts a job with no notification config yields an
// empty (non-nil) array, not null.
func TestListDeliveriesEmpty(t *testing.T) {
	s := newTestServer(t, testToken, false)
	final := runExecJob(t, s, []string{"go", "version"})

	resp := do(t, s, http.MethodGet, "/v1/jobs/"+final.ID+"/deliveries", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var body struct {
		Deliveries []jobstore.Delivery `json:"deliveries"`
	}
	decode(t, resp, &body)
	if body.Deliveries == nil {
		t.Fatal("deliveries should be a non-nil empty array")
	}
	if len(body.Deliveries) != 0 {
		t.Fatalf("expected 0 deliveries, got %d", len(body.Deliveries))
	}
}

// TestListDeliveriesUnknownJob asserts an unknown id is a 404.
func TestListDeliveriesUnknownJob(t *testing.T) {
	s := newTestServer(t, testToken, false)
	resp := do(t, s, http.MethodGet, "/v1/jobs/does-not-exist/deliveries", testToken, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}
