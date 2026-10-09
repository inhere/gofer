package tracker

import (
	"fmt"
	"strings"
	"time"
)

// MemoryListLine is one `memory ls` row: key [kind] · age · summary, with the
// 已过期 marker for an expired handoff and 久未更新 for a 90-day-old note.
func MemoryListLine(m Memory, now time.Time) string {
	var b strings.Builder
	b.WriteString(m.Key)
	b.WriteString(" [" + m.EffectiveKind() + "]")
	if age := AgeText(m.UpdatedAt, now); age != "" {
		b.WriteString(" · " + age)
	}
	if MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
		b.WriteString("（已过期）")
	} else if MemoryStale(m.MemoryMeta, m.Tags, m.UpdatedAt, now) {
		b.WriteString("（久未更新）")
	}
	if summary := DisplayMemorySummary(m.MemoryMeta, m.Content); summary != "" {
		b.WriteString(" · " + summary)
	}
	if len(m.Tags) > 0 {
		b.WriteString(" #" + strings.Join(m.Tags, " #"))
	}
	if m.Source != "" {
		b.WriteString(" · 来源 " + truncateRunes(m.Source, memorySourceShortRunes))
	}
	b.WriteString("\n")
	return b.String()
}

// memorySourceShortRunes caps the source shown in `memory ls` rows.
const memorySourceShortRunes = 24

// MemoryDetail renders every field of a memory for `memory show`, then the content.
func MemoryDetail(m Memory, now time.Time) string {
	var b strings.Builder
	field := func(name, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}
	field("key", m.Key)
	kind := m.EffectiveKind()
	if m.Kind == "" && kind == MemoryKindRule {
		kind += "（由 prime 标签推断）"
	}
	field("kind", kind)
	if m.Summary != "" {
		field("summary", m.Summary)
	} else if derived := FallbackMemorySummary(m.Content); derived != "" {
		field("summary", derived+"（自动摘取，建议 --summary 补写）")
	}
	field("tags", strings.Join(m.Tags, ", "))
	if m.When != nil {
		var parts []string
		if len(m.When.Keywords) > 0 {
			parts = append(parts, "keywords="+strings.Join(m.When.Keywords, ","))
		}
		if len(m.When.Paths) > 0 {
			parts = append(parts, "paths="+strings.Join(m.When.Paths, ","))
		}
		if len(m.When.Commands) > 0 {
			parts = append(parts, "commands="+strings.Join(m.When.Commands, ","))
		}
		field("when", strings.Join(parts, "; "))
	}
	field("source", m.Source)
	field("doctor_ignore", strings.Join(m.DoctorIgnore, ", "))
	field("created", withAge(MemoryCreatedAt(m.MemoryMeta, m.UpdatedAt), now))
	updated := withAge(m.UpdatedAt, now)
	if m.By != "" {
		updated += " by " + m.By
	}
	field("updated", updated)
	if at, ok := MemoryExpiresAt(m.MemoryMeta, m.Tags, m.UpdatedAt); ok {
		state := fmt.Sprintf("%d 天后过期", ageDays(now, at))
		if !now.Before(at) {
			state = "已过期"
		}
		field("expires", at.Format(time.RFC3339)+"（"+state+"）")
	}
	b.WriteString("---\n")
	b.WriteString(m.Content)
	if !strings.HasSuffix(m.Content, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func withAge(value string, now time.Time) string {
	if age := AgeText(value, now); age != "" {
		return value + "（" + age + "）"
	}
	return value
}
