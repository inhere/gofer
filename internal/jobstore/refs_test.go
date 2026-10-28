package jobstore

import (
	"testing"

	"github.com/inhere/gofer/internal/refs"
)

func TestReplaceRefsReplacesEditedField(t *testing.T) {
	s, err := Open(t.TempDir() + "/jobs.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ReplaceRefs("job", "job-1", "title", []refs.Reference{{Kind: refs.KindIssue, ID: "gofer-abcd"}, {Kind: refs.KindPlan, ID: "plan-old"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceRefs("job", "job-1", "title", []refs.Reference{{Kind: refs.KindTodo, ID: "todo-new"}}); err != nil {
		t.Fatal(err)
	}
	old, err := s.ListRefs("plan", "plan-old")
	if err != nil || len(old) != 0 {
		t.Fatalf("old refs = %#v, err=%v", old, err)
	}
	got, err := s.ListRefs("todo", "todo-new")
	if err != nil || len(got) != 1 || got[0].SrcID != "job-1" || got[0].Field != "title" {
		t.Fatalf("new refs = %#v, err=%v", got, err)
	}
}
