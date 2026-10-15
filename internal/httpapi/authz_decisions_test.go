package httpapi

import (
	"github.com/gookit/rux/v2"
)

// The test seam of the design's third safety net (「调用断言」): TestMain installs the
// recorder below, so any test can see, for one request, which permissions the decision
// path was asked about and what it answered — production code only calls the hook.
//
// The record lives in the rux context of the request, so it is per request and no test
// shares state with another (the hook itself is installed once and never replaced).

// ctxAuthzDecided is the rux context key of one request's decision record.
const ctxAuthzDecided = "authz_decided_test"

// decidedPerm is one decision: the permission asked about and the answer.
type decidedPerm struct {
	perm    string
	granted bool
}

// installDecisionRecorder points decisionHook at the recorder. Called from TestMain.
func installDecisionRecorder() {
	decisionHook = func(c *rux.Context, perm string, granted bool) {
		v, _ := c.Get(ctxAuthzDecided)
		rec, _ := v.(*[]decidedPerm)
		if rec == nil {
			rec = &[]decidedPerm{}
			c.Set(ctxAuthzDecided, rec)
		}
		*rec = append(*rec, decidedPerm{perm: perm, granted: granted})
	}
}

// decisionsOf returns the decisions of the request this context belongs to, in order.
func decisionsOf(c *rux.Context) []decidedPerm {
	v, _ := c.Get(ctxAuthzDecided)
	rec, _ := v.(*[]decidedPerm)
	if rec == nil {
		return nil
	}
	return *rec
}
