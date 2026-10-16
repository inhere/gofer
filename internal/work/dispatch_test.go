package work

import (
	"errors"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestDispatchOnlyCapturedItems(t *testing.T) {
	svc, st, _ := newSvc(t)
	c := mustCaptured(t, st, "想法")

	got, err := svc.Captured(c.ID)
	assert.NoErr(t, err)
	assert.Eq(t, "想法", got.Text)

	_, err = svc.Captured("w-nope")
	assert.True(t, errors.Is(err, jobstore.ErrWorkItemNotFound))

	w, err := svc.DispatchClose(c.ID, DispatchArchive, "human:a")
	assert.NoErr(t, err)
	assert.Eq(t, jobstore.WorkDone, w.Status)
	j, err := st.ListWorkJournal(c.ID, 20, 0)
	assert.NoErr(t, err)
	found := false
	for _, e := range j {
		found = found || strings.Contains(e.Text, "归档")
	}
	assert.True(t, found)

	// It left the capture box: every further dispatch is ErrNotCaptured.
	_, err = svc.DispatchClose(c.ID, DispatchDrop, "human:a")
	assert.True(t, errors.Is(err, ErrNotCaptured))
	_, _, err = svc.DispatchToTodo(c.ID, ToTodoInput{By: "human:a"})
	assert.True(t, errors.Is(err, ErrNotCaptured))
	_, err = svc.FinishDispatch(c.ID, jobstore.WorkActive, "", "", "", "by:x")
	assert.True(t, errors.Is(err, ErrNotCaptured))

	d := mustCaptured(t, st, "另一个")
	_, err = svc.DispatchClose(d.ID, "bogus", "human:a")
	assert.True(t, errors.Is(err, jobstore.ErrWorkInvalid))
	assert.Eq(t, jobstore.WorkCaptured, statusOf(t, st, d.ID))
}
