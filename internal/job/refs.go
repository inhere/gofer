package job

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
)

// maxRefInlineBytes caps how large an inlined ${steps.N.result} / ${steps.N.stdout}
// value may be before resolveRefs refuses to splice it into the next step. Large
// outputs must be passed by path via ${steps.N.result_dir} (design §5.4): inlining
// a multi-MB blob into a prompt/argv is both wasteful and bound to blow argv/CLI
// limits, so we hard-fail with a hint to use result_dir.
const maxRefInlineBytes = 32 * 1024

// stepRefRe matches a single ${steps.<N>.<field>} reference. N is 1-based; field is
// validated against allowedRefFields (here for resolve, at submit for validateRefs).
// Used with ReplaceAllStringFunc so every occurrence in a field is replaced
// independently — NEVER by re-joining into a shell string (mirrors agent.Render's
// per-argv invariant, plan §11).
var stepRefRe = regexp.MustCompile(`\$\{steps\.(\d+)\.(\w+)\}`)

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
// StepIndex. Values are read from the FULL JobResult via s.Get(jobID) — so a live
// in-memory job and a DB-evicted one both resolve, and ResultJSON/ResultDir are
// always populated — and stdout via store.ReadLogTail. A missing prior step, a
// missing result.json, or an over-cap inline value returns an error: at runtime
// advanceWorkflow turns that into a failed workflow with this error.
func (s *Service) resolveRefs(step *StepSpec, priorJobs []jobstore.JobRecord) error {
	if step.Prompt != "" {
		out, err := s.resolveString(step.Prompt, priorJobs)
		if err != nil {
			return err
		}
		step.Prompt = out
	}
	for i := range step.Cmd {
		out, err := s.resolveString(step.Cmd[i], priorJobs)
		if err != nil {
			return err
		}
		step.Cmd[i] = out
	}
	if step.Cwd != "" {
		out, err := s.resolveString(step.Cwd, priorJobs)
		if err != nil {
			return err
		}
		step.Cwd = out
	}
	return nil
}

// resolveString replaces every ${steps.N.field} in one string with its resolved
// value. It collects the first resolveOne error (ReplaceAllStringFunc has no error
// channel) and aborts the whole resolve so a bad reference fails the step rather
// than silently leaving a half-substituted string.
func (s *Service) resolveString(in string, priorJobs []jobstore.JobRecord) (string, error) {
	var firstErr error
	out := stepRefRe.ReplaceAllStringFunc(in, func(ref string) string {
		if firstErr != nil {
			return ref
		}
		m := stepRefRe.FindStringSubmatch(ref)
		// m[1]=N (\d+ — always parses), m[2]=field.
		n, _ := strconv.Atoi(m[1])
		val, err := s.resolveOne(n, m[2], priorJobs)
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

// resolveOne returns the value of ${steps.N.field} for a 1-based prior step N:
//   - result_dir → prior.ResultDir (a path; the preferred way to pass large output)
//   - result     → <result_dir>/result.json text (≤maxRefInlineBytes, else error)
//   - stdout     → prior stdout tail (≤maxRefInlineBytes, else error)
//   - exit_code / status / job_id → scalar
//
// The prior step's full JobResult is fetched via s.Get (in-memory live OR DB
// fallback) so ResultJSON/ResultDir are populated regardless of eviction.
func (s *Service) resolveOne(n int, field string, priorJobs []jobstore.JobRecord) (string, error) {
	rec := stepJob(priorJobs, n)
	if rec == nil {
		return "", fmt.Errorf("${steps.%d.%s}: prior step %d has not produced output", n, field, n)
	}
	res, ok := s.Get(rec.ID)
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
		data, err := s.TailLog(rec.ID, store.StreamStdout, maxRefInlineBytes+1)
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
