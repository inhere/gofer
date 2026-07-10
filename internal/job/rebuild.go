package job

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/secret"
)

// ErrRedactedPlaceholder marks a rebuild whose override still carries the redaction
// placeholder — the user viewed a redacted field and re-submitted it without replacing
// the secret. HTTP: 400 (defence-in-depth; unedited fields are never sent, so a
// placeholder here means an edited-but-unreplaced value). ErrUnknownJob (404) is reused
// for an absent source job; "no request" is a 404 too.
var ErrRedactedPlaceholder = errors.New("override still contains the redaction placeholder; replace it")

// RebuildOverrides is the POST /v1/jobs/{id}/rebuild body: ONLY user-edited fields.
// Pointer scalars distinguish "not provided (nil → inherit source)" from "explicit set
// (incl. empty)". env_set adds/overrides env keys (value "" = set to empty string, NOT
// delete); env_unset deletes keys; untouched keys keep the SOURCE value (env plaintext
// never leaves the server). Fields NOT here (request_id/session_id/caller_id/
// source_job_id/workflow_id/role/retry/…) are server-controlled or silently inherited.
type RebuildOverrides struct {
	ProjectKey   *string           `json:"project_key,omitempty"`
	Agent        *string           `json:"agent,omitempty"`
	Runner       *string           `json:"runner,omitempty"`
	Prompt       *string           `json:"prompt,omitempty"`
	SystemPrompt *string           `json:"system_prompt,omitempty"`
	Cmd          *[]string         `json:"cmd,omitempty"`
	AgentArgs    *[]string         `json:"agent_args,omitempty"`
	Cwd          *string           `json:"cwd,omitempty"`
	Title        *string           `json:"title,omitempty"`
	Tags         *[]string         `json:"tags,omitempty"`
	TimeoutSec   *int              `json:"timeout_sec,omitempty"`
	Interactive  *bool             `json:"interactive,omitempty"`
	Cols         *int              `json:"cols,omitempty"`
	Rows         *int              `json:"rows,omitempty"`
	WorkerID     *string           `json:"worker_id,omitempty"`
	WorkerLabels *[]string         `json:"worker_labels,omitempty"`
	PlanID       *string           `json:"plan_id,omitempty"`
	Channel      *string           `json:"channel,omitempty"`
	EnvSet       map[string]string `json:"env_set,omitempty"`
	EnvUnset     []string          `json:"env_unset,omitempty"`
}

// RedactedRequest returns the JobRequest a job was created from, SECRET-STRIPPED for
// safe read (P5, h-aii-xqe1): this is the DEFAULT and ONLY shape GET /request returns
// (there is no verbatim path — the rerun re-submit went server-side to RebuildJob). env
// values → Placeholder (wholesale — env is the main leak surface; a redacted read only
// SHOWS keys, values never leave the server), and prompt/cmd/cwd/system_prompt/agent_args
// run through secret.RedactString (workflow parity; agent_args is the --flag secret
// vector, v0.3). It also CLEARS read-noise: RequestID,
// CallerID, SessionID (a request read must not resurrect an idempotency key or a session
// binding). The second bool reports whether anything was redacted (surfaced via
// X-Gofer-Redacted so a UI marks fields as "originals kept server-side").
//
// Cwd is already the ORIGINAL RELATIVE path (parsed out of request_json, marshalled
// pre-resolution submit.go:110 — NOT reconstructed from JobResult.Cwd's absolute path).
// ok=false when the job is unknown or has no stored request.
func (s *Service) RedactedRequest(id string) (JobRequest, bool, bool, error) {
	src, found := s.Get(id)
	if !found || src.RequestJSON == "" {
		return JobRequest{}, false, false, nil
	}
	var req JobRequest
	if err := json.Unmarshal([]byte(src.RequestJSON), &req); err != nil {
		return JobRequest{}, false, false, fmt.Errorf("decode request_json of %q: %w", id, err)
	}
	redacted := false
	for k := range req.Env { // wholesale value redaction, keep keys (show what exists)
		if req.Env[k] != "" {
			req.Env[k] = secret.Placeholder
			redacted = true
		}
	}
	if r, hit := secret.RedactString(req.Prompt); hit {
		req.Prompt, redacted = r, true
	}
	if r, hit := secret.RedactString(req.SystemPrompt); hit {
		req.SystemPrompt, redacted = r, true
	}
	if r, hit := secret.RedactString(req.Cwd); hit {
		req.Cwd, redacted = r, true
	}
	for i := range req.Cmd {
		if r, hit := secret.RedactString(req.Cmd[i]); hit {
			req.Cmd[i], redacted = r, true
		}
	}
	// v0.3 真漏洞修复：AgentArgs 是 cli-agent 追加到 argv 的 CLI flags（model.go:14-16），
	// 是 `--token=x`/`--api-key=x` 类 secret 的首要向量——必须同 Cmd 一样脱敏，否则明文外泄。
	for i := range req.AgentArgs {
		if r, hit := secret.RedactString(req.AgentArgs[i]); hit {
			req.AgentArgs[i], redacted = r, true
		}
	}
	req.RequestID, req.CallerID, req.SessionID = "", "", ""
	return req, true, redacted, nil
}

// RebuildJob starts a NEW job from the SOURCE job's persisted request_json (P5, D1). It
// takes that request as the base, applies the caller's edited overrides + env merge, then
// STAMPS the lineage/identity fields server-side and Submits. The base env (real values,
// from request_json) is merged in memory and is never serialized back out of gofer: only
// env_set/env_unset touch it. Default async (like ResumeJob); the caller watches the new
// job. callerID/clientIP are stamped by the HTTP entry (anti-spoof), mirroring Submit.
//
// SECURITY (accepted, P5 v0.3 §安全声明): overrides may edit the EXECUTION BODY
// (prompt/cmd/agent/agent_args/system_prompt) while the source env is inherited. A caller
// holding a token can therefore make the new job print the inherited env to stdout and
// read it back via the job log endpoints. Redacting GET /request stops the ACCIDENTAL
// exposure (env in UI prefill / screenshots / API responses), NOT a malicious caller —
// gofer is single-trust-tier: a token means "may run arbitrary commands". Audit relies on
// the lineage columns: the new job records source_job_id plus the initiator's caller_id.
func (s *Service) RebuildJob(jobID string, ov RebuildOverrides, callerID, clientIP string) (JobResult, error) {
	src, ok := s.Get(jobID)
	if !ok {
		return JobResult{}, fmt.Errorf("%w: %q", ErrUnknownJob, jobID)
	}
	if src.RequestJSON == "" {
		return JobResult{}, fmt.Errorf("%w: %q has no stored request", ErrUnknownJob, jobID)
	}
	var base JobRequest
	if err := json.Unmarshal([]byte(src.RequestJSON), &base); err != nil {
		return JobResult{}, fmt.Errorf("decode request_json of %q: %w", jobID, err)
	}
	// Defence-in-depth: reject any edited string field still carrying the placeholder.
	if err := rejectPlaceholders(ov); err != nil {
		return JobResult{}, err
	}
	applyOverrides(&base, ov) // pointer scalars + slices; env_set/env_unset merge

	// Server-controlled fields — a rebuild is a FRESH, faithful re-submit:
	base.RequestID = ""      // else the new submit dedupes onto the source (C5)
	base.SessionID = ""      // fresh job, NOT a resume — don't rebind the source session
	base.CallerID = callerID // re-stamped from auth (anti-spoof, mirrors handleCreateJob)
	base.Client = clientIP   // new submission origin
	base.SourceJobID = jobID // lineage: server-stamped from the URL (unforgeable, json:"-")
	// PlanID inherited from source (base already carries it); ov.PlanID may override.
	return s.Submit(base)
}

// applyOverrides mutates base with only the provided (non-nil) fields, then merges env.
func applyOverrides(base *JobRequest, ov RebuildOverrides) {
	if ov.ProjectKey != nil {
		base.ProjectKey = *ov.ProjectKey
	}
	if ov.Agent != nil {
		base.Agent = *ov.Agent
	}
	if ov.Runner != nil {
		base.Runner = *ov.Runner
	}
	if ov.Prompt != nil {
		base.Prompt = *ov.Prompt
	}
	if ov.SystemPrompt != nil {
		base.SystemPrompt = *ov.SystemPrompt
	}
	if ov.Cmd != nil {
		base.Cmd = *ov.Cmd
	}
	if ov.AgentArgs != nil {
		base.AgentArgs = *ov.AgentArgs
	}
	if ov.Cwd != nil {
		base.Cwd = *ov.Cwd
	}
	if ov.Title != nil {
		base.Title = *ov.Title
	}
	if ov.Tags != nil {
		base.Tags = *ov.Tags
	}
	if ov.TimeoutSec != nil {
		base.TimeoutSec = *ov.TimeoutSec
	}
	if ov.Interactive != nil {
		base.Interactive = *ov.Interactive
	}
	if ov.Cols != nil {
		base.Cols = *ov.Cols
	}
	if ov.Rows != nil {
		base.Rows = *ov.Rows
	}
	if ov.WorkerID != nil {
		base.WorkerID = *ov.WorkerID
	}
	if ov.WorkerLabels != nil {
		base.WorkerLabels = *ov.WorkerLabels
	}
	if ov.PlanID != nil {
		base.PlanID = *ov.PlanID
	}
	if ov.Channel != nil {
		base.Channel = *ov.Channel
	}
	// env merge: base env = source real values; set adds/overrides, unset deletes.
	if len(ov.EnvSet) > 0 && base.Env == nil {
		base.Env = map[string]string{}
	}
	for k, v := range ov.EnvSet {
		base.Env[k] = v // "" = set to empty string (NOT delete; delete via EnvUnset)
	}
	// v0.3 同 key 语义（D2）：set-loop 在前、unset-loop 在后 → 一个 key 同时出现在 env_set 与
	// env_unset 时 **env_unset 优先**（先赋值、再删除，最终不存在）。前端须避免同 key 同时进两者。
	for _, k := range ov.EnvUnset {
		delete(base.Env, k)
	}
}

// rejectPlaceholders 400s when any edited string override still carries the placeholder
// (the user must replace a redacted value, not re-submit it). Unedited fields are nil and
// inherit the SOURCE real value, so they never hit this.
func rejectPlaceholders(ov RebuildOverrides) error {
	has := func(s string) bool { return strings.Contains(s, secret.Placeholder) }
	if ov.Prompt != nil && has(*ov.Prompt) {
		return ErrRedactedPlaceholder
	}
	if ov.SystemPrompt != nil && has(*ov.SystemPrompt) {
		return ErrRedactedPlaceholder
	}
	if ov.Cwd != nil && has(*ov.Cwd) {
		return ErrRedactedPlaceholder
	}
	if ov.Cmd != nil {
		for _, a := range *ov.Cmd {
			if has(a) {
				return ErrRedactedPlaceholder
			}
		}
	}
	if ov.AgentArgs != nil { // v0.3: agent_args 同 Cmd 是 secret 向量，须一并兜底
		for _, a := range *ov.AgentArgs {
			if has(a) {
				return ErrRedactedPlaceholder
			}
		}
	}
	for _, v := range ov.EnvSet {
		if has(v) {
			return ErrRedactedPlaceholder
		}
	}
	return nil
}
