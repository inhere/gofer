package hookrelay

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	rusage "github.com/inhere/gofer/internal/runner"
)

// Terminal-session usage collection (N2 §A, OBS-14). On Stop / SubagentStop /
// SessionEnd the hook reads what the agent appended to its transcript since the last
// report and sends the token DELTA on the heartbeat. A hook process is short-lived, so
// the read offset lives on disk (<config-dir>/run/hook-usage/<session_id>.json).
//
// Nothing here may hurt the hook: a failure is logged (debug only) and the beat goes
// out without usage; a read is capped (usageReadBudget bytes per call, the rest is
// picked up by the next event); and the new offset is only saved once the hub took the
// beat, so a lost heartbeat re-sends the same bytes next time.

const (
	// usageReadBudget caps how many transcript bytes one hook invocation reads across
	// the main file and every sub-agent file; the remainder is read by the next event.
	usageReadBudget int64 = 4 << 20
	// usageMaxLine is the longest single transcript line parsed; a longer one (a huge
	// tool result) is skipped, never buffered whole.
	usageMaxLine = 8 << 20
	// usageSeenCap bounds the remembered message ids (claude repeats one message's
	// usage on every content-block row; the repeats are adjacent, so a window is enough).
	usageSeenCap = 4096
	// usageMaxSubFiles bounds how many subagents/*.jsonl files one session tracks.
	usageMaxSubFiles = 256
	// usageLockStale: a lock older than this is a crashed hook's and is taken over.
	usageLockStale = 15 * time.Second
	// usageStateTTL: SessionEnd prunes state files untouched for this long.
	usageStateTTL = 30 * 24 * time.Hour
	usageStateVer = 1
)

// usageEvents are the hook events that report usage. PostToolUse is deliberately not
// one: it is the high-frequency event and must stay free of transcript IO.
func usageEvent(ev string) bool {
	return ev == "Stop" || ev == "SubagentStop" || ev == "SessionEnd"
}

// usageSupported: the dialects whose transcript format is understood. Others (jcode)
// are not collected.
func usageSupported(dialect string) bool {
	switch dialect {
	case AgentClaude, AgentCodex, AgentOmp, DialectGeneric:
		return true
	}
	return false
}

// usageFile is the saved read position of one transcript file.
type usageFile struct {
	Offset int64 `json:"offset"`
	// Head fingerprints the first bytes of the file as last read (HeadLen of them): a
	// rewritten file (compaction) that grew past the old offset is told from an append.
	HeadLen int    `json:"head_len,omitempty"`
	Head    string `json:"head,omitempty"`
	// Cum is the last cumulative total seen in a codex rollout (token_count events
	// carry running totals, so the report is the difference).
	Cum *rusage.Usage `json:"cum,omitempty"`
	// Model is the model last named by the file (codex turn_context / omp model_change).
	Model string `json:"model,omitempty"`
}

// usageState is the on-disk state of one session.
type usageState struct {
	V     int                   `json:"v"`
	Files map[string]*usageFile `json:"files"`
	// Seen is the pre-v0.125 format: bare id hashes, counted by their first row only.
	// It is read once (loadState folds it into Msgs as Legacy entries) and never written.
	// DEPRECATED(v0.125): remove in v0.128.
	Seen []string `json:"seen,omitempty"`
	// Msgs is the FIFO window (oldest first, usageSeenCap entries) of message ids with
	// the usage already counted for each.
	Msgs []*usageSeenMsg `json:"msgs,omitempty"`
}

// usageSeenMsg is the usage already counted for one message id (K = hash of the id).
// Claude may write one message's rows with a growing output_tokens (the last row holds
// the final value), so a repeat reports only what exceeds these counters.
type usageSeenMsg struct {
	K string `json:"k"`
	I int64  `json:"i,omitempty"`
	O int64  `json:"o,omitempty"`
	R int64  `json:"r,omitempty"`
	W int64  `json:"w,omitempty"`
	// Legacy: migrated from the old Seen list, whose counted amounts were not stored.
	// Treating them as 0 would re-count the whole message, so a legacy id is never
	// topped up: it keeps its first-row figure (the pre-fix behaviour) and only ids
	// first seen after the upgrade get exact accounting.
	Legacy bool `json:"l,omitempty"`
}

type usageCollector struct {
	dir     string
	sid     string
	path    string // the main transcript
	dialect string
	budget  int64
	log     func(string, ...any)

	statePath string
	lockPath  string
	state     usageState
	seen      map[string]*usageSeenMsg
	read      int64
}

// collectUsage reads the new transcript bytes of the session and returns the delta to
// report plus a commit func: call commit(true) once the hub accepted the beat (the new
// offsets are then persisted), commit(false) otherwise. delta is nil when there is
// nothing to report. It never fails the hook.
func (r *runner) collectUsage() (*rusage.SessionUsage, func(bool)) {
	dir := strings.TrimSpace(r.opts.UsageStateDir)
	if dir == "" || !usageEvent(r.p.Event) || strings.TrimSpace(r.p.SessionID) == "" ||
		strings.TrimSpace(r.p.TranscriptPath) == "" || !usageSupported(r.p.dialect()) {
		return nil, nil
	}
	budget := r.opts.UsageReadBudget
	if budget <= 0 {
		budget = usageReadBudget
	}
	c := &usageCollector{dir: dir, sid: r.p.SessionID, path: r.p.TranscriptPath,
		dialect: r.p.dialect(), budget: budget, log: r.log}
	delta, commit, err := c.collect()
	if err != nil {
		r.log("usage: skipped: %v", err)
		return nil, nil
	}
	if r.p.Event == "SessionEnd" {
		inner := commit
		commit = func(ok bool) {
			inner(ok)
			pruneUsageState(dir, r.opts.now())
		}
	}
	return delta, commit
}

func usageStateName(sid string) string {
	clean := make([]rune, 0, len(sid))
	for _, ch := range sid {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9', ch == '-', ch == '_', ch == '.':
			clean = append(clean, ch)
		default:
			clean = append(clean, '_')
		}
	}
	name := string(clean)
	if name == "" || name != sid || len(name) > 100 || strings.HasPrefix(name, ".") {
		h := sha1.Sum([]byte(sid))
		if len(name) > 60 {
			name = name[:60]
		}
		name = strings.TrimLeft(name, ".") + "-" + hex.EncodeToString(h[:6])
	}
	return name
}

func (c *usageCollector) collect() (*rusage.SessionUsage, func(bool), error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return nil, nil, err
	}
	name := usageStateName(c.sid)
	c.statePath = filepath.Join(c.dir, name+".json")
	c.lockPath = filepath.Join(c.dir, name+".lock")
	if err := c.lock(); err != nil {
		return nil, nil, err
	}
	released := false
	release := func() {
		if !released {
			released = true
			_ = os.Remove(c.lockPath)
		}
	}
	c.loadState()

	delta := &rusage.SessionUsage{}
	// Main transcript first, then the sub-agent files (their order is stable: sorted).
	c.scanFile(c.path, false, delta)
	for _, sub := range c.subagentFiles() {
		if c.read >= c.budget {
			break
		}
		c.scanFile(sub, true, delta)
	}

	commit := func(ok bool) {
		defer release()
		if !ok && !delta.Empty() {
			return // the hub never saw the delta: read the same bytes again next time.
		}
		if err := c.saveState(); err != nil {
			c.log("usage: save state: %v", err)
		}
	}
	if delta.Empty() {
		delta = nil
	}
	return delta, commit, nil
}

func (c *usageCollector) lock() error {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(c.lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = f.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		st, serr := os.Stat(c.lockPath)
		if serr != nil || time.Since(st.ModTime()) < usageLockStale {
			return errors.New("another hook is collecting usage for this session")
		}
		_ = os.Remove(c.lockPath)
	}
	return errors.New("usage lock busy")
}

func (c *usageCollector) loadState() {
	c.state = usageState{V: usageStateVer, Files: map[string]*usageFile{}}
	if b, err := os.ReadFile(c.statePath); err == nil {
		var st usageState
		if json.Unmarshal(b, &st) == nil && st.V == usageStateVer {
			c.state = st
			if c.state.Files == nil {
				c.state.Files = map[string]*usageFile{}
			}
		} else {
			c.log("usage: state unreadable, starting over")
		}
	}
	if len(c.state.Seen) > 0 { // old format: fold in as legacy entries (older first)
		legacy := make([]*usageSeenMsg, 0, len(c.state.Seen)+len(c.state.Msgs))
		for _, k := range c.state.Seen {
			legacy = append(legacy, &usageSeenMsg{K: k, Legacy: true})
		}
		c.state.Msgs = append(legacy, c.state.Msgs...)
		c.state.Seen = nil
	}
	c.seen = make(map[string]*usageSeenMsg, len(c.state.Msgs))
	for _, m := range c.state.Msgs {
		c.seen[m.K] = m
	}
}

func (c *usageCollector) saveState() error {
	b, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	tmp := c.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, c.statePath)
}

func seenKey(id string) string {
	h := sha1.Sum([]byte(id))
	return hex.EncodeToString(h[:8])
}

// remember adds a new entry, evicting the oldest beyond usageSeenCap.
func (c *usageCollector) remember(m *usageSeenMsg) {
	c.seen[m.K] = m
	c.state.Msgs = append(c.state.Msgs, m)
	if len(c.state.Msgs) > usageSeenCap {
		drop := len(c.state.Msgs) - usageSeenCap
		for _, old := range c.state.Msgs[:drop] {
			delete(c.seen, old.K)
		}
		c.state.Msgs = append([]*usageSeenMsg(nil), c.state.Msgs[drop:]...)
	}
}

// markSeen reports whether id is new, remembering it (first row wins; used by the
// dialects whose repeats are identical).
func (c *usageCollector) markSeen(id string) bool {
	k := seenKey(id)
	if _, dup := c.seen[k]; dup {
		return false
	}
	c.remember(&usageSeenMsg{K: k})
	return true
}

// claimUsage returns the part of u not yet counted for message id: all of it the first
// time, afterwards the per-component excess over the largest value counted so far
// (smaller or equal repeats count nothing). Legacy entries are never topped up.
func (c *usageCollector) claimUsage(id string, u rusage.Usage) rusage.Usage {
	k := seenKey(id)
	m, ok := c.seen[k]
	if !ok {
		c.remember(&usageSeenMsg{K: k, I: u.InputTokens, O: u.OutputTokens,
			R: u.CacheReadTokens, W: u.CacheWriteTokens})
		return u
	}
	if m.Legacy {
		return rusage.Usage{}
	}
	d := rusage.Usage{Source: u.Source,
		InputTokens:      max(u.InputTokens-m.I, 0),
		OutputTokens:     max(u.OutputTokens-m.O, 0),
		CacheReadTokens:  max(u.CacheReadTokens-m.R, 0),
		CacheWriteTokens: max(u.CacheWriteTokens-m.W, 0),
	}
	m.I, m.O = max(m.I, u.InputTokens), max(m.O, u.OutputTokens)
	m.R, m.W = max(m.R, u.CacheReadTokens), max(m.W, u.CacheWriteTokens)
	d.TotalTokens = d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
	return d
}

// subagentFiles lists <transcript without .jsonl>/subagents/*.jsonl (Claude Code keeps
// each sub-agent's own transcript there; its rows are marked isSidechain).
func (c *usageCollector) subagentFiles() []string {
	if c.dialect != AgentClaude {
		return nil
	}
	dir := strings.TrimSuffix(c.path, filepath.Ext(c.path)) + string(filepath.Separator) + "subagents"
	files, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil || len(files) == 0 {
		return nil
	}
	sort.Strings(files)
	if len(files) > usageMaxSubFiles {
		files = files[:usageMaxSubFiles]
	}
	return files
}

// scanFile reads the complete new lines of one file (up to the remaining budget) and
// folds their usage into delta. A parse problem skips the line, an IO problem the file.
func (c *usageCollector) scanFile(path string, sub bool, delta *rusage.SessionUsage) {
	f, err := os.Open(path)
	if err != nil {
		if !os.IsNotExist(err) {
			c.log("usage: open %s: %v", filepath.Base(path), err)
		}
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		return
	}
	fs := c.state.Files[path]
	if fs == nil {
		fs = &usageFile{}
		c.state.Files[path] = fs
	}
	if st.Size() < fs.Offset || (fs.Offset > 0 && !headMatches(f, fs)) {
		// Truncated or replaced in place: count from the start again (message ids stop
		// double counting for claude/omp; codex's running total restarts).
		*fs = usageFile{}
	}
	if st.Size() == fs.Offset {
		return
	}
	if _, err := f.Seek(fs.Offset, io.SeekStart); err != nil {
		return
	}
	br := bufio.NewReaderSize(f, 256*1024)
	var cumLast *rusage.Usage
	cumModel := ""
	for c.read < c.budget {
		line, n, complete := readUsageLine(br)
		if !complete {
			break // a partial last line: the writer is mid-append, take it next time.
		}
		fs.Offset += n
		c.read += n
		if line == nil {
			continue // over-long line skipped
		}
		switch c.dialect {
		case AgentClaude:
			c.claudeLine(line, sub, delta)
		case AgentCodex:
			c.codexLine(line, fs, &cumLast, &cumModel)
		case AgentOmp:
			c.ompLine(line, fs, sub, delta)
		default:
			c.genericLine(line, sub, delta)
		}
	}
	fs.setHead(f)
	if cumLast != nil {
		d := cumDelta(fs.Cum, *cumLast)
		fs.Cum = cumLast
		addUsage(delta, false, cumModel, d)
	}
}

const usageHeadBytes = 256

func headSum(f *os.File, n int) (string, bool) {
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, 0); err != nil && !errors.Is(err, io.EOF) {
		return "", false
	}
	h := sha1.Sum(buf)
	return hex.EncodeToString(h[:8]), true
}

func headMatches(f *os.File, fs *usageFile) bool {
	if fs.HeadLen == 0 {
		return true
	}
	sum, ok := headSum(f, fs.HeadLen)
	return ok && sum == fs.Head
}

// setHead records the fingerprint of the file's first bytes (all of them for a short file).
func (fs *usageFile) setHead(f *os.File) {
	n := usageHeadBytes
	if fs.Offset < int64(n) {
		n = int(fs.Offset)
	}
	if n <= 0 {
		return
	}
	if sum, ok := headSum(f, n); ok {
		fs.HeadLen, fs.Head = n, sum
	}
}

// readUsageLine returns the next newline-terminated line. complete is false at EOF
// without a trailing newline (nothing consumed). n is the bytes the line occupied; line
// is nil when it was longer than usageMaxLine (skipped).
func readUsageLine(br *bufio.Reader) (line []byte, n int64, complete bool) {
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		n += int64(len(chunk))
		if !over {
			if len(buf)+len(chunk) > usageMaxLine {
				over, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err == nil {
			if over {
				return nil, n, true
			}
			return bytes.TrimSpace(buf), n, true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, 0, false
	}
}

func addUsage(d *rusage.SessionUsage, sub bool, model string, u rusage.Usage) {
	if u.IsZero() {
		return
	}
	if sub {
		d.Sub = d.Sub.Plus(u)
	} else {
		d.Main = d.Main.Plus(u)
	}
	if model == "" {
		model = "unknown"
	}
	if d.ByModel == nil {
		d.ByModel = map[string]rusage.Usage{}
	}
	d.ByModel[model] = d.ByModel[model].Plus(u)
}

func sourceFor(dialect string) string { return rusage.UsageSourceTranscriptPrefix + dialect }

var usageNeedle = []byte(`"usage"`)

type claudeUsageRow struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage map[string]any `json:"usage"`
	} `json:"message"`
}

func (c *usageCollector) claudeLine(line []byte, sub bool, delta *rusage.SessionUsage) {
	if len(line) == 0 || line[0] != '{' || !bytes.Contains(line, usageNeedle) {
		return
	}
	var row claudeUsageRow
	if err := json.Unmarshal(line, &row); err != nil {
		c.log("usage: bad claude line: %v", err)
		return
	}
	if row.Type != "assistant" || len(row.Message.Usage) == 0 {
		return
	}
	u := rusage.UsageFromObject(row.Message.Usage, sourceFor(AgentClaude))
	if u == nil {
		return
	}
	if row.Message.ID != "" { // one message repeats per content block; count only growth
		*u = c.claimUsage(row.Message.ID, *u)
	}
	addUsage(delta, sub || row.IsSidechain, row.Message.Model, *u)
}

// genericLine: a top-level `usage` object (agent-neutral), optional `id`/`model`.
func (c *usageCollector) genericLine(line []byte, sub bool, delta *rusage.SessionUsage) {
	if len(line) == 0 || line[0] != '{' || !bytes.Contains(line, usageNeedle) {
		return
	}
	var row struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		c.log("usage: bad generic line: %v", err)
		return
	}
	if len(row.Usage) == 0 || (row.ID != "" && !c.markSeen(row.ID)) {
		return
	}
	if u := rusage.UsageFromObject(row.Usage, sourceFor(DialectGeneric)); u != nil {
		addUsage(delta, sub, row.Model, *u)
	}
}

// ompLine: `{"type":"message","id":…,"message":{"role":"assistant","model":…,"usage":{…}}}`,
// with `model_change` rows naming the model for the rows after them.
func (c *usageCollector) ompLine(line []byte, fs *usageFile, sub bool, delta *rusage.SessionUsage) {
	if len(line) == 0 || line[0] != '{' {
		return
	}
	isChange := bytes.Contains(line, []byte(`"model_change"`))
	if !isChange && !bytes.Contains(line, usageNeedle) {
		return
	}
	var row struct {
		Type    string `json:"type"`
		ID      string `json:"id"`
		Model   string `json:"model"`
		Message struct {
			Role  string         `json:"role"`
			Model string         `json:"model"`
			Usage map[string]any `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		c.log("usage: bad omp line: %v", err)
		return
	}
	if row.Type == "model_change" && row.Model != "" {
		fs.Model = row.Model
		return
	}
	if row.Type != "message" || row.Message.Role != "assistant" || len(row.Message.Usage) == 0 {
		return
	}
	if u := rusage.UsageFromObject(row.Message.Usage, sourceFor(AgentOmp)); u != nil {
		if row.ID != "" {
			*u = c.claimUsage(row.ID, *u)
		}
		model := row.Message.Model
		if model == "" {
			model = fs.Model
		}
		addUsage(delta, sub, model, *u)
	}
}

// codexLine tracks the model (turn_context) and keeps the LAST cumulative total of
// the new bytes; scanFile turns it into a difference against the saved total.
func (c *usageCollector) codexLine(line []byte, fs *usageFile, cumLast **rusage.Usage, cumModel *string) {
	if len(line) == 0 || line[0] != '{' {
		return
	}
	isCtx := bytes.Contains(line, []byte(`"turn_context"`))
	isCount := bytes.Contains(line, []byte(`"token_count"`))
	if !isCtx && !isCount {
		return
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			Type  string `json:"type"`
			Model string `json:"model"`
			Info  *struct {
				Total map[string]any `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &row); err != nil {
		c.log("usage: bad codex line: %v", err)
		return
	}
	switch {
	case row.Type == "turn_context" && row.Payload.Model != "":
		fs.Model = row.Payload.Model
	case row.Type == "event_msg" && row.Payload.Type == "token_count" && row.Payload.Info != nil:
		if u := codexUsage(row.Payload.Info.Total); u != nil {
			*cumLast = u
			*cumModel = fs.Model
		}
	}
}

// codexUsage maps codex's running total onto Usage. codex's input_tokens INCLUDES the
// cached part, so the cached share moves to cache_read (input then excludes it, like
// claude's), and total_tokens is codex's own.
func codexUsage(m map[string]any) *rusage.Usage {
	if len(m) == 0 {
		return nil
	}
	num := func(k string) int64 {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		}
		return 0
	}
	cached := num("cached_input_tokens")
	in := num("input_tokens") - cached
	if in < 0 {
		in = 0
	}
	u := &rusage.Usage{InputTokens: in, CacheReadTokens: cached, OutputTokens: num("output_tokens"),
		TotalTokens: num("total_tokens"), Source: sourceFor(AgentCodex)}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.CacheReadTokens + u.OutputTokens
	}
	if u.IsZero() {
		return nil
	}
	return u
}

// cumDelta is now - prev; a component that went backwards means the running total was
// reset (a new rollout), so now itself is the delta.
func cumDelta(prev *rusage.Usage, now rusage.Usage) rusage.Usage {
	if prev == nil {
		return now
	}
	d := rusage.Usage{
		InputTokens: now.InputTokens - prev.InputTokens, OutputTokens: now.OutputTokens - prev.OutputTokens,
		CacheReadTokens: now.CacheReadTokens - prev.CacheReadTokens,
		TotalTokens:     now.TotalTokens - prev.TotalTokens, Source: now.Source,
	}
	if d.InputTokens < 0 || d.OutputTokens < 0 || d.CacheReadTokens < 0 || d.TotalTokens < 0 {
		return now
	}
	return d
}

// pruneUsageState drops state files of sessions that went quiet long ago.
func pruneUsageState(dir string, now time.Time) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || now.Sub(info.ModTime()) < usageStateTTL {
			continue
		}
		if strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".tmp") || strings.HasSuffix(e.Name(), ".lock") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
