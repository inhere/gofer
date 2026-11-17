package workbench

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// TestQueryFiltersAndCounts pins R10: the agent / runner narrowing, the per-source
// counts, and the rule that a tab's count is exactly the number of threads its listing
// returns (the counts ignore the source filter itself, so the numbers hold while a
// caller switches tabs).
func TestQueryFiltersAndCounts(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	seed := func(sid, agent, runner string, interactive bool, status string) {
		rec := jobstore.JobRecord{
			ID: "job-" + sid, ProjectKey: "alpha", Agent: agent, Runner: runner, Cwd: ".",
			SessionID: sid, Interactive: interactive, Status: status, StartedAt: now - 5, UpdatedAt: now,
			CallerID: "alice",
		}
		if !interactive {
			rec.SessionStateJSON = `{"session":true}`
		}
		putWorkbenchJob(t, store, rec, "title "+sid, "prompt "+sid)
	}
	seed("acp-claude-local", "claude-acp", "local", false, job.StatusDone)
	seed("acp-claude-w1", "claude-acp", "w-1", false, job.StatusRunning)
	seed("acp-codex-w1", "codex-acp", "w-1", false, job.StatusDone)
	seed("pty-claude-local", "claude", "local", true, job.StatusDone)
	seedRelaySession(t, store, "rel-claude", jobstore.SessionRunning, now-30)
	svc := NewService(store, nil, nil)

	// The filters narrow the listing; the counts always describe the same filter set
	// minus the source one, so each tab's number equals the rows its own request returns.
	cases := []struct {
		name  string
		agent string
		runn  string
		// want per source: the ids each source listing returns under the filters.
		wantACP   []string
		wantPTY   []string
		wantRelay []string
	}{
		{
			name:      "no narrowing",
			wantACP:   []string{"s:acp-claude-local", "s:acp-claude-w1", "s:acp-codex-w1"},
			wantPTY:   []string{"s:pty-claude-local"},
			wantRelay: []string{"r:rel-claude"},
		},
		{
			name:    "agent",
			agent:   "claude-acp",
			wantACP: []string{"s:acp-claude-local", "s:acp-claude-w1"},
		},
		{
			name:      "runner",
			runn:      "w-1",
			wantACP:   []string{"s:acp-claude-w1", "s:acp-codex-w1"},
			wantRelay: nil,
		},
		{
			name:    "agent and runner",
			agent:   "claude-acp",
			runn:    "w-1",
			wantACP: []string{"s:acp-claude-w1"},
		},
		{
			name:    "no match",
			agent:   "nobody",
			wantACP: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := Query{Since: now - 86400, Agent: tc.agent, Runner: tc.runn, Counts: true}
			perSource := map[string][]string{
				SourceACP:   tc.wantACP,
				SourcePTY:   tc.wantPTY,
				SourceRelay: tc.wantRelay,
			}
			for src, want := range perSource {
				q := base
				q.Source = src
				resp, err := svc.List("alice", q)
				assert.NoErr(t, err)
				assert.Eq(t, want, sortedIDs(resp))

				// The count of this tab IS the listing of this tab.
				if resp.Counts == nil {
					t.Fatalf("counts=1 returned no counts (source=%s)", src)
				}
				switch src {
				case SourceACP:
					assert.Eq(t, len(want), resp.Counts.ACP)
				case SourcePTY:
					assert.Eq(t, len(want), resp.Counts.PTY)
				case SourceRelay:
					assert.Eq(t, len(want), resp.Counts.Relay)
				}
				assert.Eq(t, len(tc.wantACP)+len(tc.wantPTY)+len(tc.wantRelay), resp.Counts.All)
			}

			// Without counts the field is absent (the old listing is unchanged).
			plain := base
			plain.Counts = false
			resp, err := svc.List("alice", plain)
			assert.NoErr(t, err)
			if resp.Counts != nil {
				t.Fatalf("counts returned without counts=1: %+v", resp.Counts)
			}
		})
	}
}

// TestQueryCountsIgnorePaging pins the paged case: a source tab shows the same counts on
// every page (the numbers describe the time window, not the page) and each count IS the
// number of rows that tab returns over all its pages.
func TestQueryCountsIgnorePaging(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	want := map[string][]string{}
	// Terminal rows only, so no live / pinned thread rides along as a first-page exception:
	// every thread is an ordinary page entry.
	for i := 0; i < 7; i++ {
		sid := fmt.Sprintf("acp-%02d", i)
		seedKindThread(t, store, sid, false, now-int64(i+1), job.StatusDone)
		want[SourceACP] = append(want[SourceACP], "s:"+sid)
	}
	for i := 0; i < 3; i++ {
		sid := fmt.Sprintf("pty-%02d", i)
		seedKindThread(t, store, sid, true, now-int64(i+1), job.StatusDone)
		want[SourcePTY] = append(want[SourcePTY], "s:"+sid)
	}
	for i := 0; i < 2; i++ {
		sid := fmt.Sprintf("rel-%02d", i)
		seedRelaySession(t, store, sid, jobstore.SessionEnded, now-int64(i+1))
		want[SourceRelay] = append(want[SourceRelay], "r:"+sid)
	}
	for _, ids := range want {
		sort.Strings(ids)
	}
	svc := NewService(store, nil, nil)
	window := int64(86400)

	// Reference: the unpaged listing counts every thread of the window once.
	full, err := svc.List("alice", Query{Since: now - window, Counts: true})
	assert.NoErr(t, err)
	if full.Counts == nil {
		t.Fatal("counts=1 returned no counts without paging")
	}
	wantCounts := *full.Counts
	assert.Eq(t, ThreadSourceCounts{
		ACP: len(want[SourceACP]), PTY: len(want[SourcePTY]), Relay: len(want[SourceRelay]),
		All: len(want[SourceACP]) + len(want[SourcePTY]) + len(want[SourceRelay]),
	}, wantCounts)

	// Paged: one source tab, three rows a page, four pages of it.
	for _, src := range []string{SourceACP, SourcePTY, SourceRelay} {
		t.Run(src, func(t *testing.T) {
			var ids []string
			cursor := ""
			page := 0
			for {
				resp, err := svc.List("alice", Query{Since: now - window, Source: src, Limit: 3, Cursor: cursor, Counts: true})
				assert.NoErr(t, err)
				if resp.Counts == nil {
					t.Fatalf("counts=1 returned no counts (source=%s page=%d)", src, page)
				}
				// The counts do not follow the page or the source tab.
				assert.Eq(t, wantCounts, *resp.Counts)
				ids = append(ids, threadIDs(resp)...)
				page++
				if !resp.HasMore {
					break
				}
				cursor = resp.NextCursor
				if page > 20 {
					t.Fatalf("cursor never ended (source=%s)", src)
				}
			}
			if len(want[src]) > 3 && page < 2 {
				t.Fatalf("source=%s fitted in %d page(s): the case needs several pages", src, page)
			}
			// The tab's own count is exactly what its pages returned.
			sort.Strings(ids)
			assert.Eq(t, want[src], ids)
		})
	}
}

func TestThreadSearchCoversTopicAndLastMessage(t *testing.T) {
	store := openWorkbenchStore(t)
	now := time.Now().Unix()
	putWorkbenchJob(t, store, jobstore.JobRecord{
		ID: "job-search", ProjectKey: "beta", Agent: "claude-acp", Runner: "local", Cwd: ".",
		SessionID: "search", SessionStateJSON: `{"session":true}`, Status: job.StatusDone,
		StartedAt: now - 5, UpdatedAt: now, CallerID: "alice",
	}, "Migrate the billing page", "prompt")
	seedRelaySession(t, store, "rel-search", jobstore.SessionRunning, now-30)
	// last_message is written by the heartbeat path (a prompt the session received).
	_, _, err := store.TouchAgentSession("rel-search", jobstore.SessionHeartbeat{State: jobstore.SessionRunning, LastMessage: "review the flux capacitor"})
	assert.NoErr(t, err)
	svc := NewService(store, nil, nil)

	cases := []struct {
		needle string
		want   []string
	}{
		{needle: "billing", want: []string{"s:search"}},            // title
		{needle: "beta", want: []string{"s:search"}},               // topic (project key)
		{needle: "flux capacitor", want: []string{"r:rel-search"}}, // last message
		{needle: "nothing-here"},
	}
	for _, tc := range cases {
		t.Run(tc.needle, func(t *testing.T) {
			resp, err := svc.List("alice", Query{Since: now - 86400, Q: tc.needle})
			assert.NoErr(t, err)
			assert.Eq(t, tc.want, sortedIDs(resp))
		})
	}
}
