package sessionrelay

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

// The ceiling owner of a delivery is not the display label of the speaker
// (ceiling_origin.go): the label may be a gofer name nobody holds a ceiling for, while the
// internal job gofer runs must stay bounded by the caller that caused it.

// TestDeliverStampsTheCeilingOwnerNotTheLabel pins the split on the tmux path: the injection
// job carries `origin`, the audit row keeps `by`.
func TestDeliverStampsTheCeilingOwnerNotTheLabel(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-owner-1", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-owner-tmux")

	res, err := s.Deliver(context.Background(), "sid-owner-tmux", "hi", "gofer-nudge:n-1", "bot", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 1)
	assert.Eq(t, "bot", inj.reqs[0].OriginCaller)

	d, ok, err := s.store.GetDecision(res.DecisionID)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "gofer-nudge:n-1", d.AnsweredBy)
}

// TestNudgeDeliveryCarriesTheCreatorAsCeilingOwner is the nudge half: the nudge speaks under
// its own label, and the caller who armed it owns the job.
func TestNudgeDeliveryCarriesTheCreatorAsCeilingOwner(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-owner-nudge", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-owner-nudge")
	n, err := s.CreateNudge("sid-owner-nudge", NudgeInput{
		Kind: jobstore.NudgeEvery, Interval: 30 * time.Minute, Text: "ping", By: "bot",
	})
	assert.NoErr(t, err)
	assert.True(t, n.ID != "")

	now := time.Now().Unix()
	assert.Eq(t, 1, s.SweepNudges(context.Background(), now+30*60+1))
	assert.Len(t, inj.reqs, 1)
	assert.Eq(t, "bot", inj.reqs[0].OriginCaller)
}

// TestDeliveryWithoutCeilingOwnerFailsClosedWhenCallersAreRestricted: an owner nobody can
// name must not be read as "unrestricted". The same delivery is unchanged on a server that
// restricts no caller (the pre-ceiling behaviour).
func TestDeliveryWithoutCeilingOwnerFailsClosedWhenCallersAreRestricted(t *testing.T) {
	s := newSvc(t)
	inj := &fakeInjector{res: InjectResult{JobID: "job-open", ExitCode: 0}}
	s.SetInjector(inj)
	tmuxSession(t, s, "sid-no-owner")

	// Nobody is restricted: an empty owner is the pre-ceiling delivery and still works.
	res, err := s.Deliver(context.Background(), "sid-no-owner", "hi", "gofer", "", false)
	assert.NoErr(t, err)
	assert.Eq(t, PathTmux, res.Path)
	assert.Len(t, inj.reqs, 1)
	assert.Eq(t, "", inj.reqs[0].OriginCaller)

	// The deployment restricts a caller: the same delivery is refused, and nothing runs.
	s.SetRestrictedCallers(func() bool { return true })
	_, err = s.Deliver(context.Background(), "sid-no-owner", "hi", "gofer", "", false)
	assert.True(t, errors.Is(err, ErrNoCeilingOwner))
	assert.Len(t, inj.reqs, 1)

	_, err = s.SendMessage(context.Background(), "sid-no-owner", "hi", OperatorGofer, "")
	assert.True(t, errors.Is(err, ErrNoCeilingOwner))

	// A named owner goes through again.
	_, err = s.Deliver(context.Background(), "sid-no-owner", "hi", "gofer", "bot", false)
	assert.NoErr(t, err)
	assert.Len(t, inj.reqs, 2)
	assert.Eq(t, "bot", inj.reqs[1].OriginCaller)
}
