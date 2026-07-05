package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/store"
)

// handlePtyRecording streams a job's recorded pty session (asciinema v2 cast).
// It is the WEB-03 P3 recording download gate (D-P3-7/H6/M3):
//
// Unlike artifact download, the recording gate does NOT consult job.Source: a pty
// cast is ALWAYS written hub-side (handlePtyConnect writes <result_dir>/pty.cast on
// the relay, regardless of where the job command ran), so a worker-routed
// interactive job's recording still lives on the hub and must be served — not 409'd.
//
//   - unknown job → 404;
//   - caller lacks attach permission (owner, or admin for an unowned job) → 403;
//   - no pty session, or the recording was never made / TTL-expired-cleared
//     (empty recording_uri) / the file is gone → 404 (explicit, not misleading);
//   - encrypted casts are stream-decrypted through the recorder — the header AND
//     the first frame are authenticated BEFORE a 200 is committed, so a corrupt /
//     tampered file yields a 4xx rather than a half-sent body; a mid-stream
//     failure can only truncate the connection (status already written) and is
//     logged;
//   - plaintext casts are served with http.ServeFile.
func (s *Server) handlePtyRecording(c *rux.Context) {
	id := c.Param("id")
	caller := callerFromCtx(c)

	res, ok := s.jobs.Get(id)
	if !ok {
		writeError(c, http.StatusNotFound, "unknown job", "no job with id "+id)
		return
	}
	if !s.callerMayAttach(caller, res) {
		writeError(c, http.StatusForbidden, "recording not permitted for this caller",
			"caller lacks permission to read this job's pty recording")
		return
	}
	if s.ptySessions == nil {
		writeError(c, http.StatusNotFound, "no recording", "pty session persistence is not enabled")
		return
	}
	sess, ok, err := s.ptySessions.GetPtySessionByJob(id)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "recording lookup failed", err.Error())
		return
	}
	if !ok || sess.RecordingURI == "" {
		writeError(c, http.StatusNotFound, "no recording",
			"job has no pty recording (never recorded or expired)")
		return
	}

	// Re-anchor the recording under the job's result dir (defence in depth): the
	// stored URI is server-written as <result_dir>/pty.cast, but SafeJoinUnder its
	// base name under the CURRENT result dir so a download can never escape it.
	// Deviation from the plan snippet SafeJoinUnder(resultDir, RecordingURI): the
	// stored URI is absolute (SafeJoinUnder rejects absolute names), so pass the
	// base name.
	full, jerr := store.SafeJoinUnder(res.ResultDir, filepath.Base(sess.RecordingURI))
	if jerr != nil {
		writeError(c, http.StatusNotFound, "no recording", "recording path unavailable")
		return
	}
	fi, serr := os.Stat(full)
	if serr != nil || fi.IsDir() {
		writeError(c, http.StatusNotFound, "no recording", "recording file not found")
		return
	}

	slog.Info("pty recording download", "job", id, "pty_session", sess.PtySessionID,
		"caller", caller, "encrypted", sess.Encrypted == 1, "bytes_out", sess.BytesOut)

	if sess.Encrypted == 1 {
		s.streamEncryptedRecording(c, id, full)
		return
	}

	// Plaintext: serve directly. Set the content type first so ServeContent keeps
	// it instead of sniffing.
	c.SetHeader("Content-Type", "application/x-asciicast")
	http.ServeFile(c.Resp, c.Req, full)
}

// streamEncryptedRecording decrypts an encrypted cast to the response. It opens a
// decrypting reader (authenticates the file header), then reads one buffer EAGERLY
// to authenticate the first frame BEFORE committing a 200 — so a corrupt/tampered
// cast surfaces as a 4xx rather than a half-sent body. Only after that does it
// write the 200 and stream the remainder; a mid-stream failure can no longer
// change the status and is logged as a truncated download.
func (s *Server) streamEncryptedRecording(c *rux.Context, id, full string) {
	if s.castRecorder == nil {
		writeError(c, http.StatusInternalServerError, "recording unavailable",
			"recording is encrypted but no cast recorder is configured")
		return
	}
	dec, derr := s.castRecorder.NewDecReader(full) // header authenticated here
	if derr != nil {
		writeError(c, http.StatusUnprocessableEntity, "recording corrupt", derr.Error())
		return
	}
	defer dec.Close()

	firstBuf := make([]byte, 32*1024)
	n, rerr := dec.Read(firstBuf) // authenticates the first frame
	if rerr != nil && rerr != io.EOF {
		writeError(c, http.StatusUnprocessableEntity, "recording corrupt", rerr.Error())
		return
	}
	c.SetHeader("Content-Type", "application/x-asciicast")
	c.Resp.WriteHeader(http.StatusOK)
	if n > 0 {
		if _, werr := c.Resp.Write(firstBuf[:n]); werr != nil {
			slog.Warn("pty recording stream write failed", "job", id, "err", werr)
			return
		}
	}
	if rerr == nil { // more frames may remain
		if _, cerr := io.Copy(c.Resp, dec); cerr != nil {
			// Status already committed → can only truncate the connection + log.
			slog.Warn("pty recording stream truncated", "job", id, "err", cerr)
		}
	}
}
