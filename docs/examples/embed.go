// Package examples carries the templates gofer ships as examples (a task brief, a batch, a seat
// charter). They live in docs/examples/templates so people can read them without the binary;
// the Go source sits next to them because //go:embed cannot reference parent directories.
package examples

import (
	"embed"
	"sort"
	"strings"
)

//go:embed templates/*.md
var templates embed.FS

// Template returns the content of the shipped template called name (a file name under
// templates/, e.g. "seat-charter-flow.md").
func Template(name string) (string, bool) {
	b, err := templates.ReadFile("templates/" + strings.TrimPrefix(name, "templates/"))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// TemplateNames lists the shipped templates by file name.
func TemplateNames() []string {
	entries, err := templates.ReadDir("templates")
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}
