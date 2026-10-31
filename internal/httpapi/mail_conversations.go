package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/mail"
)

// The read views the mailbox conversation UI needs (plan M1, design
// 2026-10-20-mail-conversations-ui-design.md「四、读接口补字段」): who is behind each end of a
// letter (`persona`, additive on `from` / `to`) and the direction-free aggregation of a caller's
// letters into conversations. Both are read-only views over what `GET /v1/mail` already shows:
// the permission rules live in mailListKeep, never here.

// mailPersonaView is the person behind one end of a letter: a session, a job or a caller's
// capture box. Every field is display only; a name that cannot be derived is empty and the
// console falls back to the raw id.
type mailPersonaView struct {
	Name       string `json:"name,omitempty"`
	Agent      string `json:"agent,omitempty"`
	ProjectKey string `json:"project_key,omitempty"`
	Runner     string `json:"runner,omitempty"`
	State      string `json:"state,omitempty"`
	LastSeenAt int64  `json:"last_seen_at,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
}

// mailPersonas resolves the persona of every end of a batch of letters. Each session / job /
// caller is looked up ONCE per batch (the job titles of the whole batch come from one query), so
// a listing of n letters costs a handful of reads instead of n. A batch is one listing page or
// one conversation page, not one letter.
type mailPersonas struct {
	s *Server
	// sessions / jobs map id → persona; a nil value is a party that is gone (no persona).
	sessions map[string]*mailPersonaView
	jobs     map[string]*mailPersonaView
	// callers maps caller id → its capture-box persona.
	callers map[string]*mailPersonaView
}

// newMailPersonas resolves the parties of rows. It never fails: a lookup error counts as a party
// nobody can describe (the console then shows its id).
func (s *Server) newMailPersonas(lang i18n.Lang, rows []jobstore.Mail) *mailPersonas {
	p := &mailPersonas{
		s: s, sessions: make(map[string]*mailPersonaView), jobs: make(map[string]*mailPersonaView),
		callers: make(map[string]*mailPersonaView),
	}
	if s.jobs == nil || s.jobs.Meta() == nil {
		return p
	}
	st := s.jobs.Meta()
	var jobIDs []string
	callerIDs := make(map[string]struct{})
	for _, m := range rows {
		for _, end := range [2]struct{ kind, id, caller string }{
			{m.FromKind, m.FromID, m.FromCaller},
			{m.ToKind, m.ToID, m.ToCaller},
		} {
			switch end.kind {
			case jobstore.MailKindSession:
				if end.id == "" {
					continue
				}
				if _, done := p.sessions[end.id]; done {
					continue
				}
				a, ok, err := st.GetAgentSession(end.id)
				if err != nil || !ok {
					p.sessions[end.id] = nil // a deleted session has no persona
					continue
				}
				p.sessions[end.id] = sessionPersona(a)
			case jobstore.MailKindJob:
				if end.id == "" {
					continue
				}
				if _, done := p.jobs[end.id]; done {
					continue
				}
				p.jobs[end.id] = nil
				jobIDs = append(jobIDs, end.id)
			case jobstore.MailKindCaller:
				callerIDs[end.caller] = struct{}{}
			}
		}
	}
	if len(jobIDs) > 0 {
		titles, err := st.JobTitles(jobIDs) // one query for the whole batch
		if err != nil {
			titles = nil
		}
		for _, id := range jobIDs {
			rec, ok := s.jobs.Get(id)
			if !ok {
				continue // gone or unknown: no persona
			}
			p.jobs[id] = jobPersona(rec, titles[id])
		}
	}
	names := s.callerNames()
	for id := range callerIDs {
		p.callers[id] = callerBoxPersona(lang, names[id], id)
	}
	return p
}

// forParty fills the persona of one party view (nil when nobody can describe it).
func (p *mailPersonas) forParty(v *mailPartyView) {
	if p == nil {
		return
	}
	switch v.Kind {
	case jobstore.MailKindSession:
		v.Persona = p.sessions[v.ID]
	case jobstore.MailKindJob:
		v.Persona = p.jobs[v.ID]
	case jobstore.MailKindCaller:
		v.Persona = p.callers[v.Caller]
	}
}

// forView fills both ends of a letter view.
func (p *mailPersonas) forView(v *mailView) {
	p.forParty(&v.From)
	p.forParty(&v.To)
}

// sessionPersona describes a session: its declared topic, else its title, else agent + project
// (design 决策 2). An ended session keeps its state, which reads 「已结束」.
func sessionPersona(a jobstore.AgentSession) *mailPersonaView {
	return &mailPersonaView{
		Name:       sessionPersonaName(a),
		Agent:      a.Agent,
		ProjectKey: a.ProjectKey,
		Runner:     a.Runner,
		State:      a.State,
		LastSeenAt: a.LastSeenAt,
		Cwd:        a.Cwd,
	}
}

// sessionPersonaName is the session's display name, in the design's fallback order.
func sessionPersonaName(a jobstore.AgentSession) string {
	if t := strings.TrimSpace(a.Topic); t != "" {
		return t
	}
	if t := strings.TrimSpace(a.Title); t != "" {
		return t
	}
	agent, project := strings.TrimSpace(a.Agent), strings.TrimSpace(a.ProjectKey)
	switch {
	case agent != "" && project != "":
		return agent + " · " + project
	case agent != "":
		return agent
	case project != "":
		return project
	}
	return ""
}

// jobPersona describes a job: its title (else `job <short id>`) and the metadata a person card
// shows.
func jobPersona(rec job.JobResult, title string) *mailPersonaView {
	name := strings.TrimSpace(title)
	if name == "" {
		name = "job " + shortMailID(rec.ID)
	}
	return &mailPersonaView{
		Name: name, Agent: rec.Agent, ProjectKey: rec.ProjectKey, Runner: rec.Runner, State: rec.Status,
	}
}

// callerBoxPersona describes a caller's capture box: 「<display name> 的收集箱」, with the id when
// the caller has no configured display name.
func callerBoxPersona(lang i18n.Lang, displayName, caller string) *mailPersonaView {
	name := strings.TrimSpace(displayName)
	if name == "" {
		name = strings.TrimSpace(caller)
	}
	if name == "" {
		return &mailPersonaView{Name: i18n.T(lang, "httpapi.mail_box")}
	}
	return &mailPersonaView{Name: i18n.T(lang, "httpapi.mail_box_of", "name", name)}
}

// shortMailID is the 8-char prefix a title-less job is named by.
func shortMailID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// mailParty is one end of a letter as a conversation participant. Address is the canonical,
// scheme-qualified key ("session:<sid>" / "job:<id>" / "caller:<id>"; a letter addressed to
// nobody carries its raw address) and is what makes the aggregation direction-free.
type mailParty struct {
	Kind    string
	ID      string
	Caller  string
	Address string
}

// mailFromParty is the sender end of a letter.
func mailFromParty(m jobstore.Mail) mailParty {
	return mailParty{
		Kind: m.FromKind, ID: m.FromID, Caller: m.FromCaller,
		Address: mail.AddressOf(m.FromKind, m.FromID, m.FromCaller),
	}
}

// mailToParty is the recipient end of a letter. A letter whose address resolved to nobody (a held
// letter) has no recipient kind: its raw address is the only recipient there is, unless that
// address is the bare "caller" (or empty), which means the sender's own capture box.
func mailToParty(m jobstore.Mail) mailParty {
	switch m.ToKind {
	case jobstore.MailKindSession, jobstore.MailKindJob:
		return mailParty{
			Kind: m.ToKind, ID: m.ToID, Caller: m.ToCaller,
			Address: mail.AddressOf(m.ToKind, m.ToID, m.ToCaller),
		}
	case jobstore.MailKindCaller:
		return mailParty{
			Kind: jobstore.MailKindCaller, Caller: m.ToCaller,
			Address: mail.AddressOf(jobstore.MailKindCaller, "", m.ToCaller),
		}
	}
	raw := strings.TrimSpace(m.ToAddress)
	if raw == "" || strings.EqualFold(raw, mail.SchemeCaller) {
		return mailParty{
			Kind: jobstore.MailKindCaller, Caller: m.FromCaller,
			Address: mail.AddressOf(jobstore.MailKindCaller, "", m.FromCaller),
		}
	}
	return mailParty{Address: raw}
}

// view is the party as the API shows it: kind / id / caller, a display label and its persona.
func (p mailParty) view() mailPartyView {
	v := mailPartyView{Kind: p.Kind, ID: p.ID, Caller: p.Caller}
	if p.Address == "" || (p.Kind == "" && p.ID == "") {
		v.Display = p.Address // an address that resolved to nobody: show it verbatim
	} else {
		v.Display = mail.Origin{Kind: p.Kind, ID: p.ID, Caller: p.Caller}.Display()
	}
	return v
}

// mailConversationKey is the direction-free key of a pair of parties: the two addresses sorted,
// so from→to and to→from of the same two parties have one key.
func mailConversationKey(x, y mailParty) string {
	if x.Address > y.Address {
		x, y = y, x
	}
	return x.Address + "|" + y.Address
}

const (
	// mailConvIDPrefix marks a conversation id in what a person copies / pastes.
	mailConvIDPrefix = "cv-"
	// mailConvIDHex is how much of the key's hash the id keeps (10 hex chars = 40 bits).
	mailConvIDHex = 10
)

// mailConversationID is the short, stable id of a conversation: the key (direction-free, one per
// pair of parties) hashed and truncated. It is what a person quotes when asking about a
// conversation, so `cv-` + 10 hex chars is all it is; the same pair always gets the same id.
func mailConversationID(key string) string {
	sum := sha256.Sum256([]byte(key))
	return mailConvIDPrefix + hex.EncodeToString(sum[:])[:mailConvIDHex]
}

// mailConvIDQuery reads ?conv= — the short id of one conversation ("" = none); ok=false: the
// answer is already written.
func mailConvIDQuery(c *rux.Context) (string, bool) {
	id := strings.ToLower(strings.TrimSpace(c.Query("conv")))
	if id == "" {
		return "", true
	}
	if len(id) != len(mailConvIDPrefix)+mailConvIDHex || !strings.HasPrefix(id, mailConvIDPrefix) {
		writeError(c, http.StatusBadRequest, "invalid conversation id", "conv must be a conversation id like cv-0123456789")
		return "", false
	}
	return id, true
}

// mailConversationIDKey resolves a conversation id back to its key. Only the conversations the
// caller may see are searched — the same inbox and the same keep predicate as the listing — so an
// id of somebody else's conversation is simply not found, exactly like a letter he may not read.
// found=false means "no such conversation for this caller" (the caller answers 404).
func (s *Server) mailConversationIDKey(c *rux.Context, box, id string) (key string, found bool, err error) {
	keep := s.mailListKeep(c, box, "")
	for page := 0; page < mailListScanPages; page++ {
		rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{
			Limit: jobstore.MailListMaxLimit, Offset: page * jobstore.MailListMaxLimit,
		})
		if err != nil {
			return "", false, err
		}
		for _, m := range rows {
			if !keep(m) {
				continue
			}
			if k := mailConversationKey(mailFromParty(m), mailToParty(m)); mailConversationID(k) == id {
				return k, true, nil
			}
		}
		if len(rows) < jobstore.MailListMaxLimit {
			break
		}
	}
	return "", false, nil
}

// mailUnread reports whether a letter still waits to be read by its recipient: it has no read
// stamp and did not end up cancelled / failed / expired, and an answered letter counts as read
// (answering it WAS reading it).
func mailUnread(m jobstore.Mail) bool {
	if m.ReadAt > 0 {
		return false
	}
	switch m.State {
	case jobstore.MailCancelled, jobstore.MailFailed, jobstore.MailExpired, jobstore.MailAnswered:
		return false
	}
	return true
}

// mailConversationView is one line of the conversation list: both people, the newest letter, and
// the counts a person needs to see what is waiting.
type mailConversationView struct {
	// ID is the stable short id of the conversation ("cv-" + 10 hex chars of the key's hash):
	// the same pair of people always has the same id, whichever way their letters went.
	ID   string        `json:"id"`
	Key  string        `json:"key"`
	A    mailPartyView `json:"a"`
	B    mailPartyView `json:"b"`
	Last mailView      `json:"last_mail"`
	// Count / Unread / Urgent count the conversation's letters: all of them, those nobody read
	// yet, and those sent as urgent.
	Count      int   `json:"count"`
	Unread     int   `json:"unread"`
	Urgent     int   `json:"urgent"`
	ActivityAt int64 `json:"activity_at"`
}

// mailConvDefault / mailConvMax bound the conversation listing page.
const (
	mailConvDefault = 50
	mailConvMax     = 200
)

// mailBoxQuery reads and validates ?box= (in / out / all, default all); ok=false: the answer is
// already written.
func mailBoxQuery(c *rux.Context) (string, bool) {
	box := strings.TrimSpace(c.Query("box"))
	if box == "" {
		return "all", true
	}
	if box != "in" && box != "out" && box != "all" {
		writeError(c, http.StatusBadRequest, "invalid box", "box must be in, out or all")
		return "", false
	}
	return box, true
}

// mailPageQuery reads ?limit= / ?offset= clamped to def / max.
func mailPageQuery(c *rux.Context, def, max int) (limit, offset int) {
	limit = def
	if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 {
		limit = n
	}
	if limit > max {
		limit = max
	}
	if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
		offset = n
	}
	return limit, offset
}

// mailPeerKey canonicalises a ?peer= / ?with= address to the party key a stored letter carries
// ("session:<sid>" / "job:<id>" / "caller:<id>"). A bare `caller` (or none) is the caller's own
// capture box; `caller:<id>` names somebody else's box, which a SEND refuses but a conversation
// has to be able to name. A topic: / path: search address names nobody a letter is filed under.
func (s *Server) mailPeerKey(c *rux.Context, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	scheme, value := trimmed, ""
	if i := strings.Index(trimmed, ":"); i >= 0 {
		scheme, value = trimmed[:i], strings.TrimSpace(trimmed[i+1:])
	}
	switch scheme = strings.ToLower(strings.TrimSpace(scheme)); scheme {
	case mail.SchemeSession:
		addr, err := mail.ParseAddress(trimmed)
		if err != nil {
			return "", err
		}
		return mail.AddressOf(jobstore.MailKindSession, addr.Value, ""), nil
	case mail.SchemeJob:
		addr, err := mail.ParseAddress(trimmed)
		if err != nil {
			return "", err
		}
		return mail.AddressOf(jobstore.MailKindJob, addr.Value, ""), nil
	case mail.SchemeCaller:
		if value == "" {
			value = callerFromCtx(c)
		}
		return mail.AddressOf(jobstore.MailKindCaller, "", value), nil
	}
	return "", fmt.Errorf("%w: %s: does not name a stored participant", mail.ErrInvalidAddress, raw)
}

// mailConvAgg is one conversation while it is being aggregated.
type mailConvAgg struct {
	// a / b are the ends of the conversation's EARLIEST letter: the party that wrote first is a
	// (設計 决策 4 puts it on the left). The key is direction-free, so the pair is one
	// conversation whichever way its letters went.
	a, b mailParty
	// last is the newest letter of the conversation.
	last                  jobstore.Mail
	count, unread, urgent int
}

// mailConversationQuery reads ?peer= / ?with= / ?conv= and returns the direction-free key of that
// one conversation ("" when none is given); ok=false: the answer is already written.
//
// ?conv= takes the conversation's short id instead of the two addresses: it is resolved against
// the conversations this caller can see (mailConversationIDKey), so an id that names no visible
// conversation is a 404 — the same answer as a letter the caller may not read.
func (s *Server) mailConversationQuery(c *rux.Context, box string) (string, bool) {
	conv, ok := mailConvIDQuery(c)
	if !ok {
		return "", false
	}
	peer, with := strings.TrimSpace(c.Query("peer")), strings.TrimSpace(c.Query("with"))
	if conv != "" {
		if peer != "" || with != "" {
			writeError(c, http.StatusBadRequest, "invalid conversation", "conv and peer/with are exclusive")
			return "", false
		}
		key, found, err := s.mailConversationIDKey(c, box, conv)
		if err != nil {
			s.writeMailError(c, err)
			return "", false
		}
		if !found {
			writeError(c, http.StatusNotFound, "conversation not found", "no conversation "+conv+" in your mailbox")
			return "", false
		}
		return key, true
	}
	if peer == "" && with == "" {
		return "", true
	}
	if peer == "" || with == "" {
		writeError(c, http.StatusBadRequest, "invalid conversation", "peer and with are required together")
		return "", false
	}
	aKey, err := s.mailPeerKey(c, peer)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid conversation address", err.Error())
		return "", false
	}
	bKey, err := s.mailPeerKey(c, with)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid conversation address", err.Error())
		return "", false
	}
	return mailConversationKey(mailParty{Address: aKey}, mailParty{Address: bKey}), true
}

// mailConvProjects answers which project a conversation belongs to, for the `?project=` narrowing
// of the conversation listing. A conversation is in a project when either of its two parties is
// (the console shows a cross-project conversation under both projects rather than letting it
// vanish), which is the rule its own local filter applies. Lookups are cached per request: a
// mailbox holds many letters between a handful of sessions and jobs.
type mailConvProjects struct {
	s *Server
	// cache maps "<kind>/<id>" → the party's project ("" = none, gone, or not a project owner).
	cache map[string]string
}

// of is the project of one party ("" when the party has none).
func (p *mailConvProjects) of(party mailParty) string {
	if party.Kind != jobstore.MailKindSession && party.Kind != jobstore.MailKindJob {
		return ""
	}
	key := party.Kind + "/" + party.ID
	if project, done := p.cache[key]; done {
		return project
	}
	project, _ := p.s.mailEndProject(party.Kind, party.ID)
	p.cache[key] = project
	return project
}

// keeps reports whether a conversation is in project ("" keeps everything).
func (p *mailConvProjects) keeps(agg *mailConvAgg, project string) bool {
	if project == "" {
		return true
	}
	return strings.TrimSpace(p.of(agg.a)) == project || strings.TrimSpace(p.of(agg.b)) == project
}

// GET /v1/mail/conversations?box=in|out|all&project=&limit=&offset=
//
// The letters of the caller's mailbox grouped by the pair of people who exchanged them,
// direction-free, newest activity first. It aggregates exactly what `GET /v1/mail` shows (the
// same filter, so a letter the caller may not see neither counts nor names a participant), over
// the same bounded scan the listing walks.
// ?conv=<id> narrows the same aggregation to ONE conversation (404 when this caller has no such
// conversation), which is how the console opens `/mail?conv=<id>`.
// ?project=<key> narrows it to the conversations with a party in that project. The answer's
// `total` counts the conversations that match, before limit / offset: the filter chips and the
// "N conversations" header read it instead of pulling every conversation to count them here.
func (s *Server) handleMailConversations(c *rux.Context) {
	if !s.mailReady(c) {
		return
	}
	box, ok := mailBoxQuery(c)
	if !ok {
		return
	}
	convID, ok := mailConvIDQuery(c)
	if !ok {
		return
	}
	wantKey := ""
	if convID != "" {
		key, found, err := s.mailConversationIDKey(c, box, convID)
		if err != nil {
			s.writeMailError(c, err)
			return
		}
		if !found {
			writeError(c, http.StatusNotFound, "conversation not found", "no conversation "+convID+" in your mailbox")
			return
		}
		wantKey = key
	}
	limit, offset := mailPageQuery(c, mailConvDefault, mailConvMax)
	project := strings.TrimSpace(c.Query("project"))
	keep := s.mailListKeep(c, box, "")
	byKey := make(map[string]*mailConvAgg)
	for page := 0; page < mailListScanPages; page++ {
		rows, err := s.jobs.Meta().ListMail(jobstore.ListMailOpts{
			Limit: jobstore.MailListMaxLimit, Offset: page * jobstore.MailListMaxLimit,
		})
		if err != nil {
			s.writeMailError(c, err)
			return
		}
		for _, m := range rows {
			if !keep(m) {
				continue
			}
			a, b := mailFromParty(m), mailToParty(m)
			key := mailConversationKey(a, b)
			if wantKey != "" && key != wantKey {
				continue
			}
			agg := byKey[key]
			if agg == nil {
				// The store lists newest first, so the first letter seen IS the newest and the
				// last one seen is the earliest.
				agg = &mailConvAgg{a: a, b: b, last: m}
				byKey[key] = agg
			}
			agg.a, agg.b = a, b
			agg.count++
			if m.Urgent {
				agg.urgent++
			}
			if mailUnread(m) {
				agg.unread++
			}
		}
		if len(rows) < jobstore.MailListMaxLimit {
			break
		}
	}
	list := make([]*mailConvAgg, 0, len(byKey))
	for _, agg := range byKey {
		list = append(list, agg)
	}
	sort.Slice(list, func(i, j int) bool {
		li, lj := list[i].last, list[j].last
		if li.CreatedAt != lj.CreatedAt {
			return li.CreatedAt > lj.CreatedAt
		}
		return li.ID > lj.ID
	})
	if project != "" {
		projects := &mailConvProjects{s: s, cache: make(map[string]string)}
		kept := list[:0]
		for _, agg := range list {
			if projects.keeps(agg, project) {
				kept = append(kept, agg)
			}
		}
		list = kept
	}
	// total is the number of conversations this caller has in that box / project, whatever page
	// was asked for: it is what the console's counters read.
	total := len(list)
	if offset > 0 {
		if offset >= len(list) {
			list = nil
		} else {
			list = list[offset:]
		}
	}
	if len(list) > limit {
		list = list[:limit]
	}
	rows := make([]jobstore.Mail, 0, len(list))
	for _, agg := range list {
		rows = append(rows, agg.last)
	}
	ps := s.newMailPersonas(s.requestLang(c), rows)
	out := make([]mailConversationView, 0, len(list))
	for _, agg := range list {
		av, bv := agg.a.view(), agg.b.view()
		ps.forParty(&av)
		ps.forParty(&bv)
		last := toMailView(agg.last)
		ps.forView(&last)
		out = append(out, mailConversationView{
			ID:  mailConversationID(mailConversationKey(agg.a, agg.b)),
			Key: mailConversationKey(agg.a, agg.b), A: av, B: bv, Last: last,
			Count: agg.count, Unread: agg.unread, Urgent: agg.urgent, ActivityAt: agg.last.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, map[string]any{"conversations": out, "count": len(out), "total": total})
}
