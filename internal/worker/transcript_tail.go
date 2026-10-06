package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/inhere/gofer/internal/work/transcript"
	"github.com/inhere/gofer/internal/wsproto"
)

const transcriptTailTimeout = 20 * time.Second

// handleTranscriptTail answers one read-only transcript_tail request (W2a, protocol
// v17). It always answers — the hub is parked on the result frame.
func (cl *Client) handleTranscriptTail(ctx context.Context, fr wsproto.TranscriptTail) {
	res := wsproto.TranscriptTailResult{ReqID: fr.ReqID}
	tctx, cancel := context.WithTimeout(ctx, transcriptTailTimeout)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		data, size, truncated, err := readTranscriptTail(fr.Path, fr.MaxBytes)
		if err != nil {
			res.Error = err.Error()
			return
		}
		res.OK, res.Data, res.Size, res.Truncated = true, data, size, truncated
	}()
	select {
	case <-done:
	case <-tctx.Done():
		res = wsproto.TranscriptTailResult{ReqID: fr.ReqID, Error: "timed out reading the transcript"}
	}
	if err := cl.writeFrame(ctx, wsproto.TypeTranscriptTailResult, "", res); err != nil {
		slog.Warn("transcript_tail.report_failed", "event", "transcript_tail.report_failed", "component", "worker",
			"worker_id", cl.workerID, "req_id", fr.ReqID, "err", err)
	}
}

func readTranscriptTail(path string, maxBytes int64) (data []byte, size int64, truncated bool, err error) {
	// The wire cap is the protocol's; the vetting is shared with the server's local read.
	if maxBytes > wsproto.MaxTranscriptTailBytes {
		maxBytes = wsproto.MaxTranscriptTailBytes
	}
	return transcript.ReadTailSafe(path, maxBytes)
}
