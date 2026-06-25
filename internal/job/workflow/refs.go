package workflow

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	job "github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// maxRefInlineBytes caps how large an inlined ${steps.N.result} / ${steps.N.stdout}
// value may be before resolveRefs refuses to splice it into the next step. Large
// outputs must be passed by path via ${steps.N.result_dir} (design §5.4): inlining
// a multi-MB blob into a prompt/argv is both wasteful and bound to blow argv/CLI
// limits, so we hard-fail with a hint to use result_dir.
const maxRefInlineBytes = 32 * 1024

// stepRefRe matches a single ${steps.<N>.<field>} reference with an OPTIONAL fan
// selector ${steps.<N>.f<K>.<field>} (P2): group 1 = N (1-based prior step), group 2
// = K (1-based fan index, empty when no selector), group 3 = field. N is 1-based; the
// field is validated against allowedRefFields (here for resolve, at submit for
// validateRefs). Used with ReplaceAllStringFunc so every occurrence in a field is
// replaced independently — NEVER by re-joining into a shell string (mirrors
// agent.Render's per-argv invariant, plan §11).
//
// Without a fan selector, ${steps.N.result_dir} on a fan-out step aggregates ALL
// successful fans' result_dir (newline-joined); ${steps.N.fK.result_dir} picks fan K.
var stepRefRe = regexp.MustCompile(`\$\{steps\.(\d+)(?:\.f(\d+))?\.(\w+)\}`)

// allowedRefFields is the closed set of step-output fields a ${steps.N.field}
// reference may name (design §5.4). Kept as a map so both resolveRefs (runtime) and
// validateRefs (submit-time) check the same set.
var allowedRefFields = map[string]bool{
	"result_dir": true,
	"result":     true,
	"stdout":     true,
	"exit_code":  true,
	"status":     true,
	"job_id":     true,
}

// validateRefs statically checks every ${steps.N.field} reference in a workflow
// spec at submit time (no prior outputs needed): for a 1-based step i, every ref it
// names must point at an EARLIER step (1 <= N < i, no self/forward reference) and
// use a known field. Step 1 may not reference anything (it has no prior). A bad ref
// is an job.ErrInvalidRequest (400) so the whole submit is rejected before any DB row /
// job is created — the chain never starts a step it can't later resolve.
func validateRefs(spec Spec) error {
	for i := range spec.Steps {
		stepNo := i + 1 // 1-based
		fields := []string{spec.Steps[i].Prompt, spec.Steps[i].Cwd}
		fields = append(fields, spec.Steps[i].Cmd...)
		for _, f := range fields {
			for _, m := range stepRefRe.FindAllStringSubmatch(f, -1) {
				n, _ := strconv.Atoi(m[1]) // m[1] is \d+, always parses
				fanK := m[2]               // optional fan selector (empty when absent)
				field := m[3]
				if !allowedRefFields[field] {
					return fmt.Errorf("%w: step %d references unknown field ${steps.%d.%s}", job.ErrInvalidRequest, stepNo, n, field)
				}
				if n < 1 {
					return fmt.Errorf("%w: step %d references invalid step ${steps.%d.%s} (N must be >= 1)", job.ErrInvalidRequest, stepNo, n, field)
				}
				if n >= stepNo {
					return fmt.Errorf("%w: step %d references ${steps.%d.%s} which is not a prior step (N must be < %d)", job.ErrInvalidRequest, stepNo, n, field, stepNo)
				}
				// P2: a fan selector .fK must point at a real fan of the referenced step —
				// K in [1, fan_out]. The referenced step's FanOut is known statically here.
				if fanK != "" {
					k, _ := strconv.Atoi(fanK) // \d+, always parses
					if k < 1 {
						return fmt.Errorf("%w: step %d references ${steps.%d.f%d.%s} (fan index must be >= 1)", job.ErrInvalidRequest, stepNo, n, k, field)
					}
					want := fanWant(spec.Steps[n-1]) // referenced step's parallelism
					if k > want {
						return fmt.Errorf("%w: step %d references ${steps.%d.f%d.%s} but step %d has only %d fan(s)", job.ErrInvalidRequest, stepNo, n, k, field, n, want)
					}
				}
			}
		}
	}
	return nil
}

// resolveRefs rewrites every ${steps.N.field} reference in the step's string fields
// (prompt / each cmd argv element / cwd) to the matching prior step's output, in
// place. N is 1-based and refers to an ALREADY-FINISHED prior step (validateRefs
// guarantees N < this step's index at submit time, so priorJobs holds it).
//
// Substitution is per-field and per-argv (regexp.ReplaceAllStringFunc over each
// string): the cmd slice is rewritten element-by-element and is never joined into a
// single shell string, so a substituted value containing spaces/quotes stays one
// argv element and is never re-tokenised (the agent.Render invariant, plan §11).
//
// priorJobs is the workflow's started step-jobs (from ListWorkflowJobs), indexed by
// StepIndex. Values are read from the FULL job.JobResult via e.ops.Get(jobID) — so a live
// in-memory job and a DB-evicted one both resolve, and ResultJSON/ResultDir are
// always populated — and stdout via store.ReadLogTail. A missing prior step, a
// missing result.json, or an over-cap inline value returns an error: at runtime
// Advance turns that into a failed workflow with this error.
func (e *Engine) resolveRefs(step *StepSpec, priorJobs []jobstore.JobRecord) error {
	if step.Prompt != "" {
		out, err := e.resolveString(step.Prompt, priorJobs)
		if err != nil {
			return err
		}
		step.Prompt = out
	}
	for i := range step.Cmd {
		out, err := e.resolveString(step.Cmd[i], priorJobs)
		if err != nil {
			return err
		}
		step.Cmd[i] = out
	}
	if step.Cwd != "" {
		out, err := e.resolveString(step.Cwd, priorJobs)
		if err != nil {
			return err
		}
		step.Cwd = out
	}
	return nil
}

// resolveString replaces every ${steps.N.field} / ${steps.N.fK.field} in one string
// with its resolved value. It collects the first resolve error (ReplaceAllStringFunc
// has no error channel) and aborts the whole resolve so a bad reference fails the step
// rather than silently leaving a half-substituted string.
func (e *Engine) resolveString(in string, priorJobs []jobstore.JobRecord) (string, error) {
	var firstErr error
	out := stepRefRe.ReplaceAllStringFunc(in, func(ref string) string {
		if firstErr != nil {
			return ref
		}
		m := stepRefRe.FindStringSubmatch(ref)
		// m[1]=N (\d+ — always parses), m[2]=fanK (optional, empty=no selector), m[3]=field.
		n, _ := strconv.Atoi(m[1])
		fanK := 0
		if m[2] != "" {
			fanK, _ = strconv.Atoi(m[2])
		}
		val, err := e.resolveRef(n, fanK, m[3], priorJobs)
		if err != nil {
			firstErr = err
			return ref
		}
		return val
	})
	if firstErr != nil {
		return "", firstErr
	}
	return out, nil
}

// fanJobsOfStep returns ALL jobs of a 1-based prior step N, in fan_index order (a
// single-job step yields its one job; a fan-out step yields its fans). It pulls from
// priorJobs (ListWorkflowJobs ordering) — for a fan-out step the engine started fans
// 1..N so all rows are present here once they exist.
func fanJobsOfStep(priorJobs []jobstore.JobRecord, n int) []jobstore.JobRecord {
	out := make([]jobstore.JobRecord, 0, 4)
	for i := range priorJobs {
		if priorJobs[i].StepIndex == n {
			out = append(out, priorJobs[i])
		}
	}
	return out
}

// resolveRef returns the value of ${steps.N.field} / ${steps.N.fK.field} for a 1-based
// prior step N (P2). With a fan selector (fanK>=1) it resolves against THAT fan job
// (fan_index==fanK, or the single fan_index-0 job when fanK==1 on a single-job step).
// Without a selector (fanK==0) it resolves against the step:
//   - result_dir on a FAN-OUT step → newline-joined ResultDir of every SUCCESSFUL (done)
//     fan (design D15 引用聚合). On a single-job step → that job's ResultDir verbatim.
//   - every other field (and result_dir on a single-job step) → the first fan job (the
//     representative; identical to the v1 single-job path for a non-fan step).
//
// The chosen job's full job.JobResult is fetched via e.Get (in-memory live OR DB fallback)
// so ResultJSON/ResultDir are populated regardless of eviction.
func (e *Engine) resolveRef(n, fanK int, field string, priorJobs []jobstore.JobRecord) (string, error) {
	stepJobs := fanJobsOfStep(priorJobs, n)
	if len(stepJobs) == 0 {
		return "", fmt.Errorf("${steps.%d.%s}: prior step %d has not produced output", n, field, n)
	}

	// Fan aggregation: result_dir with NO selector on a fan-out step (>1 job) returns
	// every successful fan's result_dir, newline-joined (the path-passing aggregate).
	if fanK == 0 && field == "result_dir" && len(stepJobs) > 1 {
		dirs := make([]string, 0, len(stepJobs))
		for i := range stepJobs {
			res, ok := e.ops.Get(stepJobs[i].ID)
			if !ok {
				return "", fmt.Errorf("${steps.%d.result_dir}: fan job %s not found", n, stepJobs[i].ID)
			}
			if res.Status == job.StatusDone && res.ResultDir != "" {
				dirs = append(dirs, res.ResultDir)
			}
		}
		if len(dirs) == 0 {
			return "", fmt.Errorf("${steps.%d.result_dir}: no successful fan produced a result_dir", n)
		}
		return strings.Join(dirs, "\n"), nil
	}

	// Select the target job: a fan selector picks fan_index==fanK (fanK==1 maps to the
	// single fan_index-0 job of a non-fan step); no selector uses the first job.
	rec := stepJobs[0]
	if fanK >= 1 {
		picked := pickFanJob(stepJobs, fanK)
		if picked == nil {
			return "", fmt.Errorf("${steps.%d.f%d.%s}: prior step %d has no fan %d", n, fanK, field, n, fanK)
		}
		rec = *picked
	}

	res, ok := e.ops.Get(rec.ID)
	if !ok {
		return "", fmt.Errorf("${steps.%d.%s}: prior step job %s not found", n, field, rec.ID)
	}

	switch field {
	case "result_dir":
		return res.ResultDir, nil
	case "exit_code":
		return strconv.Itoa(res.ExitCode), nil
	case "status":
		return res.Status, nil
	case "job_id":
		return res.ID, nil
	case "result":
		if res.ResultJSON == "" {
			return "", fmt.Errorf("${steps.%d.result}: prior step wrote no result.json (use ${steps.%d.result_dir})", n, n)
		}
		if len(res.ResultJSON) > maxRefInlineBytes {
			return "", fmt.Errorf("${steps.%d.result}: result.json is %d bytes (>%d); use ${steps.%d.result_dir}", n, len(res.ResultJSON), maxRefInlineBytes, n)
		}
		return res.ResultJSON, nil
	case "stdout":
		// Read at most maxRefInlineBytes+1 so an over-cap stream is detected without
		// loading the whole (possibly huge) file: a return at the cap+1 boundary means
		// the tail itself already exceeds the inline limit.
		data, err := e.ops.TailLog(rec.ID, store.StreamStdout, maxRefInlineBytes+1)
		if err != nil {
			return "", fmt.Errorf("${steps.%d.stdout}: read stdout: %w", n, err)
		}
		if len(data) > maxRefInlineBytes {
			return "", fmt.Errorf("${steps.%d.stdout}: stdout is >%d bytes; use ${steps.%d.result_dir}", n, maxRefInlineBytes, n)
		}
		return string(data), nil
	default:
		// validateRefs rejects unknown fields at submit; this guards the runtime path.
		return "", fmt.Errorf("${steps.%d.%s}: unknown field %q", n, field, field)
	}
}

// pickFanJob returns the step job whose fan_index == fanK (P2). fanK==1 also matches a
// single-job step's fan_index-0 job (a non-fan step is "fan 1 of 1"), so ${steps.N.f1.x}
// resolves on a non-fan step. Returns nil when no such fan exists.
func pickFanJob(stepJobs []jobstore.JobRecord, fanK int) *jobstore.JobRecord {
	for i := range stepJobs {
		fi := stepJobs[i].FanIndex
		if fi == fanK || (fanK == 1 && fi == 0) {
			return &stepJobs[i]
		}
	}
	return nil
}
