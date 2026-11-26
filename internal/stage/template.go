// Package stage is the flow-progression service: a work item can carry one flow (a template's
// steps, snapshotted at start), and only an explicit action moves it — `done` after the
// current step's gate passes, `back` with a reason, `blocked` with a reason, or a person's
// approval. The gates read stored facts and git, never model text; a gate that cannot be
// judged here (a project that is not on this machine, git failing) answers needs_human
// instead of an error.
//
// Dependencies point down only (G022): stage -> jobstore / project / config. Plan:
// docs/plans/2026-11-26-flow-progression-plan.md, design:
// docs/design/2026-11-18-resident-sessions-and-team-design.md §6.
package stage

import "slices"

// The steps a template is made of.
const (
	StepDesign = "design"
	StepReview = "review"
	StepPlan   = "plan"
	StepDoing  = "doing"
	StepVerify = "verify"
	StepDone   = "done"
)

// Built-in template names.
const (
	TemplateFeature = "feature"
	TemplateSmall   = "small"
)

// DefaultReviewMax is the review round cap of the built-in templates: after that many review
// rounds only a person's approval passes the review gate.
const DefaultReviewMax = 2

// Template is a flow definition. Starting a flow copies it into the work item's row, so
// changing a template later never touches a flow in progress.
type Template struct {
	Name  string
	Steps []string
	// ReviewMax caps the review rounds a review job may satisfy the gate in.
	ReviewMax int
	// PlanDoc makes the plan gate also ask for a committed plan document (role plan).
	PlanDoc bool
}

var builtinTemplates = []Template{
	{Name: TemplateFeature, Steps: []string{StepDesign, StepReview, StepPlan, StepDoing, StepVerify, StepDone}, ReviewMax: DefaultReviewMax, PlanDoc: true},
	{Name: TemplateSmall, Steps: []string{StepDoing, StepVerify, StepDone}, ReviewMax: DefaultReviewMax},
}

// TemplateByName returns a built-in template (a copy); ok=false for an unknown name.
func TemplateByName(name string) (Template, bool) {
	for _, t := range builtinTemplates {
		if t.Name == name {
			t.Steps = slices.Clone(t.Steps)
			return t, true
		}
	}
	return Template{}, false
}

// TemplateNames lists the built-in template names.
func TemplateNames() []string {
	out := make([]string, 0, len(builtinTemplates))
	for _, t := range builtinTemplates {
		out = append(out, t.Name)
	}
	return out
}
