package sessionrelay

import (
	"sort"
	"strings"
	"unicode"

	"github.com/inhere/gofer/internal/jobstore"
)

// Suggesting a session for a piece of free text (the capture box's 「目标推荐」): the same
// matching and weights as Find, but the query is a sentence, not a set of terms that must ALL
// hit. Each keyword of the text is matched on its own, a session scores the sum of its
// best hit per keyword, and a session in the item's project gets a project hit even when no
// keyword reaches it. Sessions with no hit at all are never suggested.

const (
	// DefaultSuggestLimit is the number of candidates the capture box offers (decision 10).
	DefaultSuggestLimit = 3
	// maxSuggestTerms bounds the keywords taken from the text.
	maxSuggestTerms = 24
)

// SuggestQuery is one recommendation request.
type SuggestQuery struct {
	// Text is the item's text; Project is the item's project key (may be empty).
	Text    string
	Project string
	Limit   int
	// Visible narrows the candidates BEFORE ranking (session.read.others). nil = everything.
	Visible func(jobstore.AgentSession) bool
	// Roots returns a project's root paths; nil = none.
	Roots func(projectKey string) []string
}

// SuggestResult is a ranked candidate with the combined score.
type SuggestResult struct {
	FindResult
	Score int
}

// SuggestTerms splits free text into match keywords: lower-cased words of letters and
// digits (at least two characters), and for each run of CJK characters the run itself (when
// short) plus its overlapping two-character pieces.
func SuggestTerms(text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(t string) {
		if t == "" || seen[t] || len(out) >= maxSuggestTerms {
			return
		}
		seen[t] = true
		out = append(out, t)
	}
	var word, han []rune
	flushWord := func() {
		if len(word) >= 2 {
			add(strings.ToLower(string(word)))
		}
		word = word[:0]
	}
	flushHan := func() {
		if n := len(han); n >= 2 {
			if n <= 4 {
				add(string(han))
			}
			for i := 0; i+2 <= n; i++ {
				add(string(han[i : i+2]))
			}
		}
		han = han[:0]
	}
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

// Suggest returns the best candidate sessions for q, at most q.Limit (default 3).
func (s *Service) Suggest(q SuggestQuery) ([]SuggestResult, error) {
	terms := SuggestTerms(q.Text)
	project := strings.TrimSpace(q.Project)
	if len(terms) == 0 && project == "" {
		return nil, nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultSuggestLimit
	}
	list, err := s.store.ListAgentSessions(jobstore.ListSessionsOpts{Limit: findScanLimit})
	if err != nil {
		return nil, err
	}
	var out []SuggestResult
	for _, a := range list {
		if !IsDeliverableState(a.State) || (q.Visible != nil && !q.Visible(a)) {
			continue
		}
		var roots []string
		if q.Roots != nil {
			roots = q.Roots(a.ProjectKey)
		}
		if r, ok := suggestSession(a, roots, terms, project); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		if a.Rank != b.Rank {
			return a.Rank > b.Rank
		}
		if a.MaybeOffline != b.MaybeOffline {
			return !a.MaybeOffline
		}
		if a.Session.LastSeenAt != b.Session.LastSeenAt {
			return a.Session.LastSeenAt > b.Session.LastSeenAt
		}
		return a.Session.SessionID < b.Session.SessionID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// suggestSession matches every keyword on its own and merges the hits.
func suggestSession(a jobstore.AgentSession, roots, terms []string, project string) (SuggestResult, bool) {
	topic, src := EffectiveTopic(a, roots)
	res := SuggestResult{FindResult: FindResult{
		Session: a, Topic: topic, TopicSource: src, MaybeOffline: a.State == jobstore.SessionOffline,
	}}
	has := map[string]bool{}
	merge := func(h FindHit) {
		if k := h.Kind + "\x00" + h.Match; !has[k] {
			has[k] = true
			res.Hits = append(res.Hits, h)
		}
	}
	for _, term := range terms {
		r, ok := matchSession(a, roots, []string{term}, "")
		if !ok {
			continue
		}
		res.Score += r.Rank
		if r.Rank > res.Rank {
			res.Rank = r.Rank
		}
		for _, h := range r.Hits {
			merge(h)
		}
	}
	if project != "" && a.ProjectKey == project {
		merge(FindHit{Kind: HitProject, Match: project})
		res.Score += rankProject
		if rankProject > res.Rank {
			res.Rank = rankProject
		}
	}
	return res, len(res.Hits) > 0
}
