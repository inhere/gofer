package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// This file is the WEB-04③ V1.1 write surface: agents and a whitelisted slice of the
// server block, editable from the console (design §一) so that declaring or tuning an
// agent no longer needs an RDP session plus a hand edit of config.yaml.
//
// Every write has the same shape:
//
//	can_admin gate → classify the WHOLE body against config.FieldPolicyFor →
//	ConfigWriter.Update(clone → apply → validate the whole config → save → reload)
//
// Only the last step touches disk, and it hands its mutation a CLONE, so a rejected
// body leaves disk and memory on the old generation (core.Update's fail-safe) and a
// body is never partially applied (classification runs before the transaction).
//
// The same classify → apply → validate chain backs POST /v1/config/validate, which
// runs it on a clone and renders the candidate instead of saving it. That is what
// makes "校验通过" and "保存" the same decision rather than two implementations.

// maxConfigBodyBytes bounds a config write body. It is generous for a webhook list
// and small enough that a malformed/streamed body cannot be used to balloon memory.
const maxConfigBodyBytes = 1 << 20

// configWriteResp is the success shape of a config write. `restart_required` is the
// section's restart-only surface (design §一.3): every field the write API cannot
// carry, so the console can say what still needs a file edit + restart. It is the
// same list the dry run returns, so the impact note does not change between
// "validate" and "save".
type configWriteResp struct {
	Status string `json:"status"`
	// Section is "agents" or "server" (the audit vocabulary, same as the event).
	Section string `json:"section"`
	// Key is the agent key for an agent write, empty for the server block.
	Key string `json:"key,omitempty"`
	// Created reports whether this write DECLARED the agent (as opposed to editing an
	// operator definition). An override of a runtime-injected template counts as
	// created: nothing was written to the file before.
	Created bool `json:"created"`
	// Reloaded is always true on success: the write transaction hot-swaps the new
	// generation, so the change is live in the running process.
	Reloaded        bool     `json:"reloaded"`
	Fields          []string `json:"fields"`
	RestartRequired []string `json:"restart_required"`
}

// configAgentDeleteResp is the delete answer. FellBackToBuiltin is always emitted
// (no omitempty): "the built-in definition comes back" is the fact a caller must not
// be left guessing about, and `false` is a real answer.
type configAgentDeleteResp struct {
	Status            string `json:"status"`
	Key               string `json:"key"`
	FellBackToBuiltin bool   `json:"fell_back_to_builtin"`
	Reloaded          bool   `json:"reloaded"`
}

// configValidateReq is the dry-run body: the same section/key/value vocabulary the
// PUT endpoints take, so a console sends ONE payload to both and cannot end up
// validating something other than what it saves.
type configValidateReq struct {
	Section string          `json:"section"`
	Key     string          `json:"key,omitempty"`
	Value   json.RawMessage `json:"value"`
}

// configValidateResult is the dry-run answer, used for BOTH outcomes (400 carries
// ok=false) so a console parses one shape. ErrorFields names the inputs to
// highlight; Applied names what the body carried; RestartRequired is the section's
// restart-only surface; Preview is the candidate block, rendered by the SAME
// encoder config.Save uses (never locally assembled by the client).
type configValidateResult struct {
	OK              bool     `json:"ok"`
	Detail          string   `json:"detail,omitempty"`
	Errors          []string `json:"errors"`
	ErrorFields     []string `json:"error_fields,omitempty"`
	Applied         []string `json:"applied"`
	RestartRequired []string `json:"restart_required"`
	Preview         string   `json:"preview,omitempty"`
}

// configWriteError carries an HTTP status out of the Update closure (core.Update
// returns whatever the mutation returns, and an error raised in there must NOT be
// reported as a 500). It also carries the field paths a console can highlight.
type configWriteError struct {
	status int
	msg    string
	detail string
	fields []string
}

func (e *configWriteError) Error() string { return e.detail }

// configWriteErrorBody is the failure shape of a config write: the project routes'
// uniform {error, detail} pair plus the field paths (omitted when the failure is not
// tied to a particular input).
type configWriteErrorBody struct {
	Error       string   `json:"error"`
	Detail      string   `json:"detail,omitempty"`
	ErrorFields []string `json:"error_fields,omitempty"`
}

// configBodyField is one classified field of a write body.
type configBodyField struct {
	name string
	path string
	raw  json.RawMessage
}

// configWriteCaller runs the can_admin gate every config write route shares (design
// §一.1) — the same capability bit and the same wording the project routes use, so
// "no permission" is one condition across the product.
func (s *Server) configWriteCaller(c *rux.Context) (string, bool) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return "", false
	}
	return caller, true
}

// configWriter returns the write transaction, or answers 503 when this server was
// built without one (mcp, most tests) — the same degradation /v1/xfer uses.
func (s *Server) configWriter(c *rux.Context) (ConfigWriter, bool) {
	if s.core == nil {
		writeError(c, http.StatusServiceUnavailable, "config write unavailable",
			"this server has no config write transaction wired; start it with `gofer serve`")
		return nil, false
	}
	return s.core, true
}

// configLiveConfig returns the config generation to validate a dry run against, or
// answers 503 when there is none.
func (s *Server) configLiveConfig(c *rux.Context) (*config.Config, bool) {
	if s.projects == nil {
		writeError(c, http.StatusServiceUnavailable, "config write unavailable", "this server has no project registry wired")
		return nil, false
	}
	cfg := s.projects.Config()
	if cfg == nil {
		writeError(c, http.StatusServiceUnavailable, "config write unavailable", "no config loaded")
		return nil, false
	}
	return cfg, true
}

// readConfigBody reads a write body, bounded.
func readConfigBody(c *rux.Context) ([]byte, error) {
	if c.Req == nil || c.Req.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(c.Req.Body, maxConfigBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxConfigBodyBytes {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxConfigBodyBytes)
	}
	return raw, nil
}

// configPath renders the dotted policy path of a body field — the same string the
// policy table is keyed by and the string a caller sees in an error, so "what I sent"
// and "what it was refused as" are never two different names.
func configPath(section, key, name string) string {
	if section == "server" {
		return "server." + name
	}
	return "agents." + key + "." + name
}

// parseConfigBody decodes a flat JSON object and classifies EVERY field against the
// policy table, returning them in a stable (sorted) order so responses and audit
// events read the same on every run.
//
// Order of the three refusals matters: a secret LITERAL is named as such (and pointed
// at the `*_env` alternative) before the generic field rules, because pasting a token
// into the console is a different mistake from sending an unknown key. Everything is
// classified before anything is applied — a rejected field cannot leave a half-applied
// body behind.
func parseConfigBody(raw []byte, section, key string) ([]configBodyField, error) {
	body := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, &configWriteError{
				status: http.StatusBadRequest,
				msg:    "invalid request body",
				detail: "a JSON object of config fields is required: " + err.Error(),
			}
		}
	}
	names := sortedJSONKeys(body)

	out := make([]configBodyField, 0, len(names))
	for _, name := range names {
		path := configPath(section, key, name)
		if config.IsSecretFieldName(name) {
			return nil, &configWriteError{
				status: http.StatusBadRequest,
				msg:    "secret value not accepted",
				detail: fmt.Sprintf("secret value not accepted: %s; edit the environment-variable name (*_env) instead", path),
			}
		}
		fp, known := config.FieldPolicyFor(path)
		if !known {
			return nil, &configWriteError{status: http.StatusBadRequest, msg: "unknown field", detail: "unknown field: " + path}
		}
		if !fp.Editable {
			return nil, &configWriteError{status: http.StatusBadRequest, msg: "field not editable", detail: "field not editable: " + path}
		}
		out = append(out, configBodyField{name: name, path: path, raw: body[name]})
	}
	return out, nil
}

// sortedJSONKeys returns the keys of a decoded object in a stable order, so a
// response, an audit event and a patch apply the same fields in the same order on
// every run.
func sortedJSONKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// fieldValue decodes one body value into T. A JSON `null` (or an absent value) clears
// the field to T's zero value, which is how a console UNSETS a pointer-backed setting
// (`stall_timeout_sec: null` = inherit) — the same "present-empty means empty"
// distinction the project write form makes with its pointer fields.
func fieldValue[T any](f configBodyField) (T, error) {
	var v T
	if len(f.raw) == 0 || string(f.raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(f.raw, &v); err != nil {
		return v, &configWriteError{
			status: http.StatusBadRequest,
			msg:    "invalid field value",
			detail: fmt.Sprintf("invalid value for %s: %v", f.path, err),
			fields: []string{f.path},
		}
	}
	return v, nil
}

// validateCandidate runs the validation a freshly loaded config file gets: the
// config package's structural checks plus the agent-mode rules the agent package
// owns (shared by server and worker). Anything weaker would let this API persist a
// config the next reload refuses — a process that cannot come back from its own edit.
func validateCandidate(next *config.Config) error {
	if err := config.Validate(next); err != nil {
		return err
	}
	return agent.ValidateConfig(next)
}

// writeConfigError answers a failed write with the status the failure carries.
func (s *Server) writeConfigError(c *rux.Context, err error) {
	var we *configWriteError
	if errors.As(err, &we) {
		status := we.status
		if status == 0 {
			status = http.StatusInternalServerError
		}
		c.JSON(status, configWriteErrorBody{Error: we.msg, Detail: we.detail, ErrorFields: we.fields})
		return
	}
	c.JSON(http.StatusInternalServerError, configWriteErrorBody{Error: "config write failed", Detail: err.Error()})
}

// writeValidateFailure answers a failed dry run in the dry-run shape (so a console
// parses one response type for both outcomes).
func (s *Server) writeValidateFailure(c *rux.Context, err error) {
	var we *configWriteError
	res := configValidateResult{
		OK:              false,
		Errors:          []string{},
		Applied:         []string{},
		RestartRequired: []string{},
	}
	if errors.As(err, &we) {
		res.Detail = we.detail
		res.ErrorFields = we.fields
		res.Errors = []string{we.detail}
		status := we.status
		if status == 0 || status == http.StatusInternalServerError {
			status = http.StatusBadRequest
		}
		c.JSON(status, res)
		return
	}
	res.Detail = err.Error()
	res.Errors = []string{err.Error()}
	c.JSON(http.StatusBadRequest, res)
}

// recordConfigUpdate appends the audit event of one successful write (design §一.5):
// WHO changed WHAT. Field NAMES only, never values — a config value is a filesystem
// path or a host name, and the event log is not the place to copy it (SR403).
//
// Best-effort: an audit write can never fail a write that already committed.
func (s *Server) recordConfigUpdate(caller, section, key string, fields []string) {
	slog.Info("config updated", "caller_id", caller, "section", section, "config_key", key, "fields", fields)
	if s.jobs == nil {
		return
	}
	s.jobs.RecordScopedEvent(job.ConfigEventScope, job.EventConfigUpdated, "", map[string]any{
		"section": section,
		"key":     key,
		"by":      caller,
		"fields":  fields,
	})
}

// restartOnlyPaths returns a section's restart-only field paths, sorted: the fields
// a write body cannot carry, i.e. what the console must still show as "改这一项要改文件
// 并重启". For agents it is empty — an agent definition hot-reloads.
func restartOnlyPaths(section string) []string {
	policies := config.SectionPolicies(section)
	out := make([]string, 0, len(policies))
	for name, fp := range policies {
		if fp.RestartRequired {
			out = append(out, configPath(section, "*", name))
		}
	}
	slices.Sort(out)
	return out
}

// validateConfigKey rejects a key that cannot be an agent key. An empty key would
// otherwise resolve to the `agents..command` policy path of nothing, and whitespace
// makes a key nobody can type again.
func validateConfigKey(key string) error {
	if key == "" {
		return fmt.Errorf("agent key is required")
	}
	if strings.ContainsAny(key, " \t\r\n/\\:*?\"<>|") {
		return fmt.Errorf("agent key %q may not contain whitespace, separators or path characters", key)
	}
	return nil
}

// hasTemplatePrompt mirrors agent.hasPrompt (unexported there): a `{{prompt}}`
// placeholder in an argv template. It is used only to name the input behind a
// validation failure (see agentErrorFields).
func hasTemplatePrompt(args []string) bool {
	for _, a := range args {
		if strings.Contains(a, "{{prompt}}") {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// agents
// ---------------------------------------------------------------------------

// handlePutConfigAgent creates or replaces an agent definition (design §一.1).
func (s *Server) handlePutConfigAgent(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if err := validateConfigKey(key); err != nil {
		writeError(c, http.StatusBadRequest, "invalid agent key", err.Error())
		return
	}
	raw, err := readConfigBody(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	fields, err := parseConfigBody(raw, "agents", key)
	if err != nil {
		s.writeConfigError(c, err)
		return
	}
	// A body with no editable field says nothing: it would create an empty
	// definition (or, on an existing agent, silently blank every editable field).
	// The server PUT refuses the same shape — "remove the agent" is DELETE.
	if len(fields) == 0 {
		writeError(c, http.StatusBadRequest, "empty request body", "name at least one agent field")
		return
	}

	var applied []string
	created := false
	err = cw.Update(func(next *config.Config) error {
		if _, exists := next.Agents[key]; !exists || next.IsInjectedAgent(key) {
			created = true // nothing of the operator's was there before
		}
		applied, err = applyAgentWrite(next, key, fields)
		if err != nil {
			return err
		}
		if verr := validateCandidate(next); verr != nil {
			return &configWriteError{
				status: http.StatusBadRequest,
				msg:    "invalid config",
				detail: verr.Error(),
				fields: agentErrorFields(key, next.Agents[key]),
			}
		}
		return nil
	})
	if err != nil {
		s.writeConfigError(c, err)
		return
	}

	s.recordConfigUpdate(caller, "agents", key, applied)
	c.JSON(http.StatusOK, configWriteResp{
		Status:          "ok",
		Section:         "agents",
		Key:             key,
		Created:         created,
		Reloaded:        true,
		Fields:          applied,
		RestartRequired: restartOnlyPaths("agents"),
	})
}

// applyAgentWrite replaces the EDITABLE field set of next.Agents[key] with what the
// body carries and returns the applied field names.
//
// Replace, not merge: an editable field the body omits is CLEARED. That is what makes
// "取消最后一个勾选" expressible at all (the same reasoning as the project form's
// bd h-aii-3scy comment) — a console must therefore always send the complete editable
// set, which is exactly what GET /v1/config's view + the policy table let it do.
//
// Fields OUTSIDE the editable set are carried over verbatim: the API cannot express
// them, so a write must not destroy them. `env` may hold a plaintext secret, `detect`
// is the template's probe argv, `mcp_server_name`/`allow_raw_cmd` are operator
// plumbing — clearing them would be the very "the form wiped what it never knew
// about" bug, and nothing in the console could put them back.
//
// An agent that is currently a runtime-injected TEMPLATE starts from that template's
// definition (so overriding `claude-acp` keeps its npx detect block), and the
// injection mark is dropped: the entry becomes operator configuration and must
// survive the save, which strips injected keys (config.writer).
func applyAgentWrite(next *config.Config, key string, fields []configBodyField) ([]string, error) {
	cand := next.Agents[key]
	// REPLACE semantics for the EDITABLE SET: an editable field the body omits is
	// cleared, so "取消最后一个勾选" is expressible at all (a form always sends the
	// complete set — the policy table tells it which fields that is).
	carried := make(map[string]bool, len(fields))
	for _, f := range fields {
		carried[f.name] = true
	}
	for _, name := range config.EditableAgentFields() {
		if carried[name] {
			continue
		}
		if err := applyAgentField(&cand, clearedAgentField(key, name)); err != nil {
			return nil, err
		}
	}
	applied := make([]string, 0, len(fields))
	for _, f := range fields {
		if err := applyAgentField(&cand, f); err != nil {
			return nil, err
		}
		applied = append(applied, f.name)
	}
	if next.Agents == nil {
		next.Agents = make(map[string]config.AgentConfig, 1)
	}
	next.Agents[key] = cand
	next.UnmarkInjectedAgent(key)
	return applied, nil
}

// clearedAgentField is the clearing write of one editable agent field, expressed
// through the SAME decoder a real value goes through: `null` decodes to the zero
// value, so a nil slice stays distinguishable from an empty one (AGT-02's
// interactive_args) and no field needs a second zeroing rule.
func clearedAgentField(key, name string) configBodyField {
	return configBodyField{
		name: name,
		path: configPath("agents", key, name),
		raw:  json.RawMessage("null"),
	}
}

// applyAgentField writes one classified body field onto the candidate definition.
// The switch is the OTHER half of the policy table: a field the table calls editable
// but this switch does not handle is a bug in whichever side changed last, and it
// fails loudly rather than being dropped.
func applyAgentField(ac *config.AgentConfig, f configBodyField) error {
	unhandled := func() error {
		return &configWriteError{status: http.StatusInternalServerError, msg: "unhandled editable field", detail: "unhandled editable field: " + f.path}
	}
	switch f.name {
	case "type":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.Type = v
	case "command":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.Command = v
	case "args", "read_only_args", "session_inject", "session_resume",
		"session_resume_interactive", "system_inject", "transient_error_patterns",
		"fallback_agents", "ndjson_keep":
		v, err := fieldValue[[]string](f)
		if err != nil {
			return err
		}
		switch f.name {
		case "args":
			ac.Args = v
		case "read_only_args":
			ac.ReadOnlyArgs = v
		case "session_inject":
			ac.SessionInject = v
		case "session_resume":
			ac.SessionResume = v
		case "session_resume_interactive":
			ac.SessionResumeInteractive = v
		case "system_inject":
			ac.SystemInject = v
		case "transient_error_patterns":
			ac.TransientErrorPatterns = v
		case "fallback_agents":
			ac.FallbackAgents = v
		case "ndjson_keep":
			ac.NDJSONKeep = v
		}
	case "interactive_args":
		v, err := fieldValue[[]string](f)
		if err != nil {
			return err
		}
		// nil = batch-only, [] = interactive with no extra argv (AGT-02). The
		// ArgList type exists precisely to keep that distinction through YAML.
		ac.InteractiveArgs = config.ArgList(v)
	case "interactive":
		v, err := fieldValue[bool](f)
		if err != nil {
			return err
		}
		ac.Interactive = v
	case "session_capture":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.SessionCapture = v
	case "output_format":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.OutputFormat = v
	case "ndjson_events_to":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.NDJSONEventsTo = v
	case "ndjson_stdout":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.NDJSONStdout = v
	case "ndjson_stdout_path":
		v, err := fieldValue[string](f)
		if err != nil {
			return err
		}
		ac.NDJSONStdoutPath = v
	case "ndjson_raw":
		v, err := fieldValue[bool](f)
		if err != nil {
			return err
		}
		ac.NDJSONRaw = v
	case "ndjson_fields":
		v, err := fieldValue[map[string][]string](f)
		if err != nil {
			return err
		}
		ac.NDJSONFields = v
	case "max_concurrent":
		v, err := fieldValue[int](f)
		if err != nil {
			return err
		}
		ac.MaxConcurrent = v
	case "stall_timeout_sec":
		v, err := fieldValue[*int](f)
		if err != nil {
			return err
		}
		ac.StallTimeoutSec = v
	case "retry":
		v, err := fieldValue[*config.RetryPolicy](f)
		if err != nil {
			return err
		}
		ac.Retry = v
	case "skills":
		// JOB-10 §一.3: the agent's own binding list. `[]` and `null` both mean "this
		// agent adds nothing" (the levels UNION, so there is no per-agent off switch).
		v, err := fieldValue[[]string](f)
		if err != nil {
			return err
		}
		ac.Skills = v
	case "acp":
		return patchAgentACP(ac, f)
	default:
		return unhandled()
	}
	return nil
}

// patchAgentACP applies an agent body's `acp` sub-block.
//
// `acp` is the ONE compound editable field that PATCHES rather than replaces, and the
// reason is concrete: the view cannot echo `acp.mcp_servers[].env` (an env map may
// hold secrets, and no read path in this API exposes env VALUES), so a wholesale
// replace would silently destroy an acp-agent's MCP child environment the first time
// the console saved an unrelated field. Every other compound block (`retry`,
// `ndjson_fields`) is fully readable, so it replaces whole like the rest of the set.
//
// A JSON `null` still clears the block — that is the ordinary "an omitted editable
// field is cleared" answer, and an agent with no acp block sends null (or omits it).
func patchAgentACP(ac *config.AgentConfig, f configBodyField) error {
	if len(f.raw) == 0 || string(f.raw) == "null" {
		ac.ACP = nil
		return nil
	}
	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(f.raw, &members); err != nil {
		return &configWriteError{
			status: http.StatusBadRequest,
			msg:    "invalid field value",
			detail: fmt.Sprintf("invalid value for %s: %v", f.path, err),
			fields: []string{f.path},
		}
	}
	if ac.ACP == nil {
		ac.ACP = &config.ACPConfig{}
	}
	for _, name := range sortedJSONKeys(members) {
		sub := configBodyField{name: name, path: f.path + "." + name, raw: members[name]}
		switch name {
		case "modes":
			v, err := fieldValue[map[string]string](sub)
			if err != nil {
				return err
			}
			ac.ACP.Modes = v
		case "permission_policy":
			v, err := fieldValue[string](sub)
			if err != nil {
				return err
			}
			ac.ACP.PermissionPolicy = v
		case "load_session":
			v, err := fieldValue[*bool](sub)
			if err != nil {
				return err
			}
			ac.ACP.LoadSession = v
		case "log_thoughts":
			v, err := fieldValue[*bool](sub)
			if err != nil {
				return err
			}
			ac.ACP.LogThoughts = v
		case "mcp_servers":
			v, err := fieldValue[[]config.ACPMCPServerConfig](sub)
			if err != nil {
				return err
			}
			ac.ACP.MCPServers = v
		default:
			return &configWriteError{
				status: http.StatusBadRequest,
				msg:    "unknown field",
				detail: "unknown field: " + f.path + "." + name,
			}
		}
	}
	return nil
}

// agentErrorFields maps a validation failure onto the INPUTS a console highlights.
// These predicates deliberately mirror config.validate / agent.ValidateConfig: the
// verdict comes from those validators (validateCandidate), and this only answers
// "which box turned red". A rule missing here costs a highlight, never correctness.
func agentErrorFields(key string, ac config.AgentConfig) []string {
	p := func(field string) string { return "agents." + key + "." + field }
	var out []string
	if ac.Interactive && hasTemplatePrompt(ac.Args) {
		out = append(out, p("interactive"))
	}
	if hasTemplatePrompt(ac.InteractiveArgs) {
		out = append(out, p("interactive_args"))
	}
	if ac.Type == agent.TypeExec && ac.InteractiveArgs != nil {
		out = append(out, p("interactive_args"))
	}
	if ac.Type == agent.TypeACPAgent && hasTemplatePrompt(ac.Args) {
		out = append(out, p("args"))
	}
	switch ac.OutputFormat {
	case "", config.OutputFormatText, config.OutputFormatNDJSON:
	default:
		out = append(out, p("output_format"))
	}
	switch ac.NDJSONEventsTo {
	case "", config.NDJSONEventsStderr, config.NDJSONEventsStdout:
	default:
		out = append(out, p("ndjson_events_to"))
	}
	switch ac.NDJSONStdout {
	case "", config.NDJSONStdoutFinalText, config.NDJSONStdoutAssistantText, config.NDJSONStdoutEvents:
	default:
		out = append(out, p("ndjson_stdout"))
	}
	if ac.ACP != nil {
		switch ac.ACP.PermissionPolicy {
		case "", config.ApprovalAutoAllow, config.ApprovalAsk, config.ApprovalStrict:
		default:
			out = append(out, p("acp"))
		}
	}
	if ac.MaxConcurrent < 0 {
		out = append(out, p("max_concurrent"))
	}
	if ac.StallTimeoutSec != nil && *ac.StallTimeoutSec < 0 {
		out = append(out, p("stall_timeout_sec"))
	}
	return out
}

// handleDeleteConfigAgent deletes an agent definition (design §一.2). Deleting an
// override of a BUILT-IN key is not "removing a capability": the built-in definition
// is materialized again on the reload that rides this very transaction. The response
// says which of the two happened (agent.HasBuiltinTemplate).
func (s *Server) handleDeleteConfigAgent(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	key := strings.TrimSpace(c.Param("key"))
	if err := validateConfigKey(key); err != nil {
		writeError(c, http.StatusBadRequest, "invalid agent key", err.Error())
		return
	}
	err := cw.Update(func(next *config.Config) error {
		if _, exists := next.Agents[key]; !exists {
			return &configWriteError{
				status: http.StatusNotFound,
				msg:    "unknown agent",
				detail: fmt.Sprintf("agent %q is not declared", key),
			}
		}
		delete(next.Agents, key)
		return nil
	})
	if err != nil {
		s.writeConfigError(c, err)
		return
	}

	s.recordConfigUpdate(caller, "agents", key, []string{"*"})
	c.JSON(http.StatusOK, configAgentDeleteResp{
		Status:            "ok",
		Key:               key,
		FellBackToBuiltin: agent.HasBuiltinTemplate(key),
		Reloaded:          true,
	})
}

// ---------------------------------------------------------------------------
// server
// ---------------------------------------------------------------------------

// handlePutConfigServer applies the whitelisted server fields the body carries
// (design §一.3) — a PARTIAL update, unlike the agent PUT: every field is
// independent, and replacing the whole server block would force a caller to restate
// addr/token/callers it is not allowed to touch.
func (s *Server) handlePutConfigServer(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	raw, err := readConfigBody(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	fields, err := parseConfigBody(raw, "server", "")
	if err != nil {
		s.writeConfigError(c, err)
		return
	}
	if len(fields) == 0 {
		writeError(c, http.StatusBadRequest, "empty request body", "name at least one editable server field")
		return
	}

	var applied []string
	err = cw.Update(func(next *config.Config) error {
		for _, f := range fields {
			if aerr := applyServerField(&next.Server, f); aerr != nil {
				return aerr
			}
			applied = append(applied, f.name)
		}
		if verr := validateCandidate(next); verr != nil {
			return &configWriteError{
				status: http.StatusBadRequest,
				msg:    "invalid config",
				detail: verr.Error(),
				fields: serverErrorFields(applied, verr.Error()),
			}
		}
		return nil
	})
	if err != nil {
		s.writeConfigError(c, err)
		return
	}

	s.recordConfigUpdate(caller, "server", "", applied)
	c.JSON(http.StatusOK, configWriteResp{
		Status:          "ok",
		Section:         "server",
		Reloaded:        true,
		Fields:          applied,
		RestartRequired: restartOnlyPaths("server"),
	})
}

// applyServerField writes one classified body field onto the server block. The
// compound blocks are replaced WHOLE: a partial block would silently drop the members
// it does not mention (a webhook list, a backoff table), and the console sends the
// block it read.
func applyServerField(sc *config.ServerConfig, f configBodyField) error {
	switch f.name {
	case "max_job_timeout_sec":
		v, err := fieldValue[int](f)
		if err != nil {
			return err
		}
		sc.MaxJobTimeoutSec = v
	case "auto_resume_max":
		v, err := fieldValue[*int](f)
		if err != nil {
			return err
		}
		sc.AutoResumeMax = v
	case "stall_timeout_sec":
		v, err := fieldValue[*int](f)
		if err != nil {
			return err
		}
		sc.StallTimeoutSec = v
	case "runner_probe":
		// Decoded through the VIEW type, not config.RunnerProbeConfig: the wire body
		// speaks snake_case (`interval_seconds`) while the config struct carries only
		// yaml tags, and encoding/json cannot match an underscored key to a Go field
		// name — decoding straight into the config type silently produced a ZERO block
		// (S2 fix, 2026-09-23; the same applied to skill_limits / comment_trigger /
		// agent_health). The view type is the single place the JSON spelling lives.
		v, err := fieldValue[runnerProbeView](f)
		if err != nil {
			return err
		}
		sc.RunnerProbe = config.RunnerProbeConfig{
			IntervalSeconds: v.IntervalSeconds,
			TimeoutSeconds:  v.TimeoutSeconds,
		}
	case "retry":
		v, err := fieldValue[*config.RetryPolicy](f)
		if err != nil {
			return err
		}
		sc.Retry = v
	case "notification":
		// S4 (2026-09-23): a PATCH, not a whole-block replace — see patchNotification
		// for the two members a reader cannot round-trip.
		v, err := fieldValue[*notificationPatch](f)
		if err != nil {
			return err
		}
		patchNotification(sc, v)
	case "skills":
		// JOB-10 §一.3: the deployment's default bindings. A hot edit applies to the
		// NEXT dispatch (EffectiveSkills reads the live config), not to jobs already
		// submitted.
		v, err := fieldValue[[]string](f)
		if err != nil {
			return err
		}
		sc.Skills = v
	case "skill_limits":
		// JOB-10 §一.2: the import-size guards, read by the store at import time.
		// Decoded through the view type — see runner_probe for why.
		v, err := fieldValue[skillLimitsView](f)
		if err != nil {
			return err
		}
		sc.SkillLimits = config.SkillLimitsConfig{
			MaxFileBytes:  v.MaxFileBytes,
			MaxTotalBytes: v.MaxTotalBytes,
		}
	case "comment_trigger":
		// MCP-05 阶段 A: the @-mention dispatch throttle, read per comment. The two
		// members stay pointers (null = the built-in default, 0 = the gate is off);
		// decoded through the view type — see runner_probe for why.
		v, err := fieldValue[commentTriggerView](f)
		if err != nil {
			return err
		}
		sc.CommentTrigger = config.CommentTriggerConfig{
			MinIntervalSec: v.MinIntervalSec,
			MaxPerScope:    v.MaxPerScope,
		}
	case "dir_lock":
		// JOB-11: read once per submit (resolveDirExclusive), so this applies to the
		// NEXT job. A pointer: null = unset = the documented default (on), which is a
		// different decision from an explicit `false`.
		v, err := fieldValue[*bool](f)
		if err != nil {
			return err
		}
		sc.DirLock = v
	case "agent_health":
		// SUP-01 P3: read per health read (EffectiveAgentHealth). null clears the
		// block back to the documented defaults (1h / 3 / 1) rather than to zeros, so
		// the pointer is preserved; decoded through the view type — see runner_probe
		// for why a straight config decode came out empty.
		v, err := fieldValue[*serverAgentHealthView](f)
		if err != nil {
			return err
		}
		if v == nil {
			sc.AgentHealth = nil
			break
		}
		sc.AgentHealth = &config.AgentHealthConfig{
			WindowSec:      v.WindowSec,
			DegradedAfter:  v.DegradedAfter,
			RecoverAfterOK: v.RecoverAfterOK,
		}
	default:
		return &configWriteError{status: http.StatusInternalServerError, msg: "unhandled editable field", detail: "unhandled editable field: " + f.path}
	}
	return nil
}

// notificationPatch is the request body of a `notification` write (S4, 2026-09-23).
// Unlike the other compound server blocks (which replace whole because every member is
// readable), this one is a PATCH: an omitted member keeps the configured value.
// See patchNotification for why that is not merely convenient.
type notificationPatch struct {
	Enabled     *bool           `json:"enabled"`
	AllowHosts  []string        `json:"allow_hosts"`
	AllowHTTP   *bool           `json:"allow_http"`
	MaxAttempts *int            `json:"max_attempts"`
	Webhooks    *[]webhookPatch `json:"webhooks"`
}

// webhookPatch is one entry of a notification patch. The list it belongs to REPLACES
// the configured list (so adding and removing targets both work); what makes it a
// patch is secret_env, the ONE member a read path cannot round-trip.
type webhookPatch struct {
	URL      string   `json:"url"`
	Kind     *string  `json:"kind"`
	Events   []string `json:"events"`
	Projects []string `json:"projects"`
	Enabled  *bool    `json:"enabled"`
	// SecretEnv names the env var holding the secret — a NAME, never a value (SR403).
	// Omitted (absent or null) INHERITS the matching configured entry's name; an
	// explicit "" clears it.
	SecretEnv *string `json:"secret_env"`
}

// patchNotification applies a server.notification patch onto sc (S4, 2026-09-23).
//
// It is a PATCH rather than a whole-block replace for one concrete reason: the read
// path deliberately does not echo a webhook's `secret_env` NAME (SR403 — GET
// /v1/config reports only `secret_set`), so a console that re-sent the block it read
// would clear the HMAC key's env reference on every save without ever being able to
// show the operator what it destroyed. So:
//
//   - an omitted top-level member keeps its configured value (allow_hosts /
//     allow_http / max_attempts / enabled);
//   - a webhook entry that omits `secret_env` inherits the name of the entry it
//     replaces; an explicit "" clears it; a new name is taken verbatim.
//
// The webhook LIST itself is replaced by the body's list — that is what makes adding
// and removing targets expressible. Inheritance is by URL first (a target's identity,
// and the only key that survives removing a middle entry, where positional identity
// would copy the WRONG secret), falling back to the same index ONLY when the body's
// list has the same length (the "edited in place, url changed too" shape). A source
// entry is claimed at most once, so one secret can never land on two targets.
//
// `notification: null` clears the whole block (there is then no notification config
// at all); `webhooks: null` (or absent) keeps the configured list.
func patchNotification(sc *config.ServerConfig, p *notificationPatch) {
	if p == nil {
		sc.Notification = nil
		return
	}
	// Start from the configured block: every member the body does not mention keeps
	// its value. (The write transaction hands this function a CLONE, so sharing the
	// untouched members with the previous generation is safe.)
	next := config.NotificationConfig{}
	var cur *config.NotificationConfig
	if sc.Notification != nil {
		next = *sc.Notification
		cur = sc.Notification
	}
	if p.Enabled != nil {
		next.Enabled = p.Enabled
	}
	if p.AllowHosts != nil {
		next.AllowHosts = p.AllowHosts
	}
	if p.AllowHTTP != nil {
		next.AllowHTTP = *p.AllowHTTP
	}
	if p.MaxAttempts != nil {
		next.MaxAttempts = *p.MaxAttempts
	}
	if p.Webhooks != nil {
		next.Webhooks = patchWebhooks(*p.Webhooks, cur)
	}
	sc.Notification = &next
}

// patchWebhooks rebuilds the webhook list from a patch body, marking each configured
// entry that an incoming entry inherited its secret_env NAME from so the name is
// handed out at most once.
func patchWebhooks(in []webhookPatch, cur *config.NotificationConfig) []config.WebhookConfig {
	var prev []config.WebhookConfig
	if cur != nil {
		prev = cur.Webhooks
	}
	claimed := make([]bool, len(prev))
	out := make([]config.WebhookConfig, 0, len(in))
	for i, w := range in {
		out = append(out, buildWebhookPatch(w, i, len(in), prev, claimed))
	}
	return out
}

// buildWebhookPatch turns one body entry into a config entry, resolving the one
// member the body may legitimately omit.
func buildWebhookPatch(w webhookPatch, idx, total int, prev []config.WebhookConfig, claimed []bool) config.WebhookConfig {
	out := config.WebhookConfig{
		URL:      w.URL,
		Events:   w.Events,
		Projects: w.Projects,
		Enabled:  w.Enabled,
	}
	if w.Kind != nil {
		out.Kind = *w.Kind
	}
	if w.SecretEnv != nil {
		out.SecretEnv = *w.SecretEnv
		return out
	}
	if src := claimSecretSource(w.URL, idx, total, prev, claimed); src >= 0 {
		claimed[src] = true
		out.SecretEnv = prev[src].SecretEnv
	}
	return out
}

// claimSecretSource resolves which configured entry an incoming entry inherits its
// secret_env NAME from: the first UNCLAIMED entry with the same URL, else — only when
// the two lists have the same length, i.e. the in-place-edit shape — the unclaimed
// entry at the same index. -1 means "nothing to inherit" (a genuinely new target).
func claimSecretSource(url string, idx, total int, prev []config.WebhookConfig, claimed []bool) int {
	if url != "" {
		for i := range prev {
			if !claimed[i] && prev[i].URL == url {
				return i
			}
		}
	}
	if total == len(prev) && idx < len(prev) && !claimed[idx] {
		return idx
	}
	return -1
}

// serverErrorFields names the applied server fields the validator complained about.
// The config validators already speak dotted paths ("server.max_job_timeout_sec must
// be >= 0"), so the mapping is a substring test rather than a second rule table.
func serverErrorFields(applied []string, errMsg string) []string {
	var out []string
	for _, name := range applied {
		if strings.Contains(errMsg, "server."+name) {
			out = append(out, "server."+name)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// validate (dry run) + reload
// ---------------------------------------------------------------------------

// handleValidateConfig dry-runs a write: the same classification and validation the
// PUT endpoints run, on a CLONE of the live config, with the candidate block rendered
// back (design §一.1). Nothing is saved and nothing is reloaded — the response is the
// whole point: the console shows what would be written and what it would cost.
func (s *Server) handleValidateConfig(c *rux.Context) {
	if _, ok := s.configWriteCaller(c); !ok {
		return
	}
	if _, ok := s.configWriter(c); !ok {
		return
	}
	base, ok := s.configLiveConfig(c)
	if !ok {
		return
	}
	var req configValidateReq
	if err := c.BindJSON(&req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	section := strings.TrimSpace(req.Section)
	if section != "agents" && section != "server" {
		s.writeValidateFailure(c, &configWriteError{
			status: http.StatusBadRequest,
			msg:    "unknown section",
			detail: fmt.Sprintf("unknown section %q (want agents|server)", req.Section),
		})
		return
	}
	key := strings.TrimSpace(req.Key)
	if section == "agents" {
		if err := validateConfigKey(key); err != nil {
			s.writeValidateFailure(c, &configWriteError{status: http.StatusBadRequest, msg: "invalid agent key", detail: err.Error()})
			return
		}
	}

	fields, err := parseConfigBody(req.Value, section, key)
	if err != nil {
		s.writeValidateFailure(c, err)
		return
	}
	if len(fields) == 0 {
		s.writeValidateFailure(c, &configWriteError{
			status: http.StatusBadRequest,
			msg:    "empty request body",
			detail: "name at least one field in `value`",
		})
		return
	}
	next := base.Clone()
	var applied []string
	var aerr error
	if section == "agents" {
		applied, aerr = applyAgentWrite(next, key, fields)
	} else {
		for _, f := range fields {
			if aerr = applyServerField(&next.Server, f); aerr != nil {
				break
			}
			applied = append(applied, f.name)
		}
	}
	if aerr != nil {
		s.writeValidateFailure(c, aerr)
		return
	}
	if verr := validateCandidate(next); verr != nil {
		fields2 := serverErrorFields(applied, verr.Error())
		if section == "agents" {
			fields2 = agentErrorFields(key, next.Agents[key])
		}
		s.writeValidateFailure(c, &configWriteError{
			status: http.StatusBadRequest,
			msg:    "invalid config",
			detail: verr.Error(),
			fields: fields2,
		})
		return
	}

	preview, perr := previewBlock(section, next, key, applied)
	if perr != nil {
		writeError(c, http.StatusInternalServerError, "preview failed", perr.Error())
		return
	}
	c.JSON(http.StatusOK, configValidateResult{
		OK:              true,
		Errors:          []string{},
		Applied:         applied,
		RestartRequired: restartOnlyPaths(section),
		Preview:         preview,
	})
}

// previewBlock renders the candidate block for the console's YAML pane, from the
// candidate config itself (never assembled by the client, so the preview cannot show
// something other than what would be saved).
//
// The rendered block is REDACTED: an agent's `env` values become `***` (its keys are
// already in GET /v1/config), and the server preview carries only the applied fields
// — the full server block holds token/callers, and a preview must not echo secret
// material (SR403).
func previewBlock(section string, next *config.Config, key string, applied []string) (string, error) {
	if len(applied) == 0 {
		return "", nil
	}
	if section == "agents" {
		return config.RenderYAML(previewAgent(next.Agents[key]))
	}
	return serverPreview(next.Server, applied)
}

// previewAgent returns a copy of ac safe to render: `env` values (the agent's own and
// its acp MCP children's) are masked. GET /v1/config already exposes the KEY NAMES; a
// preview must never be the surface that turns them into values (SR403).
func previewAgent(ac config.AgentConfig) config.AgentConfig {
	out := ac
	if len(ac.Env) > 0 {
		out.Env = make(map[string]string, len(ac.Env))
		for k := range ac.Env {
			out.Env[k] = "***"
		}
	}
	if ac.ACP != nil && len(ac.ACP.MCPServers) > 0 {
		acpCopy := *ac.ACP
		servers := make([]config.ACPMCPServerConfig, len(ac.ACP.MCPServers))
		for i, srv := range ac.ACP.MCPServers {
			s := srv
			if len(srv.Env) > 0 {
				s.Env = make(map[string]string, len(srv.Env))
				for k := range srv.Env {
					s.Env[k] = "***"
				}
			}
			servers[i] = s
		}
		acpCopy.MCPServers = servers
		out.ACP = &acpCopy
	}
	return out
}

// serverPreview renders ONLY the applied server fields, masking the one secret-shaped
// member a webhook can carry (its secret_env NAME — never the value).
func serverPreview(sc config.ServerConfig, applied []string) (string, error) {
	doc := make(map[string]any, len(applied))
	for _, name := range applied {
		switch name {
		case "max_job_timeout_sec":
			doc[name] = sc.MaxJobTimeoutSec
		case "auto_resume_max":
			doc[name] = sc.AutoResumeMax
		case "stall_timeout_sec":
			doc[name] = sc.StallTimeoutSec
		case "runner_probe":
			doc[name] = map[string]any{
				"interval_seconds": sc.RunnerProbe.IntervalSeconds,
				"timeout_seconds":  sc.RunnerProbe.TimeoutSeconds,
			}
		case "skill_limits":
			doc[name] = map[string]any{
				"max_file_bytes":  sc.SkillLimits.MaxFileBytes,
				"max_total_bytes": sc.SkillLimits.MaxTotalBytes,
			}
		case "retry":
			doc[name] = sc.Retry
		case "comment_trigger":
			doc[name] = map[string]any{
				"min_interval_sec": sc.CommentTrigger.MinIntervalSec,
				"max_per_scope":    sc.CommentTrigger.MaxPerScope,
			}
		case "dir_lock":
			doc[name] = sc.DirLock
		case "agent_health":
			if sc.AgentHealth == nil {
				doc[name] = nil
				break
			}
			doc[name] = map[string]any{
				"window_sec":       sc.AgentHealth.WindowSec,
				"degraded_after":   sc.AgentHealth.DegradedAfter,
				"recover_after_ok": sc.AgentHealth.RecoverAfterOK,
			}
		case "notification":
			if sc.Notification == nil {
				doc[name] = nil
				break
			}
			hooks := make([]map[string]any, 0, len(sc.Notification.Webhooks))
			for _, w := range sc.Notification.Webhooks {
				h := map[string]any{"url": w.URL, "events": w.Events, "projects": w.Projects, "kind": w.Kind, "enabled": w.Enabled}
				if w.SecretEnv != "" {
					h["secret_env"] = "***"
				}
				hooks = append(hooks, h)
			}
			doc[name] = map[string]any{
				"enabled":      sc.Notification.Enabled,
				"webhooks":     hooks,
				"allow_hosts":  sc.Notification.AllowHosts,
				"allow_http":   sc.Notification.AllowHTTP,
				"max_attempts": sc.Notification.MaxAttempts,
			}
		}
	}
	return config.RenderYAML(doc)
}

// handleReloadConfig re-reads the config file by hand (POST /v1/config/reload). It is
// the only manual reload entry point on Windows, which has no SIGHUP: an operator who
// edited config.yaml directly has no other way to apply it without restarting the
// process. It is ALSO the recovery step for the one race this API cannot close — a
// human editing the same file in an editor while the console writes it (the file on
// disk wins on the next reload, whichever side wrote last).
func (s *Server) handleReloadConfig(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	if err := cw.ReloadConfig(); err != nil {
		c.JSON(http.StatusInternalServerError, configWriteErrorBody{Error: "config reload failed", Detail: err.Error()})
		return
	}
	slog.Info("config reloaded", "caller_id", caller)
	c.JSON(http.StatusOK, rux.M{"status": "ok", "reloaded": true})
}
