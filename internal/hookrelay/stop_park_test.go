package hookrelay

import (
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// TestStopJobParkIsAnnounced pins the signal gofer-fv6a rests on: a Stop hook that
// blocks in waitJobsOnly tells the hub how long it is parking, and clears that again
// when it returns. Without the announcement the hub cannot know that the terminal is
// sitting in the wait loop, so a job-end notice was claimed away and handed to the
// messenger — unreadable by an agent blocked in its own Stop hook.
func TestStopJobParkIsAnnounced(t *testing.T) {
	injectIdleProbe(t, 600) // away: the parked wait applies
	sid := "sid-park"
	f := stopJobWaitFake(t, sid, client.RelayModeAuto, "")
	opts := fastOpts(nil)
	opts.StopJobWait = config.StopJobWaitAway
	opts.AutoRelayIdleSec = 300
	var log strings.Builder
	opts.Log = &log

	res, err := Run(f, stopPayload(t, "claude", sid), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Blocked || !strings.Contains(res.Reason, "[gofer · job 完成] job-9") {
		t.Fatalf("res=%+v, want the job notice as the block reason (log=%s)", res, log.String())
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	var announced, cleared bool
	wantSecs := int(opts.Wait / time.Second)
	for _, hb := range f.beats {
		if hb.StopParkSec == nil {
			continue
		}
		if *hb.StopParkSec == wantSecs && wantSecs > 0 {
			announced = true
		}
		if *hb.StopParkSec == 0 {
			cleared = true
		}
	}
	if !announced {
		t.Fatalf("the park was never announced (want stop_park_sec=%d): %+v", wantSecs, f.beats)
	}
	if !cleared {
		t.Fatalf("the park was never cleared on the way out: %+v", f.beats)
	}
	if last := f.beats[len(f.beats)-1]; last.StopParkSec == nil || *last.StopParkSec != 0 {
		t.Fatalf("the LAST beat must clear the park: %+v", last)
	}
}

// TestStopJobReleasedParkIsNotAnnounced: a Stop that keeps the terminal free (the
// person is at the keyboard) opens no wait, so it must not claim a park — otherwise
// notifications would be held back from a session that is not listening.
func TestStopJobReleasedParkIsNotAnnounced(t *testing.T) {
	injectIdleProbe(t, 5) // at the keyboard
	sid := "sid-no-park"
	f := stopJobWaitFake(t, sid, client.RelayModeAuto, "")
	opts := fastOpts(nil)
	opts.StopJobWait = config.StopJobWaitAway
	opts.AutoRelayIdleSec = 300

	if _, err := Run(f, stopPayload(t, "claude", sid), opts); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, hb := range f.beats {
		if hb.StopParkSec != nil {
			t.Fatalf("a released Stop must not announce a park: %+v", hb)
		}
	}
}
