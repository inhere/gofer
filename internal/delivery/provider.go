package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Providers.
const (
	ProviderAuto   = "auto"
	ProviderGitHub = "github"
	ProviderGitLab = "gitlab"
	ProviderGitea  = "gitea" // Gitea and Forgejo share the API
)

// Remote is a parsed git remote URL.
type Remote struct {
	Host string // host[:port] of the web / API endpoint
	Path string // owner/repo (GitLab: group/sub/repo), without .git
}

// ParseRemote reads https://host/owner/repo(.git), ssh://git@host[:port]/owner/repo
// and scp-style git@host:owner/repo. An ssh port is dropped (the API is on https).
func ParseRemote(raw string) (Remote, error) {
	raw = strings.TrimSpace(raw)
	var host, path string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(raw)
		if err != nil {
			return Remote{}, err
		}
		host, path = u.Host, u.Path
		if u.Scheme == "ssh" || u.Scheme == "git" {
			host = u.Hostname()
		}
	case strings.Contains(raw, ":"):
		at := strings.LastIndex(raw[:strings.Index(raw, ":")], "@")
		hp := raw[at+1:]
		i := strings.Index(hp, ":")
		host, path = hp[:i], hp[i+1:]
	default:
		return Remote{}, fmt.Errorf("remote %q is not a URL", raw)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || !strings.Contains(path, "/") {
		return Remote{}, fmt.Errorf("remote %q has no owner/repo path", raw)
	}
	return Remote{Host: host, Path: path}, nil
}

// DetectProvider names the platform of host: github.com and gitlab.com by name, a
// self-hosted instance when its host says so (gitlab.*, gitea.*, forgejo.*,
// codeberg.org); "" when it cannot tell (set provider explicitly).
func DetectProvider(host, override string) string {
	if o := strings.ToLower(strings.TrimSpace(override)); o != "" && o != ProviderAuto {
		if o == "forgejo" {
			return ProviderGitea
		}
		return o
	}
	h := strings.ToLower(host)
	switch {
	case h == "github.com" || strings.HasSuffix(h, ".github.com"):
		return ProviderGitHub
	case strings.Contains(h, "gitlab"):
		return ProviderGitLab
	case strings.Contains(h, "gitea") || strings.Contains(h, "forgejo") || h == "codeberg.org":
		return ProviderGitea
	}
	return ""
}

// PRRequest is what a pull / merge request is opened with.
type PRRequest struct {
	Title  string
	Body   string
	Head   string // the pushed branch
	Base   string // target branch
	Draft  bool
	Labels []string
}

// PR is an opened (or already open) request.
type PR struct {
	URL     string
	Number  int
	Existed bool // an open request for the same head was found and reused
}

// Client opens requests on one platform.
type Client struct {
	Provider string
	Remote   Remote
	Token    string
	HTTP     *http.Client
	// APIBase overrides the derived API root (tests, unusual installs).
	APIBase string
}

func (c *Client) apiBase() string {
	if c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/")
	}
	switch c.Provider {
	case ProviderGitHub:
		if strings.EqualFold(c.Remote.Host, "github.com") {
			return "https://api.github.com"
		}
		return "https://" + c.Remote.Host + "/api/v3"
	case ProviderGitLab:
		return "https://" + c.Remote.Host + "/api/v4"
	case ProviderGitea:
		return "https://" + c.Remote.Host + "/api/v1"
	}
	return ""
}

// CompareURL is the platform's web page for opening a request by hand ("" unknown).
func CompareURL(provider string, r Remote, head, base string) string {
	switch provider {
	case ProviderGitHub:
		return fmt.Sprintf("https://%s/%s/compare/%s...%s?expand=1", r.Host, r.Path, branchPath(base), branchPath(head))
	case ProviderGitLab:
		return fmt.Sprintf("https://%s/%s/-/merge_requests/new?merge_request[source_branch]=%s&merge_request[target_branch]=%s",
			r.Host, r.Path, url.QueryEscape(head), url.QueryEscape(base))
	case ProviderGitea:
		return fmt.Sprintf("https://%s/%s/compare/%s...%s", r.Host, r.Path, branchPath(base), branchPath(head))
	}
	return ""
}

// branchPath escapes a branch name for a URL path, keeping its "/" separators.
func branchPath(b string) string { return strings.ReplaceAll(url.PathEscape(b), "%2F", "/") }

// Open opens the request, or returns the one already open for the same head (a
// retried delivery must not open a second one).
func (c *Client) Open(ctx context.Context, req PRRequest) (PR, error) {
	if pr, ok, err := c.findOpen(ctx, req); err == nil && ok {
		pr.Existed = true
		return pr, nil
	}
	switch c.Provider {
	case ProviderGitHub:
		var out struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		body := map[string]any{"title": req.Title, "head": req.Head, "base": req.Base, "body": req.Body, "draft": req.Draft}
		if err := c.do(ctx, http.MethodPost, "/repos/"+c.Remote.Path+"/pulls", body, &out); err != nil {
			return PR{}, err
		}
		if len(req.Labels) > 0 {
			// Best effort: a missing label permission must not fail the delivery.
			_ = c.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", c.Remote.Path, out.Number),
				map[string]any{"labels": req.Labels}, nil)
		}
		return PR{URL: out.HTMLURL, Number: out.Number}, nil
	case ProviderGitLab:
		var out struct {
			WebURL string `json:"web_url"`
			IID    int    `json:"iid"`
		}
		title := req.Title
		if req.Draft {
			title = "Draft: " + title
		}
		body := map[string]any{"source_branch": req.Head, "target_branch": req.Base, "title": title, "description": req.Body}
		if len(req.Labels) > 0 {
			body["labels"] = strings.Join(req.Labels, ",")
		}
		if err := c.do(ctx, http.MethodPost, "/projects/"+url.PathEscape(c.Remote.Path)+"/merge_requests", body, &out); err != nil {
			return PR{}, err
		}
		return PR{URL: out.WebURL, Number: out.IID}, nil
	case ProviderGitea:
		var out struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		title := req.Title
		if req.Draft {
			title = "WIP: " + title
		}
		body := map[string]any{"head": req.Head, "base": req.Base, "title": title, "body": req.Body}
		if err := c.do(ctx, http.MethodPost, "/repos/"+c.Remote.Path+"/pulls", body, &out); err != nil {
			return PR{}, err
		}
		return PR{URL: out.HTMLURL, Number: out.Number}, nil
	}
	return PR{}, fmt.Errorf("unsupported provider %q", c.Provider)
}

func (c *Client) findOpen(ctx context.Context, req PRRequest) (PR, bool, error) {
	switch c.Provider {
	case ProviderGitHub:
		owner, _, _ := strings.Cut(c.Remote.Path, "/")
		var out []struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		q := url.Values{"state": {"open"}, "head": {owner + ":" + req.Head}}
		if err := c.do(ctx, http.MethodGet, "/repos/"+c.Remote.Path+"/pulls?"+q.Encode(), nil, &out); err != nil || len(out) == 0 {
			return PR{}, false, err
		}
		return PR{URL: out[0].HTMLURL, Number: out[0].Number}, true, nil
	case ProviderGitLab:
		var out []struct {
			WebURL string `json:"web_url"`
			IID    int    `json:"iid"`
		}
		q := url.Values{"state": {"opened"}, "source_branch": {req.Head}}
		if err := c.do(ctx, http.MethodGet, "/projects/"+url.PathEscape(c.Remote.Path)+"/merge_requests?"+q.Encode(), nil, &out); err != nil || len(out) == 0 {
			return PR{}, false, err
		}
		return PR{URL: out[0].WebURL, Number: out[0].IID}, true, nil
	case ProviderGitea:
		var out []struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
			Head    struct {
				Ref string `json:"ref"`
			} `json:"head"`
		}
		if err := c.do(ctx, http.MethodGet, "/repos/"+c.Remote.Path+"/pulls?state=open&limit=50", nil, &out); err != nil {
			return PR{}, false, err
		}
		for _, p := range out {
			if p.Head.Ref == req.Head {
				return PR{URL: p.HTMLURL, Number: p.Number}, true, nil
			}
		}
	}
	return PR{}, false, nil
}

// APIError is a non-2xx answer from the platform (Body trimmed).
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body) }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	base := c.apiBase()
	if base == "" {
		return errors.New("no API endpoint for provider " + c.Provider)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	switch c.Provider {
	case ProviderGitHub:
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	case ProviderGitLab:
		req.Header.Set("PRIVATE-TOKEN", c.Token)
	case ProviderGitea:
		req.Header.Set("Authorization", "token "+c.Token)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
		return &APIError{Status: resp.StatusCode, Body: msg}
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}
