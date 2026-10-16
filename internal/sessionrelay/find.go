package sessionrelay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// Finding sessions (第一期收集箱设计「会话主题与查找」): GET /v1/sessions?q=&path=.
//
// Matching and ranking happen HERE, over every deliverable session the caller may see, and
// only then is the list cut to the limit — cutting first (as the plain session list does)
// would drop the right answer behind newer unrelated rows.

// Hit kinds (FindHit.Kind).
const (
	HitScope     = "scope"      // a scope equals the query path or is an ancestor of it (strong)
	HitScopeWeak = "scope_weak" // the query path is an ancestor of a scope (weak)
	HitTopic     = "topic"
	HitPeerName  = "peer_name"
	HitProject   = "project"
)

// Rank weights: scope strong > scope weak > topic (declared) > topic (inferred) > peer_name > project.
const (
	rankScope        = 50
	rankScopeWeak    = 40
	rankTopic        = 30
	rankTopicInferrd = 25
	rankPeerName     = 20
	rankProject      = 10
)

const (
	defaultFindLimit = 20
	maxFindLimit     = 200
	// findScanLimit bounds the candidate scan; far above any realistic session count.
	findScanLimit = 100000
)

// FindQuery is one search. Q is split on whitespace and every term must match the topic,
// the peer name or the project of a session; Path (project-relative, or absolute under the
// session's project root) must hit a scope. Giving both requires both.
type FindQuery struct {
	Q       string
	Path    string
	Project string
	Limit   int
	// Visible narrows the candidates BEFORE ranking (the caller's session.read.others
	// filter). nil = everything.
	Visible func(jobstore.AgentSession) bool
	// Roots returns a project's root paths (host_path / container_path); nil = none.
	Roots func(projectKey string) []string
}

// FindHit says why a session matched. Source is the topic's source (self | human |
// inferred) for topic and scope hits.
type FindHit struct {
	Kind   string `json:"kind"`
	Match  string `json:"match,omitempty"`
	Source string `json:"source,omitempty"`
}

// FindResult is one ranked session.
type FindResult struct {
	Session jobstore.AgentSession
	// Topic / TopicSource are the effective ones (an inferred value is marked).
	Topic       string
	TopicSource string
	Hits        []FindHit
	Rank        int
	// MaybeOffline marks an `offline` session: it stopped heart-beating, so it may be gone.
	MaybeOffline bool
}

// IsDeliverableState reports whether a session in state st can still receive something:
// the live states plus offline (a guess, flagged in the result).
func IsDeliverableState(st string) bool {
	switch st {
	case jobstore.SessionRunning, jobstore.SessionIdle, jobstore.SessionWaitingReply,
		jobstore.SessionNeedsAttention, jobstore.SessionOffline:
		return true
	}
	return false
}

// Find matches, ranks and then truncates. ErrInvalidInput when neither Q nor Path is given.
func (s *Service) Find(q FindQuery) ([]FindResult, error) {
	terms := strings.Fields(strings.ToLower(q.Q))
	pathQ := strings.TrimSpace(q.Path)
	if len(terms) == 0 && pathQ == "" {
		return nil, fmt.Errorf("%w: q or path required", ErrInvalidInput)
	}
	list, err := s.store.ListAgentSessions(jobstore.ListSessionsOpts{Project: q.Project, Limit: findScanLimit})
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = defaultFindLimit
	}
	if limit > maxFindLimit {
		limit = maxFindLimit
	}
	var out []FindResult
	for _, a := range list {
		if !IsDeliverableState(a.State) || (q.Visible != nil && !q.Visible(a)) {
			continue
		}
		var roots []string
		if q.Roots != nil {
			roots = q.Roots(a.ProjectKey)
		}
		if r, ok := matchSession(a, roots, terms, pathQ); ok {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Rank != b.Rank {
			return a.Rank > b.Rank
		}
		if a.MaybeOffline != b.MaybeOffline {
			return !a.MaybeOffline // online first on ties
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

// matchSession evaluates one session. Every keyword term and (when given) the path must hit.
func matchSession(a jobstore.AgentSession, roots, terms []string, pathQ string) (FindResult, bool) {
	topic, src := EffectiveTopic(a, roots)
	res := FindResult{Session: a, Topic: topic, TopicSource: src, MaybeOffline: a.State == jobstore.SessionOffline}
	add := func(kind, match, source string, rank int) {
		for _, h := range res.Hits {
			if h.Kind == kind && h.Match == match {
				return
			}
		}
		res.Hits = append(res.Hits, FindHit{Kind: kind, Match: match, Source: source})
		if rank > res.Rank {
			res.Rank = rank
		}
	}

	if pathQ != "" {
		rel, ok := relToRoots(pathQ, roots)
		if !ok {
			return FindResult{}, false
		}
		for _, sc := range a.Scope {
			switch {
			case rel == sc || strings.HasPrefix(rel, sc+"/"):
				add(HitScope, sc, a.TopicSource, rankScope)
			case rel == "" || strings.HasPrefix(sc, rel+"/"):
				add(HitScopeWeak, sc, a.TopicSource, rankScopeWeak)
			}
		}
		if res.Rank == 0 {
			return FindResult{}, false
		}
	}

	lowTopic := strings.ToLower(topic)
	if src == TopicSourceInferred {
		// The inferred text also carries peer_name, which has its own (stronger) hit kind:
		// match the path part only, so a peer-name hit is not counted twice as a topic hit.
		lowTopic = strings.ToLower(inferredRel(a, roots))
	}
	lowPeer := strings.ToLower(a.PeerName)
	lowProject := strings.ToLower(a.ProjectKey)
	for _, term := range terms {
		matched := false
		if lowTopic != "" && strings.Contains(lowTopic, term) {
			w := rankTopic
			if src == TopicSourceInferred {
				w = rankTopicInferrd
			}
			add(HitTopic, term, src, w)
			matched = true
		}
		if lowPeer != "" && strings.Contains(lowPeer, term) {
			add(HitPeerName, term, "", rankPeerName)
			matched = true
		}
		if lowProject != "" && strings.Contains(lowProject, term) {
			add(HitProject, term, "", rankProject)
			matched = true
		}
		if !matched {
			return FindResult{}, false
		}
	}
	return res, true
}
