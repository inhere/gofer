package worker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/work/transcript"
	"github.com/inhere/gofer/internal/wsproto"
)

// EnvTranscriptRoots adds directories (an OS path list) a transcript may live under,
// besides the user's home directory. Operators who keep agent session stores elsewhere
// (a relocated CLAUDE_CONFIG_DIR / CODEX_HOME is picked up automatically) set it.
const EnvTranscriptRoots = "GOFER_TRANSCRIPT_ROOTS"

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

// transcriptRoots lists where a transcript file may live.
func transcriptRoots() []string {
	var roots []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		roots = append(roots, home)
	}
	for _, env := range []string{"CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			roots = append(roots, v)
		}
	}
	if v := os.Getenv(EnvTranscriptRoots); v != "" {
		for _, p := range filepath.SplitList(v) {
			if p = strings.TrimSpace(p); p != "" {
				roots = append(roots, p)
			}
		}
	}
	return roots
}

func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkTranscriptPath is the worker's own boundary for a transcript_tail request: an
// absolute .jsonl file that is a regular file under an allowed root after symlinks are
// resolved. The hub only ever sends the path a session registered, but a worker must
// not trust that — it would otherwise be a remote file reader.
func checkTranscriptPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", fmt.Errorf("transcript path must be absolute")
	}
	path = filepath.Clean(path)
	if !strings.EqualFold(filepath.Ext(path), ".jsonl") {
		return "", fmt.Errorf("transcript path must be a .jsonl file")
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("transcript not readable: %w", err)
	}
	ok := false
	for _, r := range transcriptRoots() {
		rr := r
		if e, err := filepath.EvalSymlinks(r); err == nil {
			rr = e
		}
		if within(rr, real) {
			ok = true
			break
		}
	}
	if !ok {
		return "", fmt.Errorf("transcript path is outside the allowed roots")
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("transcript not readable: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("transcript is not a regular file")
	}
	return real, nil
}

func readTranscriptTail(path string, maxBytes int64) (data []byte, size int64, truncated bool, err error) {
	real, err := checkTranscriptPath(path)
	if err != nil {
		return nil, 0, false, err
	}
	if maxBytes <= 0 {
		maxBytes = transcript.DefaultTailBytes
	}
	if maxBytes > wsproto.MaxTranscriptTailBytes {
		maxBytes = wsproto.MaxTranscriptTailBytes
	}
	fi, err := os.Stat(real)
	if err != nil {
		return nil, 0, false, err
	}
	data, err = transcript.ReadTail(real, maxBytes)
	if err != nil {
		return nil, 0, false, err
	}
	return data, fi.Size(), fi.Size() > int64(len(data)), nil
}
