package worker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
)

// handleDispatch runs one dispatched job locally and bridges its log/result back
// to the hub keyed by the SERVER-side job_id (d.JobID). The worker's local job
// has its own id; only d.JobID is used on the wire.
//
// review #8: the worker re-validates with its OWN config via job.Service.Submit
// (project/agent allowlist + exec gate + SafeJoin). A local validation failure is
// reported back as result{failed} (no new frame type needed).
//
// sessionURL is the hub address this dispatch arrived on (recvLoop threads it,
// D-P2-7). An interactive dispatch derives its pty-connect URL from it (T5); the
// non-interactive path never reads it.
//
// Lifetime (RECOV-01). ctx is the PROCESS-scoped ctx of Run — threaded recvLoop ←
// runSession ← Run — and NOT the connection's: this goroutine already outlives the
// connection that dispatched it, keeps the local job running across a hub blip, and
// re-sends whatever it could not push while the socket was down. That is exactly the
// RECOV-01 requirement, and it is why no ctx change is needed here: a connection
// ends when recvLoop returns, never by cancelling this ctx. The goroutine ends when
// the job reaches its terminal Result (below) or when the process shuts down (ctx
// cancelled → the local job is cancelled and its terminal Result is attempted — or
// cached — as usual).
func (cl *Client) handleDispatch(ctx context.Context, sessionURL string, d wsproto.Dispatch) {
	startedAt := time.Now()
	// RECOV-01: this job is in the recovery table from the moment its dispatch exists
	// — BEFORE any early return — so a terminal Result nobody could receive is cached
	// and replayed, and the register frame never reports the job as gone while this
	// goroutine is still unwinding.
	cl.inflightCreate(d.JobID)
	slog.Info("worker.job_started", "event", "worker.job_started", "component", "worker", "worker_id", cl.workerID, "job_id", d.JobID)
	// stale pendingCancel cleanup (D-P2-9): whichever return path this dispatch
	// takes, ensure d.JobID does not linger in pendingCancel. The normal consume is
	// the inline take after putJobMapping below; this defer backstops the paths that
	// never map (fail-fast / Submit failure / a job this worker was never given).
	// It is idempotent — a no-op once the inline take already consumed the record.
	defer cl.takePendingCancel(d.JobID)

	// fail-fast (D-P2-4): an interactive dispatch missing its relay credentials can
	// never be attached (the serve pty-connect endpoint strong-checks nonce +
	// pty_session_id). Do not start a bare, un-attachable pty — report failed.
	if d.Interactive && (d.RelayNonce == "" || d.PtySessionID == "") {
		slog.Warn("worker.job_rejected", "event", "worker.job_rejected", "component", "worker", "worker_id", cl.workerID, "job_id", d.JobID, "reason", "interactive dispatch missing relay credentials")
		cl.sendResult(ctx, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1,
			Error: "interactive dispatch missing relay credentials",
		})
		return
	}

	res, err := cl.jobs.Submit(job.JobRequest{
		ProjectKey:   d.ProjectKey,
		Agent:        d.Agent,
		Runner:       builtinLocalRunner, // always local on the worker
		Prompt:       d.Prompt,
		AgentArgs:    d.AgentArgs,
		SystemPrompt: d.SystemPrompt,
		Cmd:          d.Cmd,
		Cwd:          d.Cwd,
		// WT-01: the worktree is created HERE (the worker owns the checkout) by the
		// shared job.Service. An old hub never sets these → byte-identical to before.
		Worktree:     d.Worktree,
		WorktreeBase: d.WorktreeBase,
		TimeoutSec:   d.TimeoutSec,
		// T5 projection: carry the interactive flag + initial window so the worker's
		// own job.Service picks its pty runner (Interactive && !remote). Zero-valued
		// for a non-interactive dispatch → byte-for-byte the existing path (G023).
		Interactive:       d.Interactive,
		Cols:              d.Cols,
		Rows:              d.Rows,
		ResumeSourceAgent: d.ResumeSourceAgent,
		// Session relay §9.1 B: the pty started here, so the hub's priming text and
		// the quiet window it resolved come from the dispatch — the worker does not
		// re-derive them from its own config. Both are empty on a pre-v7 hub's frame.
		InitialInput:        d.InitialInput,
		InitialInputQuietMs: d.InitialInputQuietMs,
		// ACP-01 S2: a continuation's session + lineage (both empty on a plain
		// dispatch, and both absent entirely from a pre-S2 hub's frame). ResumedFrom
		// is what makes SessionID a session/load rather than a plain binding.
		SessionID:   d.SessionID,
		ResumedFrom: d.ResumedFrom,
		// bd h-aii-0ql3: the worker's own job.Service re-validates read_only against
		// ITS agent config, so a worker whose agent has no read-only mode fails the job
		// with an error the hub can show instead of running it writable.
		ReadOnly: d.ReadOnly,
		// GATE-01 S3: 人工验收 is decided by the HUB (the design's "验收判定只在 hub
		// 做"), so a dispatched job's LOCAL row must finish normally — its status is
		// what the Result frame reports and what the log-tail loop waits on, and a
		// local needs_review row would stall the dispatch (no terminal Result ever
		// reaches the hub). ReviewFixed makes the worker's own project
		// require_review default inapplicable to work it merely executes.
		ReviewFixed: true,
	})
	if err != nil {
		slog.Warn("worker.job_rejected", "event", "worker.job_rejected", "component", "worker", "worker_id", cl.workerID, "job_id", d.JobID, "reason", err.Error())
		cl.sendResult(ctx, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: err.Error(),
		})
		return
	}

	localID := res.ID
	cl.inflightSetLocal(d.JobID, localID)
	// Register the hub→local id mapping so an inbound cancel/answer frame (keyed by
	// the hub id d.JobID) reaches this local job; drop it when the dispatch ends.
	cl.putJobMapping(d.JobID, localID)
	defer cl.dropJobMapping(d.JobID)
	dispatchDone := make(chan struct{})
	defer close(dispatchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cl.jobs.Cancel(localID)
		case <-dispatchDone:
		}
	}()

	// D-P2-9: a cancel frame that arrived BEFORE the mapping existed was parked in
	// pendingCancel; now that the local job is submitted+mapped, honour it at once
	// (covers non-interactive dispatches too).
	if cl.takePendingCancel(d.JobID) {
		_ = cl.jobs.Cancel(localID)
	}

	// G1: report the rendered command as soon as the local job computes it
	// (execute() sets it at StatusRunning). The full Outcome only ships at
	// completion (below); without this, a HOST-side timeout — which finalises the
	// job before the worker's terminal Outcome arrives — records no rendered
	// command. Best-effort, bounded, never blocks the dispatch.
	go cl.reportRenderedCommandEarly(ctx, d.JobID, localID)

	// interactive: dial the SECOND, pty-dedicated ws and pump bytes both ways once
	// the local pty session has started (the PtyRunner observer hands it to us via
	// waitSession). pumpDone is joined below, before the terminal Result, so the
	// serve relay has drained + closed (recordLoop EOF) first. A nil session (job
	// ended before its pty started / ctx torn down) skips the pump.
	var pumpDone <-chan struct{}
	if d.Interactive {
		if sess := cl.waitSession(ctx, localID); sess != nil {
			pumpDone = cl.pumpPtyFn(ctx, sessionURL, localID, d.JobID, d.PtySessionID, d.RelayNonce, sess)
		}
	}

	// Stream local log output back to the hub until the job is terminal, then send
	// the authoritative result. It also bridges the local job's pending interactions
	// up to the hub as interaction{open} frames (P2). For an interactive pty job the
	// log tail is empty and pumpInteractions a no-op — it is kept only for uniform
	// terminal detection; the pty bytes flow over the pump ws, not the log frames.
	cl.streamLocalJob(ctx, localID, res.ResultDir, d.JobID)

	final, ok := cl.jobs.Wait(localID)
	if !ok {
		slog.Info("worker.job_finished", "event", "worker.job_finished", "component", "worker", "worker_id", cl.workerID, "job_id", d.JobID, "status", job.StatusFailed, "exit_code", -1, "duration_ms", time.Since(startedAt).Milliseconds())
		if pumpDone != nil {
			<-pumpDone
		}
		cl.sendResult(ctx, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: "local job not found",
		})
		return
	}

	// Join the pump before the terminal Result: the worker has drained the pty and
	// closed the pump ws (→ serve recordLoop EOF) so the host's Done() can fire and
	// the browser sees the tail bytes before the job finishes (D-P2-2 / D-P2-6).
	if pumpDone != nil {
		<-pumpDone
	}

	// 产出与审计回传(P4)：本地 job 已由共享 job.Service 在终态 captureOutcomes 采集
	// (渲染命令/result.json/diff/产物清单, P1–P3)。把清单+小结果经 Outcome 帧回传
	// host（大产物文件留 worker 侧, 不进帧 — D6），在 Result 帧之前发出，保证 host
	// 端读循环先 OnOutcome 再 Finish。仅在有产出时发，旧 worker 不发 = host 端为空。
	if o, send := outcomeFrame(d.JobID, final); send {
		_ = cl.writeFrame(ctx, wsproto.TypeOutcome, d.JobID, o)
	}

	cl.sendResult(ctx, d.JobID, wsproto.Result{
		JobID:    d.JobID,
		Status:   final.Status,
		ExitCode: final.ExitCode,
		Error:    final.Error,
	})
	slog.Info("worker.job_finished", "event", "worker.job_finished", "component", "worker", "worker_id", cl.workerID, "job_id", d.JobID, "status", final.Status, "exit_code", final.ExitCode, "duration_ms", time.Since(startedAt).Milliseconds())
}

// outcomeFrame builds the P4 Outcome frame from the worker's local terminal
// JobResult (产出已由共享 job.Service 在终态 captureOutcomes 落到该 JobResult)。它
// 只回清单+小结果: rendered_command / result.json / diff摘要 / 产物清单元数据
// (ArtifactsJSON 是已序列化的 []ArtifactItem JSON, 原样塞 raw)。send=false 表示无
// 任何产出 → 不发帧 (host 端 outcome 为空)。大产物文件本身留 worker 侧 (D6)。
func outcomeFrame(remoteJobID string, final job.JobResult) (wsproto.Outcome, bool) {
	o := wsproto.Outcome{
		JobID:           remoteJobID,
		RenderedCommand: final.RenderedCommand,
		ResultJSON:      final.ResultJSON,
		DiffSummary:     final.DiffSummary,
		// worker 侧共享 job.Service 已在终态 captureOutcomes 把 session_id 填进本地
		// JobResult（claude 注入 / codex 捕获, P1）；随 Outcome 帧回传 host (P3)。
		SessionID: final.SessionID,
		// WT-01：worker 侧 worktree 位置/分支/基线/终态分支状态，随产出回传 host，
		// 让 host 行也能显示交付物分支（路径是那台 worker 机器上的）。
		WorktreePath:    final.WorktreePath,
		WorktreeBranch:  final.WorktreeBranch,
		WorktreeBaseSHA: final.WorktreeBaseSHA,
		WorktreeHeadSHA: final.WorktreeHeadSHA,
		CommitsAhead:    final.CommitsAhead,
	}
	if final.ArtifactsJSON != "" {
		o.Artifacts = json.RawMessage(final.ArtifactsJSON)
	}
	send := o.RenderedCommand != "" || o.ResultJSON != "" || o.DiffSummary != "" || len(o.Artifacts) > 0 ||
		o.SessionID != "" || o.WorktreePath != ""
	return o, send
}

// reportRenderedCommandEarly sends a rendered-command-only Outcome frame as soon as
// the local job has computed its argv (execute() sets RenderedCommand at
// StatusRunning), so a host-side timeout/cancel still records WHAT ran (G1). The
// hub sink stashes the latest Outcome (last wins): the final completion Outcome —
// carrying result.json/diff/artifacts/session_id too — overwrites this on a clean
// finish, so this early frame only takes effect when the job never completes
// cleanly. Best-effort: a bounded poll and a single frame; it never blocks the
// dispatch and gives up quietly if the command stays unrendered (e.g. the job died
// before running).
func (cl *Client) reportRenderedCommandEarly(ctx context.Context, remoteJobID, localID string) {
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if snap, ok := cl.jobs.Get(localID); ok && snap.RenderedCommand != "" {
			_ = cl.writeFrame(ctx, wsproto.TypeOutcome, remoteJobID, wsproto.Outcome{
				JobID:           remoteJobID,
				RenderedCommand: snap.RenderedCommand,
			})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// streamLocalJob tails the local job's stdout.log / stderr.log incrementally and
// pushes the new bytes as log frames to the hub, until the local job reaches a
// terminal state (a final drain follows). It is a mini in-process log consumer
// (no HTTP): it reads the files directly under <base>/<localID>/. seq is
// monotonic per (worker job, stream).
//
// RECOV-01: this loop is the worker's outage buffer. It runs for the JOB's lifetime
// (it returns only on a terminal local job or process shutdown), so a hub blip
// neither stops it nor loses bytes: the read offsets live on the job's in-flight
// entry (not in a local variable), advance ONLY after a writeFrame actually
// succeeded, and the hub's resume ack can rewind them — a dropped chunk is therefore
// re-sent verbatim after the reconnect, and a chunk the hub already has is not sent
// twice.
func (cl *Client) streamLocalJob(ctx context.Context, localID, resultDir, remoteJobID string) {
	base := filepath.Dir(resultDir)
	stdoutPath := filepath.Join(base, localID, store.StdoutFile)
	stderrPath := filepath.Join(base, localID, store.StderrFile)

	// seenStatus dedupes interaction frames: it remembers the last status pushed
	// per interaction id, so a re-poll only emits a frame on a status change (same
	// open/answered/cancelled vocabulary the SSE pumpInteractions uses).
	seenStatus := map[string]string{}

	pump := func() {
		for _, ent := range []struct {
			stream string
			path   string
		}{
			{string(store.StreamStdout), stdoutPath},
			{string(store.StreamStderr), stderrPath},
		} {
			// The offset comes from the shared in-flight entry: the resume ack may have
			// rewound it since the last tick, and this read must pick that up.
			chunk, next := tailFrom(ent.path, cl.inflightOffset(remoteJobID, ent.stream))
			if len(chunk) == 0 {
				continue
			}
			seq := cl.inflightSeq(remoteJobID) + 1
			if err := cl.writeFrame(ctx, wsproto.TypeLog, remoteJobID, wsproto.Log{
				JobID: remoteJobID, Stream: ent.stream, Seq: int(seq), Text: string(chunk),
			}); err != nil {
				// Do NOT advance: the same chunk is retried (with the same seq+offset
				// relationship) on the next tick, which is what makes a blip lossless.
				continue
			}
			cl.inflightCommit(remoteJobID, ent.stream, next, seq)
		}
		cl.pumpInteractions(ctx, localID, remoteJobID, seenStatus)
	}

	ticker := time.NewTicker(cl.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			pump() // best-effort final drain
			return
		case <-ticker.C:
			pump()
			cur, ok := cl.jobs.Get(localID)
			if ok {
				// Keep the recovery table's view of the job current: the register frame
				// reports it, and the hub decides resume-vs-wait-for-Result on it.
				cl.inflightSetStatus(remoteJobID, cur.Status)
			}
			if !ok || job.IsTerminal(cur.Status) {
				pump() // drain the tail produced just before terminal
				return
			}
		}
	}
}

// sendResult delivers a job's terminal Result and keeps the recovery table
// consistent (RECOV-01), and it is the ONLY way a dispatch reports a Result:
//
//   - on success the in-flight entry is dropped — the hub owns the job from here on;
//   - on failure (the connection is down, or dropped exactly while the job was
//     finishing) the Result is CACHED on the entry and replayed by
//     replayCachedResults after the next successful register. The local job is
//     already terminal, so nothing needs recomputing — the hub just never heard it.
func (cl *Client) sendResult(ctx context.Context, remoteJobID string, res wsproto.Result) error {
	if err := cl.writeFrame(ctx, wsproto.TypeResult, remoteJobID, res); err != nil {
		cl.inflightCacheResult(remoteJobID, res)
		return err
	}
	cl.inflightDrop(remoteJobID)
	return nil
}

// replayCachedResults re-sends every terminal Result that could not be delivered
// while the connection was down (RECOV-01) and drops each entry that lands. It runs
// from applyResume, on the freshly handshaken connection and BEFORE that connection
// is published — so the hub's recovery plan (which saw these jobs as terminal in our
// `inflight` snapshot and is holding their host jobs in `recovering`, waiting for the
// Result) gets the authoritative ending. A Result whose entry is past
// workerResultTTL was already swept by inflightResult: the hub has given up on that
// job, and replaying into a finished job would be noise.
func (cl *Client) replayCachedResults(ctx context.Context, conn *websocket.Conn) {
	for _, id := range cl.inflightIDs() {
		res, ok := cl.inflightResult(id)
		if !ok {
			continue
		}
		if err := cl.writeFrameOn(ctx, conn, wsproto.TypeResult, id, res); err != nil {
			return // still not deliverable: keep the cache for the next reconnect
		}
		cl.inflightDrop(id)
	}
}

// pumpInteractions observes the local job's interactions and pushes a frame for
// each status change up to the hub (P2 worker→hub passthrough), mirroring the
// SSE pumpInteractions action vocabulary. A new pending interaction becomes an
// interaction{open}; an answered/cancelled transition is reported too (state
// cleanup, accepted-and-ignored by the hub bridge per P2 §3.1). The interaction
// id is the worker-LOCAL id, which the hub injects verbatim onto the host job, so
// the host answer maps back 1:1.
func (cl *Client) pumpInteractions(ctx context.Context, localID, remoteJobID string, seenStatus map[string]string) {
	its, err := cl.jobs.GetInteractions(localID)
	if err != nil {
		return
	}
	for _, it := range its {
		if seenStatus[it.ID] == it.Status {
			continue
		}
		var action string
		switch it.Status {
		case job.InteractionPending:
			action = "open"
		case job.InteractionAnswered:
			action = "answered"
		case job.InteractionCancelled:
			action = "cancelled"
		default:
			continue
		}
		body, mErr := json.Marshal(it)
		if mErr != nil {
			continue
		}
		if err := cl.writeFrame(ctx, wsproto.TypeInteraction, remoteJobID, wsproto.Interaction{
			JobID:       remoteJobID,
			Action:      action,
			Interaction: body,
		}); err != nil {
			// Do NOT latch the status: a frame that never reached the wire must be
			// retried on the next tick, exactly like the log frames above — otherwise an
			// interaction that opened while the connection was down is never announced
			// and the host would wait for an answer to a question it never saw.
			// (A repeat is accepted-and-ignored by the hub bridge, P2 §3.1.)
			continue
		}
		seenStatus[it.ID] = it.Status
	}
}

// tailFrom reads the bytes of path from byte offset, returning the new chunk and
// the next offset. A missing file (job not yet started) yields an empty chunk and
// the unchanged offset so the caller keeps polling. (Simplified vs. the SSE
// handler: the worker's local log files are append-only and not rotated under it.)
func tailFrom(path string, offset int64) (chunk []byte, next int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset
	}
	data, err := io.ReadAll(f)
	if err != nil || len(data) == 0 {
		return nil, offset
	}
	return data, offset + int64(len(data))
}

// mustRaw marshals payload to json.RawMessage; on error returns nil (the frame
// then carries an empty payload rather than panicking).
func mustRaw(payload any) json.RawMessage {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}
