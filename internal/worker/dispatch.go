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
func (cl *Client) handleDispatch(ctx context.Context, d wsproto.Dispatch) {
	res, err := cl.jobs.Submit(job.JobRequest{
		ProjectKey: d.ProjectKey,
		Agent:      d.Agent,
		Runner:     builtinLocalRunner, // always local on the worker
		Prompt:     d.Prompt,
		Cmd:        d.Cmd,
		Cwd:        d.Cwd,
		TimeoutSec: d.TimeoutSec,
	})
	if err != nil {
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: err.Error(),
		})
		return
	}

	localID := res.ID
	// Stream local log output back to the hub until the job is terminal, then send
	// the authoritative result.
	cl.streamLocalJob(ctx, localID, res.ResultDir, d.JobID)

	final, ok := cl.jobs.Wait(localID)
	if !ok {
		_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
			JobID: d.JobID, Status: job.StatusFailed, ExitCode: -1, Error: "local job not found",
		})
		return
	}
	_ = cl.writeFrame(ctx, wsproto.TypeResult, d.JobID, wsproto.Result{
		JobID:    d.JobID,
		Status:   final.Status,
		ExitCode: final.ExitCode,
		Error:    final.Error,
	})
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
