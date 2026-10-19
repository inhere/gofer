package jobstore

import (
	"errors"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func sampleAttachment(id string, expires int64) Attachment {
	return Attachment{
		ID: id, Caller: "alice", Mime: "image/png", Bytes: 10, SHA256: "ab", Width: 2, Height: 3,
		CreatedAt: 1000, ExpiresAt: expires, Path: "20261019/" + id,
	}
}

func TestAttachmentInsertGetBindDelete(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("a1", 0)))
	assert.Err(t, s.InsertAttachment(sampleAttachment("a1", 0)), "duplicate id")

	got, ok, err := s.GetAttachment("a1")
	assert.NoErr(t, err)
	assert.True(t, ok)
	assert.Eq(t, "alice", got.Caller)
	assert.Eq(t, "", got.ThreadID)
	assert.Eq(t, 2, got.Width)
	_, ok, err = s.GetAttachment("nope")
	assert.NoErr(t, err)
	assert.False(t, ok)

	assert.NoErr(t, s.BindAttachment("a1", "j:job-1", "job", "job-1"))
	got, _, _ = s.GetAttachment("a1")
	assert.Eq(t, "j:job-1", got.ThreadID)
	assert.Eq(t, "job", got.RefKind)
	assert.Eq(t, "job-1", got.RefID)
	assert.True(t, errors.Is(s.BindAttachment("a1", "j:job-2", "job", "job-2"), ErrAttachmentBound), "bound at most once")
	assert.True(t, errors.Is(s.BindAttachment("zz", "j:x", "job", "x"), ErrAttachmentNotFound))

	assert.NoErr(t, s.InsertAttachment(sampleAttachment("a2", 0)))
	assert.NoErr(t, s.BindAttachment("a2", "j:job-1", "job", "job-1"))
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("a3", 0)))
	list, err := s.ListAttachmentsByThread("j:job-1")
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(list))
	assert.Eq(t, "a1", list[0].ID)

	assert.True(t, errors.Is(s.DeleteUnsentAttachment("a1"), ErrAttachmentBound))
	assert.True(t, errors.Is(s.DeleteUnsentAttachment("zz"), ErrAttachmentNotFound))
	assert.NoErr(t, s.DeleteUnsentAttachment("a3"))
	existed, err := s.DeleteAttachment("a1")
	assert.NoErr(t, err)
	assert.True(t, existed)
	existed, _ = s.DeleteAttachment("a1")
	assert.False(t, existed)
}

func TestPruneAttachments(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("old", 2000)))
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("oldsent", 2500)))
	assert.NoErr(t, s.BindAttachment("oldsent", "j:x", "job", "x"))
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("future", 9000)))
	assert.NoErr(t, s.InsertAttachment(sampleAttachment("forever", 0)))

	none, err := s.PruneAttachments(1999)
	assert.NoErr(t, err)
	assert.Eq(t, 0, len(none))

	paths, err := s.PruneAttachments(3000)
	assert.NoErr(t, err)
	assert.Eq(t, 2, len(paths))
	_, ok, _ := s.GetAttachment("old")
	assert.False(t, ok)
	for _, keep := range []string{"future", "forever"} {
		_, ok, _ := s.GetAttachment(keep)
		assert.True(t, ok, keep)
	}
}
