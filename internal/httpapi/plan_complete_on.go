package httpapi

import (
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/jobstore"
)

// effectivePlanCompleteOn normalises the column for the wire: anything that is not
// `merge` (an empty string from an old row) reads as `job`, the default.
func effectivePlanCompleteOn(v string) string {
	if v == jobstore.PlanCompleteOnMerge {
		return jobstore.PlanCompleteOnMerge
	}
	return jobstore.PlanCompleteOnJob
}

// parseCreateCompleteOn validates the create body's complete_on (empty = default
// `job`); on an invalid value it writes the 400 and returns ok=false.
func parseCreateCompleteOn(c *rux.Context, raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return jobstore.PlanCompleteOnJob, true
	}
	if !jobstore.ValidPlanCompleteOn(v) {
		writeError(c, http.StatusBadRequest, "invalid complete_on", "complete_on must be job or merge")
		return "", false
	}
	return v, true
}

// parseUpdateCompleteOn validates the PATCH body's complete_on (nil = leave alone,
// returned as ""); on an invalid value it writes the 400 and returns ok=false.
func parseUpdateCompleteOn(c *rux.Context, raw *string) (string, bool) {
	if raw == nil {
		return "", true
	}
	v := strings.TrimSpace(*raw)
	if !jobstore.ValidPlanCompleteOn(v) {
		writeError(c, http.StatusBadRequest, "invalid complete_on", "complete_on must be job or merge")
		return "", false
	}
	return v, true
}
