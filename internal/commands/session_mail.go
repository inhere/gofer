package commands

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
)

// `gofer session mail ...`: the gofer mailbox from the command line (设计「二、gofer 信箱」,
// plan M3). The client attaches the session of the environment, so a letter sent from inside an
// agent session comes "from" that session and `reply` goes back to whoever wrote.

var sessionMailOpts struct {
	urgent   bool
	bodyFile string
	to       gcli.Strings
	in, out  bool
	thread   string
	state    string
	session  string
	limit    int
}

// sessionMailRedactOpts holds `session mail redact` flags: the same literal / pattern input job
// redaction takes, so the secret itself never lands in argv / the process list.
var sessionMailRedactOpts struct {
	literalFromStdin bool
	patterns         gcli.Strings
}

// sessionMailAgentOpts holds `session mail agents` flags.
var sessionMailAgentOpts struct {
	end     string
	project string
}

// newSessionMailCmd builds `session mail`.
func newSessionMailCmd() *gcli.Command {
	common := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
	}
	idArg := func(c *gcli.Command) {
		common(c)
		c.AddArg("id", "letter id", true)
	}
	return &gcli.Command{
		Name: "mail",
		Desc: "Letters between sessions, jobs and callers: send <address> <text> | ls | read <id> | reply <id> <text> | cancel <id> | conv <cv-id> | agents | redact <id>",
		Subs: []*gcli.Command{
			{
				Name: "send",
				Desc: "Send a letter: <address> is session:<sid> | job:<id> | agent:<name>[@<project>] | topic:<keyword> | path:<sub-path> | caller (your own capture box); repeat --to or use each:topic:/each:path:/each:project:/each:agent: to send to every match",
				Config: func(c *gcli.Command) {
					common(c)
					c.AddArg("address", "session:<sid> | job:<id> | agent:<name>[@<project>] | topic:<keyword> | path:<sub-path> | caller", false)
					c.AddArg("text", "the letter (or --body-file; `-` reads stdin)", false)
					c.VarOpt(&sessionMailOpts.to, "to", "", "recipient address (repeatable; also each: selectors)")
					c.BoolOpt(&sessionMailOpts.urgent, "urgent", "", false, "deliver inside the running turn / wake an idle session at once")
					c.StrOpt(&sessionMailOpts.bodyFile, "body-file", "f", "", "read the letter from a file (`-` = stdin)")
				},
				Func: runSessionMailSend,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List letters (both directions by default)",
				Config: func(c *gcli.Command) {
					common(c)
					c.BoolOpt(&sessionMailOpts.in, "in", "", false, "only letters you received")
					c.BoolOpt(&sessionMailOpts.out, "out", "", false, "only letters you sent")
					c.StrOpt(&sessionMailOpts.thread, "thread", "", "", "only this conversation thread")
					c.StrOpt(&sessionMailOpts.state, "state", "", "", "only these states (comma-separated): pending|delivering|delivered|answered|held|expired|failed|cancelled")
					c.StrOpt(&sessionMailOpts.session, "session", "", "", "only letters of this session")
					c.IntOpt(&sessionMailOpts.limit, "limit", "", 0, "max rows (default 50)")
				},
				Func: runSessionMailList,
			},
			{
				Name:    "conv",
				Aliases: []string{"conversation"},
				Desc:    "Show one conversation by its id (cv-…): who the two people are and the letters, oldest first",
				Config: func(c *gcli.Command) {
					common(c)
					c.AddArg("id", "conversation id (cv-…)", true)
					c.BoolOpt(&sessionMailOpts.in, "in", "", false, "only the letters you received")
					c.BoolOpt(&sessionMailOpts.out, "out", "", false, "only the letters you sent")
					c.IntOpt(&sessionMailOpts.limit, "limit", "", 0, "max letters (default: the whole conversation)")
				},
				Func: runSessionMailConv,
			},
			{Name: "read", Desc: "Read a letter (this marks it read: it is not injected again)", Config: idArg, Func: runSessionMailRead},
			{
				Name: "reply",
				Desc: "Answer a letter in its thread (the answer goes back to the sender)",
				Config: func(c *gcli.Command) {
					common(c)
					c.AddArg("id", "letter id", true)
					c.AddArg("text", "the answer (or --body-file; `-` reads stdin)", false)
					c.BoolOpt(&sessionMailOpts.urgent, "urgent", "", false, "mark the answer urgent")
					c.StrOpt(&sessionMailOpts.bodyFile, "body-file", "f", "", "read the answer from a file (`-` = stdin)")
				},
				Func: runSessionMailReply,
			},
			{Name: "cancel", Desc: "Withdraw a letter that has not been delivered", Config: idArg, Func: runSessionMailCancel},
			{
				Name: "agents",
				Desc: "List the mail agents (the names `agent:<name>` may use) and their resident sessions; --end stops one",
				Config: func(c *gcli.Command) {
					common(c)
					c.StrOpt(&sessionMailAgentOpts.end, "end", "", "", "end the resident session of this agent name")
					c.StrOpt(&sessionMailAgentOpts.project, "project", "p", "", "project of the resident session")
				},
				Func: runSessionMailAgents,
			},
			{
				Name: "redact",
				Desc: "Erase secrets from a letter (and from the collection-box summaries quoting it)",
				Config: func(c *gcli.Command) {
					common(c)
					c.AddArg("id", "letter id", true)
					c.BoolOpt(&sessionMailRedactOpts.literalFromStdin, "literal-from-stdin", "", false, "read the secret from stdin (keeps it out of argv)")
					c.VarOpt(&sessionMailRedactOpts.patterns, "pattern", "", "RE2 pattern to erase (repeatable)")
				},
				Func: runSessionMailRedact,
			},
		},
	}
}

// mailBodyInput resolves the letter text: the argument, else --body-file (`-` = stdin).
func mailBodyInput(c *gcli.Command) (string, error) {
	text := c.Arg("text").String()
	file := strings.TrimSpace(sessionMailOpts.bodyFile)
	switch {
	case text != "" && file != "":
		return "", fmt.Errorf("give the text or --body-file, not both")
	case file == "-":
		b, err := io.ReadAll(os.Stdin)
		return strings.TrimSpace(string(b)), err
	case file != "":
		b, err := os.ReadFile(file)
		return strings.TrimSpace(string(b)), err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("the letter is empty: give the text or --body-file")
	}
	return text, nil
}

func parseMailID(c *gcli.Command) (int64, error) {
	raw := strings.TrimSpace(c.Arg("id").String())
	id, err := strconv.ParseInt(strings.TrimPrefix(raw, "#"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("invalid letter id %q", raw)
	}
	return id, nil
}

func runSessionMailSend(c *gcli.Command, _ []string) error {
	body, err := mailBodyInput(c)
	if err != nil {
		return err
	}
	to := mailSendAddresses(c)
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	res, err := sendMail(clientSend{cli, to}, body)
	if err != nil {
		return err
	}
	c.Print(formatMailSent("sent", res))
	return nil
}

// clientSend is the one-door send the CLI and the MCP tool share: one plain address keeps the
// single-letter request, several (or one each: selector) go out as a broadcast.
type clientSend struct {
	cli *client.Client
	to  []string
}

func sendMail(s clientSend, body string) (client.MailSent, error) {
	if len(s.to) == 1 && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.to[0])), "each:") {
		return s.cli.SendMail(s.to[0], body, sessionMailOpts.urgent)
	}
	return s.cli.SendMailTo(s.to, body, sessionMailOpts.urgent)
}

// mailSendAddresses collects where a letter goes: repeated --to, plus the positional address
// (still the common one-recipient form). Nothing at all is the caller's own capture box.
func mailSendAddresses(c *gcli.Command) []string {
	to := make([]string, 0, len(sessionMailOpts.to)+1)
	for _, a := range sessionMailOpts.to {
		if a = strings.TrimSpace(a); a != "" {
			to = append(to, a)
		}
	}
	if a := strings.TrimSpace(c.Arg("address").String()); a != "" {
		to = append(to, a)
	}
	if len(to) == 0 {
		to = append(to, "")
	}
	return to
}

func runSessionMailReply(c *gcli.Command, _ []string) error {
	id, err := parseMailID(c)
	if err != nil {
		return err
	}
	body, err := mailBodyInput(c)
	if err != nil {
		return err
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	res, err := cli.ReplyMail(id, body, sessionMailOpts.urgent)
	if err != nil {
		return err
	}
	c.Print(formatMailSent(fmt.Sprintf("replied to #%d", id), res))
	return nil
}

func runSessionMailList(c *gcli.Command, _ []string) error {
	if sessionMailOpts.in && sessionMailOpts.out {
		return fmt.Errorf("--in and --out are exclusive")
	}
	box := "all"
	if sessionMailOpts.in {
		box = "in"
	} else if sessionMailOpts.out {
		box = "out"
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	list, err := cli.ListMail(client.MailListOpts{
		Box: box, Thread: sessionMailOpts.thread, State: sessionMailOpts.state,
		Session: sessionMailOpts.session, Limit: sessionMailOpts.limit,
	})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no letters")
		return nil
	}
	c.Print(formatMailList(list))
	return nil
}

// runSessionMailConv reads one conversation by its short id (`session mail conv cv-…`): the id
// is the copyable handle of a conversation, so a person can quote it without naming both ends.
func runSessionMailConv(c *gcli.Command, _ []string) error {
	if sessionMailOpts.in && sessionMailOpts.out {
		return fmt.Errorf("--in and --out are exclusive")
	}
	box := "all"
	if sessionMailOpts.in {
		box = "in"
	} else if sessionMailOpts.out {
		box = "out"
	}
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return fmt.Errorf("give the conversation id (cv-…)")
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	conv, err := cli.GetMailConversation(id, box)
	if err != nil {
		return err
	}
	list, err := cli.ListMail(client.MailListOpts{Box: box, Conv: conv.ID, Limit: sessionMailOpts.limit})
	if err != nil {
		return err
	}
	c.Print(formatMailConversation(conv, list))
	return nil
}

// formatMailConversation renders `session mail conv`: the conversation's id and the two people,
// then its letters oldest first (the order the server reads a conversation in).
func formatMailConversation(conv client.MailConversation, list []client.Mail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "conversation %s\n", conv.ID)
	fmt.Fprintf(&b, "a: %s  [%s]\n", mailParty(conv.A, ""), mailAddress(conv.A))
	fmt.Fprintf(&b, "b: %s  [%s]\n", mailParty(conv.B, ""), mailAddress(conv.B))
	fmt.Fprintf(&b, "%d letters, %d unread, %d urgent, last %s\n", conv.Count, conv.Unread, conv.Urgent, mailTime(conv.ActivityAt))
	if len(list) == 0 {
		b.WriteString("no letters\n")
		return b.String()
	}
	b.WriteString(formatMailList(list))
	return b.String()
}

// mailAddress is the wire address of one end of a letter (session:<sid> / job:<id> / caller:<id>).
func mailAddress(p client.MailParty) string {
	switch {
	case p.Kind == "" && p.Display != "":
		return p.Display
	case p.ID != "":
		return p.Kind + ":" + p.ID
	case p.Kind != "":
		return p.Kind + ":" + p.Caller
	}
	return "-"
}

func runSessionMailRead(c *gcli.Command, _ []string) error {
	id, err := parseMailID(c)
	if err != nil {
		return err
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	m, err := cli.ReadMail(id)
	if err != nil {
		return err
	}
	c.Print(formatMail(m))
	return nil
}

func runSessionMailCancel(c *gcli.Command, _ []string) error {
	id, err := parseMailID(c)
	if err != nil {
		return err
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	m, err := cli.CancelMail(id)
	if err != nil {
		return err
	}
	c.Printf("cancelled letter #%d\n", m.ID)
	return nil
}

func runSessionMailRedact(c *gcli.Command, _ []string) error {
	id, err := parseMailID(c)
	if err != nil {
		return err
	}
	if !sessionMailRedactOpts.literalFromStdin && len(sessionMailRedactOpts.patterns) == 0 {
		return fmt.Errorf("session mail redact requires --literal-from-stdin and/or --pattern")
	}
	req := client.MailRedactRequest{Patterns: append([]string(nil), sessionMailRedactOpts.patterns...)}
	if sessionMailRedactOpts.literalFromStdin {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read literal from stdin: %w", err)
		}
		literal := strings.TrimRight(string(raw), "\r\n")
		if literal == "" {
			return fmt.Errorf("session mail redact literal from stdin is empty")
		}
		req.Literals = []string{literal}
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	report, err := cli.RedactMail(id, req)
	if err != nil {
		return err
	}
	c.Printf("letter #%d redacted: db_matches=%d file_matches=%d\n", id, report.DBMatches, report.FileMatches)
	return nil
}

func mailParty(p client.MailParty, raw string) string {
	switch {
	case p.Display != "":
		return p.Display
	case p.Kind == "":
		if raw != "" {
			return raw
		}
		return "-"
	case p.ID != "":
		return p.Kind + ":" + p.ID
	}
	return p.Kind + " " + p.Caller
}

func mailTime(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// formatMailSent renders the answer of a send / reply: one line, plus the capture-box note and
// the candidates when the letter could not be handed to a unique recipient.
func formatMailSent(verb string, r client.MailSent) string {
	var b strings.Builder
	m := r.Mail
	fmt.Fprintf(&b, "%s letter #%d → %s (%s)\n", verb, m.ID, mailParty(m.To, m.ToAddress), m.State)
	if r.BroadcastID > 0 {
		fmt.Fprintf(&b, "broadcast b%d: matched %d, delivered %d, skipped %d (thread %s)\n",
			r.BroadcastID, r.Matched, r.Delivered, r.Skipped, r.ThreadID)
		for _, rc := range r.Recipients {
			if rc.Skipped {
				fmt.Fprintf(&b, "  skipped %s: %s\n", rc.Address, rc.Reason)
				continue
			}
			fmt.Fprintf(&b, "  #%d %s %s (%s)\n", rc.MailID, rc.Kind, rc.ID, rc.State)
		}
	}
	if !r.Held {
		return b.String()
	}
	b.WriteString("held: nobody could be handed this letter")
	if m.LastError != "" {
		b.WriteString(" — " + m.LastError)
	}
	b.WriteString("\n")
	if r.CaptureItemID != "" {
		fmt.Fprintf(&b, "  recorded in the capture box as work item %s\n", r.CaptureItemID)
	}
	for _, c := range r.Candidates {
		fmt.Fprintf(&b, "  candidate: %s (session:%s)\n", c.Display, c.ID)
	}
	return b.String()
}

func mailSummary(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// formatMailList renders `session mail ls`: one line per letter, newest first.
func formatMailList(list []client.Mail) string {
	var b strings.Builder
	for _, m := range list {
		urgent := ""
		if m.Urgent {
			urgent = " !"
		}
		fmt.Fprintf(&b, "#%d  %s  %-9s%s  %s → %s  %s\n", m.ID, mailTime(m.CreatedAt), m.State, urgent,
			mailParty(m.From, ""), mailParty(m.To, m.ToAddress), mailSummary(m.Body, 60))
	}
	return b.String()
}

// formatMail renders one letter in full (`session mail read`).
func formatMail(m client.Mail) string {
	var b strings.Builder
	fmt.Fprintf(&b, "letter #%d  thread %s  [%s]%s\n", m.ID, m.ThreadID, m.State, map[bool]string{true: "  URGENT", false: ""}[m.Urgent])
	fmt.Fprintf(&b, "from: %s\n", mailParty(m.From, ""))
	fmt.Fprintf(&b, "to:   %s\n", mailParty(m.To, m.ToAddress))
	fmt.Fprintf(&b, "sent: %s", mailTime(m.CreatedAt))
	if m.ReadAt > 0 {
		fmt.Fprintf(&b, "   read: %s", mailTime(m.ReadAt))
	}
	if m.ReplyTo > 0 {
		fmt.Fprintf(&b, "   in reply to #%d", m.ReplyTo)
	}
	b.WriteString("\n")
	if m.Broadcast != nil {
		fmt.Fprintf(&b, "broadcast b%d: %d recipients, delivered %d, answered %d",
			m.Broadcast.ID, m.Broadcast.Count, m.Broadcast.Delivered, m.Broadcast.Answered)
		if m.Broadcast.Expired > 0 {
			fmt.Fprintf(&b, ", expired %d", m.Broadcast.Expired)
		}
		if m.Broadcast.Pending > 0 {
			fmt.Fprintf(&b, ", pending %d", m.Broadcast.Pending)
		}
		b.WriteString("\n")
		for _, r := range m.Broadcast.Recipients {
			who := r.ID
			if who == "" {
				who = r.Address
			}
			state := r.State
			if r.Skipped {
				state = "skipped: " + r.Reason
			}
			fmt.Fprintf(&b, "  #%d %s %s (%s)\n", r.MailID, r.Kind, who, state)
		}
	}
	b.WriteString("\n" + m.Body + "\n")
	return b.String()
}

// runSessionMailAgents lists the `agent:` names and their resident sessions (设计 2026-10-22
// mail-agent-address「D6」); --end stops one by hand.
func runSessionMailAgents(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	if name := strings.TrimSpace(sessionMailAgentOpts.end); name != "" {
		if err := cli.EndMailAgent(name, sessionMailAgentOpts.project); err != nil {
			return err
		}
		c.Printf("ended the resident session of %s\n", name)
		return nil
	}
	list, err := cli.ListMailAgents()
	if err != nil {
		return err
	}
	c.Print(formatMailAgents(list))
	return nil
}

// formatMailAgents renders the `agent:` listing: the configured names, then the sessions.
func formatMailAgents(list client.MailAgents) string {
	var b strings.Builder
	if len(list.Agents) == 0 {
		b.WriteString("no mail agents configured (add a mail.agents block to the config)\n")
	} else {
		fmt.Fprintf(&b, "mail agents (at most %d resident sessions):\n", list.MaxResident)
		for _, a := range list.Agents {
			fmt.Fprintf(&b, "  %-12s agent=%s idle=%dm letters<=%d hours<=%d\n",
				a.Name, a.Agent, a.IdleMinutes, a.MaxLetters, a.MaxHours)
		}
	}
	if len(list.Residents) == 0 {
		b.WriteString("no resident sessions\n")
		return b.String()
	}
	b.WriteString("resident sessions:\n")
	for _, r := range list.Residents {
		fmt.Fprintf(&b, "  %-12s project=%-16s job=%-14s letters=%d pending=%d status=%s",
			r.Name, dashIfEmpty(r.Project), dashIfEmpty(r.JobID), r.Letters, r.Pending, r.Status)
		if r.CreatedAt > 0 {
			fmt.Fprintf(&b, " since=%s", time.Unix(r.CreatedAt, 0).Format("2006-01-02 15:04"))
		}
		if r.LastError != "" {
			fmt.Fprintf(&b, " last_error=%s", r.LastError)
		}
		b.WriteString("\n")
	}
	return b.String()
}
