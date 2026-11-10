package httpapi

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestWorkbenchThreadsFilterParamsValidation(t *testing.T) {
	t.Parallel()
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = "s:t" + string(rune('a'+i))
	}
	for _, query := range []string{
		"source=bad",
		"live=x",
		"live=0",
		"include=nope",
		"include=s:ok,nope",
		"include=" + url.QueryEscape("x:abc"),
		"include=" + strings.Join(nine, ","),
		"include=" + strings.Join(nine[:5], ",") + "&include=" + strings.Join(nine[5:], ","),
	} {
		resp := do(t, s, http.MethodGet, "/v1/workbench/threads?"+query, testToken, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET ?%s status=%d, want 400", query, resp.StatusCode)
		}
		resp.Body.Close()
	}
	for _, query := range []string{
		"source=acp", "source=pty", "source=relay", "live=1", "live=true",
		"include=s:a,s:b&include=j:c", "include=" + strings.Join(nine[:8], ","),
	} {
		getWorkbenchThreads(t, s, testToken, query)
	}
}

func TestWorkbenchThreadsSourceLiveInclude(t *testing.T) {
	t.Parallel()
	s := newWorkbenchTestServer(t, config.ServerConfig{Token: testToken})
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "pty-1", SessionID: "pty-1", Status: job.StatusDone, StartedAt: now - 10}, "pty", "p")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "old-pty", SessionID: "old-pty", Status: job.StatusDone, StartedAt: now - 30*86400}, "old", "p")
	if err := s.jobs.Meta().UpsertJob(jobstore.JobRecord{
		ID: "acp-1", ProjectKey: "self", Agent: "cli", Runner: "local", Cwd: ".", ResultDir: t.TempDir(),
		SessionID: "acp-1", SessionStateJSON: `{"session":true}`, Status: job.StatusRunning,
		StartedAt: now - 5, UpdatedAt: now - 5, CallerID: "default", RequestJSON: `{"prompt":"x"}`,
	}); err != nil {
		t.Fatal(err)
	}
	acp := getWorkbenchThreads(t, s, testToken, "source=acp")
	findWorkbenchThread(t, acp, "s:acp-1")
	if acp.Total != 1 {
		t.Fatalf("source=acp total=%d, want 1", acp.Total)
	}
	live := getWorkbenchThreads(t, s, testToken, "live=1&limit=5")
	if live.Total != 1 || live.HasMore {
		t.Fatalf("live total=%d has_more=%v, want 1/false", live.Total, live.HasMore)
	}
	plain := getWorkbenchThreads(t, s, testToken, "limit=5")
	if plain.Total != 2 {
		t.Fatalf("plain total=%d, want 2", plain.Total)
	}
	inc := getWorkbenchThreads(t, s, testToken, "limit=5&include=s:old-pty")
	findWorkbenchThread(t, inc, "s:old-pty")
}

// A restricted caller reaches only its own threads through include as well.
func TestWorkbenchThreadsIncludeRespectsOwnership(t *testing.T) {
	t.Parallel()
	s := newWorkbenchTestServer(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice"},
		{ID: "bob", Token: "tok-bob"},
	}})
	s.setDBGrants(nil)
	now := time.Now().Unix()
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "bob-old", SessionID: "bob-old", Status: job.StatusDone, StartedAt: now - 30*86400, CallerID: "bob"}, "b", "p")
	seedWorkbenchJob(t, s, jobstore.JobRecord{ID: "alice-old", SessionID: "alice-old", Status: job.StatusDone, StartedAt: now - 30*86400, CallerID: "alice"}, "a", "p")

	got := getWorkbenchThreads(t, s, "tok-alice", "limit=5&include=s:bob-old,s:alice-old")
	findWorkbenchThread(t, got, "s:alice-old")
	for _, p := range got.Projects {
		for _, th := range p.Threads {
			if th.ID == "s:bob-old" {
				t.Fatalf("include leaked another caller's thread: %+v", got.Projects)
			}
		}
	}
}
