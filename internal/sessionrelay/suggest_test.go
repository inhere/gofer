package sessionrelay

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestSuggestTerms(t *testing.T) {
	got := strings.Join(SuggestTerms("Fix the export-bug 订单导出"), ",")
	for _, want := range []string{"fix", "the", "export", "bug", "订单导出", "订单", "单导", "导出"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Fatalf("terms %q lack %q", got, want)
		}
	}
	if n := len(SuggestTerms(strings.Repeat("词汇 word ", 100))); n > maxSuggestTerms {
		t.Fatalf("%d terms, want at most %d", n, maxSuggestTerms)
	}
	if len(SuggestTerms("a - ,")) != 0 {
		t.Fatal("one-letter words and punctuation are not terms")
	}
}

// 推荐：整句文本逐词匹配（任一词命中即可），得分累加；只取前 3；无命中不推荐；项目相同加一条 project 命中。
func TestSuggestRankingLimitAndHits(t *testing.T) {
	s := newSvc(t)
	seedFind(t, s,
		findSeed{sid: "both", project: "x", topic: "relay export"},
		findSeed{sid: "one", project: "x", topic: "relay only"},
		findSeed{sid: "peer", project: "x", peer: "export-peer"},
		findSeed{sid: "inferred", project: "gofer", cwd: "/host/ws/export-area"},
		findSeed{sid: "none", project: "x", topic: "unrelated"},
		findSeed{sid: "gone", project: "x", topic: "relay export", state: jobstore.SessionEnded},
	)
	got, err := s.Suggest(SuggestQuery{Text: "fix relay export", Roots: findRootsFn})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Session.SessionID != "both" || got[1].Session.SessionID != "one" {
		t.Fatalf("suggest = %v, want 3 with both,one first", suggestIDs(got))
	}
	for _, r := range got {
		if r.Session.SessionID == "none" || r.Session.SessionID == "gone" {
			t.Fatalf("a session with no hit / an ended one was suggested: %v", suggestIDs(got))
		}
	}
	if len(got[0].Hits) < 2 {
		t.Fatalf("hits of the best candidate: %+v", got[0].Hits)
	}

	// Limit larger than 3 is honoured when asked; the inferred one is marked.
	got, _ = s.Suggest(SuggestQuery{Text: "export", Limit: 10, Roots: findRootsFn})
	var marked bool
	for _, r := range got {
		if r.Session.SessionID == "inferred" {
			marked = r.TopicSource == TopicSourceInferred && r.Hits[0].Source == TopicSourceInferred
		}
	}
	if !marked {
		t.Fatalf("inferred candidate missing or unmarked: %+v", got)
	}

	// The item's project adds a project hit even with no keyword reaching the session.
	got, _ = s.Suggest(SuggestQuery{Text: "zzzz", Project: "gofer", Roots: findRootsFn})
	if len(got) != 1 || got[0].Session.SessionID != "inferred" || got[0].Hits[0].Kind != HitProject {
		t.Fatalf("project-only suggest = %+v", got)
	}

	// Visible narrows BEFORE ranking: hiding "both" lets a lower candidate in.
	got, _ = s.Suggest(SuggestQuery{Text: "relay export", Limit: 1, Roots: findRootsFn,
		Visible: func(a jobstore.AgentSession) bool { return a.SessionID != "both" }})
	if len(got) != 1 || got[0].Session.SessionID == "both" {
		t.Fatalf("visible filter: %v", suggestIDs(got))
	}

	// Nothing to go on = nothing suggested.
	if got, err := s.Suggest(SuggestQuery{Text: "?!"}); err != nil || len(got) != 0 {
		t.Fatalf("empty query = %v %v", got, err)
	}
}

func suggestIDs(rs []SuggestResult) string {
	ids := make([]string, 0, len(rs))
	for _, r := range rs {
		ids = append(ids, r.Session.SessionID)
	}
	return strings.Join(ids, ",")
}
