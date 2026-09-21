package worker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/testutil/testcmd"
	"github.com/inhere/gofer/internal/wsproto"
)

// parkMetrics parks the local job service at the LAST thing Submit does before it
// launches the execute goroutine. It is how the tests below hold the window open in
// which a dispatch has a published local job but no cancellable context behind it and
// no hub→local mapping in front of it (F3, bd h-aii-tcpm).
type parkMetrics struct {
	entered chan struct{} // buffered(1): signalled once JobSubmitted is entered
	release chan struct{} // closed by the test to let Submit finish
}

func (m *parkMetrics) JobSubmitted(string, string, string, string) {
	select {
	case m.entered <- struct{}{}:
	default:
	}
	<-m.release
}

func (m *parkMetrics) JobTerminal(string, string, string, string, string, float64) {}
func (m *parkMetrics) WorkflowTerminal(string, float64)                            {}

// newRealLocalJobs builds a REAL job.Service for the worker side of a cancel test:
// the cancel used to be dropped inside that service (a live job whose execute
// goroutine had not installed its cancellable context yet was cancelled as a silent
// no-op), so a fake Jobs would not observe the bug at all.
func newRealLocalJobs(t *testing.T) *job.Service {
	t.Helper()
	host, root := t.TempDir(), t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"alpha": {
				HostPath:       host,
				AllowedAgents:  []string{"exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
	}
	config.ApplyDefaults(cfg)
	st, err := jobstore.Open(filepath.Join(root, "worker.db"))
	if err != nil {
		t.Fatalf("open worker jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return job.NewService(cfg, project.NewRegistry(cfg, ""), agent.NewRegistry(cfg),
		map[string]runner.Runner{localrunner.Name: localrunner.New()}, st, nil)
}

// connectClientToCancelHub is connectClientToFakeHub with a cancel frame: the fake
// hub dispatches d1, and writes a cancel for it as soon as the test closes send.
func connectClientToCancelHub(t *testing.T, jobs Jobs, send <-chan struct{}) (*Client, chan wsproto.Envelope) {
	t.Helper()
	frames := make(chan wsproto.Envelope, 16)
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			return
		}
		ctx := req.Context()
		var reg wsproto.Envelope
		if err := wsjson.Read(ctx, conn, &reg); err != nil {
			return
		}
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeRegistered, Payload: mustRaw(wsproto.Registered{Accepted: true})})
		_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeDispatch, JobID: "d1", Payload: mustRaw(wsproto.Dispatch{
			JobID: "d1", ProjectKey: "alpha", Agent: "exec", Runner: "local",
			Cmd: testcmd.Cmd(t, "sleep", "30s"), Cwd: ".", TimeoutSec: 60,
		})})
		select {
		case <-send:
			_ = wsjson.Write(ctx, conn, wsproto.Envelope{Type: wsproto.TypeCancel, JobID: "d1", Payload: mustRaw(wsproto.Cancel{JobID: "d1"})})
		case <-ctx.Done():
			return
		}
		for {
			var env wsproto.Envelope
			if err := wsjson.Read(ctx, conn, &env); err != nil {
				return
			}
			frames <- env
		}
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/workers/connect"
	cl := New(Config{WorkerID: "w1", URLs: []string{wsURL}, Token: "t"}, jobs)
	cl.pollInterval = 20 * time.Millisecond
	return cl, frames
}

// TestCancelArrivingBetweenStartAndMappingIsHonoured (F3, bd h-aii-tcpm): the hub
// dispatches a job, then cancels it while the worker has ALREADY published its local
// job but has not yet registered the hub→local mapping — the exact interleaving the
// flaky TestE2ECancelOverWS hit (`worker.cancel_frame mapped=false` followed by a
// cancel that never reached the local job). The frame is parked (D-P2-9) and honoured
// the moment the mapping exists, and the local job must really stop: the cancel
// reaches a job whose execute goroutine has not installed its cancellable context
// yet, which is where it used to be dropped.
func TestCancelArrivingBetweenStartAndMappingIsHonoured(t *testing.T) {
	jobs := newRealLocalJobs(t)
	m := &parkMetrics{entered: make(chan struct{}, 1), release: make(chan struct{})}
	jobs.SetMetrics(m)

	sendCancel := make(chan struct{})
	cl, _ := connectClientToCancelHub(t, jobs, sendCancel)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go func() { _ = cl.Run(ctx) }()

	// The dispatch is in the window: the local job exists, the mapping does not.
	select {
	case <-m.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatched job never reached the local job service")
	}
	localID := waitLocalJobID(t, jobs)

	// The cancel frame lands in that window, so the worker has no mapping to follow.
	close(sendCancel)
	waitPendingCancel(t, cl, "d1")

	close(m.release) // Submit finishes → mapping → the parked cancel is consumed

	waitLocalJobStatus(t, jobs, localID, job.StatusCancelled, 10*time.Second)
}

// waitLocalJobID returns the only local job the service knows about.
func waitLocalJobID(t *testing.T, jobs *job.Service) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list, err := jobs.ListJobs(job.ListOpts{Limit: 10})
		if err != nil {
			t.Fatalf("list local jobs: %v", err)
		}
		if len(list) == 1 {
			return list[0].ID
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected exactly one local job")
	return ""
}

// waitLocalJobStatus polls the worker's own job service until the local job reaches
// want (or fails after d).
func waitLocalJobStatus(t *testing.T, jobs *job.Service, id, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if r, ok := jobs.Get(id); ok && r.Status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r, _ := jobs.Get(id)
	t.Fatalf("local job %s did not reach %q in time (status=%s)", id, want, r.Status)
}

// waitPendingCancel blocks until the cancel frame for remoteID has been parked as a
// pending cancel — proof that it arrived while the hub→local mapping did not exist.
func waitPendingCancel(t *testing.T, cl *Client, remoteID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cl.sessMu.Lock()
		_, ok := cl.pendingCancel[remoteID]
		cl.sessMu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("cancel for %s never arrived while the mapping was absent", remoteID)
}
