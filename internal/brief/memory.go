package brief

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/tracker"
)

// notesMax caps the path / keyword matched notes listed.
const notesMax = 10

// scopeKind is where a memory came from.
type scopeKind string

const (
	// scopeRepo is the repository's own tracker.
	scopeRepo scopeKind = ""
	// scopeGlobal is the global memory set, read locally or from the server.
	scopeGlobal scopeKind = "global"
	// scopeProject is one project's memories on the server.
	scopeProject scopeKind = "project"
)

// scopedMemory is a memory with the scope it came from ("" = repository).
type scopedMemory struct {
	scope scopeKind
	key   string // the project key of a project scope
	m     tracker.Memory
}

// scopeName is the scope as the brief spells it ("" for the repository's own).
func (s scopedMemory) scopeName(lang i18n.Lang) string {
	switch s.scope {
	case scopeGlobal:
		return briefText(lang, "brief.memory.scope_global")
	case scopeProject:
		return briefText(lang, "brief.memory.scope_project", "project", s.key)
	}
	return ""
}

func (s scopedMemory) label(lang i18n.Lang) string {
	if name := s.scopeName(lang); name != "" {
		return "[" + name + "] " + s.m.Key
	}
	return s.m.Key
}

// memoryTarget is what the memories are matched against.
type memoryTarget struct {
	files    []string // code entries (when.paths)
	text     string   // issue title + description (when.keywords)
	tags     []string // issue tags
	keywords []string // ascii title words (memory key)
}

func newMemoryTarget(item tracker.Issue, files []string) memoryTarget {
	t := memoryTarget{files: files, text: item.Title + "\n" + item.Description, tags: item.Tags}
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(item.Title), func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)))
	}) {
		if len(w) >= 4 && !stopWords[w] && !seen[w] {
			seen[w] = true
			t.keywords = append(t.keywords, w)
		}
	}
	return t
}

var stopWords = map[string]bool{"feat": true, "with": true, "from": true, "into": true, "when": true, "that": true, "this": true}

func (t memoryTarget) pathHit(m tracker.Memory) bool {
	for _, f := range t.files {
		if tracker.MemoryMatchesPath(m.MemoryMeta, f) {
			return true
		}
	}
	return false
}

func (t memoryTarget) keywordHit(m tracker.Memory) bool {
	if _, ok := tracker.MemoryMatchesKeyword(m.MemoryMeta, t.text); ok {
		return true
	}
	for _, tag := range m.Tags {
		for _, want := range t.tags {
			if strings.EqualFold(tag, want) {
				return true
			}
		}
	}
	key := strings.ToLower(m.Key)
	for _, w := range t.keywords {
		if strings.Contains(key, w) {
			return true
		}
	}
	return false
}

// memorySection lists the applicable memories: every rule that applies everywhere
// (or whose when.paths / when.keywords match) in full, then the notes matched by the
// code entries' paths or by the issue's keywords as index lines. Flagged memories
// carry the 「⚠ 待复核」 prefix.
func memorySection(opts Options, target memoryTarget) Section {
	lang := opts.lang()
	sec := Section{Title: briefText(lang, "brief.memory.title"), More: briefText(lang, "brief.memory.hint")}
	var all []scopedMemory
	if opts.Store != nil {
		if items, err := opts.Store.ReadMemories(); err == nil {
			for _, m := range items {
				all = append(all, scopedMemory{m: m})
			}
		}
	}
	globalDone := false
	if opts.GlobalMemories != nil {
		items, err := opts.GlobalMemories()
		switch {
		case err == nil:
			for _, m := range items {
				all = append(all, scopedMemory{scope: scopeGlobal, m: m})
			}
			globalDone = true
		case !errors.Is(err, tracker.ErrGlobalNotSynced):
			sec.Note = briefText(lang, "brief.memory.global_failed", "err", err.Error())
			globalDone = true
		}
	}
	switch {
	case opts.Client == nil:
		if globalDone {
			if opts.ProjectKey != "" {
				sec.Note = briefText(lang, "brief.memory.project_note", "note", opts.serverNote())
			}
		} else {
			sec.Note = briefText(lang, "brief.memory.scoped_note", "note", opts.serverNote())
		}
	default:
		if !globalDone {
			if rows, err := opts.Client.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "global"}); err == nil {
				all = appendScoped(all, rows, scopeGlobal, "")
			} else {
				sec.Note = briefText(lang, "brief.memory.global_failed", "err", err.Error())
			}
		}
		if opts.ProjectKey != "" {
			if rows, err := opts.Client.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "project", ScopeKey: opts.ProjectKey}); err == nil {
				all = appendScoped(all, rows, scopeProject, opts.ProjectKey)
			}
		}
	}
	now := opts.now()
	var rules, notes []scopedMemory
	for _, sm := range all {
		m := sm.m
		if !tracker.MemoryForAgent(m.Tags, opts.AgentName) || tracker.MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
			continue
		}
		switch {
		case m.EffectiveKind() == tracker.MemoryKindRule && (m.When == nil || target.pathHit(m) || target.keywordHit(m)):
			rules = append(rules, sm)
		case m.EffectiveKind() != tracker.MemoryKindRule && (target.pathHit(m) || target.keywordHit(m)):
			notes = append(notes, sm)
		}
	}
	sortScoped(rules)
	sortScoped(notes)
	for _, sm := range rules {
		head := "- " + flagPrefix(sm.m) + sm.label(lang) + briefText(lang, "brief.memory.rule_head") + ": "
		body := indentBlock(sm.m.Content, "    ")
		if len(body) == 0 {
			continue
		}
		sec.Lines = append(sec.Lines, head+strings.TrimSpace(body[0]))
		sec.Lines = append(sec.Lines, body[1:]...)
	}
	for i, sm := range notes {
		if i >= notesMax {
			sec.Lines = append(sec.Lines, briefText(lang, "brief.memory.more", "n", strconv.Itoa(len(notes)-i)))
			break
		}
		line := "- " + flagPrefix(sm.m) + sm.label(lang)
		if kind := sm.m.EffectiveKind(); kind == tracker.MemoryKindHandoff {
			line += briefText(lang, "brief.memory.handoff")
		}
		if headline := tracker.DisplayMemoryHeadline(sm.m.MemoryMeta, sm.m.Content); headline != "" {
			line += " · " + headline
		}
		if age := tracker.AgeText(sm.m.UpdatedAt, now); age != "" {
			line += " · " + age
		}
		sec.Lines = append(sec.Lines, line)
	}
	if len(sec.Lines) == 0 && sec.Note == "" {
		sec.Note = briefText(lang, "brief.memory.none")
	}
	return sec
}

func flagPrefix(m tracker.Memory) string {
	if p := tracker.MemoryFlagPrefix(m.MemoryMeta); p != "" {
		return p + " "
	}
	return ""
}

func appendScoped(out []scopedMemory, rows []client.ScopedMemory, scope scopeKind, key string) []scopedMemory {
	for _, row := range rows {
		if !row.Deleted {
			out = append(out, scopedMemory{scope: scope, key: key, m: row.TrackerMemory()})
		}
	}
	return out
}

// sortScoped orders repository memories first, then the global set, then by project
// key and memory key.
func sortScoped(items []scopedMemory) {
	rank := func(s scopeKind) int {
		switch s {
		case scopeRepo:
			return 0
		case scopeGlobal:
			return 1
		}
		return 2
	}
	sort.SliceStable(items, func(i, j int) bool {
		if ri, rj := rank(items[i].scope), rank(items[j].scope); ri != rj {
			return ri < rj
		}
		if items[i].key != items[j].key {
			return items[i].key < items[j].key
		}
		return items[i].m.Key < items[j].m.Key
	})
}
