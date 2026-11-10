package workbench

import (
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// Thread source filter values (Query.Source). Empty means no source filter.
const (
	SourceACP   = "acp"
	SourcePTY   = "pty"
	SourceRelay = "relay"
)

// ValidSource reports whether v is an accepted Query.Source value ("" included).
func ValidSource(v string) bool {
	switch v {
	case "", SourceACP, SourcePTY, SourceRelay:
		return true
	}
	return false
}

// threadMatchesSource applies the source filter; it must run on the full thread
// (Summary drops Interactive).
func threadMatchesSource(t Thread, src string) bool {
	switch src {
	case SourceACP:
		return t.Kind != KindRelay && !t.Interactive
	case SourcePTY:
		return t.Kind != KindRelay && t.Interactive
	case SourceRelay:
		return t.Kind == KindRelay
	}
	return true
}

// threadLive reports whether the thread still has something running: a relay
// session that is not ended / offline, or a job thread with any non-terminal row
// (RawStatus when the thread carries no job rows).
func threadLive(t Thread) bool {
	if t.Kind == KindRelay {
		return t.RawStatus != jobstore.SessionEnded && t.RawStatus != jobstore.SessionOffline
	}
	if len(t.Jobs) == 0 {
		return !job.IsTerminal(t.RawStatus)
	}
	for _, j := range t.Jobs {
		if !job.IsTerminal(j.Status) {
			return true
		}
	}
	return false
}

// threadPassesSourceLive is the source / live part of the thread filter shared by
// the paged match and buildResponse.
func threadPassesSourceLive(t Thread, q Query) bool {
	if !threadMatchesSource(t, q.Source) {
		return false
	}
	return !q.Live || threadLive(t)
}

// liveCursorPageEmpty: a live listing is a single first page, so a cursor request
// has nothing to return.
func liveCursorPageEmpty(q Query) bool {
	return q.Live && q.Cursor != ""
}

// withIncluded appends the first page's include ids to the pinned ids (deduplicated,
// order kept). Later pages and the unpaged listing ignore include. Preferences are
// never written.
func withIncluded(pinned []string, q Query) []string {
	if q.Cursor != "" || len(q.Include) == 0 {
		return pinned
	}
	seen := make(map[string]bool, len(pinned)+len(q.Include))
	for _, id := range pinned {
		seen[id] = true
	}
	for _, id := range q.Include {
		if !seen[id] {
			seen[id] = true
			pinned = append(pinned, id)
		}
	}
	return pinned
}
