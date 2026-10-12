package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/procattr"
)

// Modes.
const (
	ModeNone  = "none"
	ModeMerge = "merge"
	ModePush  = "push"
	ModePR    = "pr"
)

// States of a delivery.
const (
	StatePending  = "pending"
	StateMerged   = "merged"
	StatePushed   = "pushed"
	StatePROpened = "pr_opened"
	StateFailed   = "failed"
	StateSkipped  = "skipped"
)

// ValidMode reports whether m is a delivery mode ("" = unset).
func ValidMode(m string) bool {
	switch m {
	case "", ModeNone, ModeMerge, ModePush, ModePR:
		return true
	}
	return false
}

// Plan is one delivery, self-contained so it can be carried to the machine that
// owns the worktree (an internal exec job reads it as JSON on stdin).
type Plan struct {
	Mode         string   `json:"mode"`
	JobID        string   `json:"job_id"`
	Worktree     string   `json:"worktree"`      // the job's worktree (absolute, on the executing machine)
	Branch       string   `json:"branch"`        // the local branch (gofer/<job>)
	RemoteBranch string   `json:"remote_branch"` // pushed as
	Remote       string   `json:"remote"`
	Base         string   `json:"base,omitempty"` // "" = the main checkout's branch
	AllowPush    bool     `json:"allow_push"`
	PushBase     bool     `json:"push_base,omitempty"`
	Squash       bool     `json:"squash,omitempty"`
	Cleanup      bool     `json:"cleanup,omitempty"`
	Draft        bool     `json:"draft,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Provider     string   `json:"provider,omitempty"`  // auto | github | gitlab | gitea
	TokenEnv     string   `json:"token_env,omitempty"` // variable NAME; "" = the provider's defaults
	Title        string   `json:"title"`
	Body         string   `json:"body"`
}

// Result is what a delivery did.
type Result struct {
	Mode       string `json:"mode"`
	State      string `json:"state"`
	Branch     string `json:"branch,omitempty"`
	Remote     string `json:"remote,omitempty"`
	Base       string `json:"base,omitempty"`
	Provider   string `json:"provider,omitempty"`
	PRURL      string `json:"pr_url,omitempty"`
	PRNumber   int    `json:"pr_number,omitempty"`
	CompareURL string `json:"compare_url,omitempty"`
	Note       string `json:"note,omitempty"`
	Error      string `json:"error,omitempty"`
	At         int64  `json:"at"`
}

// Env lets the caller supply the environment and the CLI token helpers (tests).
type Env struct {
	Lookup func(string) (string, bool)
	// RunTokenCLI runs a token helper (`gh auth token`); nil = exec it.
	RunTokenCLI func(ctx context.Context, name string, args ...string) (string, error)
	HTTPBase    string // API base override (tests)
}

// Execute performs plan. It never force-pushes; any failure is a Result with
// State failed and the reason (the error return is for programming errors only).
func Execute(ctx context.Context, p Plan, env Env) Result {
	res := Result{Mode: p.Mode, Remote: p.Remote, Base: p.Base, At: time.Now().Unix()}
	fail := func(err error) Result {
		res.State, res.Error = StateFailed, err.Error()
		return res
	}
	if p.Remote == "" {
		p.Remote = "origin"
		res.Remote = p.Remote
	}
	switch p.Mode {
	case ModeMerge:
		_, base, err := MergeBranch(ctx, p.Worktree, p.Branch, p.Squash)
		res.Base = base
		if err != nil {
			return fail(err)
		}
		res.State = StateMerged
		if p.PushBase {
			if !p.AllowPush {
				res.Note = "merged locally; push_base needs delivery.allow_push"
			} else if main, err := MainCheckout(ctx, p.Worktree); err != nil {
				return fail(err)
			} else if err := Push(ctx, main, p.Remote, base, base); err != nil {
				return fail(fmt.Errorf("merged locally, pushing %s failed: %w", base, err))
			} else {
				res.Note = "merged and pushed " + base
			}
		}
		return cleanup(ctx, p, res, true)
	case ModePush, ModePR:
		if !p.AllowPush {
			return fail(errors.New("pushing is not allowed for this project (set projects.<key>.delivery.allow_push: true)"))
		}
		if p.Base == "" {
			_, base, err := BaseBranch(ctx, p.Worktree)
			if err != nil {
				return fail(err)
			}
			p.Base = base
		}
		res.Base, res.Branch = p.Base, p.RemoteBranch
		// The configured URL as written (get-url would apply insteadOf rewrites, and the
		// platform is told by the address the operator configured).
		rawURL, err := GitOut(ctx, p.Worktree, "config", "--get", "remote."+p.Remote+".url")
		if err != nil {
			return fail(err)
		}
		if err := Push(ctx, p.Worktree, p.Remote, p.Branch, p.RemoteBranch); err != nil {
			return fail(err)
		}
		res.State = StatePushed
		remote, perr := ParseRemote(rawURL)
		provider := ""
		if perr == nil {
			provider = DetectProvider(remote.Host, p.Provider)
			res.Provider = provider
			res.CompareURL = CompareURL(provider, remote, p.RemoteBranch, p.Base)
		}
		if p.Mode == ModePush {
			return cleanup(ctx, p, res, false)
		}
		if perr != nil || provider == "" {
			res.Note = "branch pushed; the platform of this remote is not known (set delivery.provider) — open the request by hand"
			return cleanup(ctx, p, res, false)
		}
		token := ResolveToken(ctx, provider, p.TokenEnv, env)
		if token == "" {
			res.Note = "branch pushed; no " + provider + " token on this machine (" + strings.Join(TokenVars(provider, p.TokenEnv), " / ") + ") — open the request by hand"
			return cleanup(ctx, p, res, false)
		}
		c := &Client{Provider: provider, Remote: remote, Token: token, APIBase: env.HTTPBase}
		pr, err := c.Open(ctx, PRRequest{Title: p.Title, Body: p.Body, Head: p.RemoteBranch, Base: p.Base, Draft: p.Draft, Labels: p.Labels})
		if err != nil {
			return fail(fmt.Errorf("branch pushed, opening the request failed: %w", err))
		}
		res.State, res.PRURL, res.PRNumber = StatePROpened, pr.URL, pr.Number
		if pr.Existed {
			res.Note = "an open request for this branch already existed"
		}
		return cleanup(ctx, p, res, false)
	}
	return fail(fmt.Errorf("unknown delivery mode %q", p.Mode))
}

// cleanup removes the worktree after a successful delivery when asked; a merged
// branch goes too, a pushed one stays (later fixes push to it). Best effort.
func cleanup(ctx context.Context, p Plan, res Result, merged bool) Result {
	if !p.Cleanup || res.State == StateFailed {
		return res
	}
	main, err := MainCheckout(ctx, p.Worktree)
	if err != nil {
		return res
	}
	if _, err := GitOut(ctx, main, "worktree", "remove", "--force", p.Worktree); err != nil {
		res.Note = strings.TrimSpace(res.Note + "; worktree not removed: " + err.Error())
		return res
	}
	if merged {
		_, _ = GitOut(ctx, main, "branch", "-D", p.Branch)
	}
	return res
}

// TokenVars are the environment variables a provider's token is read from.
func TokenVars(provider, override string) []string {
	if strings.TrimSpace(override) != "" {
		return []string{override}
	}
	switch provider {
	case ProviderGitHub:
		return []string{"GITHUB_TOKEN", "GH_TOKEN"}
	case ProviderGitLab:
		return []string{"GITLAB_TOKEN"}
	case ProviderGitea:
		return []string{"GITEA_TOKEN", "FORGEJO_TOKEN"}
	}
	return nil
}

// ResolveToken reads the platform token on this machine: the configured / default
// variables, else the logged-in CLI (`gh auth token`, `glab auth token`).
func ResolveToken(ctx context.Context, provider, override string, env Env) string {
	lookup := env.Lookup
	if lookup == nil {
		lookup = os.LookupEnv
	}
	for _, name := range TokenVars(provider, override) {
		if v, ok := lookup(name); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if strings.TrimSpace(override) != "" {
		return ""
	}
	run := env.RunTokenCLI
	if run == nil {
		run = runTokenCLI
	}
	var name string
	switch provider {
	case ProviderGitHub:
		name = "gh"
	case ProviderGitLab:
		name = "glab"
	default:
		return ""
	}
	out, err := run(ctx, name, "auth", "token")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func runTokenCLI(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	procattr.Background(cmd)
	out, err := cmd.Output()
	return string(out), err
}
