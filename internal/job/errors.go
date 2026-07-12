package job

import "fmt"

// Federation admission sentinels (config-federation P3, design §10). They extend
// the Submit validation set defined in service.go (ErrUnknownProject /
// ErrInvalidRequest / ErrNoEligibleWorker) with the runner-scoped capability
// checks: a job is validated against the capability view of the side that will
// EXECUTE it (capabilitiesFor, P2), not against the host's own config.
//
// Each sentinel WRAPS the pre-federation error whose HTTP status it must keep, so
// the httpapi mapping (submitStatus) stays correct without a new mapping rule and
// errors.Is on the coarse sentinel keeps working for existing callers/tests:
//   - the two validate-time rejections wrap ErrInvalidRequest → 400 (the request
//     names a project/agent the target runner cannot serve; retrying it verbatim
//     will not help).
//   - ErrNoCapableWorker wraps ErrNoEligibleWorker → 503 (transient: no ONLINE
//     worker qualifies right now; another may connect / free up).
var (
	// ErrUnknownProjectOnRunner is returned when the target worker is resolvable and
	// online but does not report the requested project. (An unknown project on a
	// local/peer runner stays ErrUnknownProject — see validate.)
	ErrUnknownProjectOnRunner = fmt.Errorf("%w: project not available on target runner", ErrInvalidRequest)
	// ErrAgentNotOnRunner is returned when the target worker is resolvable and online
	// but does not report the requested agent (the resume SOURCE agent for a resume).
	ErrAgentNotOnRunner = fmt.Errorf("%w: agent not available on target runner", ErrInvalidRequest)
	// ErrNoCapableWorker is returned by the label auto-select path when no connected
	// worker satisfies the request's labels/pty AND its project+agent (G2).
	ErrNoCapableWorker = fmt.Errorf("%w: no online worker satisfies the required project+agent", ErrNoEligibleWorker)
)
