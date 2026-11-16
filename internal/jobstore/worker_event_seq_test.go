package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func TestInsertWorkerJobEventDedupsBySeq(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.UpsertJob(termJob("w1", "running", 100, 100)))

	row1, ok, err := s.InsertWorkerJobEvent(JobEvent{JobID: "w1", Type: "job.permission_requested", At: 1}, 1)
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.True(t, row1 > 0)
	_, ok, err = s.InsertWorkerJobEvent(JobEvent{JobID: "w1", Type: "job.permission_answered", At: 2}, 2)
	assert.NoErr(t, err)
	assert.True(t, ok)

	// A replay of both (and of an even older one) inserts nothing and reports it.
	for _, seq := range []int64{1, 2} {
		row, ok, err := s.InsertWorkerJobEvent(JobEvent{JobID: "w1", Type: "job.permission_requested", At: 3}, seq)
		assert.NoErr(t, err)
		assert.False(t, ok)
		assert.Eq(t, int64(0), row)
	}
	evs, err := s.ListJobEvents("w1", 0)
	assert.NoErr(t, err)
	assert.Len(t, evs, 2)

	got, err := s.WorkerEventSeq("w1")
	assert.NoErr(t, err)
	assert.Eq(t, int64(2), got)

	// A gap is fine: the cursor jumps to the highest applied seq (the worker dropped
	// the lost events itself), and the next in-order one applies.
	_, ok, err = s.InsertWorkerJobEvent(JobEvent{JobID: "w1", Type: "job.verify_started", At: 4}, 5)
	assert.NoErr(t, err)
	assert.True(t, ok)
	got, _ = s.WorkerEventSeq("w1")
	assert.Eq(t, int64(5), got)

	// UpsertJob must not reset the cursor.
	assert.NoErr(t, s.UpsertJob(termJob("w1", "done", 100, 200)))
	got, _ = s.WorkerEventSeq("w1")
	assert.Eq(t, int64(5), got)
}

func TestInsertWorkerJobEventValidation(t *testing.T) {
	s := openTest(t)
	_, _, err := s.InsertWorkerJobEvent(JobEvent{JobID: "nope", Type: "x", At: 1}, 1)
	assert.Err(t, err)
	assert.NoErr(t, s.UpsertJob(termJob("w2", "running", 1, 1)))
	_, _, err = s.InsertWorkerJobEvent(JobEvent{JobID: "w2", Type: "x", At: 1}, 0)
	assert.Err(t, err)
	_, _, err = s.InsertWorkerJobEvent(JobEvent{Type: "x", At: 1}, 1)
	assert.Err(t, err)
	n, err := s.WorkerEventSeq("unknown")
	assert.NoErr(t, err)
	assert.Eq(t, int64(0), n)
}

func TestWorkerEventSeqColumnMigrated(t *testing.T) {
	s := openTest(t)
	assert.True(t, tableHasColumn(t, s, "jobs", "worker_event_seq"))
}
