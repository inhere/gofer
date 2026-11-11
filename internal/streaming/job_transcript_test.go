package streaming

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/secret"
)

func writeTurnsFile(t *testing.T, dir string, recs ...ndjsonfilter.TurnRecord) {
	t.Helper()
	var b strings.Builder
	for _, r := range recs {
		line, err := json.Marshal(r)
		assert.NoErr(t, err)
		b.Write(line)
		b.WriteByte('\n')
	}
	assert.NoErr(t, os.WriteFile(filepath.Join(dir, "turns.jsonl"), []byte(b.String()), 0o644))
}

// readAll follows the cursor of a read to the end and returns the pages.
func readAll(t *testing.T, res job.JobResult, q TranscriptQuery) []JobTranscriptPage {
	t.Helper()
	var pages []JobTranscriptPage
	for i := 0; i < 1000; i++ {
		opts, err := ParseTranscriptQuery(q)
		assert.NoErr(t, err)
		page, err := JobTranscript(res, opts)
		assert.NoErr(t, err)
		pages = append(pages, page)
		if page.Next == nil {
			return pages
		}
		q.Cursor = page.Next.String()
	}
	t.Fatal("cursor never ended")
	return nil
}

func TestParseTranscriptQueryValidation(t *testing.T) {
	ok := func(q TranscriptQuery) TranscriptOpts {
		t.Helper()
		o, err := ParseTranscriptQuery(q)
		assert.NoErr(t, err)
		return o
	}
	assert.Eq(t, TranscriptOutline, ok(TranscriptQuery{}).Mode)
	o := ok(TranscriptQuery{Turns: "2-4"})
	assert.Eq(t, 2, o.From)
	assert.Eq(t, 4, o.To)
	assert.Eq(t, 3, ok(TranscriptQuery{Turns: "3"}).To)
	assert.Eq(t, TranscriptCursor{Turn: 2, Offset: 10}, ok(TranscriptQuery{Cursor: "2:10"}).Cursor)

	for name, q := range map[string]TranscriptQuery{
		"exclusive outline+head": {Outline: true, Head: "2"},
		"exclusive head+tail":    {Head: "1", Tail: "1"},
		"exclusive turns+search": {Turns: "1-2", Search: "x"},
		"range reversed":         {Turns: "4-2"},
		"range zero":             {Turns: "0-2"},
		"range junk":             {Turns: "a-b"},
		"range huge":             {Turns: "1-99999999"},
		"head zero":              {Head: "0"},
		"head negative":          {Head: "-1"},
		"tail huge":              {Tail: "99999999"},
		"search blank":           {Search: "   "},
		"search too long":        {Search: strings.Repeat("x", TranscriptMaxSearch+1)},
		"cursor junk":            {Cursor: "abc"},
		"cursor turn zero":       {Cursor: "0:0"},
		"cursor negative offset": {Cursor: "1:-5"},
	} {
		_, err := ParseTranscriptQuery(q)
		assert.True(t, errors.Is(err, ErrInvalidTranscript), name)
	}
	// 200 characters is allowed (counted in runes).
	_, err := ParseTranscriptQuery(TranscriptQuery{Search: strings.Repeat("界", TranscriptMaxSearch)})
	assert.NoErr(t, err)
}

func TestJobTranscriptTurnsFileOutlineRangeHeadTailSearch(t *testing.T) {
	dir := t.TempDir()
	writeTurnsFile(t, dir,
		ndjsonfilter.TurnRecord{Turn: 1, Role: "assistant", Text: "first reply about alpha", Tools: map[string]int{"bash": 2}, Tokens: 10},
		ndjsonfilter.TurnRecord{Turn: 2, Role: "assistant", Text: "second reply about beta and alpha"},
		ndjsonfilter.TurnRecord{Turn: 3, Role: "assistant", Text: "third reply", Error: "boom"},
	)
	res := job.JobResult{ResultDir: dir}
	assert.Eq(t, 3, TurnCount(res))

	pages := readAll(t, res, TranscriptQuery{})
	assert.Eq(t, 1, len(pages))
	p := pages[0]
	assert.Eq(t, TranscriptSourceTurns, p.Source)
	assert.Eq(t, 3, p.Total)
	assert.Eq(t, 3, len(p.Turns))
	assert.Eq(t, "first reply about alpha", p.Turns[0].Preview)
	assert.Eq(t, 2, p.Turns[0].Tools["bash"])
	assert.Eq(t, "boom", p.Turns[2].Error)
	assert.Eq(t, "", p.Turns[0].Text) // outline carries no full text

	p = readAll(t, res, TranscriptQuery{Turns: "2-3"})[0]
	assert.Eq(t, 2, len(p.Turns))
	assert.Eq(t, "second reply about beta and alpha", p.Turns[0].Text)
	assert.Eq(t, 2, p.Turns[0].Turn)

	p = readAll(t, res, TranscriptQuery{Head: "1"})[0]
	assert.Eq(t, 1, len(p.Turns))
	assert.Eq(t, 1, p.Turns[0].Turn)
	p = readAll(t, res, TranscriptQuery{Tail: "2"})[0]
	assert.Eq(t, 2, len(p.Turns))
	assert.Eq(t, 2, p.Turns[0].Turn)
	assert.Eq(t, 3, p.Turns[1].Turn)
	p = readAll(t, res, TranscriptQuery{Tail: "50"})[0] // clamped
	assert.Eq(t, 3, len(p.Turns))

	p = readAll(t, res, TranscriptQuery{Search: "alpha"})[0]
	assert.Eq(t, 2, len(p.Turns))
	assert.Eq(t, 1, p.Turns[0].MatchCount)
	assert.True(t, strings.Contains(p.Turns[1].Matches[0], "alpha"))
	p = readAll(t, res, TranscriptQuery{Search: "nope"})[0]
	assert.Eq(t, 0, len(p.Turns))

	// A range past the end yields an empty page, not an error.
	p = readAll(t, res, TranscriptQuery{Turns: "9-10"})[0]
	assert.Eq(t, 0, len(p.Turns))
}

// A turn far bigger than one page (multi-byte text, so cuts must land on rune
// boundaries) is fetched completely through the cursor.
func TestJobTranscriptCursorPagesALargeTurnToTheEnd(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("界a", 90_000) // 360KB: > 5 pages, cut points mid-rune for a naive split
	writeTurnsFile(t, dir,
		ndjsonfilter.TurnRecord{Turn: 1, Role: "assistant", Text: "small"},
		ndjsonfilter.TurnRecord{Turn: 2, Role: "assistant", Text: big},
		ndjsonfilter.TurnRecord{Turn: 3, Role: "assistant", Text: "tail turn"},
	)
	res := job.JobResult{ResultDir: dir}
	pages := readAll(t, res, TranscriptQuery{Turns: "1-3"})
	assert.True(t, len(pages) > 5)
	texts := map[int]string{}
	for _, p := range pages {
		size := 0
		for _, e := range p.Turns {
			size += len(e.Text)
			texts[e.Turn] += e.Text
			assert.True(t, utf8Valid(e.Text), "page text must be valid UTF-8")
		}
		assert.True(t, size <= TranscriptPageBytes)
	}
	assert.Eq(t, "small", texts[1])
	assert.Eq(t, big, texts[2])
	assert.Eq(t, "tail turn", texts[3])
	// Resuming at an offset that lands inside a rune aligns forward.
	opts, err := ParseTranscriptQuery(TranscriptQuery{Turns: "2", Cursor: "2:1"})
	assert.NoErr(t, err)
	page, err := JobTranscript(res, opts)
	assert.NoErr(t, err)
	assert.True(t, utf8Valid(page.Turns[0].Text))
	assert.Eq(t, 3, page.Turns[0].Offset)
}

func utf8Valid(s string) bool { return strings.ToValidUTF8(s, "�") == s }

func TestJobTranscriptACPSourceCutsTurnsAtPrompts(t *testing.T) {
	res := job.JobResult{ResultDir: t.TempDir()}
	writeTranscript(t, res,
		map[string]any{"t": "session", "session_id": "s"},
		map[string]any{"t": "prompt", "text": "do the first thing"},
		map[string]any{"t": "tool_call", "tool_call_id": "a", "kind": "execute", "status": "pending"},
		map[string]any{"t": "tool_call", "tool_call_id": "a", "kind": "execute", "status": "completed"},
		map[string]any{"t": "message", "text": "first part"},
		map[string]any{"t": "message", "text": "second part"},
		map[string]any{"t": "prompt", "text": "now the second"},
		map[string]any{"t": "message", "text": "done"},
	)
	assert.Eq(t, 2, TurnCount(res))
	p := readAll(t, res, TranscriptQuery{Turns: "1-2"})[0]
	assert.Eq(t, TranscriptSourceACP, p.Source)
	assert.Eq(t, 2, len(p.Turns))
	assert.Eq(t, "do the first thing", p.Turns[0].Prompt)
	assert.Eq(t, "first part\n\nsecond part", p.Turns[0].Text)
	assert.Eq(t, 1, p.Turns[0].Tools["execute"]) // one tool_call_id, two updates
	assert.Eq(t, "done", p.Turns[1].Text)
	p = readAll(t, res, TranscriptQuery{Search: "second"})[0]
	assert.Eq(t, 2, len(p.Turns)) // "second part" and the "now the second" prompt
}

// A very large acp.jsonl is read only as far as the page needs: the first turn comes back
// complete although the file holds far more than one scan could keep in memory at once.
func TestJobTranscriptACPLargeFileReadsOnlyThePageItNeeds(t *testing.T) {
	res := job.JobResult{ResultDir: t.TempDir()}
	var recs []map[string]any
	filler := strings.Repeat("x", 100_000)
	for i := 1; i <= 120; i++ { // ~12MB over 120 turns
		recs = append(recs, map[string]any{"t": "prompt", "text": fmt.Sprintf("p%d", i)}, map[string]any{"t": "message", "text": filler})
	}
	writeTranscript(t, res, recs...)
	assert.Eq(t, 120, TurnCount(res))
	p := readAll(t, res, TranscriptQuery{Turns: "1"})[0]
	assert.Eq(t, 1, len(p.Turns))
	assert.Eq(t, "p1", p.Turns[0].Prompt)
	assert.Eq(t, 100_000, p.Turns[0].Chars)
	assert.NotNil(t, p.Next) // 100KB > one page: the rest is behind the cursor
	// The outline of 120 turns fits one page and lists them all.
	o := readAll(t, res, TranscriptQuery{})
	n := 0
	for _, pg := range o {
		n += len(pg.Turns)
	}
	assert.Eq(t, 120, n)
}

func TestJobTranscriptFallsBackToStdoutAsOneTurn(t *testing.T) {
	dir := t.TempDir()
	text := strings.Repeat("line of output\n", 10_000) // 150KB
	assert.NoErr(t, os.WriteFile(filepath.Join(dir, "stdout.log"), []byte(text), 0o644))
	res := job.JobResult{ResultDir: dir}
	assert.Eq(t, 1, TurnCount(res))
	pages := readAll(t, res, TranscriptQuery{Turns: "1"})
	assert.True(t, len(pages) >= 3)
	var got strings.Builder
	for _, p := range pages {
		assert.Eq(t, TranscriptSourceStdout, p.Source)
		for _, e := range p.Turns {
			got.WriteString(e.Text)
		}
	}
	assert.Eq(t, text, got.String())
	// No record at all: an empty page, not an error.
	empty, err := JobTranscript(job.JobResult{ResultDir: t.TempDir()}, TranscriptOpts{})
	assert.NoErr(t, err)
	assert.Eq(t, 0, empty.Total)
}

func TestJobTranscriptRefusesSymlinkOutsideResultDir(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	target := filepath.Join(outside, "turns.jsonl")
	writeTurnsFile(t, outside, ndjsonfilter.TurnRecord{Turn: 1, Role: "assistant", Text: "not yours"})
	if err := os.Symlink(target, filepath.Join(dir, "turns.jsonl")); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	_, err := JobTranscript(job.JobResult{ResultDir: dir}, TranscriptOpts{})
	assert.True(t, errors.Is(err, ErrTranscriptPath))
	assert.Eq(t, 0, TurnCount(job.JobResult{ResultDir: dir}))
}

// Read-side redaction: credential-looking assignments in a turn never come back, in the
// full text, the search snippets, or the outline preview.
func TestJobTranscriptRedactsCredentialAssignments(t *testing.T) {
	dir := t.TempDir()
	writeTurnsFile(t, dir, ndjsonfilter.TurnRecord{Turn: 1, Role: "assistant", Text: "export API_TOKEN=abcd1234secret then run"})
	res := job.JobResult{ResultDir: dir}
	for _, q := range []TranscriptQuery{{}, {Turns: "1"}, {Search: "then"}} {
		b, _ := json.Marshal(readAll(t, res, q))
		assert.False(t, strings.Contains(string(b), "abcd1234secret"), fmt.Sprintf("%+v leaked", q))
	}
	b, _ := json.Marshal(readAll(t, res, TranscriptQuery{Turns: "1"}))
	assert.True(t, strings.Contains(string(b), secret.Placeholder))
}
