package memorysynthesis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/i18n/prompts"
)

// ResultSchemaV1 is the schema tag of the model's answer and of the stored result_json.
const ResultSchemaV1 = "daily-memory-synthesis.v1"

// Bounds the server enforces on a model answer (design §3.3.1; the candidate fields of the
// design are not part of this version, see §11).
const (
	maxSummaryRunes   = 600
	maxHighlights     = 20
	maxHighlightRunes = 500
	maxQuestions      = 10
	maxQuestionRunes  = 300
	maxRefsPerItem    = 20
)

// Highlight is one concrete development with the source refs it rests on.
type Highlight struct {
	Text       string   `json:"text"`
	SourceRefs []string `json:"source_refs,omitempty"`
}

// Result is the validated, normalised answer stored in memory_synthesis_runs.result_json.
// Unknown fields a model may add are dropped, never stored.
type Result struct {
	Schema        string      `json:"schema"`
	Summary       string      `json:"summary"`
	Highlights    []Highlight `json:"highlights"`
	OpenQuestions []string    `json:"open_questions"`
}

// Reasons a synthesis fails; each is the stable prefix of the error kept on the run record.
var (
	ErrAgent        = errors.New("agent call failed")
	ErrOutputFormat = errors.New("invalid output")
	ErrUnsafeInput  = errors.New("unsafe input")
	ErrUnsafeOutput = errors.New("unsafe output")
)

// OneShotRequest is one read-only, tool-less model call. The adapter that implements OneShot
// fixes the safety shape (read-only, no tools, no rules, hidden internal job); the caller
// only supplies the text and the operator's model arguments.
type OneShotRequest struct {
	ProjectKey string
	Agent      string
	Args       []string
	Prompt     string
	Title      string
	Timeout    time.Duration
	// MaxOutputBytes is how much stdout the adapter reads at most (+1 so an oversized
	// answer is detected rather than silently cut).
	MaxOutputBytes int
	// Origin is the ceiling owner the call is submitted under ("" = none).
	Origin string
}

// OneShot runs a model call and returns the agent's stdout.
type OneShot interface {
	Run(ctx context.Context, r OneShotRequest) (string, error)
}

// SynthConfig is what a synthesizer needs from memory.synthesis (already resolved).
type SynthConfig struct {
	Agent string
	Model string
	Args  []string
	// Timeout bounds one call (memory.synthesis.timeout).
	Timeout time.Duration
	// MaxOutputBytes bounds the model answer (memory.synthesis.max_output_bytes).
	MaxOutputBytes int
	// Origin is the ceiling owner of the model call: the caller that asked for the run.
	Origin string
	// Lang is the project language the prompt (and so the summary) is written in.
	Lang i18n.Lang
}

// NewSynthesizer builds the model-backed Synthesizer: it renders the prompt from the
// already scrubbed and bounded envelope, makes one read-only call and validates the answer.
// It never sees anything but Materials (the output of Build), and re-checks the serialised
// envelope for secret shapes so a caller that bypassed Build cannot leak them to a model.
func NewSynthesizer(o OneShot, c SynthConfig) Synthesizer {
	return func(ctx context.Context, m Materials) (SynthResult, error) {
		res := SynthResult{Agent: c.Agent, Model: c.Model}
		if o == nil {
			return res, fmt.Errorf("%w: no model runner is wired", ErrAgent)
		}
		raw, err := json.Marshal(m.Envelope)
		if err != nil {
			return res, fmt.Errorf("%w: marshal materials: %v", ErrUnsafeInput, err)
		}
		if _, hit := Scrub(string(raw)); hit {
			return res, fmt.Errorf("%w: materials still contain a secret-shaped fragment; nothing was sent", ErrUnsafeInput)
		}
		maxOut := c.MaxOutputBytes
		if maxOut <= 0 {
			maxOut = 12000
		}
		prompt, err := prompts.Render(c.Lang, prompts.NameMemorySynthesisRequest,
			prompts.MemorySynthesisRequestData{Materials: string(raw), MaxOutputBytes: maxOut})
		if err != nil {
			return res, fmt.Errorf("%w: render prompt: %v", ErrAgent, err)
		}
		if c.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, c.Timeout)
			defer cancel()
		}
		out, err := o.Run(ctx, OneShotRequest{
			ProjectKey: m.Project, Agent: c.Agent, Args: c.Args, Prompt: prompt,
			Title: "memory synthesis " + m.Project + " " + m.Day, Timeout: c.Timeout,
			MaxOutputBytes: maxOut, Origin: c.Origin,
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return res, fmt.Errorf("%w: timed out after %s", ErrAgent, c.Timeout)
			}
			return res, fmt.Errorf("%w: %v", ErrAgent, err)
		}
		if len(out) > maxOut {
			return res, fmt.Errorf("%w: answer is %d bytes, limit %d", ErrOutputFormat, len(out), maxOut)
		}
		parsed, err := ParseResult(out, allowedRefs(m))
		if err != nil {
			return res, err
		}
		b, _ := json.Marshal(parsed)
		if _, hit := Scrub(string(b)); hit {
			return res, fmt.Errorf("%w: the answer contains a secret-shaped fragment and was discarded", ErrUnsafeOutput)
		}
		res.ResultJSON = string(b)
		return res, nil
	}
}

// allowedRefs is every source ref a model may cite: the items' own refs and merged refs.
func allowedRefs(m Materials) map[string]bool {
	out := map[string]bool{}
	for _, it := range m.Items {
		out[it.SourceRef] = true
		for _, r := range it.SourceRefs {
			out[r] = true
		}
	}
	return out
}

// ParseResult extracts the single JSON object of a model answer (a markdown fence or text
// around it is tolerated) and validates it strictly: schema tag, bounded non-empty summary,
// bounded lists, and every cited source ref must be in `allowed`.
func ParseResult(output string, allowed map[string]bool) (Result, error) {
	obj, ok := firstJSONObject(output)
	if !ok {
		return Result{}, fmt.Errorf("%w: no JSON object in the answer", ErrOutputFormat)
	}
	var in struct {
		Schema     string `json:"schema"`
		Summary    string `json:"summary"`
		Highlights []struct {
			Text       string   `json:"text"`
			SourceRefs []string `json:"source_refs"`
		} `json:"highlights"`
		OpenQuestions []string `json:"open_questions"`
	}
	if err := json.Unmarshal(obj, &in); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrOutputFormat, err)
	}
	if in.Schema != ResultSchemaV1 {
		return Result{}, fmt.Errorf("%w: schema %q, want %q", ErrOutputFormat, in.Schema, ResultSchemaV1)
	}
	out := Result{Schema: ResultSchemaV1, Summary: strings.TrimSpace(in.Summary), Highlights: []Highlight{}, OpenQuestions: []string{}}
	if out.Summary == "" {
		return Result{}, fmt.Errorf("%w: summary is empty", ErrOutputFormat)
	}
	if utf8.RuneCountInString(out.Summary) > maxSummaryRunes {
		return Result{}, fmt.Errorf("%w: summary exceeds %d characters", ErrOutputFormat, maxSummaryRunes)
	}
	if len(in.Highlights) > maxHighlights {
		return Result{}, fmt.Errorf("%w: %d highlights, limit %d", ErrOutputFormat, len(in.Highlights), maxHighlights)
	}
	for i, h := range in.Highlights {
		text := strings.TrimSpace(h.Text)
		if text == "" {
			return Result{}, fmt.Errorf("%w: highlight %d is empty", ErrOutputFormat, i+1)
		}
		if utf8.RuneCountInString(text) > maxHighlightRunes {
			return Result{}, fmt.Errorf("%w: highlight %d exceeds %d characters", ErrOutputFormat, i+1, maxHighlightRunes)
		}
		if len(h.SourceRefs) > maxRefsPerItem {
			return Result{}, fmt.Errorf("%w: highlight %d cites %d refs, limit %d", ErrOutputFormat, i+1, len(h.SourceRefs), maxRefsPerItem)
		}
		refs := make([]string, 0, len(h.SourceRefs))
		for _, r := range h.SourceRefs {
			r = strings.TrimSpace(r)
			if !allowed[r] {
				return Result{}, fmt.Errorf("%w: highlight %d cites unknown source_ref %q", ErrOutputFormat, i+1, trimRunes(r, 80))
			}
			refs = appendUnique(refs, r)
		}
		out.Highlights = append(out.Highlights, Highlight{Text: text, SourceRefs: refs})
	}
	if len(in.OpenQuestions) > maxQuestions {
		return Result{}, fmt.Errorf("%w: %d open questions, limit %d", ErrOutputFormat, len(in.OpenQuestions), maxQuestions)
	}
	for i, q := range in.OpenQuestions {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if utf8.RuneCountInString(q) > maxQuestionRunes {
			return Result{}, fmt.Errorf("%w: open question %d exceeds %d characters", ErrOutputFormat, i+1, maxQuestionRunes)
		}
		out.OpenQuestions = append(out.OpenQuestions, q)
	}
	return out, nil
}

// firstJSONObject returns the first balanced top-level {...} of s (string-aware), so a
// fenced or chatty answer still parses while a truncated one does not.
func firstJSONObject(s string) (json.RawMessage, bool) {
	start := strings.IndexByte(s, '{')
	for start >= 0 {
		depth, inStr, esc := 0, false, false
		for i := start; i < len(s); i++ {
			c := s[i]
			switch {
			case inStr:
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					inStr = false
				}
			case c == '"':
				inStr = true
			case c == '{':
				depth++
			case c == '}':
				depth--
				if depth == 0 {
					cand := []byte(s[start : i+1])
					if json.Valid(cand) {
						return bytes.Clone(cand), true
					}
					goto next
				}
			}
		}
		return nil, false
	next:
		rest := strings.IndexByte(s[start+1:], '{')
		if rest < 0 {
			return nil, false
		}
		start += 1 + rest
	}
	return nil, false
}
