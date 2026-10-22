package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/inhere/gofer/internal/jobstore"
)

// The gofer mailbox (POST/GET /v1/mail*). The client attaches the session header of the
// environment like every request, which is what makes a letter come "from" that session.

// MailParty is one end of a letter.
type MailParty struct {
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	Caller  string `json:"caller,omitempty"`
	Display string `json:"display,omitempty"`
}

// Mail is one letter as the server reports it.
type Mail struct {
	ID            int64     `json:"id"`
	ThreadID      string    `json:"thread_id"`
	ReplyTo       int64     `json:"reply_to,omitempty"`
	From          MailParty `json:"from"`
	ToAddress     string    `json:"to_address"`
	To            MailParty `json:"to"`
	JobID         string    `json:"job_id,omitempty"`
	Urgent        bool      `json:"urgent,omitempty"`
	Body          string    `json:"body"`
	State         string    `json:"state"`
	Attempts      int       `json:"attempts,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	CreatedAt     int64     `json:"created_at"`
	DeliveredAt   int64     `json:"delivered_at,omitempty"`
	ReadAt        int64     `json:"read_at,omitempty"`
	DeliverBy     int64     `json:"deliver_by,omitempty"`
	CaptureItemID string    `json:"capture_item_id,omitempty"`
}

// MailCandidate is a session an ambiguous address almost named.
type MailCandidate struct {
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Topic   string   `json:"topic,omitempty"`
	Source  string   `json:"source,omitempty"`
	Offline bool     `json:"offline,omitempty"`
	Why     []string `json:"why,omitempty"`
	Display string   `json:"display"`
}

// MailSent is the answer of a send / reply / readdress: the stored letter, and whether it was
// held in the capture box (with the candidates an ambiguous address matched).
type MailSent struct {
	Mail          Mail            `json:"mail"`
	Held          bool            `json:"held"`
	CaptureItemID string          `json:"capture_item_id,omitempty"`
	Candidates    []MailCandidate `json:"candidates,omitempty"`
	// BroadcastID / ThreadID / Matched / Delivered / Skipped / Recipients describe a broadcast
	// (设计 2026-10-22「D5」); all empty for a single letter.
	BroadcastID int64                    `json:"broadcast_id,omitempty"`
	ThreadID    string                   `json:"thread_id,omitempty"`
	Matched     int                      `json:"matched,omitempty"`
	Delivered   int                      `json:"delivered_count,omitempty"`
	Skipped     int                      `json:"skipped_count,omitempty"`
	Recipients  []MailBroadcastRecipient `json:"recipients,omitempty"`
}

// MailBroadcastRecipient is one addressee of a broadcast (a skipped one says why).
type MailBroadcastRecipient struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	ID      string `json:"id,omitempty"`
	MailID  int64  `json:"mail_id,omitempty"`
	State   string `json:"state,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// MailListOpts filters a listing (GET /v1/mail): Box is in | out | all ("" = all).
type MailListOpts struct {
	Box, Thread, State, Session string
	Limit, Offset               int
}

// SendMail sends one letter. to "" or "caller" is the caller's own capture box. The wire keeps
// the single-string form, so a single letter looks exactly as it did before broadcasts.
func (c *Client) SendMail(to, body string, urgent bool) (MailSent, error) {
	payload := map[string]any{"to": to, "body": body}
	if urgent {
		payload["urgent"] = true
	}
	return c.postMail("/v1/mail", payload)
}

// SendMailTo sends one letter to several recipients (a broadcast): repeated addresses and `each:`
// selectors are resolved by the server, which answers with the addressee table.
func (c *Client) SendMailTo(to []string, body string, urgent bool) (MailSent, error) {
	payload := map[string]any{"to": to, "body": body}
	if urgent {
		payload["urgent"] = true
	}
	return c.postMail("/v1/mail", payload)
}

// ListMail lists the letters the caller may see.
func (c *Client) ListMail(o MailListOpts) ([]Mail, error) {
	q := url.Values{}
	set := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			q.Set(k, v)
		}
	}
	set("box", o.Box)
	set("thread", o.Thread)
	set("state", o.State)
	set("session", o.Session)
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	path := "/v1/mail"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var out struct {
		Mail []Mail `json:"mail"`
	}
	err := c.doJSON(http.MethodGet, path, nil, &out)
	return out.Mail, err
}

// GetMail reads one letter without changing it.
func (c *Client) GetMail(id int64) (Mail, error) {
	var m Mail
	err := c.doJSON(http.MethodGet, mailPath(id, ""), nil, &m)
	return m, err
}

// ReadMail reads one letter AS ITS RECIPIENT: it fetches the letter and acknowledges it, so
// reading is delivery and the letter is not injected again. A caller that is only the sender
// gets the letter back unchanged.
func (c *Client) ReadMail(id int64) (Mail, error) {
	if _, err := c.GetMail(id); err != nil {
		return Mail{}, err
	}
	return c.AckMail(id)
}

// AckMail marks the letter read by its recipient (state delivered, read_at stamped).
func (c *Client) AckMail(id int64) (Mail, error) {
	var m Mail
	err := c.doJSON(http.MethodPost, mailPath(id, "ack"), bytes.NewReader([]byte("{}")), &m)
	return m, err
}

// CancelMail withdraws a letter that has not been delivered.
func (c *Client) CancelMail(id int64) (Mail, error) {
	var m Mail
	err := c.doJSON(http.MethodPost, mailPath(id, "cancel"), bytes.NewReader([]byte("{}")), &m)
	return m, err
}

// ReplyMail answers a letter in its thread; the answer goes back to the original sender.
func (c *Client) ReplyMail(id int64, body string, urgent bool) (MailSent, error) {
	payload := map[string]any{"body": body}
	if urgent {
		payload["urgent"] = true
	}
	return c.postMail(mailPath(id, "reply"), payload)
}

// ReaddressMail hands a held letter to a new address.
func (c *Client) ReaddressMail(id int64, to string) (MailSent, error) {
	return c.postMail(mailPath(id, "readdress"), map[string]any{"to": to})
}

// MailRedactRequest is the literal / pattern input of a letter redaction, the same shape job
// redaction takes (the literal itself travels in the body, never in argv).
type MailRedactRequest struct {
	Literals []string `json:"literals,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
}

// RedactMail erases secrets from one letter and from the collection-box summaries that quote it.
// The answer is aggregate-only (counts), so no matched text can travel back.
func (c *Client) RedactMail(id int64, req MailRedactRequest) (jobstore.RedactReport, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return jobstore.RedactReport{}, fmt.Errorf("encode mail redact request: %w", err)
	}
	var out jobstore.RedactReport
	if err := c.doJSON(http.MethodPost, mailPath(id, "redact"), bytes.NewReader(raw), &out); err != nil {
		return jobstore.RedactReport{}, err
	}
	return out, nil
}

func (c *Client) postMail(path string, payload map[string]any) (MailSent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return MailSent{}, fmt.Errorf("encode mail request: %w", err)
	}
	var out MailSent
	err = c.doJSON(http.MethodPost, path, bytes.NewReader(raw), &out)
	return out, err
}

func mailPath(id int64, action string) string {
	p := "/v1/mail/" + strconv.FormatInt(id, 10)
	if action != "" {
		p += "/" + action
	}
	return p
}
