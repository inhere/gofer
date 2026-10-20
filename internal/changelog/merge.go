// Package changelog merges a Keep a Changelog style CHANGELOG.md as a git merge
// driver: the entries of the "unreleased" section (grouped by its `### `
// subsections) are unioned, everything else goes through git's own line merge.
package changelog

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/inhere/gofer/internal/procattr"
)

// DefaultUnreleasedTitles are the section titles treated as "not released yet".
// The title is what stands between `## ` and the optional brackets.
var DefaultUnreleasedTitles = []string{"未发布", "Unreleased"}

// Options tunes Merge.
type Options struct {
	// UnreleasedTitles overrides DefaultUnreleasedTitles.
	UnreleasedTitles []string
}

// Result is a merged changelog.
type Result struct {
	// Merged is the file to write back (conflict markers included on Conflict).
	Merged []byte
	// Conflict is true when a place could not be merged automatically.
	Conflict bool
	// Notes describe the decisions taken between two real changes.
	Notes []string
}

// Merge three versions of a changelog. The unreleased section merges per `### `
// subsection and per entry: ours keeps the entry order, theirs' new entries are
// appended in theirs' order, an entry deleted on one untouched side disappears.
// The rest of the file (header and released sections) is merged line by line by
// git (MergeFileText), so a region both sides changed comes back with git's
// conflict markers and Conflict set.
func Merge(base, ours, theirs []byte, opts Options) (Result, error) {
	titles := opts.UnreleasedTitles
	if len(titles) == 0 {
		titles = DefaultUnreleasedTitles
	}
	bd, od, td := parseDoc(base), parseDoc(ours), parseDoc(theirs)
	bi, oi, ti := bd.unreleased(titles), od.unreleased(titles), td.unreleased(titles)

	st := &state{}
	rest, conflict, err := MergeFileText(without(bd, bi), without(od, oi), without(td, ti))
	if err != nil {
		return Result{}, err
	}
	st.conflict = st.conflict || conflict

	var secText string
	var secHeading string
	if oi >= 0 {
		secHeading = od.sections[oi].heading
	} else if ti >= 0 {
		secHeading = td.sections[ti].heading
	}
	var baseBody, oursBody, theirsBody []string
	if bi >= 0 {
		baseBody = bd.sections[bi].body
	}
	if oi >= 0 {
		oursBody = od.sections[oi].body
	}
	if ti >= 0 {
		theirsBody = td.sections[ti].body
	}
	if secHeading != "" {
		secText = renderSection(secHeading, mergeUnreleased(baseBody, oursBody, theirsBody, titles, st))
	}
	merged := insertSection(rest, secText, od.nextHeadingAfter(oi))
	return Result{Merged: merged, Conflict: st.conflict, Notes: st.notes}, nil
}

// state collects the decisions and the conflict flag of one merge.
type state struct {
	notes    []string
	conflict bool
}

func (s *state) note(format string, args ...any) {
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

func (s *state) conflictf(format string, args ...any) {
	s.conflict = true
	s.note(format, args...)
}

// --- document model -------------------------------------------------------

// doc is a changelog split into the header (everything before the first `## `
// line) and its h2 sections. Lines keep their trailing newline so the document
// renders back byte for byte.
type doc struct {
	header   []string
	sections []section
}

type section struct {
	heading string   // "## [未发布]"
	body    []string // lines after the heading
}

func parseDoc(b []byte) doc {
	var d doc
	cur := -1
	for _, line := range splitLines(b) {
		if isSectionHeading(line) {
			d.sections = append(d.sections, section{heading: trimNL(line)})
			cur = len(d.sections) - 1
			continue
		}
		if cur < 0 {
			d.header = append(d.header, line)
			continue
		}
		d.sections[cur].body = append(d.sections[cur].body, line)
	}
	return d
}

// unreleased returns the index of the unreleased section (-1 when absent).
func (d doc) unreleased(titles []string) int {
	for i, s := range d.sections {
		if matchesTitle(s.heading, titles) {
			return i
		}
	}
	return -1
}

// nextHeadingAfter is the heading that follows section idx ("" = none), used to
// place the merged unreleased section back where ours had it.
func (d doc) nextHeadingAfter(idx int) string {
	if idx < 0 || idx+1 >= len(d.sections) {
		return ""
	}
	return d.sections[idx+1].heading
}

func (d doc) render() []byte {
	var b strings.Builder
	for _, line := range d.header {
		b.WriteString(line)
	}
	for _, s := range d.sections {
		b.WriteString(s.heading)
		b.WriteString("\n")
		for _, line := range s.body {
			b.WriteString(line)
		}
	}
	return []byte(b.String())
}

// without renders d minus section idx (-1 renders d whole).
func without(d doc, idx int) []byte {
	if idx < 0 || idx >= len(d.sections) {
		return d.render()
	}
	cp := doc{header: d.header}
	for i, s := range d.sections {
		if i != idx {
			cp.sections = append(cp.sections, s)
		}
	}
	return cp.render()
}

func splitLines(b []byte) []string {
	var out []string
	s := string(b)
	for s != "" {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			out = append(out, s[:i+1])
			s = s[i+1:]
			continue
		}
		out = append(out, s+"\n")
		s = ""
	}
	return out
}

func trimNL(line string) string { return strings.TrimSuffix(line, "\n") }

func isSectionHeading(line string) bool {
	return line == "##\n" || strings.HasPrefix(line, "## ")
}

func isSubHeading(line string) bool {
	return line == "###\n" || strings.HasPrefix(line, "### ")
}

func isEntryStart(line string) bool {
	return line == "-\n" || strings.HasPrefix(line, "- ")
}

// matchesTitle reports whether a `## …` heading names one of titles: the text
// between the brackets (`## [未发布]`) or the bare heading text (`## 未发布`),
// case-insensitively.
func matchesTitle(heading string, titles []string) bool {
	t := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(heading, "##"), "##"))
	if strings.HasPrefix(t, "[") {
		if i := strings.Index(t, "]"); i > 0 {
			t = t[1:i]
		}
	}
	t = strings.ToLower(strings.TrimSpace(t))
	for _, title := range titles {
		if t == strings.ToLower(strings.TrimSpace(title)) {
			return true
		}
	}
	return false
}

// --- unreleased section merge --------------------------------------------

// subsection is one `### …` block of the unreleased section.
type subsection struct {
	heading string   // "### 新功能"
	pre     []string // lines between the heading and the first entry
	entries []string // entry blocks, each without its trailing blank lines
}

// splitSubsections splits a section body into its preamble (lines before the
// first `### `) and its subsections.
func splitSubsections(body []string) (pre []string, subs []subsection) {
	for _, line := range body {
		if isSubHeading(line) {
			subs = append(subs, subsection{heading: trimNL(line)})
			continue
		}
		if len(subs) == 0 {
			pre = append(pre, line)
			continue
		}
		subs[len(subs)-1].pre = append(subs[len(subs)-1].pre, line)
	}
	for i := range subs {
		subs[i].pre, subs[i].entries = splitEntries(subs[i].pre)
	}
	return pre, subs
}

// splitEntries splits subsection lines into the lines before the first entry and
// the entries themselves: a block from a column-0 `- ` line up to the next entry,
// continuation lines and indented sub-items included.
func splitEntries(lines []string) (pre, entries []string) {
	var cur []string
	for _, line := range lines {
		if isEntryStart(line) {
			if cur != nil {
				entries = append(entries, joinEntry(cur))
			}
			cur = []string{line}
			continue
		}
		if cur == nil {
			pre = append(pre, line)
			continue
		}
		cur = append(cur, line)
	}
	if cur != nil {
		entries = append(entries, joinEntry(cur))
	}
	return pre, entries
}

// joinEntry renders an entry block: its trailing blank lines dropped, so entries
// can be re-joined with single newlines and identical content round-trips.
func joinEntry(lines []string) string {
	return strings.TrimRight(strings.Join(lines, ""), "\n")
}

func renderSection(heading string, lines []string) string {
	return heading + "\n" + strings.Join(lines, "")
}

// mergeUnreleased merges the unreleased section bodies and returns the lines
// after the heading.
func mergeUnreleased(base, ours, theirs []string, titles []string, st *state) []string {
	bPre, bSubs := splitSubsections(base)
	oPre, oSubs := splitSubsections(ours)
	tPre, tSubs := splitSubsections(theirs)
	if len(oSubs) == 0 && len(tSubs) == 0 && len(ours) == 0 && len(theirs) == 0 {
		return nil
	}
	out := mergeBlock(bPre, oPre, tPre, "unreleased section preamble", st)
	out = ensureBlank(out, len(oSubs) > 0 || len(tSubs) > 0)

	usedTheirs := make([]bool, len(tSubs))
	for _, os := range oSubs {
		ti := findSub(tSubs, os.heading)
		bi := findSub(bSubs, os.heading)
		switch {
		case ti >= 0:
			usedTheirs[ti] = true
			var bBody []string
			if bi >= 0 {
				bBody = subBody(bSubs[bi])
			}
			out = append(out, renderSub(os.heading, mergeSubBody(bBody, subBody(os), subBody(tSubs[ti]), os.heading, st))...)
		case bi >= 0 && joinLines(subBody(bSubs[bi])) == joinLines(subBody(os)):
			// untouched on ours, removed on theirs: the removal wins.
			st.note("subsection %q removed on theirs", subLabel(os.heading))
		default:
			out = append(out, renderSub(os.heading, subBody(os))...)
		}
	}
	for i, ts := range tSubs {
		if usedTheirs[i] {
			continue
		}
		// A subsection ours no longer has: merge it with an empty ours, so entries
		// everybody left alone disappear and only theirs' additions stay.
		var bBody []string
		if bi := findSub(bSubs, ts.heading); bi >= 0 {
			bBody = subBody(bSubs[bi])
			if joinLines(bBody) != joinLines(subBody(ts)) {
				st.note("subsection %q removed on ours but changed on theirs, kept the change", subLabel(ts.heading))
			}
		}
		merged := mergeSubBody(bBody, nil, subBody(ts), ts.heading, st)
		if _, entries := splitEntries(merged); len(entries) == 0 && strings.TrimSpace(joinLines(merged)) == "" {
			continue // nothing of it is left: everybody left alone it
		}
		out = append(out, renderSub(ts.heading, merged)...)
	}
	return out
}

// findSub returns the index of the subsection with heading (-1 when absent).
func findSub(subs []subsection, heading string) int {
	for i, s := range subs {
		if s.heading == heading {
			return i
		}
	}
	return -1
}

// subLabel is the subsection title for messages and notes ("新功能").
func subLabel(heading string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(heading), "###"))
}

// subBody is a subsection as raw lines (pre-entry lines then entries).
func subBody(s subsection) []string {
	lines := append([]string(nil), s.pre...)
	for _, e := range s.entries {
		lines = append(lines, e+"\n")
	}
	return lines
}

func joinLines(lines []string) string { return strings.Join(lines, "") }

// renderSub renders a subsection: heading, its pre-entry lines (a blank line when
// it has none) and its entries one per line, then a blank line.
func renderSub(heading string, body []string) []string {
	pre, entries := splitEntries(body)
	out := []string{heading + "\n"}
	out = append(out, pre...)
	if len(pre) == 0 && len(entries) > 0 {
		out = append(out, "\n")
	}
	for _, e := range entries {
		out = append(out, e+"\n")
	}
	if len(entries) > 0 {
		out = append(out, "\n")
	}
	return out
}

func ensureBlank(lines []string, needed bool) []string {
	if !needed || (len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "") {
		return lines
	}
	return append(lines, "\n")
}

// mergeSubBody merges one subsection: its preamble three-way, then its entries.
func mergeSubBody(base, ours, theirs []string, heading string, st *state) []string {
	bPre, bEntries := splitEntries(base)
	oPre, oEntries := splitEntries(ours)
	tPre, tEntries := splitEntries(theirs)
	label := subLabel(heading)
	out := mergeBlock(bPre, oPre, tPre, "preamble of "+label, st)
	out = ensureBlank(out, len(oEntries) > 0 || len(tEntries) > 0)
	for _, e := range mergeEntries(bEntries, oEntries, tEntries, label, st) {
		out = append(out, e+"\n")
	}
	return out
}

// --- entry merge ---------------------------------------------------------

// sideInfo maps a side's entries onto the base entries.
type sideInfo struct {
	entries []string
	// origin maps a base entry index to the side entry index holding it,
	// unchanged or rewritten.
	origin map[int]int
	// modified maps a base entry index to the rewritten text.
	modified map[int]string
	// added lists the side entries that come from no base entry.
	added []int
}

// similarThreshold is how alike two entry texts must be to count as one entry
// rewritten rather than one deleted and another added.
const similarThreshold = 0.5

func analyse(base, side []string) sideInfo {
	in := sideInfo{entries: side, origin: map[int]int{}, modified: map[int]string{}}
	pairs := lcsPairs(base, side)
	baseSeen := make([]bool, len(base))
	sideSeen := make([]bool, len(side))
	pairUp := func(i, j int) {
		in.origin[i] = j
		in.modified[i] = side[j]
		baseSeen[i] = true
		sideSeen[j] = true
	}
	for _, p := range pairs {
		in.origin[p.i] = p.j
		baseSeen[p.i] = true
		sideSeen[p.j] = true
	}
	// Pair a rewritten entry with its replacement only inside the gap it was
	// rewritten in: base entries between two anchors and side entries between the
	// same anchors. Unrelated new entries across a gap therefore stay additions.
	prevB, prevS := -1, -1
	for _, anchor := range append(append([]pair(nil), pairs...), pair{len(base), len(side)}) {
		var bGap, sGap []int
		for i := prevB + 1; i < anchor.i; i++ {
			bGap = append(bGap, i)
		}
		for j := prevS + 1; j < anchor.j; j++ {
			sGap = append(sGap, j)
		}
		pairGap(bGap, sGap, base, side, pairUp)
		prevB, prevS = anchor.i, anchor.j
	}
	for j := range side {
		if !sideSeen[j] {
			in.added = append(in.added, j)
		}
	}
	return in
}

// pairGap pairs rewritten entries within one gap: first by how alike they are,
// then, when exactly one entry is left on each side, by position (a single line
// edited in place keeps a word or two, which is what makes it an edit).
func pairGap(bGap, sGap []int, base, side []string, pairUp func(i, j int)) {
	if len(bGap) == 0 || len(sGap) == 0 {
		return
	}
	type cand struct {
		i, j int
		sim  float64
	}
	var cands []cand
	for _, i := range bGap {
		for _, j := range sGap {
			if sim := similarity(base[i], side[j]); sim >= similarThreshold {
				cands = append(cands, cand{i, j, sim})
			}
		}
	}
	sort.SliceStable(cands, func(a, b int) bool { return cands[a].sim > cands[b].sim })
	usedB := make(map[int]bool, len(bGap))
	usedS := make(map[int]bool, len(sGap))
	for _, c := range cands {
		if usedB[c.i] || usedS[c.j] {
			continue
		}
		usedB[c.i], usedS[c.j] = true, true
		pairUp(c.i, c.j)
	}
	var restB, restS []int
	for _, i := range bGap {
		if !usedB[i] {
			restB = append(restB, i)
		}
	}
	for _, j := range sGap {
		if !usedS[j] {
			restS = append(restS, j)
		}
	}
	if len(restB) == 1 && len(restS) == 1 && similarity(base[restB[0]], side[restS[0]]) > 0 {
		pairUp(restB[0], restS[0])
	}
}

// mergeEntries unions the entries of one subsection: ours' order is kept, theirs'
// new entries follow in theirs' order.
func mergeEntries(base, ours, theirs []string, heading string, st *state) []string {
	ob, th := analyse(base, ours), analyse(base, theirs)
	oursBase := make([]int, len(ours))
	for i := range oursBase {
		oursBase[i] = -1
	}
	for bi, oi := range ob.origin {
		oursBase[oi] = bi
	}
	emitted := make(map[string]bool, len(ours))
	out := make([]string, 0, len(ours)+len(theirs))
	for j, text := range ours {
		bi := oursBase[j]
		if bi < 0 {
			out = append(out, text)
			emitted[text] = true
			continue
		}
		_, inTheirs := th.origin[bi]
		tMod := th.modified[bi]
		switch {
		case !inTheirs && tMod == "":
			// replaced on one side, removed on the other.
			if ob.modified[bi] != "" {
				st.conflictf("entry %q in %s: changed on ours, removed on theirs", entryLabel(text), heading)
				out = append(out, conflictBlock(text, "", true, false))
			}
		case !inTheirs || tMod != "":
			if tMod == text {
				out = append(out, text)
				emitted[text] = true
				continue
			}
			st.conflictf("entry %q in %s: changed on both sides", entryLabel(text), heading)
			out = append(out, conflictBlock(text, tMod, true, true))
		default:
			out = append(out, text)
			emitted[text] = true
		}
	}
	for _, j := range th.added {
		if emitted[theirs[j]] {
			continue
		}
		out = append(out, theirs[j])
		emitted[theirs[j]] = true
	}
	// A base entry ours removed but theirs rewrote has no place in ours' order.
	for bi := range base {
		if _, ok := ob.origin[bi]; ok {
			continue
		}
		if tMod := th.modified[bi]; tMod != "" {
			st.conflictf("entry %q in %s: removed on ours, changed on theirs", entryLabel(base[bi]), heading)
			out = append(out, conflictBlock("", tMod, false, true))
		}
	}
	return out
}

func entryLabel(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
	line = strings.Trim(line, "*` ")
	if len(line) > 40 {
		runes := []rune(line)
		line = string(runes[:40]) + "…"
	}
	return line
}

// conflictBlock renders a git style conflict for one entry or block.
func conflictBlock(ours, theirs string, hasOurs, hasTheirs bool) string {
	var b strings.Builder
	b.WriteString("<<<<<<< ours\n")
	if hasOurs {
		b.WriteString(strings.TrimRight(ours, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("=======\n")
	if hasTheirs {
		b.WriteString(strings.TrimRight(theirs, "\n"))
		b.WriteString("\n")
	}
	b.WriteString(">>>>>>> theirs")
	return b.String()
}

// mergeBlock three-way merges a plain block of lines (a preamble). It is not a
// diff, so any change on both sides is a conflict.
func mergeBlock(base, ours, theirs []string, what string, st *state) []string {
	bs, os_, ts := joinLines(base), joinLines(ours), joinLines(theirs)
	switch {
	case os_ == ts:
		return ours
	case os_ == bs:
		return theirs
	case ts == bs:
		return ours
	}
	st.conflictf("%s changed on both sides", what)
	block := conflictBlock(os_, ts, true, true) + "\n"
	return []string{block}
}

// --- helpers ------------------------------------------------------------

// lcsPairs returns the matching index pairs of a longest common subsequence.
func lcsPairs(a, b []string) []pair {
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []pair
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[i] == b[j]:
			out = append(out, pair{i, j})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

type pair struct{ i, j int }

// similarity is the Jaccard index of the words of two entries (markdown
// punctuation and the leading bullet ignored); 0 when they share no word.
func similarity(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return 0
	}
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	if shared == 0 {
		return 0
	}
	union := len(wa) + len(wb) - shared
	return float64(shared) / float64(union)
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "-")), func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}) {
		out[w] = true
	}
	return out
}

// insertSection puts the merged unreleased section into the rest of the file,
// before beforeHeading or, when that heading is gone, before the first section.
func insertSection(rest []byte, secText, beforeHeading string) []byte {
	if secText == "" {
		return rest
	}
	lines := splitLines(rest)
	idx := -1
	if beforeHeading != "" {
		for i, line := range lines {
			if trimNL(line) == beforeHeading {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		for i, line := range lines {
			if isSectionHeading(line) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		out := strings.Join(lines, "")
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		if out != "" && !strings.HasSuffix(out, "\n\n") {
			out += "\n"
		}
		return []byte(out + secText)
	}
	var b strings.Builder
	for _, line := range lines[:idx] {
		b.WriteString(line)
	}
	b.WriteString(secText)
	for _, line := range lines[idx:] {
		b.WriteString(line)
	}
	return []byte(b.String())
}

// MergeFileText runs git's own line merge on three file contents (out = the
// merged text, conflict when git left conflict markers).
func MergeFileText(base, ours, theirs []byte) (out []byte, conflict bool, err error) {
	dir, err := os.MkdirTemp("", "gofer-changelog-")
	if err != nil {
		return nil, false, err
	}
	defer os.RemoveAll(dir)
	paths := make([]string, 3)
	for i, data := range [][]byte{ours, base, theirs} {
		paths[i] = filepath.Join(dir, []string{"ours", "base", "theirs"}[i])
		if err := os.WriteFile(paths[i], data, 0o644); err != nil {
			return nil, false, err
		}
	}
	cmd := exec.Command("git", "merge-file", "-p", "-L", "ours", "-L", "base", "-L", "theirs", paths[0], paths[1], paths[2])
	cmd.Dir = dir
	procattr.Background(cmd)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	res, err := cmd.Output()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return res, false, nil
	case errors.As(err, &exitErr) && exitErr.ExitCode() > 0 && exitErr.ExitCode() < 128:
		return res, true, nil
	}
	return nil, false, fmt.Errorf("git merge-file: %v: %s", err, strings.TrimSpace(stderr.String()))
}
