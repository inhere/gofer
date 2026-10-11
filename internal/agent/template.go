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
	// Text feeds {{text}} in an agent's DeliverCommand (the web message typed to a
	// live session, already carrying the reply prefix).
	Text string
	// Model feeds {{model}} in an agent's ModelArgs (N1 §B).
	Model string
	// FromSession feeds {{from_session}} in an agent's FromSessionArgs (gofer-f4z8):
	// the earlier session the new one inherits its context from.
	FromSession string
	// MCPConfigFile feeds {{mcp_config_file}} in an agent's mcp_args (MCP-06): the MCP
	// config gofer generated for this job.
	MCPConfigFile string
}

// MCPArgsPlaceholder marks, in a continuation's prebuilt argv (job resume), where the
// job's MCP argv fragment goes; Submit replaces it once the job's result dir — and so
// the generated MCP config — exists, or drops it when nothing is bound (MCP-06).
const MCPArgsPlaceholder = "{{gofer:mcp_args}}"

// SpliceMCPArgs replaces the MCPArgsPlaceholder element of argv with fragment (an
// empty fragment just removes it). argv is not modified.
func SpliceMCPArgs(argv, fragment []string) []string {
	out := make([]string, 0, len(argv)+len(fragment))
	for _, a := range argv {
		if a == MCPArgsPlaceholder {
			out = append(out, fragment...)
			continue
		}
		out = append(out, a)
	}
	return out
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
		"{{text}}", v.Text,
		"{{model}}", v.Model,
		"{{from_session}}", v.FromSession,
		"{{mcp_config_file}}", v.MCPConfigFile,
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

// WithModelArgs returns tmpl with modelArgs spliced in right before the first argument
// that holds {{prompt}} (N1 §B), or appended at the end when no argument does (an
// interactive / interactive-resume shape). When the prompt is the VALUE of a preceding
// flag (`--prompt {{prompt}}`, `-p {{prompt}}`), the fragment goes before that flag so
// the pair stays intact — splitting it would hand the CLI "--model" as its prompt.
// Moving in front of a boolean flag (claude's `--verbose`) is harmless. Empty
// modelArgs returns tmpl unchanged, so a job that names no model renders byte-for-byte
// what it always did. The result is a fresh slice; tmpl is never modified.
func WithModelArgs(tmpl, modelArgs []string) []string {
	if len(modelArgs) == 0 {
		return tmpl
	}
	at := len(tmpl)
	for i, a := range tmpl {
		if strings.Contains(a, "{{prompt}}") {
			at = i
			if i > 0 && !strings.HasPrefix(a, "-") && strings.HasPrefix(tmpl[i-1], "-") {
				at = i - 1
			}
			break
		}
	}
	out := make([]string, 0, len(tmpl)+len(modelArgs))
	out = append(out, tmpl[:at]...)
	out = append(out, modelArgs...)
	out = append(out, tmpl[at:]...)
	return out
}
