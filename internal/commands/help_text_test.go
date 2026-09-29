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
