package agent

import (
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// ImageArgs expands the template once per path: one image is one flag pair, and the argv
// elements stay separate so a path with spaces is never re-tokenised.
func TestImageArgsExpandsOncePerPath(t *testing.T) {
	tmpl := []string{"--image", "{{path}}"}
	got := ImageArgs(tmpl, []string{`D:\a b\one.png`, "/tmp/two.png"})
	want := []string{"--image", `D:\a b\one.png`, "--image", "/tmp/two.png"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ImageArgs = %#v, want %#v", got, want)
	}
	for _, a := range got {
		if strings.Contains(a, "{{path}}") {
			t.Fatalf("placeholder left in argv: %#v", got)
		}
	}
	// The template itself is never modified.
	if !reflect.DeepEqual(tmpl, []string{"--image", "{{path}}"}) {
		t.Fatalf("template modified: %#v", tmpl)
	}
}

func TestImageArgsEmptyInputs(t *testing.T) {
	if got := ImageArgs(nil, []string{"/a.png"}); got != nil {
		t.Fatalf("no template: %#v", got)
	}
	if got := ImageArgs([]string{"--image", "{{path}}"}, nil); got != nil {
		t.Fatalf("no paths: %#v", got)
	}
}

// A path embedded in a longer template element renders inside it, still as one element.
func TestImageArgsEmbeddedPlaceholder(t *testing.T) {
	got := ImageArgs([]string{"--image={{path}}"}, []string{"/a.png", "/b.png"})
	if want := []string{"--image=/a.png", "--image=/b.png"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ImageArgs = %#v, want %#v", got, want)
	}
}

// The built-in suga template is the agent the arg delivery was added for.
func TestBuiltinSugaTakesImagesAsArgv(t *testing.T) {
	ac, ok := builtinTemplates["suga"]
	if !ok {
		t.Fatal("suga template missing")
	}
	if ac.ImageInput != config.ImageInputArg {
		t.Fatalf("suga image_input = %q, want %q", ac.ImageInput, config.ImageInputArg)
	}
	if got := ImageArgs(ac.ImageArgs, []string{"/a.png"}); !reflect.DeepEqual(got, []string{"--image", "/a.png"}) {
		t.Fatalf("suga image argv = %#v", got)
	}
}
