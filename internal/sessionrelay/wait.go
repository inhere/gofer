package sessionrelay

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Wait timings. A wait is a POLL on the stored session row: the relay has no
// per-session change notification (the store's ChangeSession hook feeds the
// browser's push topics, not a one-shot waiter), and the row is small, so a
// quarter-second read costs nothing and the state a caller acts on is the state
// that is stored.
const (
	// DefaultWaitPoll is the interval between two state reads.
	DefaultWaitPoll = 250 * time.Millisecond
	// DefaultWaitTimeout is a single wait's budget when the caller names none.
	DefaultWaitTimeout = 30 * time.Second
	// MaxWaitTimeout caps ONE HTTP long poll (a CLI loops over it until its own
	// --timeout runs out), so no request outlives a client's HTTP timeout.
	MaxWaitTimeout = 60 * time.Second
)

// waitStates are the states a caller may wait for: an agent orchestrating other
// sessions cares about "it is free now" (idle), "it asks something"
// (waiting_reply) and "it is gone" (ended). The other stored states (running,
// needs_attention, handed_off, offline) are transient or diagnostic and nobody
// schedules on them.
var waitStates = []string{jobstore.SessionIdle, jobstore.SessionWaitingReply, jobstore.SessionEnded}

// WaitStates returns the accepted `until` states, in a stable order.
func WaitStates() []string {
	return append([]string(nil), waitStates...)
}

// ValidWaitState reports whether state is one a caller may wait for.
func ValidWaitState(state string) bool {
	for _, s := range waitStates {
		if s == state {
			return true
		}
	}
	return false
}

// ParseWaitStates splits a comma-separated `until` list ("idle,ended") into its
// states, refusing an empty list and any state outside WaitStates. The error is
// ErrInvalidInput so the entry layer answers 400.
func ParseWaitStates(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		st := strings.ToLower(strings.TrimSpace(part))
		if st == "" {
			continue
		}
		if !ValidWaitState(st) {
			return nil, fmt.Errorf("%w: until must be one of %s (got %q)", ErrInvalidInput,
				strings.Join(waitStates, "|"), st)
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: until must be one of %s", ErrInvalidInput, strings.Join(waitStates, "|"))
	}
	return out, nil
}

// WaitForState blocks until the session's stored state is one of until, or the
// timeout expires. reached reports which of the two happened; the session (and
// its current state) is returned either way, so a caller can show what it found
// instead of having to read it again. A timeout <= 0 reads once and returns.
// A cancelled ctx returns the last read with ctx's error.
func (s *Service) WaitForState(ctx context.Context, sid string, until []string, timeout, poll time.Duration) (jobstore.AgentSession, bool, error) {
	if poll <= 0 {
		poll = DefaultWaitPoll
	}
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		a, ok, err := s.store.GetAgentSession(strings.TrimSpace(sid))
		if err != nil {
			return jobstore.AgentSession{}, false, err
		}
		if !ok {
			return jobstore.AgentSession{}, false, fmt.Errorf("%w: %s", ErrUnknownSession, sid)
		}
		if matchesWaitState(a.State, until) {
			return a, true, nil
		}
		if timeout <= 0 {
			return a, false, nil
		}
		wait := poll
		if left := time.Until(deadline); left <= 0 {
			return a, false, nil
		} else if left < wait {
			wait = left
		}
		select {
		case <-ctx.Done():
			return a, false, ctx.Err()
		case <-time.After(wait):
		}
	}
}

func matchesWaitState(state string, until []string) bool {
	for _, u := range until {
		if state == u {
			return true
		}
	}
	return false
}
