package workflow

import (
	"fmt"
	"log/slog"
	"strings"
)

// `run:` / `shell:` / `if:` are the GitHub-Actions-shaped spellings of a step command
// and a step condition. A `run:` script is converted into `cmd` at submit, so nothing
// downstream (wire, worker, stored spec, admission) sees anything but argv.
const (
	// ifAlways is the only known StepSpec.If value (design §一·8).
	ifAlways = "always"

	shellBash   = "bash"
	shellSh     = "sh"
	shellPwsh   = "pwsh"
	shellPython = "python"
)

// shellCommand wraps a multi-line script into the argv the executor runs. An empty
// shell means bash (the default), because the piped multi-command scripts a workflow
// carries need `-o pipefail` to fail at the right place. `shell: cmd` is refused: the
// whole point of `run:` is a POSIX-style script, and a Windows step writes `cmd:`
// (argv) or `shell: pwsh`.
func shellCommand(shell, script string) ([]string, error) {
	if strings.TrimSpace(script) == "" {
		return nil, fmt.Errorf("run: is empty")
	}
	switch strings.ToLower(strings.TrimSpace(shell)) {
	case "", shellBash:
		return []string{shellBash, "-eo", "pipefail", "-c", script}, nil
	case shellSh:
		return []string{shellSh, "-ec", script}, nil
	case shellPwsh:
		return []string{shellPwsh, "-NoProfile", "-Command", script}, nil
	case shellPython:
		return []string{shellPython, "-c", script}, nil
	case "cmd":
		return nil, fmt.Errorf("shell: cmd is not supported (on Windows use shell: pwsh, or write the command as cmd:)")
	default:
		return nil, fmt.Errorf("unknown shell %q (want bash/sh/pwsh/python)", shell)
	}
}

// normalizeRunScripts rewrites every `run:` (and a legacy `run: always` condition) of a
// spec into `cmd:` / `if:` before validation and execution. It walks the fan lists and
// inline sub-workflows too, so a nested step can not smuggle a script past the submit
// conversion. The returned spec is what gets stored and executed; the errors are
// reported by the caller as job.ErrInvalidRequest.
func normalizeRunScripts(spec Spec) (Spec, error) {
	for i := range spec.Steps {
		st := &spec.Steps[i]
		if st.SubWorkflow != nil {
			sub, err := normalizeRunScripts(*st.SubWorkflow)
			if err != nil {
				return spec, fmt.Errorf("step %d sub_workflow: %w", i+1, err)
			}
			st.SubWorkflow = &sub
		}
		if err := normalizeStepRun(st, i+1); err != nil {
			return spec, err
		}
		for j := range st.Fan {
			f := &st.Fan[j]
			cmd, _, err := convertRunLayer(f.Run, f.Shell, f.Cmd, nil, fmt.Sprintf("step %d fan[%d]", i+1, j))
			if err != nil {
				return spec, err
			}
			f.Cmd, f.Run, f.Shell = cmd, "", ""
		}
	}
	return spec, nil
}

// normalizeStepRun converts one step's `run:`/`shell:` into `cmd:` (and its legacy
// `run: always` into `if: always`), clearing the converted fields so the stored spec
// carries the same argv form a hand-written `cmd:` step does.
func normalizeStepRun(st *StepSpec, stepNo int) error {
	label := fmt.Sprintf("step %d", stepNo)
	if st.If != "" && st.If != ifAlways {
		return fmt.Errorf("%s has unknown if %q (want always)", label, st.If)
	}
	// A built-in step runs gofer's own command, so a script of its own would never run.
	if st.Uses != "" && strings.TrimSpace(st.Run) != "" {
		return fmt.Errorf("%s: a uses: step may not also set run (it runs gofer's own command)", label)
	}
	cmd, cond, err := convertRunLayer(st.Run, st.Shell, st.Cmd, &st.If, label)
	if err != nil {
		return err
	}
	st.Cmd, st.Run, st.Shell = cmd, "", ""
	if cond != "" {
		st.If = cond
	}
	return nil
}

// convertRunLayer converts one layer's (a step, or a fan when cond is nil) `run:` and
// `shell:` into the argv cmd, and a legacy `run: always` into the `if:` condition.
// cmd is the layer's own cmd; cond points at the step's `if` (a fan has no condition,
// so it passes nil and `run: always` is refused there).
//
// DEPRECATED(v0.182): remove in v0.185 — `run: always` used to BE the condition field,
// so a bare `run: always` (no shell, no cmd) is still read as `if: always` with a
// warning instead of running a script named "always".
func convertRunLayer(run, shell string, cmd []string, cond *string, label string) ([]string, string, error) {
	script := strings.TrimSpace(run)
	if script == "" {
		if strings.TrimSpace(shell) != "" {
			return cmd, "", fmt.Errorf("%s sets shell: %q without run: (shell only applies to a run: script)", label, shell)
		}
		return cmd, "", nil
	}
	if strings.EqualFold(script, ifAlways) && strings.TrimSpace(shell) == "" {
		if len(cmd) > 0 {
			// The old specs wrote `run: always` together with `cmd:`; that spelling is now a
			// cmd/run conflict, which the message explains instead of guessing.
			return cmd, "", fmt.Errorf("%s sets cmd together with the deprecated `run: always` condition (write `if: always` and keep cmd)", label)
		}
		if cond == nil {
			return cmd, "", fmt.Errorf("%s: run: always is a step condition, a fan can not use it", label)
		}
		if *cond != "" && *cond != ifAlways {
			return cmd, "", fmt.Errorf("%s sets both if: %q and the deprecated run: always", label, *cond)
		}
		slog.Warn("workflow spec: `run: always` is deprecated as a condition, use `if: always`", "layer", label)
		return cmd, ifAlways, nil
	}
	if len(cmd) > 0 {
		return cmd, "", fmt.Errorf("%s sets both cmd and run (mutually exclusive)", label)
	}
	out, err := shellCommand(shell, run)
	if err != nil {
		return cmd, "", fmt.Errorf("%s: %w", label, err)
	}
	return out, "", nil
}

// LegacyRunAlwaysWarning returns a one-line deprecation warning for a spec still using
// the old `run: always` condition spelling, or "" when it uses `if: always`. The CLI
// prints it right after parsing a workflow file, so the operator sees which steps to
// update (the server logs the same warning when the spec is submitted by any path).
func LegacyRunAlwaysWarning(spec Spec) string {
	var steps []string
	var walk func(Spec, string)
	walk = func(s Spec, prefix string) {
		for i := range s.Steps {
			st := s.Steps[i]
			// DEPRECATED(v0.182): remove in v0.185 (with convertRunLayer's legacy branch).
			if strings.EqualFold(strings.TrimSpace(st.Run), ifAlways) && strings.TrimSpace(st.Shell) == "" && len(st.Cmd) == 0 {
				steps = append(steps, fmt.Sprintf("%sstep %d", prefix, i+1))
			}
			if st.SubWorkflow != nil {
				walk(*st.SubWorkflow, fmt.Sprintf("%sstep %d sub_workflow ", prefix, i+1))
			}
		}
	}
	walk(spec, "")
	if len(steps) == 0 {
		return ""
	}
	return fmt.Sprintf("warning: %s still use `run: always` as a condition; it is read as `if: always` for now — switch to `if: always` (removed in v0.185)",
		strings.Join(steps, ", "))
}
