package commands

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/template"
)

// template.go implements `gofer template ls|show` (SUP-01 P5, design §六). Both
// subcommands are READ-ONLY views of what the SERVER holds: the templates live on
// the server's disk (the project's .gofer/templates first, then the server's
// <config-dir>/templates), so they are read over HTTP — a remote server and a local
// one behave identically. They are registered under "Jobs & workflows" because a
// template is the task book a job run renders from.

// templateLsOpts / templateShowOpts hold the subcommands' flags.
var templateLsOpts = struct {
	project string
}{}

type templateShowFlags struct {
	project string
	vars    gcli.Strings
}

var templateShowOpts = templateShowFlags{}

// NewTemplateCmd builds the `template` command group.
func NewTemplateCmd() *gcli.Command {
	return &gcli.Command{
		Name: "template",
		Desc: "Inspect task-book templates: job prompts rendered from files on the server",
		Subs: []*gcli.Command{
			{
				Name:    "ls",
				Desc:    "List the templates a project can be driven with (project copies shadow global ones)",
				Aliases: []string{"list"},
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&templateLsOpts.project, "project", "p", "", "project key (default: the project detected from the current dir)")
				},
				Func: runTemplateLs,
			},
			{
				Name: "show",
				Desc: "Print one template: source path, declared variables and the rendered body preview",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&templateShowOpts.project, "project", "p", "", "project key (default: the project detected from the current dir)")
					c.VarOpt(&templateShowOpts.vars, "var", "", "template variable k=v (repeatable)")
					c.AddArg("name", "template name", true)
				},
				Func: runTemplateShow,
			},
		},
	}
}

// runTemplateLs prints the listing as a table: name, source and — most usefully —
// why a template is unusable when it does not parse.
func runTemplateLs(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	list, err := cli.ListTemplates(templateProject(templateLsOpts.project))
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no templates; create <project>/.gofer/templates/<name>.md or <config-dir>/templates/<name>.md")
		return nil
	}
	c.Printf("%-24s %-8s %s\n", "NAME", "SOURCE", "DESC")
	for _, in := range list {
		desc := in.Desc
		if in.Err != "" {
			desc = "INVALID: " + firstLineOf(in.Err)
		}
		c.Printf("%-24s %-8s %s\n", in.Name, in.Source, desc)
	}
	return nil
}

// runTemplateShow prints one template: where it came from, the variables it declares,
// and the body a submit would send. The RENDER comes from the server (the read
// endpoint takes the --var values and renders), because only the server can expand
// {{include: …}} and resolve {{head}} against the project the job would run in —
// a client-side render would quietly differ from the real prompt.
//
// A missing required variable is an error here (nothing was rendered), while the
// render's warnings (an undeclared placeholder, an unresolvable builtin) are printed
// beside the preview: they are part of judging whether the task book says what the
// reader means.
func runTemplateShow(c *gcli.Command, args []string) error {
	// gcli binds the positional to the named arg; a direct call (tests) passes it as
	// the args slice, so both are read.
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}
	if name == "" {
		if a := c.Arg("name"); a != nil {
			name = strings.TrimSpace(a.String())
		}
	}
	if name == "" {
		return fmt.Errorf("template show requires a <name> argument")
	}
	projectKey := templateProject(templateShowOpts.project)
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	vars, err := parseVarFlags(templateShowOpts.vars)
	if err != nil {
		return err
	}
	preview, err := cli.GetTemplate(projectKey, name, vars)
	if err != nil {
		return err
	}
	tpl, rendered := preview.Template, preview.Render
	if len(rendered.Missing) > 0 {
		return fmt.Errorf("template %q is missing required vars: %s", name, strings.Join(rendered.Missing, ", "))
	}

	c.Printf("template: %s (%s)\n", tpl.Name, tpl.Source)
	if tpl.Desc != "" {
		c.Printf("desc:     %s\n", tpl.Desc)
	}
	c.Printf("path:     %s\n", tpl.Path)
	if len(tpl.Vars) > 0 {
		c.Println("vars:")
		for _, vn := range sortedVarNames(tpl.Vars) {
			spec := tpl.Vars[vn]
			line := "  " + vn
			if spec.Required {
				line += " (required)"
			}
			if spec.Default != "" {
				line += " default=" + spec.Default
			}
			if spec.Desc != "" {
				line += "  " + spec.Desc
			}
			c.Println(line)
		}
	}
	for _, w := range rendered.Warnings {
		c.Println("warning: " + w)
	}
	c.Println("prompt:")
	c.Println(rendered.Prompt)
	return nil
}

// templateProject resolves the project key for the read-only template commands: an
// explicit -p wins, otherwise the current directory is matched against the locally
// configured projects (the same convenience `job run` offers). "" means "the global
// templates only".
func templateProject(explicit string) string {
	if explicit != "" {
		return explicit
	}
	cfg, _, err := config.Load(config.InputCfgFile)
	if err != nil {
		return ""
	}
	abs, err := filepath.Abs(".")
	if err != nil {
		return ""
	}
	if key, _, found := project.ResolveByCwd(cfg, abs); found {
		return key
	}
	return ""
}

func sortedVarNames(vars map[string]template.Var) []string {
	names := make([]string, 0, len(vars))
	for n := range vars {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// firstLineOf collapses an error message to its first line for a table cell.
func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
