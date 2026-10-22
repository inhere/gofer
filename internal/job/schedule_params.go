package job

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Schedule parameter declarations (design 2026-10-22 §D1). A schedule may declare
// up to MaxScheduleParams named parameters; every run of the schedule submits its
// stored JobRequest with the parameter DEFAULTS overlaid on the stored env, and a
// run-now may override declared ones (design §D2). Values only ever travel as job
// environment variables — never into argv or the prompt — so a script reads its own
// env and no value can turn into shell syntax.
const (
	// MaxScheduleParams is the ceiling on declarations per schedule.
	MaxScheduleParams = 20
	// MaxScheduleParamValueLen caps one parameter value (the same limit a job env
	// value has to stay printable in a job detail).
	MaxScheduleParamValueLen = 1024
	// ScheduleParamTypeString is the default type; every value is passed verbatim.
	ScheduleParamTypeString = "string"
	// ScheduleParamTypeInt requires a base-10 integer value.
	ScheduleParamTypeInt = "int"
	// ScheduleParamTypeBool requires "1" or "0" (an env var cannot be a real bool).
	ScheduleParamTypeBool = "bool"
	// ScheduleParamTypeChoice requires one of the declared Choices.
	ScheduleParamTypeChoice = "choice"
	// scheduleParamPrefix is reserved for gofer's own injected variables: a schedule
	// may not name a parameter GOFER_* (it would shadow GOFER_JOB_ID etc.).
	scheduleParamPrefix = "GOFER_"
)

// scheduleParamName matches a POSIX-ish environment variable name.
var scheduleParamName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// ScheduleParam is one declared parameter of a schedule, stored as JSON in
// schedules.params_json (design §D1). Default and Choices are strings because the
// value is handed to the job as an env variable; Type only decides how it is
// validated and which web control renders it.
type ScheduleParam struct {
	Name    string   `json:"name" yaml:"name"`
	Type    string   `json:"type,omitempty" yaml:"type,omitempty"`
	Default string   `json:"default,omitempty" yaml:"default,omitempty"`
	Desc    string   `json:"desc,omitempty" yaml:"desc,omitempty"`
	Choices []string `json:"choices,omitempty" yaml:"choices,omitempty"`
}

// TypeOrString returns the declared type, defaulting to string.
func (p ScheduleParam) TypeOrString() string {
	if p.Type == "" {
		return ScheduleParamTypeString
	}
	return p.Type
}

// AvailableParams names the declared parameters, for an error that has to tell a
// caller which keys it may pass.
func AvailableParams(params []ScheduleParam) string {
	if len(params) == 0 {
		return "(none: this schedule declares no params)"
	}
	names := make([]string, 0, len(params))
	for _, p := range params {
		names = append(names, p.Name)
	}
	return strings.Join(names, ", ")
}

// DecodeScheduleParams decodes the stored params_json column. An empty string is a
// schedule that declares nothing (every schedule written before the column).
func DecodeScheduleParams(raw string) ([]ScheduleParam, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var params []ScheduleParam
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("decode schedule params: %w", err)
	}
	return params, nil
}

// EncodeScheduleParams validates the declarations and returns the JSON stored in
// schedules.params_json. An empty declaration set is stored as "" (NULL-equivalent),
// so an old row and a new param-less one read back the same.
func EncodeScheduleParams(params []ScheduleParam) (string, error) {
	if err := ValidateScheduleParams(params); err != nil {
		return "", err
	}
	if len(params) == 0 {
		return "", nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("encode schedule params: %w", err)
	}
	return string(raw), nil
}

// ValidateScheduleParams checks the declarations themselves: name shape and
// uniqueness, known type, a default that fits the type, at most MaxScheduleParams
// entries, and values that can be put into an env variable at all.
func ValidateScheduleParams(params []ScheduleParam) error {
	if len(params) > MaxScheduleParams {
		return fmt.Errorf("a schedule may declare at most %d params, got %d", MaxScheduleParams, len(params))
	}
	seen := make(map[string]bool, len(params))
	for i, p := range params {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("param #%d: name is required", i+1)
		}
		if name != p.Name {
			return fmt.Errorf("param %q: name must not have surrounding spaces", p.Name)
		}
		if !scheduleParamName.MatchString(name) {
			return fmt.Errorf("param %q: name must match ^[A-Z_][A-Z0-9_]*$ (an environment variable name)", name)
		}
		if strings.HasPrefix(name, scheduleParamPrefix) {
			return fmt.Errorf("param %q: names starting with %s are reserved for gofer", name, scheduleParamPrefix)
		}
		if seen[name] {
			return fmt.Errorf("param %q is declared twice", name)
		}
		seen[name] = true

		switch p.TypeOrString() {
		case ScheduleParamTypeString, ScheduleParamTypeInt, ScheduleParamTypeBool:
		case ScheduleParamTypeChoice:
			if len(p.Choices) == 0 {
				return fmt.Errorf("param %q: a choice param requires choices", name)
			}
		default:
			return fmt.Errorf("param %q: type must be one of string, int, bool, choice (got %q)", name, p.Type)
		}
		if err := ValidateScheduleParamValue(p, p.Default); err != nil {
			return fmt.Errorf("param %q: default: %w", name, err)
		}
	}
	return nil
}

// ValidateScheduleParamValue checks one value against a declaration: length and
// character limits, then the type's own rule.
func ValidateScheduleParamValue(p ScheduleParam, value string) error {
	if utf8.RuneCountInString(value) > MaxScheduleParamValueLen {
		return fmt.Errorf("value is longer than %d characters", MaxScheduleParamValueLen)
	}
	if strings.ContainsAny(value, "\n\r\x00") {
		return fmt.Errorf("value must not contain a newline or NUL")
	}
	switch p.TypeOrString() {
	case ScheduleParamTypeInt:
		if strings.TrimSpace(value) == "" {
			// an empty int parameter means "unset"; the script decides what that means
			return nil
		}
		if _, err := strconv.ParseInt(value, 10, 64); err != nil {
			return fmt.Errorf("must be an integer, got %q", value)
		}
	case ScheduleParamTypeBool:
		if value != "" && value != "0" && value != "1" {
			return fmt.Errorf(`must be "1" or "0", got %q`, value)
		}
	case ScheduleParamTypeChoice:
		for _, c := range p.Choices {
			if c == value {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v, got %q", p.Choices, value)
	}
	return nil
}

// ScheduleParamDefaults maps every declared parameter to its default value. A run
// that names nothing (the cron sweep, the webhook) uses exactly this.
func ScheduleParamDefaults(params []ScheduleParam) map[string]string {
	if len(params) == 0 {
		return nil
	}
	out := make(map[string]string, len(params))
	for _, p := range params {
		out[p.Name] = p.Default
	}
	return out
}

// ApplyScheduleParams overlays a schedule's parameters onto a stored job request
// (design §D2): the stored env first, then the declared defaults, then this run's
// overrides — so a param always wins over a same-named stored env value.
//
// An override naming a parameter the schedule does not declare is an error: run-now
// must not become a way to inject arbitrary environment variables into a job. The
// error names the parameters that ARE available.
func ApplyScheduleParams(req *JobRequest, params []ScheduleParam, overrides map[string]string) error {
	if len(params) == 0 {
		if len(overrides) > 0 {
			return fmt.Errorf("this schedule declares no params; available params: %s", AvailableParams(params))
		}
		return nil
	}
	declared := make(map[string]ScheduleParam, len(params))
	for _, p := range params {
		declared[p.Name] = p
	}
	// Validate the overrides BEFORE touching req.Env, so a refused run leaves the
	// request exactly as stored.
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p, ok := declared[name]
		if !ok {
			return fmt.Errorf("param %q is not declared by this schedule; available params: %s",
				name, AvailableParams(params))
		}
		if err := ValidateScheduleParamValue(p, overrides[name]); err != nil {
			return fmt.Errorf("param %q: %w", name, err)
		}
	}

	merged := make(map[string]string, len(req.Env)+len(params))
	for k, v := range req.Env {
		merged[k] = v
	}
	for _, p := range params {
		merged[p.Name] = p.Default
	}
	for _, name := range names {
		merged[name] = overrides[name]
	}
	req.Env = merged
	return nil
}

// ParseParamBindings parses the CLI's repeated `--param K=V` values into an override
// map, refusing a binding without "=" and a repeated key.
func ParseParamBindings(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(pairs))
	for _, raw := range pairs {
		name, value, ok := strings.Cut(raw, "=")
		if !ok {
			return nil, fmt.Errorf("--param %q must be NAME=VALUE", raw)
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("--param %q has an empty name", raw)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("--param %s given twice", name)
		}
		out[name] = value
	}
	return out, nil
}

// ParseParamDef parses the CLI's `--param-def 'NAME[:type]=default[#desc]'` form
// (design §D6) into a declaration. choices cannot be expressed here; they need
// --params-file.
func ParseParamDef(raw string) (ScheduleParam, error) {
	spec := raw
	desc := ""
	if i := strings.Index(spec, "#"); i >= 0 {
		spec, desc = spec[:i], strings.TrimSpace(spec[i+1:])
	}
	lhs, def, ok := strings.Cut(spec, "=")
	if !ok {
		return ScheduleParam{}, fmt.Errorf("--param-def %q must be NAME[:type]=default[#desc]", raw)
	}
	name, typ, hasType := strings.Cut(strings.TrimSpace(lhs), ":")
	if strings.TrimSpace(name) == "" {
		return ScheduleParam{}, fmt.Errorf("--param-def %q has an empty name", raw)
	}
	p := ScheduleParam{Name: name, Default: def, Desc: desc}
	if hasType {
		p.Type = strings.TrimSpace(typ)
	}
	if p.TypeOrString() == ScheduleParamTypeChoice {
		return ScheduleParam{}, fmt.Errorf("--param-def %q: choice params need --params-file (they carry a choices list)", raw)
	}
	return p, nil
}

// ParamDefsToParams parses every --param-def value, then validates the whole set
// (uniqueness and defaults included) in one go.
func ParamDefsToParams(defs []string) ([]ScheduleParam, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	params := make([]ScheduleParam, 0, len(defs))
	for _, raw := range defs {
		p, err := ParseParamDef(raw)
		if err != nil {
			return nil, err
		}
		params = append(params, p)
	}
	if err := ValidateScheduleParams(params); err != nil {
		return nil, err
	}
	return params, nil
}
