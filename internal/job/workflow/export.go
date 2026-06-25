package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// secretPlaceholder replaces a value that matched a secret pattern in an exported
// workflow spec (T4.1, SR403): the structure is preserved so the export is still a
// runnable template, but the secret material never leaves the server / lands in a
// file. The importer must fill the real value back in before running.
const secretPlaceholder = "***REDACTED***"

// secretKVPattern matches a "key = value" / "key: value" / "--key value" / "KEY=value"
// assignment whose KEY looks like a credential (token / secret / password / api_key /
// access_key / private_key / auth / bearer / credential). It captures the leading
// "key<sep>" so the redactor can keep it and replace only the trailing value. The value
// is taken up to the next whitespace / quote / line end so a longer prompt is not
// swallowed whole. Case-insensitive (?i); applied to prompt + each cmd arg + cwd.
//
// This is a defence-in-depth heuristic, NOT a guarantee — it strips the COMMON shapes a
// secret leaks in (a flag, an env-style assignment, a JSON/yaml field). A spec author
// who must keep a literal secret out of an export should not put it in the prompt/cmd in
// the first place; the placeholder makes an accidental leak visible.
var secretKVPattern = regexp.MustCompile(
	`(?i)\b([\w.\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w.\-]*\s*[:=]\s*)("?[^"\s]+"?)`,
)

// secretFlagPattern matches a "--flag value" / "-flag value" credential flag (the value is
// a separate argv token, so the KV pattern above does not catch it). It captures the flag
// (and its trailing space) so only the value token is redacted.
var secretFlagPattern = regexp.MustCompile(
	`(?i)(--?[\w\-]*(?:secret|token|password|passwd|api[_\-]?key|access[_\-]?key|private[_\-]?key|auth|bearer|credential)[\w\-]*[=\s]+)(\S+)`,
)

// redactSecretsInString replaces credential-looking assignments / flags in s with the
// placeholder, keeping the key/flag so the export stays a usable template. It returns the
// scrubbed string and whether anything was redacted (so the caller can report it).
func redactSecretsInString(s string) (string, bool) {
	if s == "" {
		return s, false
	}
	redacted := false
	out := secretFlagPattern.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretFlagPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + secretPlaceholder
	})
	out = secretKVPattern.ReplaceAllStringFunc(out, func(m string) string {
		sub := secretKVPattern.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + secretPlaceholder
	})
	return out, redacted
}

// redactStepSecrets scrubs a single step's prompt / cmd args / cwd in place and reports
// whether any value was redacted. It recurses into an inline sub-workflow (a workflow-type
// step) so a secret nested in a child spec is stripped too (T4.1).
func redactStepSecrets(step *StepSpec) bool {
	redacted := false
	if r, hit := redactSecretsInString(step.Prompt); hit {
		step.Prompt = r
		redacted = true
	}
	for i := range step.Cmd {
		if r, hit := redactSecretsInString(step.Cmd[i]); hit {
			step.Cmd[i] = r
			redacted = true
		}
	}
	if r, hit := redactSecretsInString(step.Cwd); hit {
		step.Cwd = r
		redacted = true
	}
	if step.SubWorkflow != nil {
		for i := range step.SubWorkflow.Steps {
			if redactStepSecrets(&step.SubWorkflow.Steps[i]) {
				redacted = true
			}
		}
	}
	return redacted
}

// redactWorkflowSecrets returns a DEEP COPY of spec with every step's prompt/cmd/cwd
// (and any nested sub-workflow) scrubbed of credential-looking material (T4.1, SR403),
// plus whether anything was redacted. The input spec is never mutated (the copy is via
// a JSON round-trip), so the persisted spec_json is unchanged — only the export is
// scrubbed.
func redactWorkflowSecrets(spec Spec) (Spec, bool, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return Spec{}, false, fmt.Errorf("marshal spec for redact: %w", err)
	}
	var cp Spec
	if err := json.Unmarshal(raw, &cp); err != nil {
		return Spec{}, false, fmt.Errorf("clone spec for redact: %w", err)
	}
	redacted := false
	for i := range cp.Steps {
		if redactStepSecrets(&cp.Steps[i]) {
			redacted = true
		}
	}
	return cp, redacted, nil
}

// ExportWorkflow returns a workflow's Spec reconstructed from its persisted
// spec_json, with all credential-looking values stripped (T4.1, E18 + SR403). The
// returned spec is a runnable template: re-submit it (POST /v1/workflows / `workflow
// run`) to reproduce the chain — after filling any redacted secret back in. The bool
// reports whether anything was redacted (the HTTP/CLI layer surfaces it so the operator
// knows a placeholder must be replaced). An unknown id returns ok=false.
func (e *Engine) ExportWorkflow(wfID string) (Spec, bool, bool, error) {
	wf, ok, err := e.meta.GetWorkflow(wfID)
	if err != nil {
		return Spec{}, false, false, err
	}
	if !ok {
		return Spec{}, false, false, nil
	}
	var spec Spec
	if err := json.Unmarshal([]byte(wf.SpecJSON), &spec); err != nil {
		return Spec{}, false, false, fmt.Errorf("decode spec_json of %q: %w", wfID, err)
	}
	// The title travels with the export (spec_json already carries it, but a header-only
	// title set post-submit would be lost otherwise — keep them in sync, header wins).
	if wf.Title != "" {
		spec.Title = wf.Title
	}
	scrubbed, redacted, err := redactWorkflowSecrets(spec)
	if err != nil {
		return Spec{}, false, false, err
	}
	return scrubbed, true, redacted, nil
}
