package config

import (
	"fmt"
	"regexp"
	"strings"
)

// ProjectStage is the per-project setting of the stage flow (plan 2026-11-26-flow-progression-plan.md §0.6):
//
//	projects:
//	  <key>:
//	    stage:
//	      enabled: false              # default true; false stops the start-a-flow prompts
//	      verify_templates: [verify]  # workflow templates whose finished run satisfies the verify gate
//
// A flow that was already started is not affected by `enabled`.
type ProjectStage struct {
	// Enabled is a pointer so unset (nil) stays distinguishable from an explicit false.
	Enabled *bool `yaml:"enabled,omitempty"`
	// VerifyTemplates names the workflow templates whose runs count for the verify gate.
	// Empty = DefaultStageVerifyTemplates.
	VerifyTemplates []string `yaml:"verify_templates,omitempty"`
}

// DefaultStageVerifyTemplates is the verify gate's template list when a project names none.
var DefaultStageVerifyTemplates = []string{"verify"}

// stageTemplateNameRe is the shape of a workflow template name (the same rule as
// workflow.CheckTemplateName, which the config package cannot import).
var stageTemplateNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// StageEnabled reports whether the project's stage flow is on (default true).
func (p ProjectConfig) StageEnabled() bool {
	return p.Stage == nil || p.Stage.Enabled == nil || *p.Stage.Enabled
}

// StageVerifyTemplates returns the workflow templates the verify gate looks for, as a copy.
func (p ProjectConfig) StageVerifyTemplates() []string {
	if p.Stage != nil && len(p.Stage.VerifyTemplates) > 0 {
		return append([]string(nil), p.Stage.VerifyTemplates...)
	}
	return append([]string(nil), DefaultStageVerifyTemplates...)
}

// StageEnabledFor is StageEnabled of the named project; an unknown key is enabled (the default).
func (c *Config) StageEnabledFor(projectKey string) bool {
	if p, ok := c.Projects[projectKey]; ok {
		return p.StageEnabled()
	}
	return true
}

// StageVerifyTemplatesFor is StageVerifyTemplates of the named project (the default for an unknown key).
func (c *Config) StageVerifyTemplatesFor(projectKey string) []string {
	if p, ok := c.Projects[projectKey]; ok {
		return p.StageVerifyTemplates()
	}
	return append([]string(nil), DefaultStageVerifyTemplates...)
}

// validate checks the stage block; a nil block is valid.
func (s *ProjectStage) validate() error {
	if s == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, name := range s.VerifyTemplates {
		name = strings.TrimSpace(name)
		if name == "" || name == "." || name == ".." || !stageTemplateNameRe.MatchString(name) {
			return fmt.Errorf("stage.verify_templates: invalid template name %q (allowed characters: A-Z a-z 0-9 . _ -)", name)
		}
		if seen[name] {
			return fmt.Errorf("stage.verify_templates: duplicate template %q", name)
		}
		seen[name] = true
	}
	return nil
}
