package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// sampleMessage builds an unread MessageRecord for tests.
func sampleMessage(id, toAgent string, createdAt int64) MessageRecord {
	return MessageRecord{
		ID:        id,
		ToAgent:   toAgent,
		FromAgent: "sender",
		ToSpec:    toAgent,
		Kind:      "task",
		Body:      "review PR",
		Ref:       "pr-1",
		Status:    MessageUnread,
		CreatedAt: createdAt,
	}
}

func TestInsertMessagesBatchAndListInbox(t *testing.T) {
	s := openTest(t)

	// One role:/broadcast fan-out → two recipient rows, one tx.
	recs := []MessageRecord{
		sampleMessage("m1", "bob", 100),
		sampleMessage("m2", "carol", 100),
	}
	recs[0].ToSpec = "role:reviewer"
	recs[1].ToSpec = "role:reviewer"
	assert.NoErr(t, s.InsertMessages(recs))

	bobInbox, err := s.ListInbox("bob", false)
	assert.NoErr(t, err)
	assert.Len(t, bobInbox, 1)
	assert.Eq(t, "m1", bobInbox[0].ID)
	assert.Eq(t, "role:reviewer", bobInbox[0].ToSpec)
	assert.Eq(t, "task", bobInbox[0].Kind)
	assert.Eq(t, "review PR", bobInbox[0].Body)
	assert.Eq(t, "pr-1", bobInbox[0].Ref)
	assert.Eq(t, MessageUnread, bobInbox[0].Status)

	carolInbox, err := s.ListInbox("carol", false)
	assert.NoErr(t, err)
	assert.Len(t, carolInbox, 1)
	assert.Eq(t, "m2", carolInbox[0].ID)
}

func TestInsertMessagesEmptyBatchNoop(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertMessages(nil))
}

func TestListInboxOrderedAndUnreadOnly(t *testing.T) {
	s := openTest(t)

	assert.NoErr(t, s.InsertMessages([]MessageRecord{
		sampleMessage("c", "bob", 300),
		sampleMessage("a", "bob", 100),
		sampleMessage("b", "bob", 200),
	}))

	list, err := s.ListInbox("bob", false)
	assert.NoErr(t, err)
	assert.Len(t, list, 3)
	assert.Eq(t, "a", list[0].ID) // created_at 100
	assert.Eq(t, "b", list[1].ID)
	assert.Eq(t, "c", list[2].ID)
}

func TestMarkReadConsumesFromUnreadInbox(t *testing.T) {
	s := openTest(t)
	assert.NoErr(t, s.InsertMessages([]MessageRecord{
		sampleMessage("m1", "bob", 100),
		sampleMessage("m2", "bob", 200),
	}))

	// Consume m1.
	assert.NoErr(t, s.MarkRead([]string{"m1"}, 500))

	unread, err := s.ListInbox("bob", false)
	assert.NoErr(t, err)
	assert.Len(t, unread, 1)
	assert.Eq(t, "m2", unread[0].ID)

	// includeRead returns the full history; m1 now read with read_at stamped.
	all, err := s.ListInbox("bob", true)
	assert.NoErr(t, err)
	assert.Len(t, all, 2)
	assert.Eq(t, MessageRead, all[0].Status)
	assert.Eq(t, int64(500), all[0].ReadAt)

	// MarkRead with no ids is a no-op.
	assert.NoErr(t, s.MarkRead(nil, 600))
}

func TestPruneMessagesDropsReadAndExpired(t *testing.T) {
	s := openTest(t)

	read := sampleMessage("read", "bob", 100)
	freshUnread := sampleMessage("fresh", "bob", 200)
	expired := sampleMessage("expired", "bob", 100)
	expired.ExpiresAt = 150 // past TTL when now=300
	liveTTL := sampleMessage("live", "bob", 100)
	liveTTL.ExpiresAt = 999 // TTL still in the future
	assert.NoErr(t, s.InsertMessages([]MessageRecord{read, freshUnread, expired, liveTTL}))
	assert.NoErr(t, s.MarkRead([]string{"read"}, 250))

	// now=300: prunes "read" (status=read) + "expired" (expires_at 150 < 300).
	n, err := s.PruneMessages(300)
	assert.NoErr(t, err)
	assert.Eq(t, 2, n)

	all, err := s.ListInbox("bob", true)
	assert.NoErr(t, err)
	assert.Len(t, all, 2) // fresh + live remain
}

func TestInsertMessagesRejectsEmpty(t *testing.T) {
	s := openTest(t)
	assert.Err(t, s.InsertMessages([]MessageRecord{{ToAgent: "bob", Kind: "task"}}))
	assert.Err(t, s.InsertMessages([]MessageRecord{{ID: "m1", Kind: "task"}}))
}
