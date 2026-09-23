package config

import "testing"

// TestSkillsUnionAcrossLevels: the four binding levels are a UNION, not an override
// — server + agent + project + the job's own --skill, deduplicated with the first
// appearance order kept, so the same skill named at two levels is read (and
// materialized) once.
func TestSkillsUnionAcrossLevels(t *testing.T) {
	cfg := &Config{
		Server: ServerConfig{Skills: []string{"a"}},
		Agents: map[string]AgentConfig{"omp": {Skills: []string{"b"}}},
		Projects: map[string]ProjectConfig{
			"hyy": {Skills: []string{"a", "c"}},
		},
	}

	got := cfg.EffectiveSkills("hyy", "omp", []string{"d"}, false, "cli-agent")
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("EffectiveSkills = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("EffectiveSkills = %v, want %v (level order server → agent → project → job)", got, want)
		}
	}

	// --no-skills wins over every level.
	if got := cfg.EffectiveSkills("hyy", "omp", []string{"d"}, true, "cli-agent"); len(got) != 0 {
		t.Fatalf("with disable = %v, want none", got)
	}
	// An exec agent runs a command; it has no notion of reading a doc.
	if got := cfg.EffectiveSkills("hyy", "omp", []string{"d"}, false, "exec"); len(got) != 0 {
		t.Fatalf("exec agent = %v, want none", got)
	}
	// An unknown project/agent key never panics: the server level still applies.
	if got := cfg.EffectiveSkills("nope", "nope", nil, false, "cli-agent"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("unknown keys = %v, want the server level [a]", got)
	}
}
