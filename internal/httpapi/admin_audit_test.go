package httpapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/jobstore"
)

type auditPage struct {
	Entries    []jobstore.AdminAuditEntry `json:"entries"`
	Count      int                        `json:"count"`
	NextCursor int64                      `json:"next_cursor"`
}

// readAudit reads GET /v1/audit as the root administrator after the queue is flushed.
func readAudit(t *testing.T, s *Server, query string) auditPage {
	t.Helper()
	s.FlushAudit()
	resp := do(t, s, http.MethodGet, "/v1/audit"+query, "tok-root", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/audit%s = %d, want 200", query, resp.StatusCode)
	}
	var page auditPage
	decode(t, resp, &page)
	return page
}

// TestAdminAuditRecordsWriteNotRead: a successful write and a refused write are recorded
// with the route template, target and status; a read is not.
func TestAdminAuditRecordsWriteNotRead(t *testing.T) {
	s := authTestServer(t)
	if resp := do(t, s, http.MethodGet, "/v1/auth/grants", "tok-root", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("read = %d", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{"subject": "caller:alice", "pattern": authz.PermTunnelOpen}); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create grant = %d, want 201", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/auth/grants", "tok-alice", map[string]any{"subject": "caller:alice", "pattern": authz.PermTunnelOpen}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("refused create = %d, want 403", resp.StatusCode)
	}
	page := readAudit(t, s, "")
	if page.Count != 2 {
		t.Fatalf("audit lines = %+v, want exactly the 2 writes", page.Entries)
	}
	denied, okLine := page.Entries[0], page.Entries[1] // newest first
	if okLine.CallerID != "root" || okLine.Method != "POST" || okLine.Route != "POST /v1/auth/grants" || okLine.Status != http.StatusCreated || okLine.Error != "" {
		t.Errorf("success line = %+v", okLine)
	}
	if denied.CallerID != "alice" || denied.Status != http.StatusForbidden || !strings.HasPrefix(denied.Error, "permission required") || denied.CallerKind != "user" {
		t.Errorf("refused line = %+v, want alice 403 with the gate's reason", denied)
	}
	for _, e := range page.Entries {
		if e.At == 0 || e.IP == "" {
			t.Errorf("line misses at / ip: %+v", e)
		}
	}
}

// TestAdminAuditRecordsTargetAndQueryKeysOnly: path parameters become `name=value`; the
// query contributes its key names, never its values; no body text reaches the table.
func TestAdminAuditRecordsTargetAndQueryKeysOnly(t *testing.T) {
	s := authTestServer(t)
	resp := do(t, s, http.MethodDelete, "/v1/auth/grants/g-none?zeta=SECRETVALUE&alpha=2", "tok-root", map[string]any{"note": "SECRETBODY"})
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("revoking an unknown grant answered 200")
	}
	page := readAudit(t, s, "")
	if page.Count != 1 {
		t.Fatalf("audit = %+v", page.Entries)
	}
	e := page.Entries[0]
	if e.Route != "DELETE /v1/auth/grants/{id}" || e.Target != "id=g-none" || e.QueryKeys != "alpha,zeta" {
		t.Errorf("line = %+v", e)
	}
	// Nothing the request carried as a value is anywhere in what the API returns.
	raw := do(t, s, http.MethodGet, "/v1/audit", "tok-root", nil)
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := raw.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	_ = raw.Body.Close()
	if strings.Contains(sb.String(), "SECRETVALUE") || strings.Contains(sb.String(), "SECRETBODY") {
		t.Errorf("a query value or a body leaked into the audit: %s", sb.String())
	}
}

// TestAdminAuditNoAuditRoutesAreNotRecorded: a heartbeat (NoAudit) leaves no line.
func TestAdminAuditNoAuditRoutesAreNotRecorded(t *testing.T) {
	s := authTestServer(t)
	do(t, s, http.MethodPost, "/v1/sessions/sess-x/heartbeat", "tok-root", map[string]any{})
	if page := readAudit(t, s, ""); page.Count != 0 {
		t.Fatalf("a NoAudit route was recorded: %+v", page.Entries)
	}
}

// TestAdminAuditReadNeedsPermission: audit.read is required; a plain caller is refused, a
// holder of the grant reads, a job credential never does.
func TestAdminAuditReadNeedsPermission(t *testing.T) {
	s := authTestServer(t)
	if resp := do(t, s, http.MethodGet, "/v1/audit", "tok-alice", nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain caller = %d, want 403", resp.StatusCode)
	}
	s.setDBGrants(grantsFor([]string{authz.PermAuditRead}))
	if resp := do(t, s, http.MethodGet, "/v1/audit", "tok-alice", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("holder of audit.read = %d, want 200", resp.StatusCode)
	}
}

// TestAdminAuditFilterAndCursor: the caller / route / status filters and the cursor pages.
func TestAdminAuditFilterAndCursor(t *testing.T) {
	s := authTestServer(t)
	for i := 0; i < 3; i++ {
		do(t, s, http.MethodPost, "/v1/auth/grants", "tok-root", map[string]any{"subject": "caller:alice", "pattern": authz.PermTunnelOpen})
	}
	do(t, s, http.MethodPost, "/v1/auth/grants", "tok-bob", map[string]any{"subject": "caller:bob", "pattern": authz.PermTunnelOpen})
	if p := readAudit(t, s, "?caller=bob"); p.Count != 1 || p.Entries[0].CallerID != "bob" {
		t.Errorf("caller filter = %+v", p.Entries)
	}
	if p := readAudit(t, s, "?status=fail"); p.Count != 1 || p.Entries[0].Status < 400 {
		t.Errorf("status=fail = %+v", p.Entries)
	}
	if p := readAudit(t, s, "?status=ok"); p.Count != 3 {
		t.Errorf("status=ok = %+v", p.Entries)
	}
	if p := readAudit(t, s, "?route=/v1/nothing"); p.Count != 0 {
		t.Errorf("route filter = %+v", p.Entries)
	}
	first := readAudit(t, s, "?limit=3")
	if first.Count != 3 || first.NextCursor == 0 {
		t.Fatalf("first page = %+v", first)
	}
	second := readAudit(t, s, "?limit=3&cursor="+strconv.FormatInt(first.NextCursor, 10))
	if second.Count != 1 || second.NextCursor != 0 || second.Entries[0].ID >= first.NextCursor {
		t.Fatalf("second page = %+v", second)
	}
	if resp := do(t, s, http.MethodGet, "/v1/audit?status=bogus", "tok-root", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad status = %d, want 400", resp.StatusCode)
	}
	if p := readAudit(t, s, "?since=2999-01-01"); p.Count != 0 {
		t.Errorf("since in the future = %+v", p.Entries)
	}
}

type fakeAuditSink struct {
	mu      sync.Mutex
	batches [][]jobstore.AdminAuditEntry
}

func (f *fakeAuditSink) InsertAdminAuditBatch(e []jobstore.AdminAuditEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, append([]jobstore.AdminAuditEntry(nil), e...))
	return nil
}

func (f *fakeAuditSink) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, b := range f.batches {
		n += len(b)
	}
	return n
}

// TestAuditRecorderDropsWhenFullAndCounts: a full queue drops the new entry and counts it.
func TestAuditRecorderDropsWhenFullAndCounts(t *testing.T) {
	sink := &fakeAuditSink{}
	var dropped int
	var mu sync.Mutex
	r := newAuditRecorder(func() auditSink { return sink }, func() { mu.Lock(); dropped++; mu.Unlock() })
	// Fill the queue without letting a flush start: hold flushMu like a slow flush would.
	r.flushMu.Lock()
	for i := 0; i < auditQueueMax+10; i++ {
		r.mu.Lock()
		full := len(r.queue) >= auditQueueMax
		r.mu.Unlock()
		if full {
			r.add(jobstore.AdminAuditEntry{Route: "POST /x"})
			continue
		}
		r.mu.Lock()
		r.queue = append(r.queue, jobstore.AdminAuditEntry{Route: "POST /x"})
		r.mu.Unlock()
	}
	mu.Lock()
	got := dropped
	mu.Unlock()
	if got != 10 {
		t.Fatalf("dropped = %d, want 10", got)
	}
	r.flushMu.Unlock()
	r.close()
	if sink.total() != auditQueueMax {
		t.Errorf("written = %d, want %d", sink.total(), auditQueueMax)
	}
}

// TestAuditRecorderCloseDrains: close writes what is queued, in batches of at most
// auditBatchMax, and a later entry is dropped (the server is shutting down).
func TestAuditRecorderCloseDrains(t *testing.T) {
	sink := &fakeAuditSink{}
	dropped := 0
	r := newAuditRecorder(func() auditSink { return sink }, func() { dropped++ })
	r.flushMu.Lock() // keep the batch-size flush from racing the check below
	for i := 0; i < 250; i++ {
		r.mu.Lock()
		r.queue = append(r.queue, jobstore.AdminAuditEntry{Route: "POST /x"})
		r.mu.Unlock()
	}
	r.flushMu.Unlock()
	r.close()
	if sink.total() != 250 {
		t.Fatalf("written = %d, want 250", sink.total())
	}
	for _, b := range sink.batches {
		if len(b) > auditBatchMax {
			t.Errorf("batch of %d exceeds %d", len(b), auditBatchMax)
		}
	}
	r.add(jobstore.AdminAuditEntry{Route: "POST /late"})
	if dropped != 1 {
		t.Errorf("an entry after close: dropped = %d, want 1", dropped)
	}
}

// TestAuditRecorderFlushesWithinASecond: a lone entry is written by the timer, without a
// close.
func TestAuditRecorderFlushesWithinASecond(t *testing.T) {
	sink := &fakeAuditSink{}
	r := newAuditRecorder(func() auditSink { return sink }, nil)
	r.add(jobstore.AdminAuditEntry{Route: "POST /x"})
	deadline := time.Now().Add(10 * time.Second)
	for sink.total() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if sink.total() != 1 {
		t.Fatalf("the timer did not flush: %d written", sink.total())
	}
	r.close()
}

// TestNoAuditExemptionList pins every route exempt from the audit: each one is a write
// route with no administrative meaning (a worker's own handshake or upload, a heartbeat, a
// log append). Adding to the list needs a reviewed reason here.
func TestNoAuditExemptionList(t *testing.T) {
	want := map[string]string{
		"GET /v1/workers/connect":           "worker's own long-lived handshake (reconnects on its own)",
		"GET /v1/workers/tunnel-connect":    "worker's own tunnel data-plane handshake",
		"GET /v1/workers/preview-connect":   "worker's own preview data-plane handshake",
		"GET /v1/workers/pty-connect":       "worker's own pty data-plane handshake",
		"PUT /v1/xfer/{id}/content":         "worker's own upload of transfer bytes (data plane, high frequency)",
		"POST /v1/sessions/{sid}/heartbeat": "session liveness heartbeat",
		"POST /v1/work-items/{id}/journal":  "append-only journal log line (job credentials and the console)",
	}
	var got []string
	for key, d := range routeDecls {
		if d.audit == auditOff {
			got = append(got, key)
			if d.effectiveRW(key) != rwWrite {
				t.Errorf("%s is NoAudit but is not a write route", key)
			}
		}
	}
	var wantKeys []string
	for k := range want {
		wantKeys = append(wantKeys, k)
	}
	sort.Strings(got)
	sort.Strings(wantKeys)
	if strings.Join(got, "\n") != strings.Join(wantKeys, "\n") {
		t.Errorf("NoAudit routes differ:\n got  %v\n want %v", got, wantKeys)
	}
}
