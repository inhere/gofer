package commands

import (
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// checkCompleteOnFlag validates a --complete-on value ("" = flag not given).
func checkCompleteOnFlag(v string) error {
	v = strings.TrimSpace(v)
	if v != "" && !jobstore.ValidPlanCompleteOn(v) {
		return fmt.Errorf("invalid --complete-on %q: must be job or merge", v)
	}
	return nil
}

// effectivePlanCompleteOnWord renders a plan's complete_on for the CLI: an older server
// that does not send the field reports an empty string, which reads as `job`.
func effectivePlanCompleteOnWord(v string) string {
	if v == jobstore.PlanCompleteOnMerge {
		return jobstore.PlanCompleteOnMerge
	}
	return jobstore.PlanCompleteOnJob
}
