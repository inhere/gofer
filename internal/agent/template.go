package agent

import "strings"

// Vars holds the values substituted into command-template placeholders. See
// plan §9 (P3): first-phase placeholders are {{prompt}} {{cwd}} {{job_id}}
// {{result_dir}}. SessionID feeds {{session_id}} for session inject/resume
// templates (session-capture §5.1/§6.4).
type Vars struct {
	Prompt    string
	Cwd       string
	JobID     string
	ResultDir string
	SessionID string
	// SystemPrompt feeds {{system_prompt}} in an agent's SystemInject template
	// (E35 role injection, e.g. claude --append-system-prompt).
	SystemPrompt string
}

// placeholders maps the supported template tokens to their values. Kept as a
// method so each call uses the receiver's Vars without sharing state.
func (v Vars) replacements() []string {
	// strings.NewReplacer pairs: old1, new1, old2, new2, ...
	return []string{
		"{{prompt}}", v.Prompt,
		"{{cwd}}", v.Cwd,
		"{{job_id}}", v.JobID,
		"{{result_dir}}", v.ResultDir,
		"{{session_id}}", v.SessionID,
		"{{system_prompt}}", v.SystemPrompt,
	}
}

// Render substitutes placeholders in each template argument INDEPENDENTLY and
// returns a fresh argv slice. This is the core security invariant (plan §11):
// the argv array is preserved element-by-element and is NEVER joined into a
// single shell string, so a prompt containing spaces/quotes stays one argv
// element and is never re-tokenised by a shell.
//
// Example: tmplArgs ["exec", "{{prompt}}"] with Prompt=`fix "x" now` renders to
// ["exec", `fix "x" now`] — still two argv elements.
func Render(tmplArgs []string, vars Vars) []string {
	repl := strings.NewReplacer(vars.replacements()...)
	out := make([]string, len(tmplArgs))
	for i, a := range tmplArgs {
		out[i] = repl.Replace(a)
	}
	return out
}
