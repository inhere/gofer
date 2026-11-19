package seat

import (
	"strings"

	"github.com/inhere/gofer/docs/examples"
)

// charterTemplates maps the short name a person types (`--charter-template <name>`) to the
// shipped template file. "flow" is the design → review → plan → doing → verify convention.
var charterTemplates = map[string]string{
	"flow": "seat-charter-flow.md",
}

// CharterTemplate returns the built-in charter template called name.
func CharterTemplate(name string) (string, bool) {
	file, ok := charterTemplates[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return "", false
	}
	return examples.Template(file)
}

// CharterTemplateNames lists the names CharterTemplate accepts, for error messages.
func CharterTemplateNames() []string {
	out := make([]string, 0, len(charterTemplates))
	for k := range charterTemplates {
		out = append(out, k)
	}
	return out
}
