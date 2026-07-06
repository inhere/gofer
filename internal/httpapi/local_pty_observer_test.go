package httpapi

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/ptyrelay"
)

type localObserverFakeSource struct {
	outCh chan []byte

	mu       sync.Mutex
	closed   bool
	leftover []byte
}

func newLocalObserverFakeSource() *localObserverFakeSource {
	return &localObserverFakeSource{outCh: make(chan []byte, 16)}
}

func (f *localObserverFakeSource) Emit(b []byte) { f.outCh <- b }
func (f *localObserverFakeSource) EOF()          { close(f.outCh) }

func (f *localObserverFakeSource) Read(p []byte) (int, error) {
	if len(f.leftover) > 0 {
		n := copy(p, f.leftover)
		f.leftover = f.leftover[n:]
		return n, nil
	}
	chunk, ok := <-f.outCh
	if !ok {
		return 0, io.EOF
	}
	n := copy(p, chunk)
	if n < len(chunk) {
		f.leftover = append([]byte(nil), chunk[n:]...)
	}
	return n, nil
}

func (f *localObserverFakeSource) Write(p []byte) (int, error) { return len(p), nil }
func (f *localObserverFakeSource) Resize(int, int) error       { return nil }
func (f *localObserverFakeSource) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func TestLocalPtyObserverOpensAttachableRelayAndSessionRow(t *testing.T) {
	s := newTestServerCfg(t, config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "alice", Token: "tok-alice", CanAttach: true}},
	})
	s.SetPtyRelay(ptyrelay.NewNonceStore(), ptyrelay.NewRegistry())
	s.SetPtySessionStore(s.jobs.Meta())

	now := time.Now().Unix()
	requestJSON := `{"project_key":"self","agent":"exec","runner":"local","interactive":true,"cols":132,"rows":43}`
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "job-local", ProjectKey: "self", Agent: "exec", Runner: "local",
		Interactive: true, Status: "running", Cwd: ".", ResultDir: t.TempDir(),
		RequestJSON: requestJSON, StartedAt: now, UpdatedAt: now, CallerID: "alice",
	}); err != nil {
		t.Fatalf("upsert local job: %v", err)
	}

	src := newLocalObserverFakeSource()
	done := make(chan struct{})
	go s.runLocalPtyRelay("job-local", src, done)

	entry := waitForPtyRelay(t, s.ptyRelays, "job-local", ptyrelay.RelayOpen)
	if got := entry.Binding.PtySessionID; got != "local-job-local" {
		t.Fatalf("pty session id = %q, want local-job-local", got)
	}
	if entry.Binding.Cols != 132 || entry.Binding.Rows != 43 {
		t.Fatalf("binding size = %dx%d, want 132x43", entry.Binding.Cols, entry.Binding.Rows)
	}
	if got := getJobDetailMap(t, s, "job-local", "tok-alice")["can_attach"]; got != true {
		t.Fatalf("can_attach=%v, want true", got)
	}
	open := waitPtySession(t, s.jobs.Meta(), "job-local", "open")
	if open.Owner != "alice" || open.Cols != 132 || open.Rows != 43 {
		t.Fatalf("open row = %+v, want owner alice size 132x43", open)
	}

	src.Emit([]byte("local-output"))
	waitRecordedLen(t, entry.Relay, len("local-output"))
	src.EOF()
	close(done)

	closed := waitPtySession(t, s.jobs.Meta(), "job-local", "closed")
	if closed.BytesOut < int64(len("local-output")) {
		t.Fatalf("bytes_out=%d, want >= %d", closed.BytesOut, len("local-output"))
	}
	if _, ok := s.ptyRelays.Lookup("job-local"); ok {
		t.Fatalf("local relay still live after close")
	}
	resp, _ := postAttachTicket(t, s, "job-local", "tok-alice", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("attach after local close status=%d, want 409", resp.StatusCode)
	}
}
