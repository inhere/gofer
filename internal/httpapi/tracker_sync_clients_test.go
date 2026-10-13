package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// TestTrackerSyncRecordsClients: every successful /v1/tracker/sync records which
// machine synced (client_host) under which caller, and
// GET /v1/tracker/repos/{tracker_id}/clients lists one row per machine — for the
// global memory tracker and for a repository tracker alike.
func TestTrackerSyncRecordsClients(t *testing.T) {
	t.Parallel()
	s, _ := newTrackerSyncServer(t)
	for _, host := range []string{"host-a", "host-b", "host-a"} {
		resp := do(t, s, http.MethodPost, "/v1/tracker/sync", "tok-user", map[string]any{"tracker_id": tracker.GlobalTrackerID, "client_host": host})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("sync from %s status=%d: %s", host, resp.StatusCode, bodyText(t, resp))
		}
		resp.Body.Close()
	}
	// A repository tracker synced by a client that does not say its host.
	resp := do(t, s, http.MethodPost, "/v1/tracker/sync", "tok-user", map[string]any{"tracker_id": "tr-clients", "prefix": "c"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("repo sync status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	resp.Body.Close()

	var out struct {
		Clients []jobstore.TrackerSyncClient `json:"clients"`
	}
	resp = do(t, s, http.MethodGet, "/v1/tracker/repos/"+tracker.GlobalTrackerID+"/clients", "tok-user", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d: %s", resp.StatusCode, bodyText(t, resp))
	}
	decode(t, resp, &out)
	if len(out.Clients) != 2 {
		t.Fatalf("global clients = %+v, want one row per machine", out.Clients)
	}
	hosts := []string{out.Clients[0].ClientHost, out.Clients[1].ClientHost}
	sort.Strings(hosts)
	if hosts[0] != "host-a" || hosts[1] != "host-b" {
		t.Fatalf("global client hosts = %v", hosts)
	}
	for _, c := range out.Clients {
		if c.Caller != "alice" || c.LastSyncAt == 0 || c.Summary == "" || c.TrackerID != tracker.GlobalTrackerID {
			t.Fatalf("client row = %+v, want caller alice with a sync time and summary", c)
		}
	}

	out.Clients = nil
	resp = do(t, s, http.MethodGet, "/v1/tracker/repos/tr-clients/clients", "tok-user", nil)
	decode(t, resp, &out)
	if len(out.Clients) != 1 || out.Clients[0].ClientHost != jobstore.UnknownSyncClientHost {
		t.Fatalf("repo clients = %+v, want the one unknown-host row", out.Clients)
	}

	// Never synced: an empty list, not null.
	resp = do(t, s, http.MethodGet, "/v1/tracker/repos/tr-never/clients", "tok-user", nil)
	if txt := strings.TrimSpace(bodyText(t, resp)); resp.StatusCode != http.StatusOK || txt != `{"clients":[]}` {
		t.Fatalf("never-synced status=%d body=%q", resp.StatusCode, txt)
	}
}

// Jobs pulling the global memories are one caller ("job") per machine, not one row
// per job id.
func TestTrackerSyncClientsJobsShareOneRow(t *testing.T) {
	t.Parallel()
	s, _ := newTrackerSyncServer(t)
	for i := 0; i < 2; i++ {
		j := submitExecJob(t, s, "tok-user")
		tok := seedJobToken(t, s, j.ID, jobstore.JobCredentialMember, "")
		resp := do(t, s, http.MethodPost, "/v1/tracker/sync", tok, map[string]any{"tracker_id": tracker.GlobalTrackerID, "client_host": "worker-1"})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("job pull status=%d: %s", resp.StatusCode, bodyText(t, resp))
		}
		resp.Body.Close()
	}
	rows, err := s.trackerStore.ListTrackerSyncClients(tracker.GlobalTrackerID)
	if err != nil || len(rows) != 1 || rows[0].Caller != syncClientJobCaller || rows[0].ClientHost != "worker-1" {
		t.Fatalf("rows = %+v %v", rows, err)
	}
}
