package job

import (
	"strings"
	"testing"
)

// TestEncodeDecodeScheduleParams covers the declaration round trip: an empty list is
// stored as "" (so an old row and a param-less one read back the same), and whatever
// goes in comes back unchanged.
func TestEncodeDecodeScheduleParams(t *testing.T) {
	t.Parallel()
	raw, err := EncodeScheduleParams(nil)
	if err != nil || raw != "" {
		t.Fatalf("empty params: raw=%q err=%v, want \"\" and no error", raw, err)
	}
	got, err := DecodeScheduleParams("")
	if err != nil || got != nil {
		t.Fatalf("decode empty: %+v err=%v", got, err)
	}

	in := []ScheduleParam{
		{Name: "CLEAN_DAYS", Type: "int", Default: "7", Desc: "keep days"},
		{Name: "CLEAN_ONLY", Type: "choice", Default: "", Choices: []string{"", "a"}},
	}
	raw, err = EncodeScheduleParams(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err = DecodeScheduleParams(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 || got[0].Name != "CLEAN_DAYS" || got[1].Choices[1] != "a" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// TestValidateScheduleParams covers the boundaries of a declaration (design §D1):
// a name that is an env variable, not reserved, not duplicated, a default that fits
// its type, and the ceilings on count / value length / characters.
func TestValidateScheduleParams(t *testing.T) {
	t.Parallel()
	ok := []struct {
		name   string
		params []ScheduleParam
	}{
		{"string default", []ScheduleParam{{Name: "NOTE", Default: "hello"}}},
		{"int", []ScheduleParam{{Name: "DAYS", Type: "int", Default: "-3"}}},
		{"empty int is unset", []ScheduleParam{{Name: "DAYS", Type: "int", Default: ""}}},
		{"bool 1", []ScheduleParam{{Name: "APPLY", Type: "bool", Default: "1"}}},
		{"bool empty", []ScheduleParam{{Name: "APPLY", Type: "bool", Default: ""}}},
		{"choice", []ScheduleParam{{Name: "ONLY", Type: "choice", Default: "", Choices: []string{"", "x"}}}},
		{"underscore name", []ScheduleParam{{Name: "_PRIVATE", Default: "v"}}},
	}
	for _, c := range ok {
		if err := ValidateScheduleParams(c.params); err != nil {
			t.Fatalf("%s: unexpected error: %v", c.name, err)
		}
	}

	bad := []struct {
		name   string
		params []ScheduleParam
		want   string
	}{
		{"reserved", []ScheduleParam{{Name: "GOFER_JOB_ID"}}, "reserved"},
		{"lowercase", []ScheduleParam{{Name: "clean"}}, "must match"},
		{"starts with digit", []ScheduleParam{{Name: "1DAY"}}, "must match"},
		{"space padded", []ScheduleParam{{Name: " DAYS"}}, "surrounding spaces"},
		{"empty name", []ScheduleParam{{Name: ""}}, "name is required"},
		{"duplicate", []ScheduleParam{{Name: "A"}, {Name: "A"}}, "declared twice"},
		{"unknown type", []ScheduleParam{{Name: "A", Type: "float"}}, "type must be one of"},
		{"int default", []ScheduleParam{{Name: "A", Type: "int", Default: "one"}}, "must be an integer"},
		{"bool default", []ScheduleParam{{Name: "A", Type: "bool", Default: "true"}}, `must be "1" or "0"`},
		{"choice without list", []ScheduleParam{{Name: "A", Type: "choice", Default: ""}}, "requires choices"},
		{"choice not listed", []ScheduleParam{{Name: "A", Type: "choice", Default: "z", Choices: []string{"x"}}}, "must be one of"},
		{"newline default", []ScheduleParam{{Name: "A", Default: "a\nb"}}, "newline or NUL"},
		{"too long", []ScheduleParam{{Name: "A", Default: strings.Repeat("x", MaxScheduleParamValueLen+1)}}, "longer than"},
	}
	for _, c := range bad {
		err := ValidateScheduleParams(c.params)
		if err == nil {
			t.Fatalf("%s: expected an error", c.name)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: error %q does not contain %q", c.name, err, c.want)
		}
	}

	// The count ceiling.
	many := make([]ScheduleParam, 0, MaxScheduleParams+1)
	for i := 0; i <= MaxScheduleParams; i++ {
		many = append(many, ScheduleParam{Name: "P" + string(rune('A'+i%26)) + string(rune('A'+i/26))})
	}
	if err := ValidateScheduleParams(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("over the count ceiling: err=%v", err)
	}
}

// TestApplyScheduleParams covers the overlay order (design §D2): stored env first, then
// the declared defaults, then this run's overrides — and the refusal of an undeclared
// key, which must leave the request untouched.
func TestApplyScheduleParams(t *testing.T) {
	t.Parallel()
	params := []ScheduleParam{
		{Name: "CLEAN_DAYS", Type: "int", Default: "7"},
		{Name: "CLEAN_APPLY", Type: "bool", Default: "0"},
	}

	// Defaults only (a cron tick / a webhook).
	req := &JobRequest{Env: map[string]string{"CLEAN_DAYS": "99", "KEEP": "v"}}
	if err := ApplyScheduleParams(req, params, nil); err != nil {
		t.Fatalf("apply defaults: %v", err)
	}
	if req.Env["CLEAN_DAYS"] != "7" || req.Env["CLEAN_APPLY"] != "0" || req.Env["KEEP"] != "v" {
		t.Fatalf("defaults overlay = %+v", req.Env)
	}

	// A run-now override wins over the default.
	req = &JobRequest{Env: map[string]string{"KEEP": "v"}}
	if err := ApplyScheduleParams(req, params, map[string]string{"CLEAN_APPLY": "1"}); err != nil {
		t.Fatalf("apply override: %v", err)
	}
	if req.Env["CLEAN_APPLY"] != "1" || req.Env["CLEAN_DAYS"] != "7" {
		t.Fatalf("override overlay = %+v", req.Env)
	}

	// An undeclared key is refused, naming the declared ones, and changes nothing.
	req = &JobRequest{Env: map[string]string{"KEEP": "v"}}
	err := ApplyScheduleParams(req, params, map[string]string{"SO_SECRET": "x"})
	if err == nil {
		t.Fatal("undeclared override was accepted")
	}
	if !strings.Contains(err.Error(), "CLEAN_DAYS, CLEAN_APPLY") {
		t.Fatalf("error %q does not list the declared params", err)
	}
	if len(req.Env) != 1 || req.Env["KEEP"] != "v" {
		t.Fatalf("a refused apply mutated the request env: %+v", req.Env)
	}

	// A value that does not fit its type is refused as well.
	if err := ApplyScheduleParams(&JobRequest{}, params, map[string]string{"CLEAN_DAYS": "many"}); err == nil {
		t.Fatal("non-integer value for an int param was accepted")
	}

	// A schedule with no params takes no overrides at all.
	if err := ApplyScheduleParams(&JobRequest{}, nil, map[string]string{"A": "b"}); err == nil {
		t.Fatal("an override on a param-less schedule was accepted")
	}
}

// TestParseParamDef covers the CLI declaration form (design §D6):
// NAME[:type]=default[#desc].
func TestParseParamDef(t *testing.T) {
	t.Parallel()
	p, err := ParseParamDef("CLEAN_DAYS:int=7#保留天数")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Name != "CLEAN_DAYS" || p.Type != "int" || p.Default != "7" || p.Desc != "保留天数" {
		t.Fatalf("parsed %+v", p)
	}

	p, err = ParseParamDef("NOTE=hello world")
	if err != nil {
		t.Fatalf("parse without type: %v", err)
	}
	if p.Name != "NOTE" || p.TypeOrString() != ScheduleParamTypeString || p.Default != "hello world" {
		t.Fatalf("parsed %+v", p)
	}

	for _, bad := range []string{"NOEQUALS", "=v", "A:choice=x"} {
		if _, err := ParseParamDef(bad); err == nil {
			t.Fatalf("ParseParamDef(%q) accepted an invalid declaration", bad)
		}
	}

	params, err := ParamDefsToParams([]string{"DAYS:int=7", "APPLY:bool=0", "DAYS:int=1"})
	if err == nil {
		t.Fatalf("duplicate --param-def accepted: %+v", params)
	}
}

// TestParseParamBindings covers `schedule run --param K=V`.
func TestParseParamBindings(t *testing.T) {
	t.Parallel()
	got, err := ParseParamBindings([]string{"A=1", "B=x=y"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got["A"] != "1" || got["B"] != "x=y" {
		t.Fatalf("parsed %+v", got)
	}
	for _, bad := range [][]string{{"A"}, {"=1"}, {"A=1", "A=2"}} {
		if _, err := ParseParamBindings(bad); err == nil {
			t.Fatalf("ParseParamBindings(%v) accepted an invalid binding", bad)
		}
	}
}
