//go:build unix

// pty_e2e_test.go is the WEB-03 P2 T7 end-to-end matrix: a real httptest serve
// core (hub + relay registry + nonce + pty-connect + browser attach) + a real
// worker.Client (dialing the hub + a second pty-connect ws) + a REAL pty child
// (interactive agent) + a browser attach ws (coder/websocket). Every hop is real
// loopback — no fakes on the data path — so these tests prove the interactive
// attach protocol end to end (design §4 timing, §8 matrix).
//
// It reuses the non-interactive harness helpers in e2e_test.go (same package):
// e2eToken / e2eWorkerID / createJob / waitWorkerOnline.
//
// Empirical grounding (verified with throwaway probes on the container's real
// pty before these tests were written):
//   - cancel teardown closes the master fd then sends an UNCATCHABLE SIGKILL
//     (unixPty.Close → ptmx.Close + Process.Kill), so a signal-trap "sentinel on
//     cancel" NEVER fires. The deterministic tail-byte proof therefore uses a
//     process-FINAL byte on a NATURAL exit (reliably drained by the sole reader),
//     not a trap sentinel — see TestE2EPtyTailBytesReachBrowserBeforeFinish.
package worker_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/core"
	"github.com/inhere/gofer/internal/httpapi"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/ptyrelay"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
	"github.com/inhere/gofer/internal/worker"
	"github.com/inhere/gofer/internal/wshub"
)

// interactiveAgentNames are registered (identically) on BOTH the host and the
// worker: the host validates interactive admission (interactive + no-raw-cmd +
// non-exec, P1 五闸) before dispatch; the WORKER actually resolves + runs the
// agent's fixed command under its pty runner. Because an interactive job cannot
// override Cmd, the command below is what really executes.
var interactiveAgentNames = []string{"termecho", "termtail", "termchatty", "exec"}

// interactiveAgents builds the shared agent set. termecho is a read-loop that
// echoes each line as "echo:<line>" and prints the terminal size on the literal
// line "SIZE" (so a browser resize is observable end to end). termtail blocks on
// one line of input, then emits a process-FINAL sentinel right before a natural
// exit (the deterministic tail-byte case). termchatty floods output forever
// (cancelled by the test) to prove the dedicated pty ws does not starve a quiet
// job on the shared hub ws.
func interactiveAgents() map[string]config.AgentConfig {
	cli := func(script string) config.AgentConfig {
		return config.AgentConfig{
			Type:        agent.TypeCLIAgent,
			Command:     "sh",
			Args:        []string{"-c", script},
			Interactive: true,
			NoRawCmd:    true,
		}
	}
	return map[string]config.AgentConfig{
		"termecho":   cli(`while IFS= read -r line; do if [ "$line" = SIZE ]; then stty size; else printf 'echo:%s\n' "$line"; fi; done`),
		"termtail":   cli(`head -n1 >/dev/null; printf FINAL_TAIL_SENTINEL_9Z`),
		"termchatty": cli(`while :; do printf 'CHATTY_%d\n' "$i"; i=$((i+1)); done`),
	}
}

// interactiveProject is project alpha allowing the interactive agents + the exec
// agent (a non-interactive slot holder / quiet job), the local runner (worker
// side) and remote-w1 (host side). maxConc gates local execution concurrency
// (0 = unbounded; 1 exercises queued-interactive rendezvous).
func interactiveProject(host string, allowedRunners []string, maxConc int) config.ProjectConfig {
	return config.ProjectConfig{
		HostPath:                 host,
		AllowedAgents:            interactiveAgentNames,
		InteractiveAllowedAgents: []string{"termecho", "termtail", "termchatty"},
		AllowedRunners:           allowedRunners,
		AllowExec:                true,
		MaxConcurrentJobs:        maxConc,
	}
}

// ptyHubSide bundles the serve side: the http server, its job service (whose
// persisted terminal rows the tests assert), the hub and the live pty relay
// registry (polled to know when the worker has dialed the pty ws in / relay Open).
type ptyHubSide struct {
	ts     *httptest.Server
	jobs   *job.Service
	relays *ptyrelay.Registry
	hub    *wshub.Hub
}

// buildInteractiveHubSide stands up a real serve core via core.Build — which wires
// the pty-CAPABLE hub worker selector (interactive worker admission checks
// cand.PtyCapable, config.go), the worker runner with the shared relay nonce/relay
// registry, and the hub singleton — then mounts the httpapi with SetPtyRelay so
// /v1/workers/pty-connect + browser attach resolve the same live relay state. The
// host's own pty runner is unused: a worker-routed interactive job forwards to
// remote-w1 (the pty is chosen on the worker, submit.go).
func buildInteractiveHubSide(t *testing.T) *ptyHubSide {
	t.Helper()
	host := t.TempDir()
	root := t.TempDir()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Token:   "server-default-token",
			Workers: map[string]config.WorkerAuthConfig{e2eWorkerID: {Token: e2eToken}},
		},
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"alpha": interactiveProject(host, []string{"remote-w1"}, 0)},
		Agents:   interactiveAgents(),
		Runners:  map[string]config.RunnerConfig{"remote-w1": {Type: "worker", WorkerID: e2eWorkerID}},
	}
	config.ApplyDefaults(cfg)

	cr, err := core.Build(cfg)
	if err != nil {
		t.Fatalf("core.Build: %v", err)
	}
	t.Cleanup(func() { _ = cr.Close() })

	srv := httpapi.New(&cfg.Server, "server-default-token", false, cr.Jobs, cr.Workflow(), cr.Projects, cr.Agents, cr.Hub, cfg.Runners, nil, nil)
	srv.SetPtyRelay(cr.RelayNonces, cr.PtyRelays)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &ptyHubSide{ts: ts, jobs: cr.Jobs, relays: cr.PtyRelays, hub: cr.Hub}
}

// buildInteractiveWorkerSide builds the worker's OWN local core (interactive
// agents + local runner + a REAL pty runner) and a worker.Client dialing the hub,
// wiring the client as the pty session observer exactly like the worker command
// (SetObserver before Serve). maxProjConc gates the worker project's local
// execution concurrency.
func buildInteractiveWorkerSide(t *testing.T, hubURL string, maxProjConc int) (*worker.Client, *job.Service) {
	t.Helper()
	if !ptyrunner.Available() {
		t.Skip("pty backend not available")
	}
	host := t.TempDir()
	root := t.TempDir()
	cfg := &config.Config{
		Storage:  config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{"alpha": interactiveProject(host, []string{"local"}, maxProjConc)},
		Agents:   interactiveAgents(),
	}
	config.ApplyDefaults(cfg)
	projReg := project.NewRegistry(cfg, "")
	agentReg := agent.NewRegistry(cfg)
	st, err := jobstore.Open(root + "/worker.db")
	if err != nil {
		t.Fatalf("open worker jobstore: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	ptyR := ptyrunner.New()
	runners[ptyrunner.Name] = ptyR
	localJobs := job.NewService(cfg, projReg, agentReg, runners, st, nil)

	wsURL := "ws" + strings.TrimPrefix(hubURL, "http") + "/v1/workers/connect"
	cl := worker.New(worker.Config{
		WorkerID: e2eWorkerID,
		URLs:     []string{wsURL},
		Token:    e2eToken,
		Projects: []string{"alpha"},
		Agents:   interactiveAgentNames,
	}, localJobs)
	ptyR.SetObserver(cl) // worker path only (serve never sets it → keeps discard, G023)
	return cl, localJobs
}

// startWorker runs the client in the background and blocks until it is registered
// (deterministic: hub.IsOnline flips true once the registered ack is sent — reuses
// the non-interactive harness's waitWorkerOnline).
func startWorker(t *testing.T, hub *ptyHubSide, cl *worker.Client) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = cl.Run(ctx) }()
	waitWorkerOnline(t, hub.hub)
	t.Cleanup(cancel)
	return cancel
}

// createInteractiveJob POSTs an interactive worker-routed job and returns its id.
func createInteractiveJob(t *testing.T, hub *ptyHubSide, agentName string) string {
	t.Helper()
	created := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: agentName, Runner: "remote-w1", WorkerID: e2eWorkerID,
		Prompt: "start", Cwd: ".", TimeoutSec: 60, Interactive: true,
	})
	if created.ID == "" {
		t.Fatal("interactive job has no id")
	}
	return created.ID
}

// waitRelayState polls the live relay registry until jobID's relay reaches Open or
// Attached (i.e. the worker dialed the pty ws in). It is the deterministic "the
// pty is ready to attach" signal (no sleep-and-hope).
func waitRelayOpen(t *testing.T, hub *ptyHubSide, jobID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := hub.relays.Lookup(jobID); ok && e.Relay != nil &&
			(e.State == ptyrelay.RelayOpen || e.State == ptyrelay.RelayAttached) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("relay for job %s never reached open within timeout", jobID)
}

// postAttachTicket asks the serve HTTP API for a browser attach ticket (same
// caller token that created the job, so callerMayAttach passes).
func postAttachTicket(t *testing.T, hub *ptyHubSide, jobID string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, hub.ts.URL+"/v1/jobs/"+jobID+"/attach-ticket", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST attach-ticket: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("attach-ticket status = %d", resp.StatusCode)
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode ticket: %v", err)
	}
	if out.Ticket == "" {
		t.Fatal("empty attach ticket")
	}
	return out.Ticket
}

// dialBrowserAttach opens the browser attach ws (no Origin header → same-origin,
// accepted). The returned conn reads binary pty output + writes {t:i}/{t:r} frames.
func dialBrowserAttach(t *testing.T, hub *ptyHubSide, jobID, ticket string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(hub.ts.URL, "http") + "/v1/jobs/" + jobID + "/attach?ticket=" + ticket
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial attach: %v", err)
	}
	conn.SetReadLimit(1 << 20)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "test done") })
	return conn
}

// attachBrowser is the full helper: wait relay open → ticket → dial.
func attachBrowser(t *testing.T, hub *ptyHubSide, jobID string) *websocket.Conn {
	t.Helper()
	waitRelayOpen(t, hub, jobID)
	return dialBrowserAttach(t, hub, jobID, postAttachTicket(t, hub, jobID))
}

func sendBrowserInput(t *testing.T, conn *websocket.Conn, b []byte) {
	t.Helper()
	frame, _ := json.Marshal(map[string]any{"t": "i", "d": base64.StdEncoding.EncodeToString(b)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write input frame: %v", err)
	}
}

func sendBrowserResize(t *testing.T, conn *websocket.Conn, cols, rows int) {
	t.Helper()
	frame, _ := json.Marshal(map[string]any{"t": "r", "cols": cols, "rows": rows})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatalf("write resize frame: %v", err)
	}
}

// readBrowserUntil accumulates binary pty output until want appears (or timeout).
// It returns the full accumulated output so the caller can make further asserts.
func readBrowserUntil(t *testing.T, conn *websocket.Conn, want string, timeout time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var acc []byte
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("attach read waiting for %q: got=%q err=%v", want, string(acc), err)
		}
		if typ == websocket.MessageBinary {
			acc = append(acc, data...)
			if bytes.Contains(acc, []byte(want)) {
				return string(acc)
			}
		}
	}
}

func cancelHostJob(t *testing.T, hub *ptyHubSide, jobID string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, hub.ts.URL+"/v1/jobs/"+jobID+"/cancel", nil)
	req.Header.Set("Authorization", "Bearer server-default-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST cancel: %v", err)
	}
	_ = resp.Body.Close()
}

// waitHostTerminal waits for the host job to reach a terminal state and returns it.
func waitHostTerminal(t *testing.T, hub *ptyHubSide, jobID string) job.JobResult {
	t.Helper()
	final, ok := hub.jobs.Wait(jobID)
	if !ok {
		t.Fatalf("host job %s not found", jobID)
	}
	return final
}

// --- #1 正常 attach: dispatch→observer 交接→pty ws→输入 echo→输出回传→resize 生效 ---

// TestE2EPtyAttachEchoAndResize proves the full happy path end to end: an
// interactive worker job starts a real pty, the worker hands its output to the
// pump (observer handoff) + dials the second pty ws in, a browser attaches, its
// input is echoed back through the whole pipe, and a browser resize reaches the
// real pty (the child reports the new stty size).
func TestE2EPtyAttachEchoAndResize(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createInteractiveJob(t, hub, "termecho")
	conn := attachBrowser(t, hub, jobID)

	// input → echo round-trips through: browser → serve viewer → relay → pty ws →
	// worker ptyInputLoop → sess.WriteInput → child; child stdout → sess.Read (pump)
	// → pty ws → serve source → relay ring → viewer → browser.
	sendBrowserInput(t, conn, []byte("hello\n"))
	readBrowserUntil(t, conn, "echo:hello", 5*time.Second)

	// resize → the browser {t:r} reaches the real pty (child prints the new size on
	// the SIZE trigger, sent AFTER the resize on the same ordered ws).
	sendBrowserResize(t, conn, 120, 40)
	sendBrowserInput(t, conn, []byte("SIZE\n"))
	readBrowserUntil(t, conn, "40 120", 5*time.Second)

	// end the persistent child so the job/relay converge cleanly.
	cancelHostJob(t, hub, jobID)
	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusCancelled && final.Status != job.StatusFailed {
		t.Fatalf("status = %s, want cancelled/failed after cancel", final.Status)
	}
}

// --- #2 尾字节证明: 尾字节到 browser 后 host job 才 finish (D-P2-2 + D-P2-6) ---

// TestE2EPtyTailBytesReachBrowserBeforeFinish proves the drain-before-finish
// invariant: the child emits a process-FINAL sentinel right before it exits; the
// browser must receive that complete tail, and only THEN does the host job reach
// terminal (the host waits relay.Done() = recordLoop EOF before finishing, so the
// terminal Result never truncates the visible output).
//
// (A signal-trap "sentinel on cancel" cannot be used: cancel teardown SIGKILLs the
// child — verified empirically — so the tail is produced on a NATURAL exit instead,
// which is the same relay drain machinery.)
func TestE2EPtyTailBytesReachBrowserBeforeFinish(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createInteractiveJob(t, hub, "termtail")
	conn := attachBrowser(t, hub, jobID) // attached BEFORE the sentinel → live, not scrollback

	// Assert the host job is NOT yet terminal (the child blocks on one input line).
	if r, ok := hub.jobs.Get(jobID); ok && job.IsTerminal(r.Status) {
		t.Fatalf("job terminal too early: %s", r.Status)
	}

	// Trigger the natural exit: head -n1 consumes this line, then the child prints
	// the final sentinel and exits.
	sendBrowserInput(t, conn, []byte("go\n"))

	// The browser must receive the COMPLETE process-final tail.
	got := readBrowserUntil(t, conn, "FINAL_TAIL_SENTINEL_9Z", 8*time.Second)
	if !strings.Contains(got, "FINAL_TAIL_SENTINEL_9Z") {
		t.Fatalf("browser missing tail sentinel; got=%q", got)
	}

	// And only after the drain does the host job finish (done). If the drain-wait
	// were broken the browser would have seen a truncated tail above.
	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
}

// --- #3 cancel e2e: host cancel→teardown→relay 读完→Done→cancelled ---

// TestE2EPtyHostCancel proves a host cancel of a live interactive session tears
// the pty down and converges the host job to a terminal state, with the browser
// having seen live output first (the input pump must not itself spuriously cancel —
// selfClosing gate — which would still land here but the clean cancelled/failed
// terminal + prior live echo is the e2e signal).
func TestE2EPtyHostCancel(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createInteractiveJob(t, hub, "termecho")
	conn := attachBrowser(t, hub, jobID)

	// live echo proves the session is genuinely running + attached before cancel.
	sendBrowserInput(t, conn, []byte("ping\n"))
	readBrowserUntil(t, conn, "echo:ping", 5*time.Second)

	cancelHostJob(t, hub, jobID)

	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusCancelled && final.Status != job.StatusFailed {
		t.Fatalf("status = %s, want cancelled/failed after host cancel", final.Status)
	}
	// The relay must be finalized after convergence (Done fired / closed).
	if e, ok := hub.relays.Lookup(jobID); ok && e.State != ptyrelay.RelayFinalized {
		t.Fatalf("relay state = %s after cancel, want finalized", e.State)
	}
}

// --- #6 queued interactive: 并发=1 长任务占槽, interactive 排队后仍成功拨入 ---

// TestE2EPtyQueuedInteractive proves the event-driven rendezvous survives the
// worker's local concurrency queue: with the worker project capped at 1, a long
// non-interactive job holds the only slot, so a subsequently-dispatched interactive
// job QUEUES (its pty has not started). waitSession parks (no polling); once the
// slot frees the pty session starts, the pump dials in and the browser can attach +
// echo — i.e. the queued interactive job still attaches successfully.
func TestE2EPtyQueuedInteractive(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 1) // worker project concurrency = 1
	startWorker(t, hub, cl)

	// Slot holder: a non-interactive exec sleep occupies the single project slot.
	holder := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"sleep", "2"}, Cwd: ".", TimeoutSec: 60,
	})
	if holder.ID == "" {
		t.Fatal("slot-holder job has no id")
	}

	// Interactive job dispatched while the slot is taken → it queues on the worker.
	jobID := createInteractiveJob(t, hub, "termecho")

	// It must eventually dial its pty ws in (relay Open) AFTER the holder releases —
	// waitRelayOpen tolerates the queue delay (10s budget > 2s sleep).
	conn := attachBrowser(t, hub, jobID)
	sendBrowserInput(t, conn, []byte("queued\n"))
	readBrowserUntil(t, conn, "echo:queued", 8*time.Second)

	cancelHostJob(t, hub, jobID)
	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusCancelled && final.Status != job.StatusFailed {
		t.Fatalf("queued interactive status = %s, want cancelled/failed", final.Status)
	}
}

// --- #9 pty ws 外部断 (sess running) → worker Cancel → job 终态 ---

// TestE2EPtyExternalDisconnectFailsJob proves the断连即终止 judgement end to end:
// forcibly closing the serve-side relay (as a serve crash / network drop would)
// drops the worker's pty ws; with the session still running the worker cancels the
// local job (D-P2-5), so the host job converges to a terminal state rather than the
// pty running headless forever.
func TestE2EPtyExternalDisconnectFailsJob(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createInteractiveJob(t, hub, "termecho")
	conn := attachBrowser(t, hub, jobID)
	sendBrowserInput(t, conn, []byte("live\n"))
	readBrowserUntil(t, conn, "echo:live", 5*time.Second)

	// External drop: close the serve relay → source ws closed → worker pty ws errors.
	hub.relays.Close(jobID, "e2e_external_drop")

	final := waitHostTerminal(t, hub, jobID)
	if final.Status != job.StatusFailed && final.Status != job.StatusCancelled {
		t.Fatalf("status = %s, want failed/cancelled after external pty drop", final.Status)
	}
}

// --- #11 browser 断/重连 → viewer 掉 → worker 无感 → 重连回放 Scrollback ---

// TestE2EPtyBrowserReconnectReplaysScrollback proves a browser disconnect only
// drops the viewer (the job + relay + worker are unaffected), and a reconnect (new
// ticket) replays the recorded scrollback so the second browser sees the earlier
// output.
func TestE2EPtyBrowserReconnectReplaysScrollback(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	jobID := createInteractiveJob(t, hub, "termecho")
	b1 := attachBrowser(t, hub, jobID)
	sendBrowserInput(t, b1, []byte("history\n"))
	readBrowserUntil(t, b1, "echo:history", 5*time.Second)

	// Browser 1 leaves; the job must stay live (relay not finalized).
	_ = b1.Close(websocket.StatusNormalClosure, "reconnect test")
	// Give the serve side a moment to drop the viewer; the relay must remain open.
	time.Sleep(100 * time.Millisecond)
	if e, ok := hub.relays.Lookup(jobID); !ok || e.State == ptyrelay.RelayFinalized {
		t.Fatalf("relay finalized after browser disconnect — worker should be unaffected")
	}

	// Reconnect: a fresh ticket + ws must replay the scrollback containing the
	// earlier echo.
	b2 := dialBrowserAttach(t, hub, jobID, postAttachTicket(t, hub, jobID))
	readBrowserUntil(t, b2, "echo:history", 5*time.Second)

	cancelHostJob(t, hub, jobID)
	_ = waitHostTerminal(t, hub, jobID)
}

// --- #13 chatty pty 不饿死 quiet job (专用 ws 隔离) ---

// TestE2EPtyChattyDoesNotStarveQuiet proves the dedicated pty ws isolates a chatty
// interactive job's byte flood from a quiet job's terminal result: while a chatty
// interactive job floods its OWN pty ws, a quiet non-interactive job's result still
// arrives promptly over the SHARED hub ws (no head-of-line block).
func TestE2EPtyChattyDoesNotStarveQuiet(t *testing.T) {
	hub := buildInteractiveHubSide(t)
	cl, _ := buildInteractiveWorkerSide(t, hub.ts.URL, 0)
	startWorker(t, hub, cl)

	// Chatty interactive job floods output forever (its pump reads it on a dedicated
	// pty ws). Wait until its relay is open so it is genuinely streaming.
	chatty := createInteractiveJob(t, hub, "termchatty")
	waitRelayOpen(t, hub, chatty)

	// A quiet non-interactive job's result must not be starved by the flood.
	quiet := createJob(t, hub.ts, job.JobRequest{
		ProjectKey: "alpha", Agent: "exec", Runner: "remote-w1", WorkerID: e2eWorkerID,
		Cmd: []string{"echo", "quiet-done"}, Cwd: ".", TimeoutSec: 30,
	})
	done := make(chan job.JobResult, 1)
	go func() {
		final, _ := hub.jobs.Wait(quiet.ID)
		done <- final
	}()
	select {
	case final := <-done:
		if final.Status != job.StatusDone {
			t.Fatalf("quiet status = %s, want done", final.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("quiet job's result starved by chatty pty flood (HOL block)")
	}

	// Tear the chatty job down.
	cancelHostJob(t, hub, chatty)
	_ = waitHostTerminal(t, hub, chatty)
}
