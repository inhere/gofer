package worker

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

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
func (cl *Client) handleDispatch(ctx context.Context, sessionURL string, d wsproto.Dispatch) {
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
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1,
			Error: "interactive dispatch missing relay credentials",
		})
		return
	}

	res, err := cl.jobs.Submit(job.JobRequest{
		ProjectKey: d.ProjectKey,
		Agent:      d.Agent,
		Runner:     builtinLocalRunner, // always local on the worker
		Prompt:     d.Prompt,
		Cmd:        d.Cmd,
		Cwd:        d.Cwd,
		TimeoutSec: d.TimeoutSec,
		// T5 projection: carry the interactive flag + initial window so the worker's
		// own job.Service picks its pty runner (Interactive && !remote). Zero-valued
		// for a non-interactive dispatch → byte-for-byte the existing path (G023).
		Interactive: d.Interactive,
		Cols:        d.Cols,
		Rows:        d.Rows,
	})
	if err != nil {
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: err.Error(),
		})
		return
	}

	localID := res.ID
	// Register the hub→local id mapping so an inbound cancel/answer frame (keyed by
	// the hub id d.JobID) reaches this local job; drop it when the dispatch ends.
	cl.putJobMapping(d.JobID, localID)
	defer cl.dropJobMapping(d.JobID)

	// D-P2-9: a cancel frame that arrived BEFORE the mapping existed was parked in
	// pendingCancel; now that the local job is submitted+mapped, honour it at once
	// (covers non-interactive dispatches too).
	if cl.takePendingCancel(d.JobID) {
		_ = cl.jobs.Cancel(localID)
	}

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
		if pumpDone != nil {
			<-pumpDone
		}
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
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

	_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
		JobID:    d.JobID,
		Status:   final.Status,
		ExitCode: final.ExitCode,
		Error:    final.Error,
	})
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
	}
	if final.ArtifactsJSON != "" {
		o.Artifacts = json.RawMessage(final.ArtifactsJSON)
	}
	send := o.RenderedCommand != "" || o.ResultJSON != "" || o.DiffSummary != "" || len(o.Artifacts) > 0 || o.SessionID != ""
	return o, send
}

// streamLocalJob tails the local job's stdout.log / stderr.log incrementally and
// pushes the new bytes as log frames to the hub, until the local job reaches a
// terminal state (a final drain follows). It is a mini in-process log consumer
// (no HTTP): it reads the files directly under <base>/<localID>/. seq is
// monotonic per (worker job, stream).
func (cl *Client) streamLocalJob(ctx context.Context, localID, resultDir, remoteJobID string) {
	base := filepath.Dir(resultDir)
	stdoutPath := filepath.Join(base, localID, store.StdoutFile)
	stderrPath := filepath.Join(base, localID, store.StderrFile)

	var stdoutOff, stderrOff int64
	seq := 0
	// seenStatus dedupes interaction frames: it remembers the last status pushed
	// per interaction id, so a re-poll only emits a frame on a status change (same
	// open/answered/cancelled vocabulary the SSE pumpInteractions uses).
	seenStatus := map[string]string{}

	pump := func() {
		for _, ent := range []struct {
			stream string
			path   string
			off    *int64
		}{
			{string(store.StreamStdout), stdoutPath, &stdoutOff},
			{string(store.StreamStderr), stderrPath, &stderrOff},
		} {
			chunk, next := tailFrom(ent.path, *ent.off)
			if len(chunk) == 0 {
				continue
			}
			*ent.off = next
			seq++
			_ = cl.writeFrame(ctx, wsproto.TypeLog, remoteJobID, wsproto.Log{
				JobID: remoteJobID, Stream: ent.stream, Seq: seq, Text: string(chunk),
			})
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
			if !ok || job.IsTerminal(cur.Status) {
				pump() // drain the tail produced just before terminal
				return
			}
		}
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
		_ = cl.writeFrame(ctx, wsproto.TypeInteraction, remoteJobID, wsproto.Interaction{
			JobID:       remoteJobID,
			Action:      action,
			Interaction: body,
		})
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
