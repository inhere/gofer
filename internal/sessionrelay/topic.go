package sessionrelay

import (
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
)

// Session topics (第一期收集箱设计「会话主题与查找」): what a session works on and which
// project sub-paths it owns, so a person or another agent can find the right session.
// This file holds the write path (validation + SetTopic), the display-only inference and
// the shared path helpers; the search itself is in find.go.

// Limits of a stored topic and scope.
const (
	maxTopicRunes  = 200
	maxScopeItems  = 20
	maxScopeBytes  = 300
	topicInferred  = "inferred" // the read view's TopicSource for a derived (never stored) topic
	inferredJoiner = " · "
)

// ErrSessionEnded reports an edit of a session that has already ended: a finished session
// is no longer a delivery target, so its topic is not edited (HTTP 409).
var ErrSessionEnded = fmt.Errorf("sessionrelay: session has ended")

// TopicSourceInferred is the TopicSource a view reports for a topic derived at read time.
const TopicSourceInferred = topicInferred

// NormalizeScope validates a scope list and returns it normalised: every item a
// project-root-relative slash path, cleaned, deduplicated in order. Absolute paths (POSIX,
// UNC-ish backslash and drive-letter forms), any `..` segment, the project root itself
// ("." / empty) and control characters are refused with ErrInvalidInput.
func NormalizeScope(items []string) ([]string, error) {
	if len(items) > maxScopeItems {
		return nil, fmt.Errorf("%w: scope has %d items, at most %d", ErrInvalidInput, len(items), maxScopeItems)
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, raw := range items {
		p, err := normalizeScopeItem(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out, nil
}

func normalizeScopeItem(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("%w: scope item is empty", ErrInvalidInput)
	}
	if len(p) > maxScopeBytes {
		return "", fmt.Errorf("%w: scope item longer than %d bytes", ErrInvalidInput, maxScopeBytes)
	}
	if hasControl(p) {
		return "", fmt.Errorf("%w: scope item %q has control characters", ErrInvalidInput, raw)
	}
	if isAbsLike(p) {
		return "", fmt.Errorf("%w: scope item %q must be relative to the project root", ErrInvalidInput, raw)
	}
	slash := strings.ReplaceAll(p, "\\", "/")
	for _, seg := range strings.Split(slash, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: scope item %q must not contain ..", ErrInvalidInput, raw)
		}
	}
	clean := path.Clean(slash)
	if clean == "." || clean == "" {
		return "", fmt.Errorf("%w: scope item %q is the project root itself", ErrInvalidInput, raw)
	}
	return clean, nil
}

// isAbsLike reports whether p is an absolute path in any platform's spelling.
func isAbsLike(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}
	return len(p) >= 2 && p[1] == ':' && unicode.IsLetter(rune(p[0]))
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// SetTopic stores a session's topic and/or scope. topic / scope nil keep that part; source
// is jobstore.SessionTopicSelf (the session itself) or SessionTopicHuman (anybody else).
// ErrUnknownSession for no such session, ErrSessionEnded for a finished one,
// ErrInvalidInput for a bad value (including nothing to set).
func (s *Service) SetTopic(sid string, topic *string, scope *[]string, source string) (jobstore.AgentSession, error) {
	if topic == nil && scope == nil {
		return jobstore.AgentSession{}, fmt.Errorf("%w: topic or scope required", ErrInvalidInput)
	}
	var t *string
	if topic != nil {
		v := strings.TrimSpace(*topic)
		if utf8.RuneCountInString(v) > maxTopicRunes {
			return jobstore.AgentSession{}, fmt.Errorf("%w: topic longer than %d characters", ErrInvalidInput, maxTopicRunes)
		}
		if hasControl(v) {
			return jobstore.AgentSession{}, fmt.Errorf("%w: topic has control characters", ErrInvalidInput)
		}
		t = &v
	}
	var sc *[]string
	if scope != nil {
		norm, err := NormalizeScope(*scope)
		if err != nil {
			return jobstore.AgentSession{}, err
		}
		sc = &norm
	}
	a, ok, ended, err := s.store.SetSessionTopic(sid, t, sc, source)
	if err != nil {
		return jobstore.AgentSession{}, err
	}
	if !ok {
		return jobstore.AgentSession{}, ErrUnknownSession
	}
	if ended {
		return jobstore.AgentSession{}, ErrSessionEnded
	}
	return a, nil
}

// EffectiveTopic is the topic a view shows and the finder matches: the stored one with its
// source, or — when the session has none — the inferred value marked TopicSourceInferred
// (never stored). roots are the session project's roots (host_path / container_path); both
// may be empty, in which case only peer_name feeds the inference. An empty topic and source
// mean there is nothing to show.
func EffectiveTopic(a jobstore.AgentSession, roots []string) (topic, source string) {
	if strings.TrimSpace(a.Topic) != "" {
		src := a.TopicSource
		if src == "" {
			src = jobstore.SessionTopicHuman
		}
		return a.Topic, src
	}
	if inf := InferTopic(a, roots); inf != "" {
		return inf, TopicSourceInferred
	}
	return "", ""
}

// InferTopic derives a display-only topic: the path of the session's last_cwd (cwd when it
// never reported one) relative to its project root, then its peer_name. The root is
// whichever of the project's roots is a prefix of that path (G002: the session's cwd is a
// path on the machine it runs on — host or container — so the project's root on that same
// side is the one that matches; the longest wins).
func InferTopic(a jobstore.AgentSession, roots []string) string {
	var parts []string
	if rel := inferredRel(a, roots); rel != "" {
		parts = append(parts, rel)
	}
	if name := strings.TrimSpace(a.PeerName); name != "" {
		parts = append(parts, name)
	}
	return strings.Join(parts, inferredJoiner)
}

// inferredRel is the path part of the inferred topic: the session's current directory
// relative to its project root ("" at the root or outside every root).
func inferredRel(a jobstore.AgentSession, roots []string) string {
	cwd := a.LastCwd
	if strings.TrimSpace(cwd) == "" {
		cwd = a.Cwd
	}
	rel, ok := relToRoots(cwd, roots)
	if !ok {
		return ""
	}
	return rel
}

// relToRoots returns p relative to the longest root that is a path prefix of it ("" when p
// is the root itself). ok is false when no root is a prefix. p that is already relative is
// returned cleaned, as is.
func relToRoots(p string, roots []string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	if !isAbsLike(p) {
		clean := path.Clean(strings.ReplaceAll(p, "\\", "/"))
		if clean == "." {
			clean = ""
		}
		return clean, true
	}
	slash := strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	best, bestLen, found := "", -1, false
	for _, root := range roots {
		r := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(root), "\\", "/"), "/")
		if r == "" {
			continue
		}
		rest, ok := trimPathPrefix(slash, r)
		if ok && len(r) > bestLen {
			best, bestLen, found = rest, len(r), true
		}
	}
	if !found {
		return "", false
	}
	return path.Clean("/" + best)[1:], true
}

// trimPathPrefix strips root from p when root is p or an ancestor directory of it. Drive-letter
// paths compare case-insensitively (Windows).
func trimPathPrefix(p, root string) (string, bool) {
	eq := func(a, b string) bool { return a == b }
	if len(root) >= 2 && root[1] == ':' {
		eq = strings.EqualFold
	}
	if len(p) < len(root) || !eq(p[:len(root)], root) {
		return "", false
	}
	rest := p[len(root):]
	if rest == "" {
		return "", true
	}
	if rest[0] != '/' {
		return "", false
	}
	return strings.TrimLeft(rest, "/"), true
}
