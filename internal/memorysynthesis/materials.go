// Package memorysynthesis contains the bounded, privacy-filtered material envelope used by
// daily memory synthesis. It deliberately has no database or agent dependency: callers provide
// already structured source items and this package performs the common ordering, de-duplication,
// secret scrubbing, budget enforcement, and digest calculation.
package memorysynthesis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const SchemaV1 = "daily-memory-materials.v1"

// Item is one allow-listed fact from a source adapter. Text is never treated as instructions.
type Item struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	OccurredAt  string   `json:"occurred_at,omitempty"`
	Title       string   `json:"title,omitempty"`
	Text        string   `json:"text,omitempty"`
	SourceRef   string   `json:"source_ref"`
	SourceRefs  []string `json:"source_refs,omitempty"`
	Sensitivity string   `json:"sensitivity,omitempty"`
}

type Omitted struct {
	Items   int      `json:"items,omitempty"`
	Bytes   int      `json:"bytes,omitempty"`
	Reasons []string `json:"reasons,omitempty"`
}

type Envelope struct {
	Schema   string  `json:"schema"`
	Project  string  `json:"project"`
	Day      string  `json:"day"`
	Timezone string  `json:"timezone"`
	Items    []Item  `json:"items"`
	Omitted  Omitted `json:"omitted"`
}

// Materials is the persisted-safe result of preparing an envelope. The digest covers the
// canonical envelope, including omission counts, and is therefore stable for identical input.
type Materials struct {
	Envelope
	InputDigest string `json:"input_digest"`
}

type Options struct {
	MaxInputBytes   int
	MaxItemsPerKind int
	MaxRunesPerText int
}

func (o Options) withDefaults() Options {
	if o.MaxInputBytes <= 0 {
		o.MaxInputBytes = 24000
	}
	if o.MaxItemsPerKind <= 0 {
		o.MaxItemsPerKind = 50
	}
	if o.MaxRunesPerText <= 0 {
		o.MaxRunesPerText = 1000
	}
	return o
}

// Source is a read-only structured material adapter. Adapters must not return transcripts,
// prompts, argv, environment values, or attachments.
type Source interface {
	Collect(context.Context, string, string) ([]Item, error)
}

func Collect(ctx context.Context, project, day string, sources ...Source) ([]Item, error) {
	var all []Item
	for _, source := range sources {
		if source == nil {
			continue
		}
		items, err := source.Collect(ctx, project, day)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

var (
	privateKeyRE = regexp.MustCompile(`(?is)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
	secretRE     = regexp.MustCompile(`(?i)\b(authorization\s*:\s*bearer|bearer|token|api[_-]?key|password|passwd|secret)\s*[:=]\s*[^\s,;]+`)
	envRE        = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\s*=\s*[^\s,;]+`)
)

func scrub(s string) (string, bool) {
	matched := false
	for _, re := range []*regexp.Regexp{privateKeyRE, secretRE, envRE} {
		if re.MatchString(s) {
			matched = true
			s = re.ReplaceAllString(s, "[redacted]")
		}
	}
	return s, matched
}

func trimRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

func normalize(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func addReason(reasons []string, reason string) []string {
	for _, r := range reasons {
		if r == reason {
			return reasons
		}
	}
	return append(reasons, reason)
}

func canonicalBytes(e Envelope) []byte {
	b, _ := json.Marshal(e)
	return b
}

// Build prepares source items into the versioned envelope. It is deterministic: items are
// ordered by occurrence and source, duplicate source refs/text are merged, and all limits are
// applied before hashing. The returned bytes never contain the original value of a scrubbed
// secret.
func Build(project, day, timezone string, input []Item, opts Options) (Materials, error) {
	if strings.TrimSpace(project) == "" {
		return Materials{}, errors.New("memorysynthesis: project is required")
	}
	if strings.TrimSpace(day) == "" {
		return Materials{}, errors.New("memorysynthesis: day is required")
	}
	opts = opts.withDefaults()

	items := make([]Item, 0, len(input))
	seenRef := make(map[string]int, len(input))
	seenText := make(map[string]int, len(input))
	perKind := make(map[string]int)
	omitted := Omitted{}
	for _, raw := range input {
		item := raw
		item.ID = strings.TrimSpace(item.ID)
		item.Kind = strings.TrimSpace(item.Kind)
		item.SourceRef = strings.TrimSpace(item.SourceRef)
		if item.Kind == "" || item.SourceRef == "" {
			omitted.Items++
			omitted.Reasons = addReason(omitted.Reasons, "invalid")
			continue
		}
		if perKind[item.Kind] >= opts.MaxItemsPerKind {
			omitted.Items++
			omitted.Reasons = addReason(omitted.Reasons, "count")
			continue
		}
		item.Title = trimRunes(strings.TrimSpace(item.Title), opts.MaxRunesPerText)
		item.Text = trimRunes(strings.TrimSpace(item.Text), opts.MaxRunesPerText)
		var redacted bool
		item.Title, redacted = scrub(item.Title)
		var textRedacted bool
		item.Text, textRedacted = scrub(item.Text)
		if redacted || textRedacted {
			item.Sensitivity = "redacted"
			omitted.Reasons = addReason(omitted.Reasons, "secret")
		}
		if n, ok := seenRef[item.SourceRef]; ok {
			items[n].SourceRefs = appendUnique(items[n].SourceRefs, item.SourceRef)
			continue
		}
		h := sha256.Sum256([]byte(normalize(item.Title + "\n" + item.Text)))
		textKey := hex.EncodeToString(h[:])
		if n, ok := seenText[textKey]; ok {
			items[n].SourceRefs = appendUnique(items[n].SourceRefs, item.SourceRef)
			seenRef[item.SourceRef] = n
			continue
		}
		seenRef[item.SourceRef] = len(items)
		seenText[textKey] = len(items)
		perKind[item.Kind]++
		items = append(items, item)
	}
	for i := range items {
		sort.Strings(items[i].SourceRefs)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].OccurredAt != items[j].OccurredAt {
			return items[i].OccurredAt < items[j].OccurredAt
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].SourceRef < items[j].SourceRef
	})
	e := Envelope{Schema: SchemaV1, Project: project, Day: day, Timezone: timezone, Items: items, Omitted: omitted}
	for len(e.Items) > 0 && len(canonicalBytes(e)) > opts.MaxInputBytes {
		last := e.Items[len(e.Items)-1]
		omitted.Items++
		omitted.Bytes += len(last.Title) + len(last.Text)
		omitted.Reasons = addReason(omitted.Reasons, "budget")
		e.Items = e.Items[:len(e.Items)-1]
		e.Omitted = omitted
	}
	b := canonicalBytes(e)
	if len(b) > opts.MaxInputBytes {
		return Materials{}, errors.New("memorysynthesis: input budget too small for envelope")
	}
	h := sha256.Sum256(b)
	return Materials{Envelope: e, InputDigest: hex.EncodeToString(h[:])}, nil
}

func appendUnique(values []string, value string) []string {
	for _, v := range values {
		if v == value {
			return values
		}
	}
	return append(values, value)
}
