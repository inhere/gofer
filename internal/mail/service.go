// Package mail is the gofer 信箱's business layer (design
// 2026-10-16-session-topics-mailbox-and-inbox-design.md 「二、gofer 信箱」): sending a letter to an
// address, resolving that address to the ONE session / job / caller it names, stamping where
// the letter came from, and parking what cannot be delivered in the capture box.
//
// It owns no transport: delivery is the hook's (SessionStart / UserPromptSubmit / PreToolUse),
// the relay's ladder and the server deliverer's job, all of which read the letters through
// jobstore and the CLI / MCP. The mailbox does not import httpapi, commands or the hooks
// (G022); the entry layer authenticates the caller and vouches for the session it claims,
// while this package checks only that the resulting origin is a legal combination.
package mail

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
)

// The mailbox's error vocabulary. ErrRateLimited is the one the entry layer maps to 429.
var (
	// ErrInvalidAddress: the address is malformed (an unknown scheme, an empty value).
	ErrInvalidAddress = fmt.Errorf("mail: invalid address")
	// ErrSessionNotFound: a session: address names no session (no id equals it, none starts with it).
	ErrSessionNotFound = fmt.Errorf("mail: session not found")
	// ErrSessionAmbiguous: a session: address is a prefix of several sessions.
	ErrSessionAmbiguous = fmt.Errorf("mail: session id prefix is ambiguous")
	// ErrUnsupportedAddress: a well-formed address whose scheme this version does not
	// deliver to (kept for the entry layers' wording; `agent:` is delivered since
	// 2026-10-22).
	ErrUnsupportedAddress = fmt.Errorf("mail: address not supported yet")
	// ErrInvalidOrigin: the sender's (kind, id, caller) combination is not legal.
	ErrInvalidOrigin = fmt.Errorf("mail: invalid sender origin")
	// ErrMailTooLong: the body exceeds the single-letter limit.
	ErrMailTooLong = fmt.Errorf("mail: body too long")
	// ErrRateLimited: the sender passed its per-minute / per-hour ceiling.
	ErrRateLimited = fmt.Errorf("mail: send rate limited")
	// ErrMailNotFound: no letter with that id.
	ErrMailNotFound = fmt.Errorf("mail: letter not found")
	// ErrJobNotFound: a job: address names no job at all (a typo, a deleted row).
	ErrJobNotFound = fmt.Errorf("mail: job not found")
	// ErrJobCannotReceive: a job: address names a job that can never take a letter — an ended
	// job, or a one-off run (no ACP session, so nothing can be said into it). The send is
	// refused instead of storing a letter that would hang until the delivery deadline.
	ErrJobCannotReceive = fmt.Errorf("mail: job cannot receive mail")
	// ErrNotAnswerable: the letter was written by gofer itself (a 退信), so there is no sender
	// to answer.
	ErrNotAnswerable = fmt.Errorf("mail: letter cannot be answered")
	// ErrInvalidBody: an empty body (there is nothing to deliver).
	ErrInvalidBody = fmt.Errorf("mail: body required")
)

// SessionFinder is the session lookup a topic: / path: address is resolved with. It IS
// sessionrelay.Service.Find — the matching, the ranking and the "offline still counts, but is
// flagged" rule live there (设计「会话主题与查找」), never here.
type SessionFinder interface {
	Find(sessionrelay.FindQuery) ([]sessionrelay.FindResult, error)
}

// CaptureInput is one captured work item (the collection box's own input).
type CaptureInput struct {
	Text string
	Kind string
	By   string
	// SourceSession / SourceJob are recorded as fields only (who wrote the letter down).
	SourceSession string
	SourceJob     string
}

// CaptureBox is the 收集箱 seam (CAP-03): the captured work item a letter that nobody could be
// handed to becomes. The default implementation is the metadata store's own capture function
// (internal/work.Capture is a thin wrapper over it, adding the default kind); it stays an
// interface because the dispatch direction comes back the other way later — a capture-box item
// dispatched to a session sends a LETTER (M6), which would be a cycle if the mailbox imported
// internal/work.
type CaptureBox interface {
	// CaptureWork records one captured item and returns its id.
	CaptureWork(CaptureInput) (string, error)
}

// storeCaptureBox is the default box: the jobstore owns the captured-item write.
type storeCaptureBox struct{ store *jobstore.Store }

func (b storeCaptureBox) CaptureWork(in CaptureInput) (string, error) {
	w, err := b.store.CaptureWorkItem(jobstore.CaptureInput{
		Text: in.Text, Kind: in.Kind, ProjectKey: "", Workspace: "",
		SourceSession: in.SourceSession, SourceJob: in.SourceJob, By: in.By,
	})
	if err != nil {
		return "", err
	}
	return w.ID, nil
}

// captureKind is the kind a held letter is recorded as. A plain note is the least presumptuous
// label: idea / issue / request are claims about the text, and a letter nobody could be handed
// to is only a record awaiting a person's decision.
const captureKind = jobstore.WorkKindNote

// captureBy is the journal author of a mailbox-recorded item.
const captureBy = "mail"

// MailSummaryRunes is how much of a letter's body a capture-box item keeps (设计「敏感信息」:
// 条目只存信件 id 与正文前若干字摘要 — the body itself is never copied).
const MailSummaryRunes = 200

// SetLanguageFn supplies the language used for generated mailbox notices. Nil uses the
// deployment default, preserving existing callers.
func (s *Service) SetLanguageFn(fn func() i18n.Lang) { s.language = fn }

func (s *Service) textLanguage() i18n.Lang {
	if s.language != nil {
		return i18n.Resolve(s.language())
	}
	return i18n.Default
}

// Service sends and replies to letters over the shared metadata store.
type Service struct {
	store  *jobstore.Store
	finder SessionFinder
	box    CaptureBox
	// Limits are the send limits (见 limits.go); DefaultLimits at construction.
	Limits Limits
	now    func() time.Time
	// deliverByOverride (seconds, 0 = none) is the hot-reloaded delivery deadline; see SetDeliverBy.
	deliverByOverride atomic.Int64
	// language resolves the language of human-facing mailbox text.
	language func() i18n.Lang

	// broadcastMax / broadcastRate / broadcastWindow override Limits' broadcast numbers from
	// the `mail:` config block (0 = use Limits): hot-reloaded like deliverByOverride.
	broadcastMax    atomic.Int64
	broadcastRate   atomic.Int64
	broadcastWindow atomic.Int64
	// residents is the `mail.agents` block (hot-reloaded like the numbers above): which names
	// an `agent:` address may use and how their resident session is started.
	residents atomic.Pointer[ResidentsConfig]
	// sessionJob says whether a job id is an ACP session job (`--session`), the only job kind a
	// letter can be delivered into (nil = this process cannot tell, and a live job is accepted).
	sessionJob SessionJobCheck
	// residentHost starts / ends / looks up the resident session jobs (nil = this process cannot
	// start sessions, so `agent:` letters are parked).
	residentHost ResidentHost
}

// NewService builds the mailbox over the store, resolving topic: / path: through finder (nil =
// those addresses cannot be resolved).
func NewService(store *jobstore.Store, finder SessionFinder) *Service {
	return &Service{
		store: store, finder: finder, box: storeCaptureBox{store: store},
		Limits: DefaultLimits, now: time.Now,
	}
}

// SetCaptureBox replaces the collection-box seam (tests, and a deployment whose capture box is
// not the metadata store).
func (s *Service) SetCaptureBox(b CaptureBox) { s.box = b }

// SessionJobCheck reports whether a job id is an ACP session job — one started with `session: true`
// (the only job kind a letter can be said into) — and whether the job is known at all (found).
// It is the job service's judgement, injected at assembly: the mailbox does not read job request
// payloads to find out.
type SessionJobCheck func(jobID string) (session, found bool)

// SetSessionJobCheck installs the job-side judgement of a `job:` address (nil = none: every live
// job is then accepted, which is what a process without a job service can do).
func (s *Service) SetSessionJobCheck(fn SessionJobCheck) { s.sessionJob = fn }

// SetClock overrides the service clock (tests).
func (s *Service) SetClock(fn func() time.Time) {
	if fn == nil {
		fn = time.Now
	}
	s.now = fn
}

// SendInput is one letter to send. Visible is the reader-permission judgement the entry layer
// passes in (a topic: / path: candidate the sender may not see is not a candidate at all — the
// mailbox never reads permissions itself); Roots are a project's root paths for the finder.
type SendInput struct {
	To      string
	Body    string
	Urgent  bool
	Origin  Origin
	Project string
	Visible func(jobstore.AgentSession) bool
	Roots   func(projectKey string) []string
	// ThreadID puts the letter into an existing conversation thread (a readdressed letter
	// keeps its thread); empty starts a new one.
	ThreadID string
	// CaptureItemID marks the letter as one that came OUT of the capture box: the item id is
	// recorded on the letter (mail.capture_item_id), so whatever happens to the letter can be
	// written back into that item (收集箱「交给会话」, design §3). Empty for a letter that was
	// written by hand.
	CaptureItemID string
	// Authorize is the entry layer's permission / ceiling judgement on the RESOLVED
	// recipient (the mailbox never reads permissions itself). It runs after the address is
	// resolved and BEFORE anything is stored, so a refused letter leaves no trace; its error
	// is returned as is. It is not called when the address resolved to nobody.
	Authorize func(Target) error
}

// ReplyInput is one answer to an existing letter.
type ReplyInput struct {
	ID     int64
	Body   string
	Urgent bool
	Origin Origin
	// Authorize is SendInput.Authorize for the reply's recipient (the original sender).
	Authorize func(Target) error
}

// SendResult is what a send / reply produced. Held means the letter could not be delivered and
// went to the capture box (Candidates lists the sessions a topic: / path: address matched);
// CaptureItemID is that item.
type SendResult struct {
	Mail          jobstore.Mail
	Target        Target
	Candidates    []Candidate
	CaptureItemID string
	Held          bool
}

// Send stores one letter, resolving its address first: exactly one recipient is delivered to,
// none or several are parked in the capture box with the candidates.
func (s *Service) Send(in SendInput) (SendResult, error) {
	org, err := in.Origin.Validate()
	if err != nil {
		return SendResult{}, err
	}
	if err := s.checkBody(in.Body); err != nil {
		return SendResult{}, err
	}
	if err := s.checkRate(org); err != nil {
		return SendResult{}, err
	}
	addr, err := ParseAddress(in.To)
	if err != nil {
		return SendResult{}, err
	}
	res, err := s.resolve(resolveInput{
		addr: addr, origin: org, project: in.Project, visible: in.Visible, roots: in.Roots,
	})
	if err != nil {
		return SendResult{}, err
	}
	if in.Authorize != nil && res.target.Kind != "" {
		if err := in.Authorize(res.target); err != nil {
			return SendResult{}, err
		}
	}
	mi := jobstore.MailInput{
		ThreadID: in.ThreadID,
		FromKind: org.Kind, FromID: org.ID, FromCaller: org.Caller,
		ToAddress: addr.Raw, ToKind: res.target.Kind, ToID: res.target.ID, ToCaller: res.target.Caller,
		JobID: mailJobID(res.target.Kind, res.target.ID, org), Urgent: in.Urgent, Body: in.Body,
		DeliverBy: s.deliverBy(), CaptureItemID: in.CaptureItemID,
	}
	return s.storeLetter(mi, res, org)
}

// Reply stores an answer inside the original letter's thread and marks the original answered.
// The answer goes back to the original sender's address, and the thread's automatic
// round-trip ceiling parks it in the capture box instead of sending it when two agents would
// otherwise keep answering each other.
func (s *Service) Reply(in ReplyInput) (SendResult, error) {
	org, err := in.Origin.Validate()
	if err != nil {
		return SendResult{}, err
	}
	orig, ok, err := s.store.GetMail(in.ID)
	if err != nil {
		return SendResult{}, err
	}
	if !ok {
		return SendResult{}, ErrMailNotFound
	}
	if orig.FromKind == jobstore.MailKindGofer {
		return SendResult{}, fmt.Errorf("%w: letter %d is a gofer notice (退信), there is no sender to answer", ErrNotAnswerable, orig.ID)
	}
	if err := s.checkBody(in.Body); err != nil {
		return SendResult{}, err
	}
	if err := s.checkRate(org); err != nil {
		return SendResult{}, err
	}
	back := AddressOf(orig.FromKind, orig.FromID, orig.FromCaller)
	res := resolution{target: Target{
		Kind: orig.FromKind, ID: orig.FromID, Caller: orig.FromCaller,
		Address: back, Deliver: true,
	}}
	if in.Authorize != nil {
		if err := in.Authorize(res.target); err != nil {
			return SendResult{}, err
		}
	}
	if orig.FromKind == jobstore.MailKindCaller {
		// A caller has no session and no job to be delivered into: its mail lands in its own
		// capture box, exactly like the `caller` address does (设计「地址写法」).
		res.target.Deliver = false
		res.reason = back + " names a caller: the letter goes to the capture box"
	} else if ceiling := s.Limits.MaxThreadLetters; ceiling > 0 {
		n, err := s.store.CountThreadMail(orig.ThreadID)
		if err != nil {
			return SendResult{}, err
		}
		if n >= ceiling {
			res.target.Deliver = false
			res.reason = fmt.Sprintf("thread %s reached its %d-letter ceiling", orig.ThreadID, ceiling)
		}
	}
	mi := jobstore.MailInput{
		ThreadID: orig.ThreadID, ReplyTo: orig.ID,
		FromKind: org.Kind, FromID: org.ID, FromCaller: org.Caller,
		ToAddress: back, ToKind: orig.FromKind, ToID: orig.FromID, ToCaller: orig.FromCaller,
		JobID: mailJobID(orig.FromKind, orig.FromID, org), Urgent: in.Urgent, Body: in.Body,
		DeliverBy: s.deliverBy(),
	}
	out, err := s.storeLetter(mi, res, org)
	if err != nil {
		return out, err
	}
	// Answering implies reading, and reading IS delivery: the original is marked read first so a
	// letter the delivery path never got to still becomes answered.
	if _, _, err := s.store.MarkMailRead(orig.ID); err != nil {
		return out, err
	}
	if _, _, err := s.store.MarkMailAnswered(orig.ID); err != nil {
		return out, err
	}
	return out, nil
}

// ReaddressInput hands a held letter to a new address.
type ReaddressInput struct {
	ID        int64
	To        string
	Origin    Origin
	Project   string
	Visible   func(jobstore.AgentSession) bool
	Roots     func(projectKey string) []string
	Authorize func(Target) error
}

// Readdress is the capture box's 「改投」: a held (待指派) letter is sent again to a new address
// — same body, same urgency, same thread, the caller as the sender — and the held one is
// cancelled with the new address as the reason. It is a fresh send for every purpose
// (resolution, limits, authorization on the NEW recipient). A letter that is not held cannot
// be readdressed (jobstore.ErrMailState).
func (s *Service) Readdress(in ReaddressInput) (SendResult, error) {
	orig, ok, err := s.store.GetMail(in.ID)
	if err != nil {
		return SendResult{}, err
	}
	if !ok {
		return SendResult{}, ErrMailNotFound
	}
	if orig.State != jobstore.MailHeld {
		return SendResult{}, fmt.Errorf("%w: letter %d is %s, only a held letter can be readdressed", jobstore.ErrMailState, orig.ID, orig.State)
	}
	out, err := s.Send(SendInput{
		To: in.To, Body: orig.Body, Urgent: orig.Urgent, Origin: in.Origin, Project: in.Project,
		Visible: in.Visible, Roots: in.Roots, ThreadID: orig.ThreadID, Authorize: in.Authorize,
	})
	if err != nil {
		return out, err
	}
	if _, _, err := s.CancelMail(orig.ID, "readdressed to "+strings.TrimSpace(in.To)); err != nil {
		return out, err
	}
	return out, nil
}

// CancelMail withdraws a letter. When the letter was held, the capture-box item it was recorded
// as is dropped with it: the item only stood for "this letter needs an address", and a withdrawn
// letter needs nothing, so leaving it captured would be noise a person has to triage by hand.
// Dropping (not deleting) matches the box's own 「丢弃」 and keeps the journal line saying why.
// changed is false when the letter's state no longer allowed a cancel. Closing the item is
// best effort: the letter is already cancelled, and an item that was triaged meanwhile
// (no longer captured) is left alone.
func (s *Service) CancelMail(id int64, reason string) (jobstore.Mail, bool, error) {
	prev, found, err := s.store.GetMail(id)
	if err != nil {
		return jobstore.Mail{}, false, err
	}
	got, changed, err := s.store.CancelMail(id, reason)
	if err != nil || !changed {
		return got, changed, err
	}
	if found && prev.State == jobstore.MailHeld && prev.CaptureItemID != "" {
		s.dropCaptureItem(prev.CaptureItemID, fmt.Sprintf("信件 #%d 已撤回，条目随之丢弃", id))
	}
	return got, true, nil
}

// dropCaptureItem moves a still-captured item to dropped and journals why.
func (s *Service) dropCaptureItem(itemID, why string) {
	w, ok, err := s.store.GetWorkItem(itemID)
	if err != nil || !ok || w.Status != jobstore.WorkCaptured {
		return
	}
	status, src := jobstore.WorkDropped, jobstore.WorkSourceHuman
	if _, _, err := s.store.UpdateWorkItem(itemID, jobstore.WorkItemPatch{
		Status: &status, StatusSource: &src, FromCapture: true, Quiet: true,
	}, 0, captureBy); err != nil {
		return
	}
	_, _ = s.store.AppendWorkJournal(itemID, jobstore.WorkJournalNote, why, captureBy)
}

// storeLetter inserts the letter and, when its recipient is not a delivery target, parks it
// (held) and records it in the capture box — the letter's CaptureItemID and the item's text
// point at each other.
func (s *Service) storeLetter(mi jobstore.MailInput, res resolution, org Origin) (SendResult, error) {
	m, err := s.store.InsertMail(mi)
	if err != nil {
		return SendResult{}, err
	}
	out := SendResult{Mail: m, Target: res.target, Candidates: res.candidates}
	if res.target.Deliver {
		return out, nil
	}
	reason := res.reason
	if reason == "" {
		reason = subjectWithoutTarget(mi.ToAddress, res.candidates)
	}
	held, ok, err := s.store.HoldMail(m.ID, reason)
	if err != nil {
		return out, err
	}
	if ok {
		out.Mail = held
	}
	out.Held = true
	itemID, err := s.recordCapture(out.Mail, res.candidates)
	if err != nil {
		// The letter is safely held even when the item could not be written; the person's
		// entry point is missing, so the caller has to hear about it.
		return out, err
	}
	if itemID != "" {
		if _, err := s.store.SetMailCaptureItem(m.ID, itemID); err != nil {
			return out, err
		}
		out.CaptureItemID = itemID
	}
	return out, nil
}

// recordCapture writes the held letter into the collection box as a captured item. It records
// the letter's ID and a short body summary only — never the body itself.
func (s *Service) recordCapture(m jobstore.Mail, cands []Candidate) (string, error) {
	if s.box == nil {
		return "", nil
	}
	in := CaptureInput{Text: CaptureTextLang(s.textLanguage(), m, cands), Kind: captureKind, By: captureBy}
	switch m.FromKind {
	case jobstore.MailKindSession:
		in.SourceSession = m.FromID
	case jobstore.MailKindJob:
		in.SourceJob = m.FromID
	}
	return s.box.CaptureWork(in)
}

// captureTextBudgetBytes is the work item's own text cap (jobstore's title limit, 200 bytes).
// Everything a captured item says has to fit inside it, so the body preview is shortened to the
// room the header leaves instead of being cut off by the store mid-summary.
const captureTextBudgetBytes = 200

// bodyLabel is retained for the package characterization tests; production rendering uses the
// localized catalog value below.
const bodyLabel = "\n正文摘录："

// CaptureText renders a capture-box item in the default language.
func CaptureText(m jobstore.Mail, cands []Candidate) string {
	return CaptureTextLang(i18n.Default, m, cands)
}

// CaptureTextLang renders a capture-box item in lang.
func CaptureTextLang(lang i18n.Lang, m jobstore.Mail, cands []Candidate) string {
	var b strings.Builder
	b.WriteString(i18n.T(lang, "mail.capture.heading", "id", m.ID))
	if m.ToAddress != "" {
		fmt.Fprintf(&b, "：%s", m.ToAddress)
	}
	b.WriteString("\n" + i18n.T(lang, "mail.capture.from", "origin", OriginOf(m).DisplayLang(lang)))
	if m.LastError != "" {
		b.WriteString("\n" + i18n.T(lang, "mail.capture.reason", "reason", m.LastError))
	}
	if len(cands) > 0 {
		parts := make([]string, 0, len(cands))
		for _, c := range cands {
			parts = append(parts, c.DisplayLang(lang))
		}
		b.WriteString("\n" + i18n.T(lang, "mail.capture.candidates", "candidates", strings.Join(parts, "；")))
	}
	head := b.String()
	bodyLabel := "\n" + i18n.T(lang, "mail.capture.body")
	room := captureTextBudgetBytes - len(head) - len(bodyLabel) - len("…")
	preview := SummarizeBytes(Summarize(m.Body, MailSummaryRunes), room)
	if preview == "" {
		return head
	}
	return head + bodyLabel + preview
}

// SummarizeBytes truncates text to at most n BYTES on a rune boundary, with an ellipsis when it
// had to cut. A budget too small for even one rune yields no preview at all.
func SummarizeBytes(text string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(text) <= n {
		return text
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	if cut == 0 {
		return ""
	}
	return text[:cut] + "…"
}

// Summarize returns the first n runes of text, with an ellipsis when it was cut.
func Summarize(text string, n int) string {
	if n <= 0 || utf8.RuneCountInString(text) <= n {
		return text
	}
	runes := []rune(text)
	return string(runes[:n]) + "…"
}

// subjectWithoutTarget explains a hold that no resolution reason covered (a session: / job:
// address that named nothing at all).
func subjectWithoutTarget(address string, cands []Candidate) string {
	if len(cands) > 0 {
		return fmt.Sprintf("%s matched %d sessions", address, len(cands))
	}
	return fmt.Sprintf("%s matched no deliverable session", address)
}
