// Package memorysynthesis contains the bounded, privacy-filtered material envelope used by
// daily memory synthesis. The envelope (materials.go) has no database or agent dependency:
// callers provide already structured source items and Build performs the common ordering,
// de-duplication, secret scrubbing, budget enforcement, and digest calculation. service.go
// coordinates one run record through the jobstore; concrete material adapters live in the
// sources subpackage.
package memorysynthesis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	privateKeyRE = regexp.MustCompile(`(?is)-----BEGIN [^-]*PRIVATE KEY-----.*?(?:-----END [^-]*PRIVATE KEY-----|$)`)
	// authHeaderRE covers `Authorization: <scheme> <credential>` for any scheme.
	authHeaderRE = regexp.MustCompile(`(?i)\bauthorization["']?\s*[:=]\s*(?:(?:bearer|basic|token|digest)\s+)?["']?[^\s,;"']+`)
	bearerRE     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{8,}`)
	// bareTokenRE matches well-known credential shapes that appear without any key name.
	bareTokenRE = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{16,}|github_pat_[A-Za-z0-9_]{16,}|glpat-[A-Za-z0-9_-]{12,}|xox[abprs]-[A-Za-z0-9-]{8,}|AKIA[0-9A-Z]{12,}|AIza[0-9A-Za-z_-]{20,}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{4,})`)
	secretRE    = regexp.MustCompile(`(?i)\b[\w-]*(?:token|api[_-]?key|password|passwd|secret)[\w-]*["']?\s*[:=]\s*["']?[^\s,;"']+`)
	// argvSecretRE covers `--token xxx` / `--password=xxx` style command-line options.
	argvSecretRE = regexp.MustCompile(`(?i)(--?(?:[\w-]*(?:token|password|passwd|secret|api[_-]?key))(?:=|\s+))[^\s]+`)
	envRE        = regexp.MustCompile(`\b[A-Z][A-Z0-9_]{2,}\s*=\s*[^\s,;]+`)
	emailRE      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
)

// Scrub replaces every secret-shaped fragment of s with [redacted] and reports whether it
// changed anything. Callers must scrub BEFORE truncating: a cut through the middle of a
// secret would otherwise leave an unrecognisable (and still sensitive) prefix behind.
func Scrub(s string) (string, bool) { return scrub(s) }

func scrub(s string) (string, bool) {
	matched := false
	for _, re := range []*regexp.Regexp{privateKeyRE, authHeaderRE, bearerRE, bareTokenRE, secretRE, argvSecretRE, envRE, emailRE} {
		if re.MatchString(s) {
			matched = true
			if re == argvSecretRE {
				s = re.ReplaceAllString(s, "${1}[redacted]")
			} else {
				s = re.ReplaceAllString(s, "[redacted]")
			}
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
		// Scrub first, truncate second: truncating first could cut a secret in half and let
		// the surviving prefix slip past every pattern.
		var redacted, textRedacted bool
		item.Title, redacted = scrub(strings.TrimSpace(item.Title))
		item.Text, textRedacted = scrub(strings.TrimSpace(item.Text))
		item.Title = trimRunes(item.Title, opts.MaxRunesPerText)
		item.Text = trimRunes(item.Text, opts.MaxRunesPerText)
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
	for i := range items {
		// Stable per-envelope handles ("m-001") for items whose adapter did not name one.
		if items[i].ID == "" {
			items[i].ID = fmt.Sprintf("m-%03d", i+1)
		}
	}
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
