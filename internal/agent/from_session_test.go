package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// suagLike mirrors the suag cli-agent shape: the prompt is a `--prompt "{{prompt}}"`
// PAIR, which is why from_session_args is appended after the template instead of
// being spliced before the prompt argument like model_args.
func suagLike() config.AgentConfig {
	return config.AgentConfig{
		Type:            TypeCLIAgent,
		Command:         "suag",
		Args:            []string{"run", "--output-format", "stream-json", "--prompt", "{{prompt}}"},
		InteractiveArgs: []string{"chat"},
		FromSessionArgs: []string{"--from", "{{from_session}}"},
	}
}

func TestBuildFromSessionArgs(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentConfig{
		"suag":  suagLike(),
		"plain": {Type: TypeCLIAgent, Command: "x", Args: []string{"{{prompt}}"}},
		"acp":   {Type: TypeACPAgent, Command: "x"},
		"exec":  {Type: TypeExec},
	}}

	t.Run("with value: appended after the template, before agent args", func(t *testing.T) {
		res, err := BuildFrom(cfg, "suag", "go on", nil, Vars{}, BuildOptions{FromSession: "s-old", AgentArgs: []string{"--x"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"run", "--output-format", "stream-json", "--prompt", "go on", "--from", "s-old", "--x"}
		if !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("argv = %#v, want %#v", res.Args, want)
		}
	})

	t.Run("interactive shape", func(t *testing.T) {
		res, err := BuildFrom(cfg, "suag", "", nil, Vars{}, BuildOptions{FromSession: "s-old", Interactive: true, AllowEmptyPrompt: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"chat", "--from", "s-old"}; !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("argv = %#v, want %#v", res.Args, want)
		}
	})

	t.Run("without value: argv unchanged and template not mutated", func(t *testing.T) {
		res, err := BuildFrom(cfg, "suag", "go on", nil, Vars{}, BuildOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"run", "--output-format", "stream-json", "--prompt", "go on"}; !reflect.DeepEqual(res.Args, want) {
			t.Fatalf("argv = %#v, want %#v", res.Args, want)
		}
		if got := cfg.Agents["suag"].Args; len(got) != 5 {
			t.Fatalf("template mutated: %#v", got)
		}
	})

	for _, key := range []string{"plain", "acp", "exec"} {
		t.Run(key+" refuses", func(t *testing.T) {
			_, err := BuildFrom(cfg, key, "p", []string{"true"}, Vars{}, BuildOptions{FromSession: "s-old"})
			if err == nil || !strings.Contains(err.Error(), "from-session") && !strings.Contains(err.Error(), "from_session_args") {
				t.Fatalf("err = %v, want a from-session refusal", err)
			}
		})
	}
}

func TestRenderFromSessionVar(t *testing.T) {
	got := Render([]string{"--from={{from_session}}"}, Vars{FromSession: "abc"})
	if !reflect.DeepEqual(got, []string{"--from=abc"}) {
		t.Fatalf("got %#v", got)
	}
}
