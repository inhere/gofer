package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/inhere/gofer/internal/castrec"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
	ptyrunner "github.com/inhere/gofer/internal/runner/pty"
)

type localPtySource struct {
	sess *ptyrunner.PtySession
}

func (s localPtySource) Read(p []byte) (int, error) { return s.sess.Read(p) }
func (s localPtySource) Write(p []byte) (int, error) {
	return s.sess.WriteInput(p)
}
func (s localPtySource) Resize(cols, rows int) error { return s.sess.Resize(cols, rows) }
func (s localPtySource) Close() error {
	// Browser disconnects and relay registry cleanup must not kill a local job.
	// Job cancellation/timeout owns PtySession teardown through its runner context.
	return nil
}

// OnSessionStart implements ptyrunner.SessionObserver for serve-local pty jobs.
// It must stay non-blocking because PtyRunner calls it synchronously before the
// child run loop starts; all relay/session persistence work happens in a goroutine.
func (s *Server) OnSessionStart(jobID string, sess *ptyrunner.PtySession) {
	if s == nil || sess == nil {
		return
	}
	go s.runLocalPtyRelay(jobID, localPtySource{sess: sess}, sess.Done())
}

func (s *Server) runLocalPtyRelay(jobID string, source ptyrelay.PtySource, done <-chan struct{}) {
	if s.jobs == nil || s.ptyRelays == nil {
		return
	}
	res, ok := s.jobs.Get(jobID)
	if !ok || !res.Interactive || job.IsTerminal(res.Status) {
		return
	}
	cols, rows := ptySizeFromRequestJSON(res.RequestJSON)
	startedAt := time.Now().Unix()
	ptySessionID := "local-" + jobID
	nonce := localRelayNonce()

	var sink castrec.CastSink
	recURI := ""
	encrypted := false
	if shouldRecordPty(res.RequestJSON) && s.castRecorder != nil {
		uri := filepath.Join(res.ResultDir, "pty.cast")
		if cs, cerr := s.castRecorder.Open(uri, cols, rows, startedAt); cerr == nil {
			sink = cs
			recURI = uri
			encrypted = s.castRecorder.Encrypted()
		} else {
			slog.Warn("local pty cast recording open failed", "job", jobID, "err", cerr)
		}
	}

	s.ptyRelays.Prepare(ptyrelay.RelayBinding{
		JobID:        jobID,
		PtySessionID: ptySessionID,
		Nonce:        nonce,
		Expiry:       time.Now().Add(24 * time.Hour).Unix(),
		Cols:         cols,
		Rows:         rows,
	})
	opts := []ptyrelay.Option{}
	if sink != nil {
		opts = append(opts, ptyrelay.WithCast(sink))
	}
	if obs := s.sessionIDObserver(res); obs != nil {
		opts = append(opts, ptyrelay.WithOutputObserver(obs))
	}
	entry, err := s.ptyRelays.Open(nonce, source, opts...)
	if err != nil {
		if sink != nil {
			_ = sink.Close()
		}
		slog.Warn("local pty relay open failed", "job", jobID, "err", err)
		return
	}

	s.upsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: ptySessionID,
		JobID:        jobID,
		Owner:        res.CallerID,
		State:        "open",
		Cols:         cols,
		Rows:         rows,
		RecordingURI: recURI,
		Encrypted:    encryptedFlag(encrypted),
		StartedAt:    startedAt,
	})

	select {
	case <-entry.Relay.Done():
	case <-done:
	}

	s.ptyRelays.Close(jobID, "local_pty_closed")
	<-entry.Relay.Done()
	uriFinal := recURI
	if (sink != nil && sink.Err() != nil) || !entry.Relay.CastClosedCleanly() {
		uriFinal = ""
	}
	s.upsertPtySession(jobstore.PtySessionRecord{
		PtySessionID: ptySessionID,
		JobID:        jobID,
		Owner:        res.CallerID,
		State:        "closed",
		Cols:         cols,
		Rows:         rows,
		RecordingURI: uriFinal,
		Encrypted:    encryptedFlag(encrypted),
		BytesIn:      entry.Relay.InputLen(),
		BytesOut:     int64(entry.Relay.RecordedLen()),
		StartedAt:    startedAt,
		EndedAt:      time.Now().Unix(),
	})
}

func (s *Server) sessionIDObserver(res job.JobResult) ptyrelay.OutputObserver {
	if s == nil || s.jobs == nil || s.agents == nil || res.SessionID != "" {
		return nil
	}
	ac, ok := s.agents.Get(res.Agent)
	if !ok || ac.SessionCapture == "" {
		return nil
	}
	var buf []byte
	var done bool
	const maxObserve = 64 * 1024
	return func(chunk []byte) {
		if done || len(chunk) == 0 {
			return
		}
		if len(buf) < maxObserve {
			remain := maxObserve - len(buf)
			if len(chunk) > remain {
				chunk = chunk[:remain]
			}
			buf = append(buf, chunk...)
		}
		if sid := job.CaptureSessionIDBytes(buf, ac.SessionCapture); sid != "" {
			done = true
			s.jobs.SetSessionID(res.ID, sid)
		}
	}
}

func ptySizeFromRequestJSON(s string) (int, int) {
	var req job.JobRequest
	if s == "" || json.Unmarshal([]byte(s), &req) != nil {
		return 0, 0
	}
	return req.Cols, req.Rows
}

func shouldRecordPty(s string) bool {
	var req job.JobRequest
	if s == "" || json.Unmarshal([]byte(s), &req) != nil {
		return false
	}
	return req.Interactive && req.RecordPty
}

func localRelayNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return "local-" + hex.EncodeToString(b[:])
	}
	return "local-" + time.Now().Format("20060102150405.000000000")
}
