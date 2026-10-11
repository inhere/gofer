package configlint

import (
	"fmt"
	"reflect"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/config"
)

// extendsHints suggests rewriting agents that redeclare a built-in template in full
// as `extends: builtin` plus their real differences. The rewrite is built on the
// text (the agent's own key blocks, comments included) and shrunk greedily; it is
// offered only when it loads to the same effective config and is shorter.
func (s *session) extendsHints(file string, lines []string) []Finding {
	if config.BuiltinAgentTemplate == nil {
		return nil
	}
	l, err := s.load(nil)
	if err != nil {
		return nil
	}
	nodes := scanNodes(lines)
	var out []Finding
	for _, n := range nodes {
		if n.depth != 2 || !strings.HasPrefix(n.path, "agents.") {
			continue
		}
		name := strings.TrimPrefix(n.path, "agents.")
		decl, ok := l.agents[name]
		if !ok || decl.Extends != "" {
			continue
		}
		tpl, ok := config.BuiltinAgentTemplate(name)
		if !ok {
			continue
		}
		var children []node
		for _, c := range nodes {
			if c.depth == 3 && strings.HasPrefix(c.path, n.path+".") && c.start >= n.start && c.end <= n.end {
				children = append(children, c)
			}
		}
		if len(children) == 0 {
			continue // a flow-style entry: leave it alone
		}
		indent := strings.Repeat(" ", children[0].indent)
		var pieces [][]string
		written := map[string]bool{}
		for _, c := range children {
			pieces = append(pieces, lines[c.start:c.end])
			written[strings.TrimPrefix(c.path, n.path+".")] = true
		}
		// A field the template sets but the declaration does not: keep today's value
		// explicitly, otherwise the rewrite would adopt the template's.
		extra := templateOnlyFields(decl, tpl, written, indent)
		pieces = append(pieces, extra...)

		build := func(ps [][]string) []string {
			block := []string{lines[n.key], indent + "extends: " + config.ExtendsBuiltin}
			for _, p := range ps {
				block = append(block, p...)
			}
			out := append([]string{}, lines[:n.start]...)
			out = append(out, lines[n.start:n.key]...) // comments above the agent
			out = append(out, block...)
			return append(out, lines[n.end:]...)
		}
		if !s.equivalent(file, build(pieces)) {
			continue
		}
		for i := len(pieces) - 1; i >= 0; i-- {
			trial := append(append([][]string{}, pieces[:i]...), pieces[i+1:]...)
			if s.equivalent(file, build(trial)) {
				pieces = trial
			}
		}
		suggestion := []string{lines[n.key], indent + "extends: " + config.ExtendsBuiltin}
		for _, p := range pieces {
			suggestion = append(suggestion, p...)
		}
		saved := (n.end - n.key) - len(suggestion)
		if saved <= 0 {
			continue
		}
		out = append(out, Finding{
			File: file, Line: n.key + 1, Path: n.path, Kind: KindExtends,
			Reason:     fmt.Sprintf("can be written as extends: builtin (%d lines -> %d)", n.end-n.key, len(suggestion)),
			Suggestion: strings.Join(suggestion, "\n"), Lines: saved,
		})
	}
	return out
}

// templateOnlyFields renders `key: <current value>` lines for the fields the template
// sets to something other than what the declaration (which does not write them) has.
func templateOnlyFields(decl, tpl config.AgentConfig, written map[string]bool, indent string) [][]string {
	var out [][]string
	dv, tv := reflect.ValueOf(decl), reflect.ValueOf(tpl)
	t := dv.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" || key == "-" || key == "extends" || written[key] {
			continue
		}
		if reflect.DeepEqual(dv.Field(i).Interface(), tv.Field(i).Interface()) {
			continue
		}
		out = append(out, []string{indent + key + ": " + flowValue(dv.Field(i))})
	}
	return out
}

// flowValue renders a field value on one line; nil / zero becomes null or "".
func flowValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return "null"
		}
	case reflect.String:
		if v.String() == "" {
			return `""`
		}
	}
	b, err := yaml.MarshalWithOptions(v.Interface(), yaml.Flow(true))
	if err != nil {
		return "null"
	}
	return strings.TrimSpace(string(b))
}
