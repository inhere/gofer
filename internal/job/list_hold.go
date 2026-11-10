package job

import "github.com/inhere/gofer/internal/jobstore"

// holdMatches reports whether a merged job row passes the ListOpts.Hold filter; it
// mirrors the SQL side (jobstore holdFilterSQL): only a human hold counts, an update
// park never does. An empty filter keeps every row.
func holdMatches(snap JobResult, filter string) bool {
	if filter == "" {
		return true
	}
	if snap.Hold == nil || snap.Hold.Kind != HoldKindHuman {
		return false
	}
	switch filter {
	case jobstore.HoldFilterPending:
		return snap.Status == StatusAwaitingApproval
	case jobstore.HoldFilterDecided:
		return snap.Hold.Decision != ""
	}
	return true
}
