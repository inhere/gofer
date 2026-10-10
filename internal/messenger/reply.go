package messenger

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// PeerReply is a message another Claude session sent TO a resident messenger —
// in practice a target session answering a web message with SendMessage (its
// natural reply path: the message came from the messenger's socket). Body is the
// verbatim text from the messenger's own transcript; it is empty when the
// transcript could not be read, and Retold then holds the messenger model's own
// account of the turn (never posted as the target's words).
type PeerReply struct {
	From   string // peer address, e.g. uds:/tmp/cc-socks/<pid>.sock
	Name   string // peer session name (Claude Code's from-name)
	Body   string
	Retold string
	MsgID  string
	At     int64
}

// ReplyHandler receives every PeerReply with a verbatim Body, on its own
// goroutine (the stdout reader never waits for it).
type ReplyHandler func(runner string, r PeerReply)

// replyState is the Manager's reply bookkeeping: the handler, the Claude config
// dir override and the transcript entries already reported.
type replyState struct {
	mu        sync.Mutex
	handler   ReplyHandler
	configDir string
	seen      map[string]bool
	seenOrder []string
}

// maxSeenReplies bounds the reported-entry memory (oldest forgotten first).
const maxSeenReplies = 512

// replyTranscriptTail is how much of the messenger's own transcript is scanned for
// peer messages; a messenger turn is short, so the newest entries are near the end.
const replyTranscriptTail = 1 << 20

// SetReplyHandler installs the callback for peer replies (nil = only record them
// in the delivery history).
func (m *Manager) SetReplyHandler(h ReplyHandler) {
	m.replies.mu.Lock()
	m.replies.handler = h
	m.replies.mu.Unlock()
}

// SetClaudeConfigDir overrides where the resident process's Claude transcripts
// live ("" = ~/.claude: the child runs with every CLAUDE* variable scrubbed, so
// CLAUDE_CONFIG_DIR never applies to it).
func (m *Manager) SetClaudeConfigDir(dir string) {
	m.replies.mu.Lock()
	m.replies.configDir = strings.TrimSpace(dir)
	m.replies.mu.Unlock()
}

func (p *process) setSession(id, cwd string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	p.sessMu.Lock()
	p.sessionID, p.sessCwd = id, cwd
	p.sessMu.Unlock()
}

func (p *process) session() (string, string) {
	p.sessMu.Lock()
	defer p.sessMu.Unlock()
	return p.sessionID, p.sessCwd
}

// unsolicited handles a turn nobody asked for: almost always a peer's message.
// It never blocks the stdout reader.
func (p *process) unsolicited(retold string) {
	if p.owner == nil {
		return
	}
	go p.owner.collectReplies(p, retold, true)
}

// scanReplies looks for peer messages absorbed into a requested turn (Claude Code
// folds a message that arrives mid-turn into the running one).
func (p *process) scanReplies(retold string) {
	if p.owner == nil {
		return
	}
	go p.owner.collectReplies(p, retold, false)
}

// collectReplies reads the process's transcript for peer messages not reported
// yet, records each in the runner's delivery history and hands verbatim ones to
// the handler. peerTurn says the trigger was an unsolicited turn: when its message
// cannot be read the model's retelling is still recorded (history only).
//
// The retelling is recorded only when the transcript holds no peer message at all.
// The requested turn's scan runs on its own goroutine and often reads the peer
// message first (Claude Code writes it before the peer turn's result); the peer
// turn then finds nothing new, and recording its retelling as well listed the
// reply twice, once without a sender (gofer-rgnw).
func (m *Manager) collectReplies(p *process, retold string, peerTurn bool) {
	sid, cwd := p.session()
	m.replies.mu.Lock()
	defer m.replies.mu.Unlock()
	var found []PeerReply
	inTranscript := false
	if sid != "" && cwd != "" {
		path := filepath.Join(m.claudeDirLocked(), "projects", claudeProjectDirName(cwd), sid+".jsonl")
		peers := peerMessagesIn(path)
		inTranscript = len(peers) > 0
		for _, r := range peers {
			key := path + "\x00" + r.MsgID
			if r.MsgID == "" {
				key = path + "\x00" + r.From + "\x00" + r.Body
			}
			if m.replies.seen[key] {
				continue
			}
			m.markSeenLocked(key)
			found = append(found, r)
		}
	}
	if len(found) == 0 && peerTurn && !inTranscript {
		found = append(found, PeerReply{Retold: strings.TrimSpace(retold), At: time.Now().Unix()})
	}
	for _, r := range found {
		if r.Retold == "" && peerTurn && len(found) == 1 {
			r.Retold = strings.TrimSpace(retold)
		}
		text := r.Body
		if text == "" {
			text = r.Retold
		}
		m.recordReply(p.runner, r.Name, text)
		if r.Body != "" && m.replies.handler != nil {
			go m.replies.handler(p.runner, r)
		}
	}
}

func (m *Manager) markSeenLocked(key string) {
	if m.replies.seen == nil {
		m.replies.seen = map[string]bool{}
	}
	m.replies.seen[key] = true
	m.replies.seenOrder = append(m.replies.seenOrder, key)
	if len(m.replies.seenOrder) > maxSeenReplies {
		delete(m.replies.seen, m.replies.seenOrder[0])
		m.replies.seenOrder = m.replies.seenOrder[1:]
	}
}

func (m *Manager) claudeDirLocked() string {
	if m.replies.configDir != "" {
		return m.replies.configDir
	}
	return filepath.Join(homeDir(), ".claude")
}

// recordReply adds a "reply" entry to the runner's delivery history (Runners →
// 传话人 drawer; a worker reports it with its messenger snapshot).
func (m *Manager) recordReply(runner, from, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statsFor(runner).record(Delivery{At: time.Now().Unix(), Op: OpReply, Target: from, Message: excerpt(text), OK: true})
}

// OpReply marks a delivery-history entry that is a session's reply received by
// the messenger (not a request sent through it).
const OpReply = "reply"

// peerMessagesIn returns the peer messages in a Claude Code transcript's tail, in
// order: user entries (and mid-turn attachments) whose origin is
// {kind:"peer", from, name, body, msg_id}. Tolerant: unreadable input → nil.
func peerMessagesIn(path string) []PeerReply {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if st.Size() > replyTranscriptTail {
		start = st.Size() - replyTranscriptTail
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	var out []PeerReply
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if !bytes.Contains(line, []byte(`"peer"`)) {
			continue
		}
		var entry struct {
			Type       string      `json:"type"`
			Timestamp  string      `json:"timestamp"`
			Origin     *peerOrigin `json:"origin"`
			Attachment struct {
				Origin *peerOrigin `json:"origin"`
			} `json:"attachment"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type == "assistant" {
			continue
		}
		o := entry.Origin
		if o == nil {
			o = entry.Attachment.Origin
		}
		if o == nil || o.Kind != "peer" || strings.TrimSpace(o.Body) == "" {
			continue
		}
		at := time.Now().Unix()
		if ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
			at = ts.Unix()
		}
		out = append(out, PeerReply{From: o.From, Name: o.Name, Body: strings.TrimSpace(o.Body), MsgID: o.MsgID, At: at})
	}
	return out
}

type peerOrigin struct {
	Kind  string `json:"kind"`
	From  string `json:"from"`
	Name  string `json:"name"`
	Body  string `json:"body"`
	MsgID string `json:"msg_id"`
}

// claudeProjectDirName is Claude Code's directory name for a project under
// ~/.claude/projects (same rule as sessionrelay.EncodeClaudeProjectDir: every
// character that is not an ASCII letter or digit becomes "-", one per UTF-16
// code unit).
func claudeProjectDirName(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			for range utf16.RuneLen(r) {
				b.WriteByte('-')
			}
		}
	}
	return b.String()
}
