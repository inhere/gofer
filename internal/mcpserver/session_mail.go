package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/inhere/gofer/internal/client"
)

// The gofer mailbox over MCP (plan M3): the same operations as `gofer session mail`, under the
// same semantics. The letter's origin comes from the MCP process environment (the session the
// agent runs in) or, for a job credential, from the job; all of them need a running server.

type mailSendInput struct {
	// To is session:<sid> | job:<id> | topic:<keyword> | path:<sub-path> | caller ("" = caller).
	To     string `json:"to,omitempty"`
	Body   string `json:"body"`
	Urgent bool   `json:"urgent,omitempty"`
}

type mailListInput struct {
	Box     string `json:"box,omitempty"` // in | out | all (default all)
	Thread  string `json:"thread,omitempty"`
	State   string `json:"state,omitempty"`
	Session string `json:"session,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type mailListOutput struct {
	Mail []client.Mail `json:"mail"`
}

type mailIDInput struct {
	ID int64 `json:"id"`
}

type mailReplyInput struct {
	ID     int64  `json:"id"`
	Body   string `json:"body"`
	Urgent bool   `json:"urgent,omitempty"`
}

func registerSessionMailTools(s *mcp.Server, b Backend) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "gofer_session_mail_send",
		Description: "Send a letter to another session, a session job or yourself. to: session:<sid> | job:<id> | topic:<keyword> | path:<sub-path> | caller (your own capture box). " +
			"Only a unique recipient is delivered to; an ambiguous topic / path address is held in the capture box with its candidates (held=true in the answer — pick one and send again with session:<sid>). " +
			"A letter arrives in the recipient's next turn wrapped in a <gofer-mail> tag; it is not a user message. urgent=true asks for delivery inside the running turn. Needs a running gofer server.",
	}, mailSendHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_mail_list",
		Description: "List letters you sent or received (read-only). box: in | out | all (default), plus optional thread / state / session filters and limit. Needs a running gofer server.",
	}, mailListHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_mail_read",
		Description: "Read one letter in full. Reading marks it read as its recipient, so it is not injected into a later turn again. Needs a running gofer server.",
	}, mailReadHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_mail_reply",
		Description: "Answer a letter in its thread; the answer goes back to the sender's address. This is the only way to answer a letter. Needs a running gofer server.",
	}, mailReplyHandler(b))
	mcp.AddTool(s, &mcp.Tool{
		Name:        "gofer_session_mail_cancel",
		Description: "Withdraw a letter that has not been delivered yet (an already delivered letter cannot be cancelled). Needs a running gofer server.",
	}, mailCancelHandler(b))
}

func mailSendHandler(b Backend) mcp.ToolHandlerFor[mailSendInput, client.MailSent] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in mailSendInput) (*mcp.CallToolResult, client.MailSent, error) {
		if strings.TrimSpace(in.Body) == "" {
			return nil, client.MailSent{}, fmt.Errorf("body is required")
		}
		r, err := b.SendMail(strings.TrimSpace(in.To), in.Body, in.Urgent)
		return nil, r, err
	}
}

func mailListHandler(b Backend) mcp.ToolHandlerFor[mailListInput, mailListOutput] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in mailListInput) (*mcp.CallToolResult, mailListOutput, error) {
		r, err := b.ListMail(client.MailListOpts{
			Box: strings.TrimSpace(in.Box), Thread: strings.TrimSpace(in.Thread), State: strings.TrimSpace(in.State),
			Session: strings.TrimSpace(in.Session), Limit: in.Limit,
		})
		if r == nil {
			r = []client.Mail{}
		}
		return nil, mailListOutput{Mail: r}, err
	}
}

func mailReadHandler(b Backend) mcp.ToolHandlerFor[mailIDInput, client.Mail] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in mailIDInput) (*mcp.CallToolResult, client.Mail, error) {
		if in.ID <= 0 {
			return nil, client.Mail{}, fmt.Errorf("id is required")
		}
		m, err := b.ReadMail(in.ID)
		return nil, m, err
	}
}

func mailReplyHandler(b Backend) mcp.ToolHandlerFor[mailReplyInput, client.MailSent] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in mailReplyInput) (*mcp.CallToolResult, client.MailSent, error) {
		if in.ID <= 0 || strings.TrimSpace(in.Body) == "" {
			return nil, client.MailSent{}, fmt.Errorf("id and body are required")
		}
		r, err := b.ReplyMail(in.ID, in.Body, in.Urgent)
		return nil, r, err
	}
}

func mailCancelHandler(b Backend) mcp.ToolHandlerFor[mailIDInput, client.Mail] {
	return func(_ context.Context, _ *mcp.CallToolRequest, in mailIDInput) (*mcp.CallToolResult, client.Mail, error) {
		if in.ID <= 0 {
			return nil, client.Mail{}, fmt.Errorf("id is required")
		}
		m, err := b.CancelMail(in.ID)
		return nil, m, err
	}
}
