package brief

import (
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/tracker"
)

// notesMax caps the path / keyword matched notes listed.
const notesMax = 10

// scopedMemory is a memory with the scope it came from ("" = repository).
type scopedMemory struct {
	scope string // "" | "全局" | "项目 <key>"
	m     tracker.Memory
}

func (s scopedMemory) label() string {
	if s.scope == "" {
		return s.m.Key
	}
	return "[" + s.scope + "] " + s.m.Key
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
	sec := Section{Title: "适用记忆", More: "gofer memory ls <关键字>"}
	var all []scopedMemory
	if opts.Store != nil {
		if items, err := opts.Store.ReadMemories(); err == nil {
			for _, m := range items {
				all = append(all, scopedMemory{m: m})
			}
		}
	}
	switch {
	case opts.Client == nil:
		sec.Note = "全局 / 项目记忆：" + opts.serverNote()
	default:
		if rows, err := opts.Client.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "global"}); err == nil {
			all = appendScoped(all, rows, "全局")
		} else {
			sec.Note = "全局记忆读取失败：" + err.Error()
		}
		if opts.ProjectKey != "" {
			if rows, err := opts.Client.ListScopedMemories(client.ScopedMemoryListOpts{Scope: "project", ScopeKey: opts.ProjectKey}); err == nil {
				all = appendScoped(all, rows, "项目 "+opts.ProjectKey)
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
		head := "- " + flagPrefix(sm.m) + sm.label() + "（规则）: "
		body := indentBlock(sm.m.Content, "    ")
		if len(body) == 0 {
			continue
		}
		sec.Lines = append(sec.Lines, head+strings.TrimSpace(body[0]))
		sec.Lines = append(sec.Lines, body[1:]...)
	}
	for i, sm := range notes {
		if i >= notesMax {
			sec.Lines = append(sec.Lines, "- 另有 "+strconv.Itoa(len(notes)-i)+" 条：`gofer memory ls <关键字>`")
			break
		}
		line := "- " + flagPrefix(sm.m) + sm.label()
		if kind := sm.m.EffectiveKind(); kind == tracker.MemoryKindHandoff {
			line += "（交接）"
		}
		if summary := tracker.DisplayMemorySummary(sm.m.MemoryMeta, sm.m.Content); summary != "" {
			line += " · " + summary
		}
		if age := tracker.AgeText(sm.m.UpdatedAt, now); age != "" {
			line += " · " + age
		}
		sec.Lines = append(sec.Lines, line)
	}
	if len(sec.Lines) == 0 && sec.Note == "" {
		sec.Note = "无匹配记忆"
	}
	return sec
}

func flagPrefix(m tracker.Memory) string {
	if p := tracker.MemoryFlagPrefix(m.MemoryMeta); p != "" {
		return p + " "
	}
	return ""
}

func appendScoped(out []scopedMemory, rows []client.ScopedMemory, scope string) []scopedMemory {
	for _, row := range rows {
		if !row.Deleted {
			out = append(out, scopedMemory{scope: scope, m: row.TrackerMemory()})
		}
	}
	return out
}

// sortScoped orders repository memories first, then by key.
func sortScoped(items []scopedMemory) {
	sort.SliceStable(items, func(i, j int) bool {
		if (items[i].scope == "") != (items[j].scope == "") {
			return items[i].scope == ""
		}
		if items[i].scope != items[j].scope {
			return items[i].scope < items[j].scope
		}
		return items[i].m.Key < items[j].m.Key
	})
}
