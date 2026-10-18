package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
)

// The hook's mail take (POST /v1/sessions/{sid}/mail/claim|confirm and
// /turns/{id}/complete-mail): claim, confirm, then inject only what was confirmed.

// MailTake bounds one take; zero values take the server defaults (5 letters / 8000 characters).
type MailTake struct {
	Limit      int  `json:"limit,omitempty"`
	MaxChars   int  `json:"max_chars,omitempty"`
	UrgentOnly bool `json:"urgent_only,omitempty"`
}

// TakenMail is one letter a hook took, with what it renders into <gofer-mail>.
type TakenMail struct {
	ID        int64     `json:"id"`
	ThreadID  string    `json:"thread_id"`
	From      MailParty `json:"from"`
	Topic     string    `json:"topic,omitempty"`
	Urgent    bool      `json:"urgent,omitempty"`
	Body      string    `json:"body"`
	Truncated bool      `json:"truncated,omitempty"`
	CreatedAt int64     `json:"created_at"`
}

// MailClaim is the answer of a claim: the claimed letters and how many eligible ones remain.
type MailClaim struct {
	Mails     []TakenMail `json:"mails"`
	Remaining int         `json:"remaining"`
}

// ClaimSessionMail claims the session's letters for injection (pending → delivering, with a lease).
func (c *Client) ClaimSessionMail(sid string, take MailTake) (MailClaim, error) {
	body, err := json.Marshal(take)
	if err != nil {
		return MailClaim{}, err
	}
	var out MailClaim
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/mail/claim", bytes.NewReader(body), &out)
	return out, err
}

// ConfirmSessionMail confirms claimed letters (delivering → delivered) and returns the ids the
// server confirmed; only those may be injected.
func (c *Client) ConfirmSessionMail(sid string, ids []int64) ([]int64, error) {
	body, err := json.Marshal(map[string][]int64{"ids": ids})
	if err != nil {
		return nil, err
	}
	var out struct {
		Confirmed []int64 `json:"confirmed"`
	}
	err = c.doJSON(http.MethodPost, "/v1/sessions/"+url.PathEscape(sid)+"/mail/confirm", bytes.NewReader(body), &out)
	return out.Confirmed, err
}

// CompleteSessionMailTurn closes the open relay turn and takes the letters in one server
// operation (the Stop delivery). completed=false: a web answer won, or nothing was pending.
func (c *Client) CompleteSessionMailTurn(sid, turnID string, take MailTake) (bool, []TakenMail, error) {
	body, err := json.Marshal(take)
	if err != nil {
		return false, nil, err
	}
	var out struct {
		Completed bool        `json:"completed"`
		Mails     []TakenMail `json:"mails"`
	}
	path := "/v1/sessions/" + url.PathEscape(sid) + "/turns/" + url.PathEscape(turnID) + "/complete-mail"
	err = c.doJSON(http.MethodPost, path, bytes.NewReader(body), &out)
	return out.Completed, out.Mails, err
}
