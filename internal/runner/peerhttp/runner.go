// Package peerhttp implements a remote runner that forwards a job to a peer
// agent-bridge over HTTP (plan §11.1, P7).
//
// Model: the host bridge does NOT resolve the agent/command/cwd locally for a
// peer-http job. It re-submits the ORIGINAL request (carried in
// runner.Request.Forward) to the peer's /v1/jobs with runner="local" (or the
// configured peer runner); the peer resolves and executes it using its OWN
// config. The host then consumes the peer's SSE stream to MIRROR the peer's log
// output into the local job's stdout.log / stderr.log (so the local /logs,
// /stream and list stay transparently usable for the proxied job) and to learn
// the authoritative terminal exit code / status. Host-side cancel/timeout flows
// through ctx and is forwarded to the peer.
package peerhttp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"

	"dev-agent-bridge/internal/client"
	"dev-agent-bridge/internal/job"
	"dev-agent-bridge/internal/runner"
)

// defaultPeerRunner is the runner the peer uses to actually execute the job.
const defaultPeerRunner = "local"

// Runner forwards jobs to a peer bridge identified by a base URL + token.
type Runner struct {
	name string
	c    *client.Client
}

// New builds a peer-http runner named name targeting baseURL with token (empty
// token => no Authorization header; the peer must allow empty-token auth).
func New(name, baseURL, token string) *Runner {
	return &Runner{name: name, c: client.New(baseURL, token)}
}

// Name implements runner.Runner.
func (r *Runner) Name() string { return r.name }

// Run forwards req.Forward to the peer, mirrors the peer's logs into
// req.Stdout/Stderr and returns the peer's authoritative terminal result.
//
//   - req.Forward must be set (the job service populates it for remote runners);
//     a nil Forward is a programming error and yields ExitCode -1.
//   - The host context (req's ctx) carries cancel/timeout: when it ends, the peer
//     job is cancelled best-effort. The job service classifies the resulting
//     status from ctx + Result, so this runner does not itself decide
//     cancelled vs timeout.
func (r *Runner) Run(ctx context.Context, req runner.Request) runner.Result {
	f := req.Forward
	if f == nil {
		return runner.Result{ExitCode: -1, Err: errors.New("peer runner requires forward request")}
	}

	peerRunner := f.PeerRunner
	if peerRunner == "" {
		peerRunner = defaultPeerRunner
	}
	jr := job.JobRequest{
		ProjectKey: f.ProjectKey,
		Agent:      f.Agent,
		Runner:     peerRunner,
		Prompt:     f.Prompt,
		Cmd:        f.Cmd,
		Cwd:        f.Cwd,
		TimeoutSec: f.TimeoutSec,
	}

	peerRes, err := r.c.SubmitJob(jr)
	if err != nil {
		return runner.Result{ExitCode: -1, Err: err}
	}
	peerID := peerRes.ID
	if peerID == "" {
		return runner.Result{ExitCode: -1, Err: errors.New("peer returned no job id")}
	}

	// Mirror the peer's SSE log stream into the local log writers and watch for a
	// terminal status. The stream is consumed under ctx so a host-side cancel
	// tears it down promptly.
	r.mirrorStream(ctx, peerID, req.Stdout, req.Stderr)

	// If the host context ended (cancel/timeout), forward the cancel to the peer
	// best-effort. We then still fetch the authoritative terminal result below.
	if ctx.Err() != nil {
		_, _ = r.c.CancelJob(peerID)
	}

	// Authoritative terminal state: regardless of how the SSE stream ended,
	// fetch the peer's final snapshot for exit_code / status.
	final, err := r.c.GetJob(peerID)
	if err != nil {
		// Fall back to whatever the submit / stream gave us; surface the error so
		// the job service marks the job failed.
		return runner.Result{ExitCode: -1, Err: err}
	}
	return runner.Result{ExitCode: final.ExitCode, Err: errFromStatus(final)}
}

// mirrorStream consumes the peer SSE stream and writes each `log` frame's text
// into the matching local writer; it returns when the stream emits `end`, a
// terminal `status`, the stream closes, or ctx ends. It is best-effort: the
// authoritative terminal result is fetched separately by the caller, so a
// stream hiccup never loses the job outcome.
func (r *Runner) mirrorStream(ctx context.Context, peerID string, stdout, stderr io.Writer) {
	resp, err := r.c.OpenStream(ctx, peerID)
	if err != nil {
		return // caller falls back to GetJob for the terminal result
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var buf []byte
	tmp := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			return
		}
		n, readErr := reader.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
			frames, rest := parseSSE(string(buf))
			buf = []byte(rest)
			for _, fr := range frames {
				if r.handleFrame(fr, stdout, stderr) {
					return // terminal frame seen
				}
			}
		}
		if readErr != nil {
			// EOF or transport error: drain any complete trailing frame, then stop.
			if len(buf) > 0 {
				frames, _ := parseSSE(string(buf) + "\n\n")
				for _, fr := range frames {
					if r.handleFrame(fr, stdout, stderr) {
						return
					}
				}
			}
			return
		}
	}
}

// handleFrame applies one SSE frame: `log` mirrors its text into the matching
// writer; `status` (terminal) and `end` signal the stream is finished (returns
// true). Unknown events are ignored.
func (r *Runner) handleFrame(fr sseFrame, stdout, stderr io.Writer) (done bool) {
	switch fr.Event {
	case "log":
		var lf logFrame
		if err := json.Unmarshal([]byte(fr.Data), &lf); err != nil {
			return false
		}
		w := stdout
		if lf.Stream == "stderr" {
			w = stderr
		}
		if w != nil && lf.Text != "" {
			_, _ = io.WriteString(w, lf.Text)
		}
	case "status":
		var jr job.JobResult
		if err := json.Unmarshal([]byte(fr.Data), &jr); err == nil && job.IsTerminal(jr.Status) {
			return true
		}
	case "end":
		return true
	}
	return false
}

// logFrame mirrors the server's SSE `log` payload (stream/seq/text). Declared
// here to avoid importing the httpapi package (which would pull the whole HTTP
// server into the runner).
type logFrame struct {
	Stream string `json:"stream"`
	Seq    int    `json:"seq"`
	Text   string `json:"text"`
}

// errFromStatus maps a peer terminal JobResult to a runner error: done => nil;
// any non-done terminal status surfaces the peer's error (or a generic message)
// so the host job service classifies the job consistently. The host also
// inspects its own ctx, so cancel/timeout classification stays correct even
// when the peer reports a different terminal reason.
func errFromStatus(res job.JobResult) error {
	switch res.Status {
	case job.StatusDone:
		return nil
	case job.StatusFailed, job.StatusTimeout, job.StatusCancelled:
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return errors.New("peer job " + res.Status)
	default:
		// Non-terminal (stream ended without a terminal snapshot and GetJob still
		// shows running/queued): treat as failed so we never mark a not-finished
		// job as done.
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return errors.New("peer job not terminal: " + res.Status)
	}
}
