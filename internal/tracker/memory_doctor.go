package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/inhere/gofer/internal/procattr"
)

// Doctor finding slugs (design §2.5). They are stable: `prime.doctor.suppress`
// and a memory's `doctor_ignore` refer to them, and the steward reads them from
// `memory doctor --json`.
const (
	DoctorHandoffExpired = "handoff-expired"
	DoctorNoteStale      = "note-stale"
	DoctorPathMissing    = "path-missing"
	DoctorCommitMissing  = "commit-missing"
	DoctorSummaryMissing = "summary-missing"
	DoctorDuplicate      = "duplicate"
)

// DoctorSlugs lists every finding slug in report order.
var DoctorSlugs = []string{DoctorHandoffExpired, DoctorNoteStale, DoctorPathMissing, DoctorCommitMissing, DoctorSummaryMissing, DoctorDuplicate}

// doctorStaleSlugs are the findings that mean "the content may no longer be
// true"; prime marks rule / note entries carrying one with 「⚠ 可能过期」.
// summary-missing and duplicate are housekeeping, reported by doctor only.
var doctorStaleSlugs = []string{DoctorHandoffExpired, DoctorNoteStale, DoctorPathMissing, DoctorCommitMissing}

// PrimeStaleMarker is appended to flagged rule / note entries in prime.
const PrimeStaleMarker = "⚠ 可能过期"

const (
	// duplicateJaccard is the token-set similarity from which two memories with
	// the same key prefix count as duplicates.
	duplicateJaccard = 0.6
	// doctorCacheFile caches the git / filesystem findings for prime.
	doctorCacheFile = "doctor.json"
	// doctorCacheMaxAge bounds how long prime trusts a cached doctor run.
	doctorCacheMaxAge = 24 * time.Hour
	// doctorDetailMax caps the paths / hashes listed in one finding.
	doctorDetailMax = 5
	// doctorAncestorRoots is how many parent directories of the repository count
	// as workspace roots for path resolution (4 also covers a git worktree under
	// <repo>/.worktrees/<name> inside a workspace).
	doctorAncestorRoots = 4
)

// DoctorFinding is one problem of one memory.
type DoctorFinding struct {
	Slug   string `json:"slug"`
	Detail string `json:"detail,omitempty"`
}

// DoctorEntry lists the findings of one memory.
type DoctorEntry struct {
	Key       string          `json:"key"`
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary,omitempty"`
	UpdatedAt string          `json:"updated_at,omitempty"`
	Findings  []DoctorFinding `json:"findings"`
}

// DoctorReport is `gofer memory doctor --json`. Memories without findings are
// left out; Suppressed counts findings silenced by config or doctor_ignore.
type DoctorReport struct {
	GeneratedAt string        `json:"generated_at"`
	Checked     int           `json:"checked"`
	Flagged     int           `json:"flagged"`
	Suppressed  int           `json:"suppressed"`
	Memories    []DoctorEntry `json:"memories"`
}

// DoctorOptions are the inputs of DiagnoseMemories.
type DoctorOptions struct {
	Now time.Time
	// Roots are the directories relative paths are resolved against (repository
	// root first, then workspace roots). Empty disables path-missing.
	Roots []string
	// CommitsMissing returns the hashes git does not know; nil disables
	// commit-missing.
	CommitsMissing func(hashes []string) map[string]bool
	// Suppress silences slugs repository-wide (prime.doctor.suppress).
	Suppress []string
}

// ValidDoctorSlug reports whether slug is a known finding slug.
func ValidDoctorSlug(slug string) bool { return hasTag(DoctorSlugs, slug) }

// cheapFindings are the checks that need nothing but the memory itself.
func cheapFindings(m Memory, now time.Time) []DoctorFinding {
	var out []DoctorFinding
	kind := m.EffectiveKind()
	if MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
		at, _ := MemoryExpiresAt(m.MemoryMeta, m.Tags, m.UpdatedAt)
		out = append(out, DoctorFinding{Slug: DoctorHandoffExpired, Detail: "expired " + at.Format("2006-01-02")})
	}
	if MemoryStale(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
		out = append(out, DoctorFinding{Slug: DoctorNoteStale, Detail: "updated " + AgeText(m.UpdatedAt, now)})
	}
	if kind != MemoryKindHandoff && strings.TrimSpace(m.Summary) == "" {
		if n := len([]rune(m.Content)); n > MemorySummaryRequiredRunes {
			out = append(out, DoctorFinding{Slug: DoctorSummaryMissing, Detail: fmt.Sprintf("content %d chars without --summary", n)})
		}
	}
	return out
}

// referenceFindings are the path / commit checks. They only apply to rule and
// note: a handoff naming an old path or commit is a normal historical record
// (design §5 decision 3).
func referenceFindings(m Memory, opts DoctorOptions, missingCommits map[string]bool) []DoctorFinding {
	if m.EffectiveKind() == MemoryKindHandoff {
		return nil
	}
	var out []DoctorFinding
	if len(opts.Roots) > 0 {
		var missing []string
		for _, p := range memoryPathRefs(m.Content) {
			if !pathExistsUnder(p, opts.Roots) {
				missing = append(missing, p)
			}
		}
		if len(missing) > 0 {
			out = append(out, DoctorFinding{Slug: DoctorPathMissing, Detail: detailList(missing)})
		}
	}
	if missingCommits != nil {
		var missing []string
		for _, h := range memoryCommitRefs(m.Content) {
			if missingCommits[h] {
				missing = append(missing, h)
			}
		}
		if len(missing) > 0 {
			out = append(out, DoctorFinding{Slug: DoctorCommitMissing, Detail: detailList(missing)})
		}
	}
	return out
}

func detailList(items []string) string {
	if len(items) <= doctorDetailMax {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:doctorDetailMax], ", ") + fmt.Sprintf(" (+%d)", len(items)-doctorDetailMax)
}

// DiagnoseMemories runs every check and returns the unsuppressed findings.
func DiagnoseMemories(items []Memory, opts DoctorOptions) DoctorReport {
	raw := diagnoseRaw(items, opts)
	return buildDoctorReport(items, raw, opts)
}

// diagnoseRaw returns every finding per key before suppression.
func diagnoseRaw(items []Memory, opts DoctorOptions) map[string][]DoctorFinding {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	var missingCommits map[string]bool
	if opts.CommitsMissing != nil {
		var hashes []string
		seen := map[string]bool{}
		for _, m := range items {
			if m.EffectiveKind() == MemoryKindHandoff {
				continue
			}
			for _, h := range memoryCommitRefs(m.Content) {
				if !seen[h] {
					seen[h] = true
					hashes = append(hashes, h)
				}
			}
		}
		missingCommits = map[string]bool{}
		if len(hashes) > 0 {
			missingCommits = opts.CommitsMissing(hashes)
		}
	}
	raw := map[string][]DoctorFinding{}
	for _, m := range items {
		f := cheapFindings(m, opts.Now)
		f = append(f, referenceFindings(m, opts, missingCommits)...)
		if len(f) > 0 {
			raw[m.Key] = f
		}
	}
	for key, dups := range duplicateFindings(items) {
		raw[key] = append(raw[key], dups...)
	}
	return raw
}

func buildDoctorReport(items []Memory, raw map[string][]DoctorFinding, opts DoctorOptions) DoctorReport {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	report := DoctorReport{GeneratedAt: opts.Now.UTC().Format(time.RFC3339), Checked: len(items), Memories: []DoctorEntry{}}
	sorted := append([]Memory(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	for _, m := range sorted {
		kept, dropped := suppressFindings(raw[m.Key], opts.Suppress, m.DoctorIgnore)
		report.Suppressed += dropped
		if len(kept) == 0 {
			continue
		}
		sort.SliceStable(kept, func(i, j int) bool { return slugOrder(kept[i].Slug) < slugOrder(kept[j].Slug) })
		report.Memories = append(report.Memories, DoctorEntry{Key: m.Key, Kind: m.EffectiveKind(), Summary: DisplayMemorySummary(m.MemoryMeta, m.Content), UpdatedAt: m.UpdatedAt, Findings: kept})
	}
	report.Flagged = len(report.Memories)
	return report
}

func slugOrder(slug string) int {
	for i, s := range DoctorSlugs {
		if s == slug {
			return i
		}
	}
	return len(DoctorSlugs)
}

func suppressFindings(findings []DoctorFinding, suppress, ignore []string) ([]DoctorFinding, int) {
	var kept []DoctorFinding
	dropped := 0
	for _, f := range findings {
		if hasTag(suppress, f.Slug) || hasTag(ignore, f.Slug) {
			dropped++
			continue
		}
		kept = append(kept, f)
	}
	return kept, dropped
}

// ---- path references ----

var (
	backtickSpan = regexp.MustCompile("`([^`\n]+)`")
	// domainSegment is a host name like github.com (Go import paths, bare URLs).
	domainSegment = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)*\.[a-z]{2,}$`)
	// lineSuffix strips `:12`, `:12:3` and `#L12` from file references.
	lineSuffix = regexp.MustCompile(`(?::\d+){1,2}$|#L\d+(?:-L?\d+)?$`)
)

// memoryPathRefs extracts the path-like tokens of content: backticked tokens or
// whitespace-separated words that contain `/`. URLs, Go import paths, globs,
// placeholders, flags, home-relative and API-route-like tokens are ignored. A
// bare word outside backticks must look like a file reference (./, ../, a
// leading /, a trailing / or a file extension) so prose such as "server/worker"
// is not mistaken for a path.
func memoryPathRefs(content string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(token string, quoted bool) {
		if p, ok := normalizePathRef(token, quoted); ok && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, m := range backtickSpan.FindAllStringSubmatch(content, -1) {
		for _, tok := range splitRefTokens(m[1]) {
			add(tok, true)
		}
	}
	rest := backtickSpan.ReplaceAllString(content, " ")
	for _, tok := range splitRefTokens(rest) {
		add(tok, false)
	}
	return out
}

func splitRefTokens(text string) []string {
	return strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '，' || r == '、' || r == '；' || r == '|'
	})
}

func isPathRune(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("._/~-", r))
}

func normalizePathRef(token string, quoted bool) (string, bool) {
	if strings.Contains(token, "://") || !strings.Contains(token, "/") || strings.ContainsAny(token, "$<>{}*?[]=@") {
		return "", false
	}
	original := token
	token = strings.TrimFunc(token, func(r rune) bool { return !isPathRune(r) })
	if strings.HasSuffix(token, "/") && !strings.HasSuffix(original, "/") {
		return "", false // "exec/检测类": a slash between words, not a directory
	}
	token = strings.TrimRight(token, ".")
	token = lineSuffix.ReplaceAllString(token, "")
	if token == "" || !strings.Contains(token, "/") || strings.HasPrefix(token, "//") {
		return "", false
	}
	for _, r := range token {
		if !isPathRune(r) {
			return "", false // globs, placeholders, $VARS, key=value, mixed scripts
		}
	}
	if strings.HasPrefix(token, "-") || strings.HasPrefix(token, "~") || strings.HasPrefix(token, "www.") || strings.HasPrefix(token, "refs/") {
		return "", false
	}
	segments := strings.Split(strings.Trim(token, "/"), "/")
	if segments[0] == "" || domainSegment.MatchString(segments[0]) {
		return "", false
	}
	if strings.HasPrefix(token, "/") {
		// `/clear`-style commands and HTTP routes are not file paths.
		if len(segments) < 2 || segments[0] == "v1" || segments[0] == "api" {
			return "", false
		}
	}
	if !quoted {
		last := segments[len(segments)-1]
		fileLike := strings.HasPrefix(token, "./") || strings.HasPrefix(token, "../") || strings.HasPrefix(token, "/") || strings.HasPrefix(token, ".") ||
			strings.HasSuffix(token, "/") || strings.Contains(strings.TrimPrefix(last, "."), ".")
		if !fileLike {
			return "", false
		}
	}
	return strings.TrimSuffix(token, "/"), true
}

func pathExistsUnder(p string, roots []string) bool {
	if filepath.IsAbs(filepath.FromSlash(p)) || strings.HasPrefix(p, "/") {
		_, err := os.Stat(filepath.FromSlash(p))
		return err == nil
	}
	rel := filepath.FromSlash(strings.TrimPrefix(p, "./"))
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, rel)); err == nil {
			return true
		}
	}
	return false
}

// doctorRoots is the repository root plus up to doctorAncestorRoots parents
// (the workspace a repository lives in).
func doctorRoots(repoRoot string) []string {
	if repoRoot == "" {
		return nil
	}
	roots := []string{repoRoot}
	dir := repoRoot
	for i := 0; i < doctorAncestorRoots; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		roots = append(roots, parent)
		dir = parent
	}
	return roots
}

// ---- commit references ----

// memoryCommitRefs extracts 7–40 char lowercase hex words holding both a digit
// and a letter (so plain numbers and words like "deadbeef" in prose are skipped).
func memoryCommitRefs(content string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(content, func(r rune) bool { return unicode.IsSpace(r) || r == '`' }) {
		tok = strings.TrimFunc(tok, func(r rune) bool { return r >= unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r)) })
		if len(tok) < 7 || len(tok) > 40 || seen[tok] || !looksLikeCommit(tok) {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

func looksLikeCommit(tok string) bool {
	digit, letter := false, false
	for _, r := range tok {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'f':
			letter = true
		default:
			return false
		}
	}
	return digit && letter
}

// gitCommitsMissing asks `git cat-file --batch-check` in dir which hashes are
// unknown. Ambiguous short hashes count as present. Any git failure yields nil
// (the check is skipped rather than reporting everything missing).
func gitCommitsMissing(dir string, hashes []string) map[string]bool {
	cmd := exec.Command("git", "-C", dir, "cat-file", "--batch-check")
	procattr.Background(cmd)
	cmd.Stdin = strings.NewReader(strings.Join(hashes, "\n") + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(hashes) {
		return nil
	}
	missing := map[string]bool{}
	for i, line := range lines {
		if strings.HasSuffix(strings.TrimSpace(line), " missing") {
			missing[hashes[i]] = true
		}
	}
	return missing
}

// ---- duplicates ----

// keyPrefix is the first segment of a memory key (split on - _ . : /).
func keyPrefix(key string) string {
	if i := strings.IndexAny(key, "-_.:/"); i > 0 {
		return key[:i]
	}
	return key
}

// similarityTokens are lowercase ASCII words (2+ chars) and CJK bigrams.
func similarityTokens(text string) map[string]bool {
	out := map[string]bool{}
	var word []rune
	var han []rune
	flushWord := func() {
		if len(word) >= 2 {
			out[string(word)] = true
		}
		word = word[:0]
	}
	flushHan := func() {
		switch {
		case len(han) == 1:
			out[string(han)] = true
		case len(han) > 1:
			for i := 0; i+1 < len(han); i++ {
				out[string(han[i:i+2])] = true
			}
		}
		han = han[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			flushHan()
			word = append(word, r)
		case unicode.Is(unicode.Han, r):
			flushWord()
			han = append(han, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if b[k] {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// duplicateFindings pairs memories with the same key prefix whose contents have
// a token Jaccard similarity >= duplicateJaccard; both sides get a finding.
func duplicateFindings(items []Memory) map[string][]DoctorFinding {
	groups := map[string][]Memory{}
	for _, m := range items {
		groups[keyPrefix(m.Key)] = append(groups[keyPrefix(m.Key)], m)
	}
	out := map[string][]DoctorFinding{}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Key < group[j].Key })
		tokens := make([]map[string]bool, len(group))
		for i, m := range group {
			tokens[i] = similarityTokens(m.Content)
		}
		for i := range group {
			for j := i + 1; j < len(group); j++ {
				sim := jaccard(tokens[i], tokens[j])
				if sim < duplicateJaccard {
					continue
				}
				out[group[i].Key] = append(out[group[i].Key], DoctorFinding{Slug: DoctorDuplicate, Detail: fmt.Sprintf("similar to %s (%.2f)", group[j].Key, sim)})
				out[group[j].Key] = append(out[group[j].Key], DoctorFinding{Slug: DoctorDuplicate, Detail: fmt.Sprintf("similar to %s (%.2f)", group[i].Key, sim)})
			}
		}
	}
	return out
}

// ---- store integration and the prime cache ----

// doctorCache is .local/doctor.json: the raw findings of the last doctor run,
// keyed to the memories.jsonl it was computed from.
type doctorCache struct {
	GeneratedAt   string                     `json:"generated_at"`
	MemoriesMtime int64                      `json:"memories_mtime"`
	MemoriesSize  int64                      `json:"memories_size"`
	Findings      map[string][]DoctorFinding `json:"findings"`
}

func (s *Store) memoriesStat() (int64, int64) {
	info, err := os.Stat(filepath.Join(s.Dir, "memories.jsonl"))
	if err != nil {
		return 0, 0
	}
	return info.ModTime().UnixNano(), info.Size()
}

// Doctor checks every local memory (archived ones excluded) and records the raw
// findings in .local/doctor.json so prime can show path / commit findings
// without running git. It never changes a memory.
func (s *Store) Doctor(now time.Time) (DoctorReport, error) {
	if now.IsZero() {
		now = time.Now()
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return DoctorReport{}, err
	}
	items, err := s.ReadMemories()
	if err != nil {
		return DoctorReport{}, err
	}
	opts := DoctorOptions{Now: now, Suppress: cfg.Prime.DoctorSuppress()}
	if root := s.RepoRoot(); root != "" {
		opts.Roots = doctorRoots(root)
		opts.CommitsMissing = func(hashes []string) map[string]bool { return gitCommitsMissing(root, hashes) }
	}
	raw := diagnoseRaw(items, opts)
	mtime, size := s.memoriesStat()
	cache := doctorCache{GeneratedAt: now.UTC().Format(time.RFC3339), MemoriesMtime: mtime, MemoriesSize: size, Findings: raw}
	if data, err := json.Marshal(cache); err == nil {
		_ = atomicWrite(filepath.Join(s.Dir, ".local", doctorCacheFile), append(data, '\n'))
	}
	return buildDoctorReport(items, raw, opts), nil
}

// cachedDoctorFindings returns the reference findings (path / commit /
// duplicate) of a doctor run made against the current memories.jsonl within
// doctorCacheMaxAge, else nil.
func (s *Store) cachedDoctorFindings(now time.Time) map[string][]DoctorFinding {
	data, err := os.ReadFile(filepath.Join(s.Dir, ".local", doctorCacheFile))
	if err != nil {
		return nil
	}
	var cache doctorCache
	if json.Unmarshal(bytes.TrimSpace(data), &cache) != nil {
		return nil
	}
	mtime, size := s.memoriesStat()
	at, ok := parseTime(cache.GeneratedAt)
	if !ok || cache.MemoriesMtime != mtime || cache.MemoriesSize != size || now.Sub(at) > doctorCacheMaxAge {
		return nil
	}
	return cache.Findings
}

// primeStaleKeys returns the rule / note keys prime marks 「⚠ 可能过期」: the cheap
// checks run every time, the path / commit findings come from a fresh doctor
// cache only (prime never runs git for this).
func (s *Store) primeStaleKeys(items []Memory, cfg Config, now time.Time) map[string]bool {
	cached := s.cachedDoctorFindings(now)
	out := map[string]bool{}
	for _, m := range items {
		if m.EffectiveKind() == MemoryKindHandoff {
			continue
		}
		findings := cheapFindings(m, now)
		for _, f := range cached[m.Key] {
			if f.Slug == DoctorPathMissing || f.Slug == DoctorCommitMissing {
				findings = append(findings, f)
			}
		}
		kept, _ := suppressFindings(findings, cfg.Prime.DoctorSuppress(), m.DoctorIgnore)
		for _, f := range kept {
			if hasTag(doctorStaleSlugs, f.Slug) {
				out[m.Key] = true
				break
			}
		}
	}
	return out
}

// FormatDoctorReport is the human `memory doctor` output.
func FormatDoctorReport(r DoctorReport) string {
	var b strings.Builder
	for _, e := range r.Memories {
		fmt.Fprintf(&b, "%s [%s]", e.Key, e.Kind)
		if e.Summary != "" {
			b.WriteString(" · " + e.Summary)
		}
		b.WriteString("\n")
		for _, f := range e.Findings {
			fmt.Fprintf(&b, "  - %s", f.Slug)
			if f.Detail != "" {
				b.WriteString(": " + f.Detail)
			}
			b.WriteString("\n")
		}
	}
	fmt.Fprintf(&b, "checked %d, flagged %d, suppressed %d (advisory; silence with `memory set <key> … --doctor-ignore <slug>` or prime.doctor.suppress)\n", r.Checked, r.Flagged, r.Suppressed)
	return b.String()
}
