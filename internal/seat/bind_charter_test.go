package seat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/msgprefix"
)

// errSayer refuses every say, like a session whose turn is already running.
type errSayer struct{ err error }

func (s errSayer) SaySession(string, string) error { return s.err }

// liveACPSeat is an acp seat of project proj whose job job-live is in status.
func liveACPSeat(t *testing.T, e *env, clientID, status string) jobstore.Seat {
	t.Helper()
	x, err := e.svc.Create(CreateInput{CallerID: "u1", ProjectKey: "proj", Name: clientID, Kind: jobstore.SeatKindACP,
		Agent: "acp", Charter: "be brief"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-live", ProjectKey: "proj", Agent: "acp", Runner: "local",
		Status: status, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return x
}

// TestBindSaysTheCharterToALiveACPJob: a job bound while it is still running or waiting for input
// never restarts, so the bind says the seat's charter into it right away — once, behind gofer's
// system prefix.
func TestBindSaysTheCharterToALiveACPJob(t *testing.T) {
	for _, status := range []string{job.StatusRunning, job.StatusAwaitingInput} {
		e, _, sy := scheduleEnv(t)
		e.svc.d.Lang = func() i18n.Lang { return i18n.EN }
		x := liveACPSeat(t, e, "desk", status)

		got, info, err := e.svc.BindInfo(x.ID, "job-live", false, "user")
		if err != nil || got.CurrentRef != "job-live" {
			t.Fatalf("%s: bind = %+v, %v", status, got, err)
		}
		if !info.CharterSent || info.CharterError != nil {
			t.Fatalf("%s: bind info = %+v", status, info)
		}
		if len(sy.said) != 1 || sy.said[0][0] != "job-live" {
			t.Fatalf("%s: said = %+v", status, sy.said)
		}
		if text := sy.said[0][1]; !msgprefix.IsSystem(text) || !strings.Contains(text, "desk") || !strings.Contains(text, "be brief") {
			t.Fatalf("%s: the charter message is wrong: %q", status, text)
		}

		// Binding the same job again — with --replace, and after unbinding — is not a second
		// delivery.
		if _, info, err := e.svc.BindInfo(x.ID, "job-live", true, "user"); err != nil || info.CharterSent {
			t.Fatalf("%s: rebind = %+v, %v", status, info, err)
		}
		if _, err := e.svc.Unbind(x.ID, "user"); err != nil {
			t.Fatal(err)
		}
		if _, info, err := e.svc.BindInfo(x.ID, "job-live", false, "user"); err != nil || info.CharterSent {
			t.Fatalf("%s: bind after unbind = %+v, %v", status, info, err)
		}
		if len(sy.said) != 1 {
			t.Fatalf("%s: the charter was sent %d times, want once: %+v", status, len(sy.said), sy.said)
		}
	}
}

// TestBindSaysNoCharterToAnEndedACPJob: a job that already ended is restarted with the charter as
// its opening input, so binding it sends nothing — and the restarted job carries it too.
func TestBindSaysNoCharterToAnEndedACPJob(t *testing.T) {
	e, _, sy := scheduleEnv(t)
	x := acpSeat(t, e, job.StatusDone)
	if len(sy.said) != 0 {
		t.Fatalf("an ended job must get no charter message: %+v", sy.said)
	}
	if _, err := e.svc.Restart(context.Background(), x.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpsertJob(jobstore.JobRecord{ID: "job-new", ProjectKey: "proj", Agent: "acp", Runner: "local",
		Status: job.StatusRunning, SessionID: "acp-sess", Cwd: ".", ResultDir: "/r", RequestJSON: "{}", StartedAt: 2}); err != nil {
		t.Fatal(err)
	}
	if _, info, err := e.svc.BindInfo(x.ID, "job-new", true, "user"); err != nil || info.CharterSent {
		t.Fatalf("the restarted job already carries the charter: %+v, %v", info, err)
	}
	if len(sy.said) != 0 {
		t.Fatalf("nothing may be said into a restarted session: %+v", sy.said)
	}
}

// TestBindCharterFailureDoesNotFailTheBind: the say is best effort — a session that cannot take the
// message leaves the bind done and reports why.
func TestBindCharterFailureDoesNotFailTheBind(t *testing.T) {
	e, _, _ := scheduleEnv(t)
	e.svc.d.Sayer = errSayer{err: errors.New("session turn in progress")}
	x := liveACPSeat(t, e, "desk", job.StatusAwaitingInput)
	got, info, err := e.svc.BindInfo(x.ID, "job-live", false, "user")
	if err != nil || got.CurrentRef != "job-live" {
		t.Fatalf("a failed say must not fail the bind: %+v, %v", got, err)
	}
	if info.CharterSent || info.CharterError == nil {
		t.Fatalf("bind info = %+v, want the say error", info)
	}
}
