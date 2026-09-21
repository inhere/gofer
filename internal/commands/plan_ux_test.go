package commands

import (
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/jobstore"
)

func TestPlanIDValidation(t *testing.T) {
	cases := map[string]bool{
		"":                  false, // empty is handled before validation (server generates)
		"tun-01":            false, // 6 chars: too short
		"12345678":          false, // exactly 8: still too short
		"gofer-ux-260912":   true,
		"plan.v2:alpha_1-x": true,
		"has space1":        false,
		"slash/in-id":       false,
		"中文计划名称测试一二三":       false,
	}
	for id, want := range cases {
		if got := validPlanID(id); got != want {
			t.Errorf("validPlanID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestPlanTodoNoteOptionalString(t *testing.T) {
	var o optionalString
	if o.set {
		t.Fatal("zero value must be unset")
	}
	if err := o.Set(""); err != nil || !o.set || o.val != "" {
		t.Fatalf("explicit empty value: set=%v val=%q err=%v", o.set, o.val, err)
	}
}

func TestPlanTodoNoteResolve(t *testing.T) {
	str := func(s string) *string { return &s }
	assignee := "omp"
	cases := []struct {
		name       string
		status     string
		undone     bool
		note       optionalString
		appendNote string
		patch      jobstore.TodoPatch
		want       client.TodoUpdate
		wantErr    bool
	}{
		{"bare means done", "", false, optionalString{}, "", jobstore.TodoPatch{}, client.TodoUpdate{Status: "done"}, false},
		{"--undone means pending", "", true, optionalString{}, "", jobstore.TodoPatch{}, client.TodoUpdate{Status: "pending"}, false},
		{"--status wins", "doing", true, optionalString{}, "", jobstore.TodoPatch{}, client.TodoUpdate{Status: "doing"}, false},
		{"--note alone keeps status", "", false, optionalString{val: "x", set: true}, "", jobstore.TodoPatch{}, client.TodoUpdate{Note: str("x")}, false},
		{"--note \"\" clears", "", false, optionalString{set: true}, "", jobstore.TodoPatch{}, client.TodoUpdate{Note: str("")}, false},
		{"--append-note alone keeps status", "", false, optionalString{}, "more", jobstore.TodoPatch{}, client.TodoUpdate{AppendNote: "more"}, false},
		{"--status + --append-note in one update", "done", false, optionalString{}, "more", jobstore.TodoPatch{}, client.TodoUpdate{Status: "done", AppendNote: "more"}, false},
		{"--note + --append-note conflict", "", false, optionalString{val: "x", set: true}, "more", jobstore.TodoPatch{}, client.TodoUpdate{}, true},
		// PLAN-02 P2: a dispatch field alone must NOT be read as the legacy bare
		// "done" — describing an item is not completing it.
		{"--assign alone keeps status", "", false, optionalString{}, "", jobstore.TodoPatch{Assignee: &assignee},
			client.TodoUpdate{TodoPatch: jobstore.TodoPatch{Assignee: &assignee}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTodoUpdate(tc.status, tc.undone, tc.note, tc.appendNote, tc.patch)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got.Status != tc.want.Status || got.AppendNote != tc.want.AppendNote {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			if (got.Note == nil) != (tc.want.Note == nil) || (got.Note != nil && *got.Note != *tc.want.Note) {
				t.Fatalf("note got %v, want %v", got.Note, tc.want.Note)
			}
			if (got.Assignee == nil) != (tc.want.Assignee == nil) {
				t.Fatalf("assignee got %v, want %v", got.Assignee, tc.want.Assignee)
			}
		})
	}
}

func TestPlanShowNoteMultiline(t *testing.T) {
	out := captureOutput(t, func() {
		printPlanTodos(gcli.NewCommand("show", "", nil), []client.Todo{
			{TodoID: "todo-1", Title: "multi", Note: "first line\nsecond line"},
		})
	})
	for _, want := range []string{"      note: first line\n", "            second line\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("multi-line note not indented as expected (missing %q):\n%s", want, out)
		}
	}
}
