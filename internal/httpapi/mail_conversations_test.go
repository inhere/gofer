package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// The mailbox read views of plan M1 (design 2026-10-20-mail-conversations-ui-design.md
// 「四、读接口补字段」): the persona of a letter's two ends and the conversation aggregation.

// mailListBody is the answer of GET /v1/mail.
type mailListBody struct {
	Mail         []mailView           `json:"mail"`
	Count        int                  `json:"count"`
	HasMore      bool                 `json:"has_more"`
	Conversation *mailConversationRef `json:"conversation"`
}

// convListBody is the answer of GET /v1/mail/conversations.
type convListBody struct {
	Conversations []mailConversationView `json:"conversations"`
	Count         int                    `json:"count"`
	// Total is the number of conversations that match box / project, before limit / offset.
	Total int `json:"total"`
}

func mailDecode(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
}

// convOf returns the conversation of the pair (unordered) or fails.
func convOf(t *testing.T, body, addrA, addrB string) mailConversationView {
	t.Helper()
	want := mailConversationKey(mailParty{Address: addrA}, mailParty{Address: addrB})
	var list convListBody
	mailDecode(t, body, &list)
	for _, cv := range list.Conversations {
		if cv.Key == want {
			return cv
		}
	}
	t.Fatalf("conversation %s not in %s", want, body)
	return mailConversationView{}
}

// TestMailLetterPersonas: from / to carry the persona of their end — a session's topic (else
// title, else agent + project), a job's title (else `job <short id>`), a caller's capture box —
// and a party nobody can describe is left without one.
func TestMailLetterPersonas(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	toS := func(sid string) int64 {
		return e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
			ToAddress: "session:" + sid, ToKind: jobstore.MailKindSession, ToID: sid})
	}
	topicLetter := toS(e.sB) // sB declared the topic "billing refactor"
	plainLetter := toS(e.sA) // sA has no topic: agent + project
	jobLetter := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "job:" + e.jA, ToKind: jobstore.MailKindJob, ToID: e.jA, JobID: e.jA})
	goneLetter := toS("sess-gone")

	_, body := e.req(t, mailTokBob, "", "GET", fmt.Sprintf("/v1/mail/%d", topicLetter), nil)
	var one mailView
	mailDecode(t, body, &one)
	if one.To.Persona == nil {
		t.Fatalf("session persona missing: %s", body)
	}
	p := one.To.Persona
	if p.Name != "billing refactor" || p.Agent != "claude" || p.ProjectKey != "p1" || p.Runner != "local" ||
		p.State != jobstore.SessionIdle || p.Cwd != "." || p.LastSeenAt == 0 {
		t.Errorf("topic session persona = %+v, want the topic and the session's metadata", p)
	}
	if one.From.Persona == nil || one.From.Persona.Name != "bob 的收集箱" {
		t.Errorf("caller persona = %+v, want 「bob 的收集箱」", one.From.Persona)
	}

	// The listing carries the same personas; a session without a topic falls back to agent +
	// project, and a deleted one has none.
	_, body = e.req(t, mailTokBob, "", "GET", "/v1/mail", nil)
	var list mailListBody
	mailDecode(t, body, &list)
	got := map[int64]mailView{}
	for _, v := range list.Mail {
		got[v.ID] = v
	}
	if p := got[plainLetter].To.Persona; p == nil || p.Name != "claude · p1" {
		t.Errorf("title-less session persona = %+v, want 「claude · p1」", p)
	}
	if p := got[jobLetter].To.Persona; p == nil || p.Name != "job job-alic" || p.Agent != "exec" ||
		p.ProjectKey != "p1" || p.Runner != "worker" || p.State != job.StatusRunning {
		t.Errorf("job persona = %+v, want its short-id name and metadata", p)
	}
	if p := got[goneLetter].To.Persona; p != nil {
		t.Errorf("deleted session persona = %+v, want none", p)
	}

	// A server without named callers still names the box by the caller id; no caller at all is
	// just the box.
	box := callerBoxPersona("", "")
	if box.Name != "收集箱" {
		t.Errorf("anonymous box persona = %q, want 收集箱", box.Name)
	}
	named := callerBoxPersona("admin", "default")
	if named.Name != "admin 的收集箱" {
		t.Errorf("named box persona = %q, want 「admin 的收集箱」", named.Name)
	}
}

// TestMailConversationsAggregate: letters between the same two parties are ONE conversation
// whichever way they went and whatever thread they were in, with the counts and the newest
// letter.
func TestMailConversationsAggregate(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	// An older conversation: alice's session and her own job.
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sA, FromCaller: "alice",
		ToAddress: "job:" + e.jA, ToKind: jobstore.MailKindJob, ToID: e.jA, JobID: e.jA, Body: "job letter"})
	// bob's capture box <-> alice's own session: two threads, both directions.
	first := e.letter(t, jobstore.MailInput{ThreadID: "t-1", FromKind: jobstore.MailKindCaller, FromCaller: "bob", Urgent: true,
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "first"})
	e.letter(t, jobstore.MailInput{ThreadID: "t-1", ReplyTo: first, FromKind: jobstore.MailKindSession, FromID: e.sA,
		FromCaller: "alice", ToAddress: "caller", ToKind: jobstore.MailKindCaller, ToCaller: "bob", Body: "second"})
	newest := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sA, FromCaller: "alice",
		ToAddress: "caller", ToKind: jobstore.MailKindCaller, ToCaller: "bob", Body: "third"})
	if _, changed, err := e.s.jobs.Meta().MarkMailRead(first); err != nil || !changed {
		t.Fatalf("mark first read: %v %v", changed, err)
	}

	_, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	var list convListBody
	mailDecode(t, body, &list)
	if list.Count != 2 || len(list.Conversations) != 2 {
		t.Fatalf("conversations = %d (%s), want 2", list.Count, body)
	}
	// The conversation with the newest letter comes first.
	if list.Conversations[0].Key != mailConversationKey(mailParty{Address: "session:" + e.sA}, mailParty{Address: "caller:bob"}) {
		t.Errorf("first conversation = %q, want the session/bob-box one", list.Conversations[0].Key)
	}
	cv := convOf(t, body, "session:"+e.sA, "caller:bob")
	if cv.Count != 3 || cv.Unread != 2 || cv.Urgent != 1 {
		t.Errorf("conversation = %d letters, %d unread, %d urgent; want 3 / 2 / 1", cv.Count, cv.Unread, cv.Urgent)
	}
	if cv.Last.ID != newest || cv.Last.Body != "third" || cv.ActivityAt != cv.Last.CreatedAt {
		t.Errorf("last letter = %d %q (%d), want letter %d", cv.Last.ID, cv.Last.Body, cv.ActivityAt, newest)
	}
	// a is the party that wrote first (design 决策 4 puts it on the left).
	if cv.A.Kind != jobstore.MailKindCaller || cv.A.Caller != "bob" || cv.B.ID != e.sA {
		t.Errorf("conversation parties = %+v / %+v, want bob's box first, then %s", cv.A, cv.B, e.sA)
	}
	if cv.A.Persona == nil || cv.A.Persona.Name != "bob 的收集箱" || cv.B.Persona == nil || cv.B.Persona.Name != "claude · p1" {
		t.Errorf("conversation personas = %+v / %+v", cv.A.Persona, cv.B.Persona)
	}
	if cv.Last.To.Persona == nil || cv.Last.To.Persona.Name != "bob 的收集箱" {
		t.Errorf("last mail to persona = %+v, want bob's box", cv.Last.To.Persona)
	}

	// box=out keeps the conversations with a letter alice sent, box=in the ones she received.
	_, out := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?box=out", nil)
	if cv := convOf(t, out, "session:"+e.sA, "caller:bob"); cv.Count != 2 {
		t.Errorf("box=out conversation = %d letters, want the 2 alice sent", cv.Count)
	}
	_, in := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?box=in", nil)
	if cv := convOf(t, in, "session:"+e.sA, "caller:bob"); cv.Count != 1 {
		t.Errorf("box=in conversation = %d letters, want only the one bob's box sent", cv.Count)
	}
	// Paging: one conversation per page.
	_, page := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?limit=1&offset=1", nil)
	var pageList convListBody
	mailDecode(t, page, &pageList)
	if pageList.Count != 1 || pageList.Conversations[0].Key == list.Conversations[0].Key {
		t.Errorf("page 2 = %s, want the other conversation", page)
	}
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?box=sideways", nil); status != http.StatusBadRequest {
		t.Errorf("invalid box = %d, want 400", status)
	}
}

// TestMailConversationsProjectAndTotal (gofer-av8f): `?project=` narrows the aggregation by the
// project of a conversation's parties (either end), and `total` counts the matching conversations
// before paging, so a counter can ask the server for one row and still say how many there are.
func TestMailConversationsProjectAndTotal(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	// bob's box → alice's p1 session, alice → bob's p2 session, and bob's p2 session → alice's p1
	// session (a conversation that belongs to BOTH projects).
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "p1 only"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "session:" + e.sP2, ToKind: jobstore.MailKindSession, ToID: e.sP2, Body: "p2 only"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sP2, FromCaller: "bob",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "across"})

	list := func(q string) convListBody {
		t.Helper()
		status, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations"+q, nil)
		if status != http.StatusOK {
			t.Fatalf("GET /v1/mail/conversations%s = %d (%s)", q, status, body)
		}
		var out convListBody
		mailDecode(t, body, &out)
		return out
	}
	keyOf := func(x, y string) string {
		return mailConversationKey(mailParty{Address: x}, mailParty{Address: y})
	}
	// Every conversation of the mailbox, and the same answer for a single row: total is the number
	// before limit, count the size of the page.
	if all := list(""); all.Count != 3 || all.Total != 3 {
		t.Errorf("all = %d of %d conversations, want 3 of 3", all.Count, all.Total)
	}
	one := list("?limit=1")
	if one.Count != 1 || one.Total != 3 || len(one.Conversations) != 1 {
		t.Errorf("limit=1 = %d of %d (%d rows), want 1 of 3", one.Count, one.Total, len(one.Conversations))
	}
	// A conversation whose either end is in the project is in it; one across two projects shows
	// under both.
	p1 := list("?project=p1")
	if p1.Total != 2 || len(p1.Conversations) != 2 {
		t.Fatalf("project=p1 = %d of %d, want the 2 conversations with a p1 party", p1.Count, p1.Total)
	}
	got1 := map[string]bool{}
	for _, cv := range p1.Conversations {
		got1[cv.Key] = true
	}
	for _, want := range []string{keyOf("session:"+e.sA, "caller:bob"), keyOf("session:"+e.sA, "session:"+e.sP2)} {
		if !got1[want] {
			t.Errorf("project=p1 misses %q", want)
		}
	}
	p2 := list("?project=p2")
	if p2.Total != 2 {
		t.Fatalf("project=p2 = %d, want 2", p2.Total)
	}
	got2 := map[string]bool{}
	for _, cv := range p2.Conversations {
		got2[cv.Key] = true
	}
	for _, want := range []string{keyOf("session:"+e.sP2, "caller:alice"), keyOf("session:"+e.sA, "session:"+e.sP2)} {
		if !got2[want] {
			t.Errorf("project=p2 misses %q", want)
		}
	}
	// A project nobody is in, and a project combined with a box that has nothing in it.
	if none := list("?project=p-none"); none.Total != 0 || len(none.Conversations) != 0 {
		t.Errorf("project=p-none = %d conversations, want none", none.Total)
	}
	if in := list("?project=p2&box=in"); in.Total != 1 {
		t.Errorf("project=p2&box=in = %d, want the one conversation bob's p2 session wrote", in.Total)
	}
	if out := list("?project=p2&box=out"); out.Total != 1 {
		t.Errorf("project=p2&box=out = %d, want the one alice sent to p2", out.Total)
	}
}

// TestMailConversationsPermission: the aggregation is the listing's filter — a letter the caller
// may not see neither counts nor names its parties.
func TestMailConversationsPermission(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "mine"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "dave",
		ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB, Body: "not yours"})

	_, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	var list convListBody
	mailDecode(t, body, &list)
	if list.Count != 1 {
		t.Errorf("alice's conversations = %s, want her own only", body)
	}
	for _, cv := range list.Conversations {
		if strings.Contains(cv.Key, e.sB) || strings.Contains(cv.Key, "dave") {
			t.Errorf("conversation %q discloses a letter alice may not see", cv.Key)
		}
		if cv.B.Persona != nil && cv.B.ID == e.sB {
			t.Errorf("conversation %q carries bob's session persona", cv.Key)
		}
	}
	// With the permission the same letter shows up, in its own conversation.
	e.grant(authz.PermSessionReplyOthers)
	_, body = e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	cv := convOf(t, body, "caller:dave", "session:"+e.sB)
	if cv.Count != 1 || cv.Unread != 1 {
		t.Errorf("visible conversation = %+v, want the dave letter", cv)
	}
}

// TestMailConversationPeerWith: ?peer=&with= returns exactly one conversation, oldest first.
func TestMailConversationPeerWith(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "in"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sA, FromCaller: "alice",
		ToAddress: "caller", ToKind: jobstore.MailKindCaller, ToCaller: "bob", Body: "out"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "job:" + e.jA, ToKind: jobstore.MailKindJob, ToID: e.jA, JobID: e.jA, Body: "elsewhere"})

	path := "/v1/mail?peer=caller:bob&with=session:" + e.sA
	_, body := e.req(t, mailTokAlice, "", "GET", path, nil)
	var list mailListBody
	mailDecode(t, body, &list)
	if list.Count != 2 || len(list.Mail) != 2 {
		t.Fatalf("peer/with = %s, want the two letters of that conversation", body)
	}
	if list.Mail[0].Body != "in" || list.Mail[1].Body != "out" {
		t.Errorf("peer/with order = %q,%q, want oldest first", list.Mail[0].Body, list.Mail[1].Body)
	}
	// The same pair written the other way round is the same conversation.
	_, swapped := e.req(t, mailTokAlice, "", "GET", "/v1/mail?peer=session:"+e.sA+"&with=caller:bob", nil)
	var swappedList mailListBody
	mailDecode(t, swapped, &swappedList)
	if swappedList.Count != 2 {
		t.Errorf("swapped peer/with = %s, want the same 2 letters", swapped)
	}
	// A bare `caller` is the caller's OWN box: nothing was exchanged between alice's box and her
	// session here.
	_, own := e.req(t, mailTokAlice, "", "GET", "/v1/mail?peer=caller&with=session:"+e.sA, nil)
	var ownList mailListBody
	mailDecode(t, own, &ownList)
	if ownList.Count != 0 {
		t.Errorf("bare caller peer = %s, want alice's own box (no such letters)", own)
	}
	// One of the two names must be given.
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?peer=caller", nil); status != http.StatusBadRequest {
		t.Errorf("peer alone = %d, want 400", status)
	}
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?peer=caller&with=bogus:x", nil); status != http.StatusBadRequest {
		t.Errorf("unknown scheme = %d, want 400", status)
	}
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?peer=topic:x&with=caller", nil); status != http.StatusBadRequest {
		t.Errorf("a search address = %d, want 400", status)
	}
}

// TestMailListUnrestrictedCallerUnchanged: for a caller with no ceiling the listing still shows
// exactly her own letters, with the fields it always had — the persona is purely additive.
func TestMailListUnrestrictedCallerUnchanged(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	own := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "alice",
		ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB})
	incoming := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA})
	others := e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "dave",
		ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB})

	_, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail", nil)
	var list mailListBody
	mailDecode(t, body, &list)
	got := map[int64]mailView{}
	for _, v := range list.Mail {
		got[v.ID] = v
	}
	if list.Count != 2 || got[own].ID != own || got[incoming].ID != incoming {
		t.Fatalf("unrestricted listing = %s, want her own two letters", body)
	}
	if _, ok := got[others]; ok {
		t.Errorf("unrestricted listing shows another caller's letter: %s", body)
	}
	// The pre-existing party fields are untouched by the additive persona.
	in := got[incoming]
	if in.From.Kind != jobstore.MailKindCaller || in.From.Caller != "bob" || in.From.Display != "调用方 bob" ||
		in.To.Kind != jobstore.MailKindSession || in.To.ID != e.sA || in.ToAddress != "session:"+e.sA {
		t.Errorf("letter view = %+v, want the fields it always had", in)
	}
	out := got[own]
	if out.From.Kind != jobstore.MailKindCaller || out.From.Caller != "alice" || out.To.Display != "" {
		t.Errorf("sent letter view = %+v, want the fields it always had", out)
	}
	// Nothing was hidden by the aggregation either: one conversation per pair she may see.
	_, conv := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	var list2 convListBody
	mailDecode(t, conv, &list2)
	if list2.Count != 2 {
		t.Errorf("unrestricted conversations = %s, want 2", conv)
	}
}

// TestMailConversationID: a conversation has one stable short id derived from its key, so the
// same pair of people always gets the same id whichever way their letters went.
func TestMailConversationID(t *testing.T) {
	key := mailConversationKey(mailParty{Address: "session:s1"}, mailParty{Address: "caller:bob"})
	one := mailConversationID(key)
	swapped := mailConversationID(mailConversationKey(mailParty{Address: "caller:bob"}, mailParty{Address: "session:s1"}))
	if one != swapped {
		t.Errorf("conversation id depends on the direction: %q vs %q", one, swapped)
	}
	if len(one) != len(mailConvIDPrefix)+mailConvIDHex || !strings.HasPrefix(one, mailConvIDPrefix) {
		t.Errorf("conversation id = %q, want %s + %d hex chars", one, mailConvIDPrefix, mailConvIDHex)
	}
	if again := mailConversationID(key); again != one {
		t.Errorf("conversation id is not stable: %q then %q", one, again)
	}
	if other := mailConversationID(mailConversationKey(mailParty{Address: "session:s2"}, mailParty{Address: "caller:bob"})); other == one {
		t.Errorf("two different conversations share the id %q", one)
	}
}

// TestMailConversationByID: the conversations list and the timeline both carry the short id, and
// `?conv=<id>` reads that one conversation — a 404 when the caller has no such conversation,
// which is the same answer as a letter the caller may not read.
func TestMailConversationByID(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: "in"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sA, FromCaller: "alice",
		ToAddress: "caller", ToKind: jobstore.MailKindCaller, ToCaller: "bob", Body: "out"})
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
		ToAddress: "job:" + e.jA, ToKind: jobstore.MailKindJob, ToID: e.jA, JobID: e.jA, Body: "elsewhere"})

	_, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	cv := convOf(t, body, "session:"+e.sA, "caller:bob")
	want := mailConversationID(cv.Key)
	if cv.ID != want {
		t.Fatalf("conversation id = %q, want %q", cv.ID, want)
	}

	// The conversation's own letters, oldest first, whichever way they went: the id needs no
	// peer=/with= and no knowledge of which end is which.
	_, list := e.req(t, mailTokAlice, "", "GET", "/v1/mail?conv="+cv.ID, nil)
	var letters mailListBody
	mailDecode(t, list, &letters)
	if letters.Count != 2 || letters.Mail[0].Body != "in" || letters.Mail[1].Body != "out" {
		t.Errorf("conv listing = %s, want the two letters of that conversation, oldest first", list)
	}
	if letters.Conversation == nil || letters.Conversation.ID != want || letters.Conversation.Key != cv.Key {
		t.Errorf("conv listing conversation = %+v, want id %q and key %q", letters.Conversation, want, cv.Key)
	}
	// The same conversation as one item.
	_, one := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?conv="+cv.ID, nil)
	var only convListBody
	mailDecode(t, one, &only)
	if only.Count != 1 || only.Conversations[0].ID != want || only.Conversations[0].Key != cv.Key {
		t.Errorf("conversations?conv= = %s, want that one conversation", one)
	}

	// An id that is not a conversation of this mailbox is a 404, not an empty list: a caller
	// cannot tell a typo from somebody else's conversation.
	otherID := mailConversationID(mailConversationKey(mailParty{Address: "session:" + e.sA}, mailParty{Address: "job:" + e.jA}))
	if otherID == cv.ID {
		t.Fatalf("the two conversations collide on %q", cv.ID)
	}
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "dave",
		ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB, Body: "not yours"})
	_, theirs := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations", nil)
	hiddenID := mailConversationID(mailConversationKey(mailParty{Address: "caller:dave"}, mailParty{Address: "session:" + e.sB}))
	if strings.Contains(theirs, hiddenID) {
		t.Fatalf("alice sees dave's conversation id: %s", theirs)
	}
	for _, path := range []string{"/v1/mail?conv=" + hiddenID, "/v1/mail/conversations?conv=" + hiddenID} {
		if status, body := e.req(t, mailTokAlice, "", "GET", path, nil); status != http.StatusNotFound {
			t.Errorf("GET %s = %d (%s), want 404", path, status, body)
		}
	}
	// ... and the same id becomes readable once the caller holds the permission.
	e.grant(authz.PermSessionReplyOthers)
	if status, body := e.req(t, mailTokAlice, "", "GET", "/v1/mail/conversations?conv="+hiddenID, nil); status != http.StatusOK ||
		!strings.Contains(body, hiddenID) {
		t.Errorf("permitted GET conv=%s = %d (%s), want that conversation", hiddenID, status, body)
	}
	// A malformed id and an id combined with peer/with are refused.
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?conv=bogus", nil); status != http.StatusBadRequest {
		t.Errorf("conv=bogus = %d, want 400", status)
	}
	if status, _ := e.req(t, mailTokAlice, "", "GET", "/v1/mail?conv="+cv.ID+"&peer=caller:bob&with=session:"+e.sA, nil); status != http.StatusBadRequest {
		t.Errorf("conv with peer/with = %d, want 400", status)
	}
}

// TestMailConversationPaging: a conversation is read newest first with order=desc, before= walks
// backwards from its oldest loaded letter, and both fill a full page AFTER the permission filter
// — the letters of that conversation this caller sent are dropped by box=in, so the store pages
// must not count them.
func TestMailConversationPaging(t *testing.T) {
	e := newMailEnv(t)
	e.grant()
	path := "/v1/mail?peer=caller:bob&with=session:" + e.sA
	list := func(p string) mailListBody {
		t.Helper()
		status, body := e.req(t, mailTokAlice, "", "GET", p, nil)
		if status != http.StatusOK {
			t.Fatalf("GET %s = %d (%s)", p, status, body)
		}
		var out mailListBody
		mailDecode(t, body, &out)
		return out
	}
	// Eight letters bob sent to alice's session, each followed by one of her answers: with box=in
	// only the first kind is hers to read, and the two kinds alternate in id order.
	inIDs := make([]int64, 0, 8)
	for i := 0; i < 8; i++ {
		inIDs = append(inIDs, e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "bob",
			ToAddress: "session:" + e.sA, ToKind: jobstore.MailKindSession, ToID: e.sA, Body: fmt.Sprintf("in-%d", i)}))
		e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindSession, FromID: e.sA, FromCaller: "alice",
			ToAddress: "caller", ToKind: jobstore.MailKindCaller, ToCaller: "bob", Body: fmt.Sprintf("out-%d", i)})
	}
	// Another caller's letter sits in the middle of the mailbox and must not fill the page.
	e.letter(t, jobstore.MailInput{FromKind: jobstore.MailKindCaller, FromCaller: "dave",
		ToAddress: "session:" + e.sB, ToKind: jobstore.MailKindSession, ToID: e.sB, Body: "elsewhere"})

	page := list(path + "&box=in&order=desc&limit=6")
	if page.Count != 6 || !page.HasMore {
		t.Fatalf("newest page = count %d has_more %v, want 6 / true", page.Count, page.HasMore)
	}
	if page.Mail[0].ID != inIDs[7] || page.Mail[5].ID != inIDs[2] {
		t.Errorf("newest page ids = %d…%d, want %d…%d (newest first)", page.Mail[0].ID, page.Mail[5].ID, inIDs[7], inIDs[2])
	}
	older := list(path + "&box=in&order=desc&limit=6&before=" + fmt.Sprint(page.Mail[5].ID))
	if older.Count != 2 || older.HasMore {
		t.Fatalf("older page = count %d has_more %v, want the 2 remaining letters and no more", older.Count, older.HasMore)
	}
	if older.Mail[0].ID != inIDs[1] || older.Mail[1].ID != inIDs[0] {
		t.Errorf("older page ids = %d,%d, want %d,%d", older.Mail[0].ID, older.Mail[1].ID, inIDs[1], inIDs[0])
	}
	// A page of exactly what is left says so.
	if all := list(path + "&box=in&order=desc&limit=8"); all.Count != 8 || all.HasMore {
		t.Errorf("full page = count %d has_more %v, want 8 / false", all.Count, all.HasMore)
	}
	// A conversation still reads from its first letter by default, with the same has_more.
	asc := list(path + "&box=in&limit=6")
	if asc.Count != 6 || !asc.HasMore || asc.Mail[0].ID != inIDs[0] {
		t.Errorf("default page = %+v, want the 6 oldest letters and has_more", asc)
	}
	// The plain listing pages after the permission filter too (dave's letter is not alice's).
	inbox := list("/v1/mail?box=in&limit=6")
	if inbox.Count != 6 || !inbox.HasMore {
		t.Errorf("inbox page = count %d has_more %v, want 6 / true", inbox.Count, inbox.HasMore)
	}
	for _, v := range inbox.Mail {
		if v.From.Caller == "dave" {
			t.Errorf("inbox page shows another caller's letter: %+v", v)
		}
	}
	// Cursor and order are validated.
	if status, body := e.req(t, mailTokAlice, "", "GET", path+"&before=abc", nil); status != http.StatusBadRequest {
		t.Errorf("before=abc = %d (%s), want 400", status, body)
	}
	if status, body := e.req(t, mailTokAlice, "", "GET", path+"&order=sideways", nil); status != http.StatusBadRequest {
		t.Errorf("order=sideways = %d (%s), want 400", status, body)
	}
}
