package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/job"
)

// gofer_ask: a resident session's way to put a question to the person watching it. The
// session's own gofer MCP is started by the server with GOFER_ASK=1 (see job.applyGoferAskRun)
// and, unless another marker narrows it differently (steward, resident mailbox), registers
// only this tool. The question is an interaction on the session's OWN job, so it shows up as
// a card in the web conversation and on 「等我」, and the answer comes back as the tool result.

// envGoferAsk is set (to "1") by the job service for a resident ACP session job.
const envGoferAsk = "GOFER_ASK"

// envMailResident marks the resident mailbox session (full gofer surface plus gofer_ask).
const envMailResident = "GOFER_MAIL_RESIDENT"

func goferAskMode() bool { return strings.TrimSpace(os.Getenv(envGoferAsk)) != "" }

func mailResidentMode() bool { return strings.TrimSpace(os.Getenv(envMailResident)) != "" }

const (
	// askDefaultTimeoutSec / askMaxTimeoutSec bound how long a question stays open.
	askDefaultTimeoutSec = 1800
	askMaxTimeoutSec     = 86400
	// askExpiryGrace is how long past the deadline the tool waits for the server's own
	// expiry (it cancels the interaction at the deadline) before it answers expired itself.
	askExpiryGrace = 10 * time.Second
)

// goferAskPollInterval is the interaction poll cadence while gofer_ask waits.
var goferAskPollInterval = 2 * time.Second

type askOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// goferAskInput is the gofer_ask tool input. No options = a free-text question.
type goferAskInput struct {
	Question    string      `json:"question"`
	Options     []askOption `json:"options,omitempty"`
	Multi       bool        `json:"multi,omitempty"`
	AllowOther  bool        `json:"allow_other,omitempty"`
	Recommended string      `json:"recommended,omitempty"`
	TimeoutSec  int         `json:"timeout_sec,omitempty"`
}

// goferAskOutput is the tool result. answered carries the answer (a multi-select also fills
// answers); expired means nobody answered in time (continue with the recommended option);
// cancelled means the question was withdrawn (the job ended or was cancelled).
type goferAskOutput struct {
	State       string   `json:"state"` // answered | expired | cancelled
	Answer      string   `json:"answer,omitempty"`
	Answers     []string `json:"answers,omitempty"`
	AnsweredBy  string   `json:"answered_by,omitempty"`
	Recommended string   `json:"recommended,omitempty"`
}

// registerGoferAsk adds gofer_ask to a server surface.
func registerGoferAsk(s *mcp.Server, b Backend) {
	addGoferTool(s, &mcp.Tool{
		Name: "gofer_ask",
		Description: "Ask the PERSON watching this session a question and block until they answer. Use it when you need a decision or a missing fact instead of guessing. " +
			"Give options (label, optional description) for a choice, set multi=true to allow several, allow_other=true to accept free text besides the options, " +
			"recommended=<label> to mark your suggested option; no options = a free-text question. " +
			"Returns {state:\"answered\", answer} (multi: answers too), {state:\"expired\"} when nobody answered within timeout_sec (default 1800, max 86400) - then continue with your recommended option - or {state:\"cancelled\"}.",
	}, goferAskHandler(b))
}

func goferAskHandler(b Backend) mcp.ToolHandlerFor[goferAskInput, goferAskOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in goferAskInput) (*mcp.CallToolResult, goferAskOutput, error) {
		jobID := strings.TrimSpace(os.Getenv(envJobID))
		if jobID == "" {
			return nil, goferAskOutput{}, fmt.Errorf("gofer_ask only works inside a gofer job (%s is unset)", envJobID)
		}
		input, timeoutSec, err := buildAskInteraction(in)
		if err != nil {
			return nil, goferAskOutput{}, err
		}
		it, err := b.CreateInteraction(jobID, input, timeoutSec)
		if err != nil {
			return nil, goferAskOutput{}, err
		}
		deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
		out, err := waitAskAnswer(ctx, b, jobID, it, deadline, in.Multi)
		out.Recommended = ""
		if out.State == "expired" {
			out.Recommended = in.Recommended
		}
		return nil, out, err
	}
}

// buildAskInteraction validates the tool input and shapes the interaction.
func buildAskInteraction(in goferAskInput) (job.InteractionInput, int, error) {
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return job.InteractionInput{}, 0, fmt.Errorf("question is required")
	}
	timeoutSec := in.TimeoutSec
	switch {
	case timeoutSec <= 0:
		timeoutSec = askDefaultTimeoutSec
	case timeoutSec > askMaxTimeoutSec:
		timeoutSec = askMaxTimeoutSec
	}
	input := job.InteractionInput{
		Type: job.InteractionTypeQuestion, Prompt: q,
		Multi: in.Multi, AllowOther: in.AllowOther, Recommended: strings.TrimSpace(in.Recommended),
	}
	if len(in.Options) == 0 {
		if in.Multi || in.AllowOther || input.Recommended != "" {
			return job.InteractionInput{}, 0, fmt.Errorf("multi / allow_other / recommended need options")
		}
		return input, timeoutSec, nil
	}
	seen := make(map[string]bool, len(in.Options))
	input.Type = job.InteractionTypeChoice
	for _, o := range in.Options {
		label := strings.TrimSpace(o.Label)
		if label == "" {
			return job.InteractionInput{}, 0, fmt.Errorf("every option needs a label")
		}
		if seen[label] {
			return job.InteractionInput{}, 0, fmt.Errorf("duplicate option label %q", label)
		}
		seen[label] = true
		input.Options = append(input.Options, job.InteractionOption{Value: label, Label: label, Description: strings.TrimSpace(o.Description)})
	}
	if input.Recommended != "" && !seen[input.Recommended] {
		return job.InteractionInput{}, 0, fmt.Errorf("recommended %q is not one of the options", input.Recommended)
	}
	return input, timeoutSec, nil
}

// waitAskAnswer polls the job's interactions until the asked one is answered, cancelled or
// past its deadline. A cancel before the deadline is a withdrawal (the job ended); at or
// after it, an expiry. The server cancels at the deadline itself, so the grace only covers
// a lost timer. ctx ending (the agent host went away) is an error: the interaction is left
// to the server's own deadline.
func waitAskAnswer(ctx context.Context, b Backend, jobID string, it job.Interaction, deadline time.Time, multi bool) (goferAskOutput, error) {
	ticker := time.NewTicker(goferAskPollInterval)
	defer ticker.Stop()
	for {
		list, err := b.GetInteractions(jobID)
		if err != nil {
			return goferAskOutput{}, err
		}
		for _, cur := range list {
			if cur.ID != it.ID {
				continue
			}
			switch cur.Status {
			case job.InteractionAnswered:
				return askAnswerOutput(cur, multi), nil
			case job.InteractionCancelled:
				if !time.Now().Before(deadline.Add(-2 * time.Second)) {
					return goferAskOutput{State: "expired"}, nil
				}
				return goferAskOutput{State: "cancelled"}, nil
			}
		}
		if !time.Now().Before(deadline.Add(askExpiryGrace)) {
			return goferAskOutput{State: "expired"}, nil
		}
		select {
		case <-ctx.Done():
			return goferAskOutput{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func askAnswerOutput(it job.Interaction, multi bool) goferAskOutput {
	out := goferAskOutput{State: "answered", Answer: it.Answer, AnsweredBy: it.AnsweredBy}
	if multi {
		var picked []string
		if json.Unmarshal([]byte(it.Answer), &picked) == nil {
			out.Answers = picked
		}
	}
	return out
}
