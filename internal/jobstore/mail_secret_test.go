package jobstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// M8 of the mailbox (design 2026-10-16 §「安全、数据、运维与回滚」的「敏感信息」): the secret scan
// reaches letters, a letter can be redacted by id (its collection-box summaries included), and
// the retention sweep only deletes letters whose conversation is over.

const mailSecret = "mail-secret-7f3a91"

// insertSecretLetter stores one letter whose body carries the secret, from sessionA (caller
// alice) to sessionB.
func insertSecretLetter(t *testing.T, s *Store, over ...func(*MailInput)) Mail {
	t.Helper()
	in := sampleMail(func(in *MailInput) { in.Body = "token " + mailSecret + " end" })
	for _, fn := range over {
		fn(&in)
	}
	m, err := s.InsertMail(in)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestSecretScanReportsMailSection: a letter's secret shows up in the report's mail section with
// its thread, both ends as labels, its state and the masked position — and never the body.
func TestSecretScanReportsMailSection(t *testing.T) {
	s := openTest(t)
	m := insertSecretLetter(t, s)

	report, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Mail) != 1 {
		t.Fatalf("mail section = %+v, want the one letter", report.Mail)
	}
	hit := report.Mail[0]
	if hit.MailID != m.ID || hit.ThreadID != m.ThreadID {
		t.Fatalf("hit = %+v, want letter %d of thread %s", hit, m.ID, m.ThreadID)
	}
	if hit.From != "会话 sess-a（调用方 alice）" || hit.To != "会话 sess-b" {
		t.Fatalf("parties = %q → %q, want the labels of both ends", hit.From, hit.To)
	}
	if hit.State != MailPending {
		t.Fatalf("state = %q, want %q", hit.State, MailPending)
	}
	if len(hit.Locations) != 1 || hit.Locations[0].Position != "db:mail.body" || hit.Locations[0].Matches != 1 {
		t.Fatalf("locations = %+v, want one masked db:mail.body hit", hit.Locations)
	}
	if strings.Contains(report.String(), mailSecret) {
		t.Fatalf("report echoed the literal: %q", report.String())
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), mailSecret) {
		t.Fatalf("report JSON echoed the literal: %s", raw)
	}

	// A scan with a filter that does not cover the letter reports nothing.
	if other, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{OwnerID: "bob"}); err != nil {
		t.Fatal(err)
	} else if len(other.Mail) != 0 {
		t.Fatalf("bob's scan = %+v, want nothing", other.Mail)
	}
}

// TestSecretScanMailOwnerScope: a non-admin sees the letters of its own either side only.
func TestSecretScanMailOwnerScope(t *testing.T) {
	s := openTest(t)
	mine := insertSecretLetter(t, s) // from_caller alice
	// A letter alice received: from bob's session, addressed to alice's session.
	theirs, err := s.InsertMail(MailInput{
		FromKind: MailKindSession, FromID: "sess-b", FromCaller: "bob",
		ToAddress: "session:sess-a", ToKind: MailKindSession, ToID: "sess-a", ToCaller: "alice",
		Body: "received " + mailSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{OwnerID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]bool{}
	for _, hit := range report.Mail {
		got[hit.MailID] = true
	}
	if !got[mine.ID] || !got[theirs.ID] || len(report.Mail) != 2 {
		t.Fatalf("alice's scan = %+v, want both her sent and her received letter", report.Mail)
	}
	// The recipient-side letter of alice does carry the caller as its to_caller; a third
	// caller sees neither.
	if bob, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{OwnerID: "bob"}); err != nil {
		t.Fatal(err)
	} else if len(bob.Mail) != 1 || bob.Mail[0].MailID != theirs.ID {
		t.Fatalf("bob's scan = %+v, want only the letter he sent", bob.Mail)
	}
}

// TestSecretScanMailProjectFilter: -p selects the letters whose recipient or sender session / job
// is in that project.
func TestSecretScanMailProjectFilter(t *testing.T) {
	s := openTest(t)
	if _, err := s.UpsertAgentSession(AgentSession{
		SessionID: "sess-b", Agent: "claude", ProjectKey: "proj-mail", State: SessionRunning,
		LastSeenAt: 100, StartedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	insertSecretLetter(t, s)
	// A job end covers the job: a letter sent BY that job is in the job's project too.
	job := sampleJob("mail-job", "proj-other", 100)
	if err := s.UpsertJob(job); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertMail(MailInput{
		FromKind: MailKindJob, FromID: job.ID, FromCaller: "alice",
		ToAddress: "caller:", ToKind: MailKindCaller, ToCaller: "alice", Body: "from job " + mailSecret,
	}); err != nil {
		t.Fatal(err)
	}

	inProject, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{ProjectKey: "proj-mail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(inProject.Mail) != 1 || inProject.Mail[0].Project != "proj-mail" {
		t.Fatalf("project scan = %+v, want the letter whose recipient session is in proj-mail", inProject.Mail)
	}
	fromJob, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{ProjectKey: "proj-other"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fromJob.Mail) != 1 || fromJob.Mail[0].To == "" {
		t.Fatalf("project scan = %+v, want the letter whose sender job is in proj-other", fromJob.Mail)
	}
}

// TestRedactMailRewritesBodyAndSummaries: redacting one letter erases the secret from the letter
// and from the collection-box texts that quote it (the item's text and its journal lines), while
// the reference to the letter stays.
func TestRedactMailRewritesBodyAndSummaries(t *testing.T) {
	s := openTest(t)
	m := insertSecretLetter(t, s)
	item, err := s.CaptureWorkItem(CaptureInput{Text: "邮件 #" + itoa(m.ID) + " 待指派\n正文摘录：token " + mailSecret, Kind: WorkKindNote, By: "mail"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendWorkJournal(item.ID, WorkJournalNote, "信件 #"+itoa(m.ID)+" 收到回复：用了 "+mailSecret+" 这个 token", "mail"); err != nil {
		t.Fatal(err)
	}
	// An unrelated journal line that happens to quote another letter id is untouched.
	if _, err := s.AppendWorkJournal(item.ID, WorkJournalNote, "信件 #"+itoa(m.ID)+"8 收到回复："+mailSecret, "mail"); err != nil {
		t.Fatal(err)
	}

	report, err := s.RedactMail(m.ID, []string{mailSecret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.DBMatches == 0 {
		t.Fatalf("report = %+v, want the matches of the letter and of its summaries", report)
	}
	got, ok, err := s.GetMail(m.ID)
	if err != nil || !ok {
		t.Fatalf("read the letter back: ok=%t err=%v", ok, err)
	}
	if strings.Contains(got.Body, mailSecret) || !strings.Contains(got.Body, redactReplacement) {
		t.Fatalf("letter body = %q, want the literal replaced", got.Body)
	}
	itemText, ok, err := s.GetWorkItem(item.ID)
	if err != nil || !ok {
		t.Fatalf("read the item back: ok=%t err=%v", ok, err)
	}
	if strings.Contains(itemText.Title, mailSecret) {
		t.Fatalf("item text = %q, want the summary redacted too", itemText.Title)
	}
	if !strings.Contains(itemText.Title, "#"+itoa(m.ID)) {
		t.Fatalf("item text = %q, want the reference to the letter kept", itemText.Title)
	}
	entries, err := s.ListWorkJournal(item.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	redacted, untouched := 0, 0
	for _, e := range entries {
		switch {
		case strings.Contains(e.Text, "信件 #"+itoa(m.ID)+" 收到回复"):
			redacted++
			if strings.Contains(e.Text, mailSecret) {
				t.Fatalf("journal line kept the secret: %q", e.Text)
			}
		case strings.Contains(e.Text, "信件 #"+itoa(m.ID)+"8 收到回复"):
			untouched++
			if !strings.Contains(e.Text, mailSecret) {
				t.Fatalf("another letter's line was redacted: %q", e.Text)
			}
		}
	}
	if redacted != 1 || untouched != 1 {
		t.Fatalf("journal scan: redacted=%d untouched=%d, want 1/1 in %+v", redacted, untouched, entries)
	}
	// Nothing is left anywhere for the scan to find.
	left, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Mail) != 0 {
		t.Fatalf("secret survived the redaction: %+v", left.Mail)
	}
}

// TestRedactMailUnknownAndInvalidInput: a missing letter and an empty input are refused.
func TestRedactMailRefusals(t *testing.T) {
	s := openTest(t)
	if _, err := s.RedactMail(4242, []string{mailSecret}, nil); err == nil {
		t.Fatal("redacting a letter that does not exist must fail")
	}
	m := insertSecretLetter(t, s)
	if _, err := s.RedactMail(m.ID, nil, nil); err == nil {
		t.Fatal("redacting without a literal or pattern must fail")
	}
	if got, _, _ := s.GetMail(m.ID); got.Body != m.Body {
		t.Fatalf("a refused redaction changed the letter: %q", got.Body)
	}
}

// TestRedactSecretsCoversMail: `secret-scan --redact` redacts the letters it matched, in the same
// pass as the jobs.
func TestRedactSecretsCoversMail(t *testing.T) {
	s := openTest(t)
	job := sampleJob("batch-mail-job", "proj", 100)
	job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
	job.RequestJSON = `{"prompt":"` + mailSecret + `"}`
	if err := s.UpsertJob(job); err != nil {
		t.Fatal(err)
	}
	m := insertSecretLetter(t, s)

	report, err := s.RedactSecrets([]string{mailSecret}, nil, SecretScanFilter{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Redacted) != 1 || len(report.Mail) != 1 || report.Mail[0].MailID != m.ID || !report.WALPurged {
		t.Fatalf("batch report = %+v", report)
	}
	if got, _, _ := s.GetMail(m.ID); strings.Contains(got.Body, mailSecret) {
		t.Fatalf("letter body = %q, want it redacted by the batch", got.Body)
	}
}

// TestRedactJobIncludesRelatedLetters: a letter carrying a job_id belongs to that job, so `job
// redact` erases its secret — and the collection-box summary quoting it.
func TestRedactJobIncludesRelatedLetters(t *testing.T) {
	s := openTest(t)
	job := sampleJob("redact-job-mail", "proj", 100)
	job.Status, job.EndedAt, job.UpdatedAt = "done", 200, 200
	if err := s.UpsertJob(job); err != nil {
		t.Fatal(err)
	}
	m := insertSecretLetter(t, s, func(in *MailInput) { in.JobID = job.ID })
	item, err := s.CaptureWorkItem(CaptureInput{Text: "信件 #" + itoa(m.ID) + " 收到回复：token " + mailSecret, Kind: WorkKindNote, By: "mail"})
	if err != nil {
		t.Fatal(err)
	}

	report, err := s.RedactJob(job.ID, []string{mailSecret}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.DBMatches == 0 {
		t.Fatalf("report = %+v, want the letter's matches", report)
	}
	if got, _, _ := s.GetMail(m.ID); strings.Contains(got.Body, mailSecret) {
		t.Fatalf("the job's letter kept the secret: %q", got.Body)
	}
	if itemText, ok, err := s.GetWorkItem(item.ID); err != nil || !ok {
		t.Fatalf("read the item back: ok=%t err=%v", ok, err)
	} else if strings.Contains(itemText.Title, mailSecret) {
		t.Fatalf("the summary of the job's letter kept the secret: %q", itemText.Title)
	}
	left, err := s.ScanSecrets([]string{mailSecret}, nil, SecretScanFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left.Mail) != 0 {
		t.Fatalf("secret survived job redaction: %+v", left.Mail)
	}
}

// TestPruneMailKeepsNonTerminalLetters: the retention sweep deletes terminal letters only — a
// held letter is somebody's to-do and a letter still on its way has not been read yet.
func TestPruneMailKeepsNonTerminalLetters(t *testing.T) {
	f := newMailFixture(t)
	insert := func(body string) int64 {
		t.Helper()
		m, err := f.store.InsertMail(sampleMail(func(in *MailInput) { in.Body = body }))
		if err != nil {
			t.Fatal(err)
		}
		return m.ID
	}
	delivered := insert("delivered letter")
	held := insert("held letter")
	cancelled := insert("cancelled letter")
	inflight := insert("in-flight letter")
	if _, _, err := f.store.ClaimMail(delivered, "hook", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.ConfirmMail(delivered); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.HoldMail(held, "nobody to hand it to"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.CancelMail(cancelled, "withdrawn"); err != nil {
		t.Fatal(err)
	}
	// Its claim outlives the sweep, so it is genuinely in flight rather than abandoned.
	if _, _, err := f.store.ClaimMail(inflight, "deliverer", 200*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	pending := insert("pending letter")

	f.advance(91 * 24 * time.Hour)
	cut := f.now.Add(-90 * 24 * time.Hour).Unix()
	n, err := f.store.PruneMail(cut)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // the delivered and the cancelled letter
		t.Fatalf("pruned %d letter(s), want 2 (delivered + cancelled)", n)
	}
	for _, tc := range []struct {
		state string
		id    int64
	}{{MailHeld, held}, {MailPending, pending}, {MailDelivering, inflight}} {
		if got, ok, err := f.store.GetMail(tc.id); err != nil || !ok {
			t.Fatalf("the %s letter was pruned (ok=%t err=%v), want it kept", tc.state, ok, err)
		} else if got.State != tc.state {
			t.Fatalf("letter %d is %s, want %s", tc.id, got.State, tc.state)
		}
	}
	if _, ok, _ := f.store.GetMail(delivered); ok {
		t.Fatal("the delivered letter survived the sweep")
	}
	if _, ok, _ := f.store.GetMail(cancelled); ok {
		t.Fatal("the cancelled letter survived the sweep")
	}
}

func itoa(id int64) string { return mailIDString(id) }
