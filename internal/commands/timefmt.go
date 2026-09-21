package commands

import (
	"sync"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// serverTZ renders every timestamp the CLI prints in the SERVER's local zone,
// with its UTC offset (bd h-aii-tnua): the job times a human reads are the ones
// the server acted on, and a parent of another timezone used to see a schedule
// fire "at 09:00" while the server's own log said 01:00.
//
// It is the ONE formatter for those fields — formatStarted / probeTime /
// formatScheduleTime / formatScheduleListTime all delegate here — and it resolves
// the server's offset from GET /v1/stats once per process (server_tz_offset_sec).
// When that call cannot be made (offline, older server) it falls back to the
// process's local zone and marks the value "(local)" rather than silently
// pretending it is the server's clock.
var (
	serverTZMutex    sync.Mutex
	serverTZResolved bool
	serverTZOffset   int
	serverTZOK       bool
	// serverTZFetch is the test seam: nil = fetch over the server's stats API.
	serverTZFetch func() (int, bool)
)

// fmtServerTime renders a unix-seconds timestamp in the server's zone; 0 / unset
// renders as "-" (the CLI's existing convention for "never happened").
func fmtServerTime(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	off, ok := serverTZ()
	if !ok {
		return time.Unix(sec, 0).In(time.Local).Format("2006-01-02 15:04:05") + " (local)"
	}
	// FixedZone with the server's offset formats as +08:00 / -05:30 — the suffix is
	// what tells a reader which clock the stamp belongs to.
	return time.Unix(sec, 0).In(time.FixedZone("", off)).Format("2006-01-02 15:04:05 -07:00")
}

// fmtServerClock renders the time-of-day part only (the schedule list's "within a
// day" column), in the server's zone.
func fmtServerClock(sec int64) string {
	if sec <= 0 {
		return "-"
	}
	off, ok := serverTZ()
	t := time.Unix(sec, 0)
	if !ok {
		return t.In(time.Local).Format("15:04:05") + " (local)"
	}
	return t.In(time.FixedZone("", off)).Format("15:04:05")
}

// serverTZ resolves (and caches) the server's UTC offset in seconds. A failure is
// cached too: a CLI invocation must not retry a dead server on every timestamp.
func serverTZ() (int, bool) {
	serverTZMutex.Lock()
	defer serverTZMutex.Unlock()
	if !serverTZResolved {
		fetch := serverTZFetch
		if fetch == nil {
			fetch = fetchServerTZOffset
		}
		serverTZOffset, serverTZOK = fetch()
		serverTZResolved = true
	}
	return serverTZOffset, serverTZOK
}

// setServerTZ pins the resolved offset (tests / callers that already know it).
func setServerTZ(off int, ok bool) {
	serverTZMutex.Lock()
	defer serverTZMutex.Unlock()
	serverTZOffset, serverTZOK, serverTZResolved = off, ok, true
}

// fetchServerTZOffset asks the server for its UTC offset (one stats call, the
// same client every other command uses). Any failure — offline, an older server
// without the field — degrades to the local zone.
func fetchServerTZOffset() (int, bool) {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return 0, false
	}
	off, ok, err := cli.GetServerTZOffset()
	if err != nil || !ok {
		return 0, false
	}
	return off, true
}
