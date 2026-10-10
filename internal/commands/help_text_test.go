package commands

import (
	"regexp"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/gcli/v3/gflag"
)

var planMarkerPattern = regexp.MustCompile(`\b(?:[A-Z]{1,5}-[0-9]{1,3}|E[0-9]{1,3}|P[0-9])\b`)

func TestHelpTextHasNoPlanMarkers(t *testing.T) {
	var walk func(*gcli.Command, string)
	walk = func(cmd *gcli.Command, path string) {
		cmd.Init()
		texts := []string{cmd.Desc, cmd.Help, cmd.Examples}
		cmd.Flags.FSet().VisitAll(func(flag *gflag.Flag) { texts = append(texts, flag.Usage) })
		for _, text := range texts {
			if match := planMarkerPattern.FindString(text); match != "" {
				t.Fatalf("%s help contains plan marker %q: %s", path, match, text)
			}
		}
		for name, sub := range cmd.Commands() {
			walk(sub, path+" "+name)
		}
	}
	for name, cmd := range NewApp("test").Commands() {
		walk(cmd, strings.TrimSpace(name))
	}
}

// TestJobRunBoolFlagUsageHasNoBackticks: gcli renders the first `quoted` word of a flag's
// usage as its value name, so a bool flag whose usage quotes a command reads as if it
// took that value (it showed `--hold gofer job approve`). Bool flags take no value.
func TestJobRunBoolFlagUsageHasNoBackticks(t *testing.T) {
	jobCmd, ok := NewApp("test").Command("job")
	if !ok {
		t.Fatal("job command not found")
	}
	jobCmd.Init()
	run, ok := jobCmd.Command("run")
	if !ok {
		t.Fatal("job run command not found")
	}
	run.Init()
	run.Flags.FSet().VisitAll(func(flag *gflag.Flag) {
		if bf, ok := flag.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() && strings.Contains(flag.Usage, "`") {
			t.Errorf("job run --%s: bool flag usage has a backtick-quoted value name: %s", flag.Name, flag.Usage)
		}
	})
}
