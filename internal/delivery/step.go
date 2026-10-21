package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/util"
)

// Built-in workflow step support (design 2026-10-21 §一·5): the git and platform side of
// the `gofer/push`, `gofer/wait-ci` and `gofer/wait-assets` steps. The steps run inside an
// internal exec job (internal/workflowstep dispatches here), so nothing in this file
// knows about a workflow: it takes a directory, a ref list / an options struct and does
// the git / HTTP work it is asked for.
//
// Everything here is non-interactive and never force-pushes. A failure is returned as an
// error whose text names the ref / workflow / asset that failed, because that text is
// what the workflow step's log and error show.

// StepPollFloor is the shortest interval between two platform polls. A package variable
// so tests can shorten it; the step's own interval comes from its `with` block.
var StepPollFloor = time.Second

// RefResult is one ref pushed and verified against the remote.
type RefResult struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"` // branch | tag
	Remote string `json:"remote"`
	SHA    string `json:"sha"`
}

// PushRefs pushes every ref to remote and verifies the remote now holds the same sha.
// A name that resolves to a local branch is pushed to refs/heads/<name>, a local tag to
// refs/tags/<name>. Nothing is ever forced: a remote ref that moved elsewhere makes git
// reject the push and the rejection is returned as-is. After the push every ref is read
// back with `git ls-remote`, so a "successful" push that did not land (a hook, a mirror)
// is a failure here rather than a silently wrong release.
func PushRefs(ctx context.Context, dir, remote string, refs []string, proxy string) ([]RefResult, error) {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("push: no refs to push")
	}
	kinds := make([]string, len(refs))
	shas := make([]string, len(refs))
	for i, ref := range refs {
		kind, sha, err := localRef(ctx, dir, ref)
		if err != nil {
			return nil, err
		}
		kinds[i], shas[i] = kind, sha
	}
	for i, ref := range refs {
		src := fullRef(kinds[i], ref)
		args := []string{"push", "--porcelain", remote, src + ":" + src}
		if proxy != "" {
			args = append([]string{"-c", "http.proxy=" + proxy}, args...)
		}
		if _, err := GitOut(ctx, dir, args...); err != nil {
			return nil, fmt.Errorf("push %s %s: %w", kinds[i], ref, err)
		}
	}
	remoteRefs, err := lsRemote(ctx, dir, remote)
	if err != nil {
		return nil, err
	}
	out := make([]RefResult, 0, util.CapSum(len(refs), 0))
	for i, ref := range refs {
		full := fullRef(kinds[i], ref)
		got, ok := remoteRefs[full]
		if !ok {
			return nil, fmt.Errorf("push %s %s: the remote does not have %s after the push", kinds[i], ref, full)
		}
		if got != shas[i] {
			return nil, fmt.Errorf("push %s %s: the remote has %s at %s, local is %s (the push did not land)", kinds[i], ref, full, got, shas[i])
		}
		out = append(out, RefResult{Ref: ref, Kind: kinds[i], Remote: remote, SHA: shas[i]})
	}
	return out, nil
}

// fullRef spells a ref name in its full form: a branch under refs/heads/, a tag under
// refs/tags/.
func fullRef(kind, ref string) string {
	if kind == "tag" {
		return "refs/tags/" + ref
	}
	return "refs/heads/" + ref
}

// localRef classifies a ref name against the local repository and returns its sha: a
// branch first (refs/heads), then an annotated or lightweight tag (refs/tags).
func localRef(ctx context.Context, dir, ref string) (kind, sha string, err error) {
	if strings.TrimSpace(ref) == "" {
		return "", "", fmt.Errorf("push: empty ref")
	}
	if strings.ContainsAny(ref, " ~^:?*[\\") || strings.HasPrefix(ref, "-") || strings.Contains(ref, "..") {
		return "", "", fmt.Errorf("push %q: not a valid branch or tag name", ref)
	}
	for _, candidate := range []string{"heads", "tags"} {
		full := "refs/" + candidate + "/" + ref
		if out, gerr := GitOut(ctx, dir, "rev-parse", "--verify", full); gerr == nil && out != "" {
			kind := "branch"
			if candidate == "tags" {
				kind = "tag"
			}
			return kind, out, nil
		}
	}
	return "", "", fmt.Errorf("push %q: no local branch or tag of that name", ref)
}

// lsRemote reads `<remote>` into a map of full ref name → sha.
func lsRemote(ctx context.Context, dir, remote string) (map[string]string, error) {
	out, err := GitOut(ctx, dir, "ls-remote", "--refs", remote)
	if err != nil {
		return nil, fmt.Errorf("ls-remote %s: %w", remote, err)
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		refs[fields[1]] = fields[0]
	}
	return refs, nil
}

// RemoteURL is the URL a remote points at (`git remote get-url`), for parsing the
// platform and repository out of it. An empty remote means origin.
func RemoteURL(ctx context.Context, dir, remote string) (string, error) {
	if strings.TrimSpace(remote) == "" {
		remote = "origin"
	}
	out, err := GitOut(ctx, dir, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("remote %s: %w", remote, err)
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("remote %s has no URL", remote)
	}
	return out, nil
}

// WaitCIOptions is what `gofer/wait-ci` asks for.
type WaitCIOptions struct {
	Ref          string   // branch, tag or sha whose checks to wait for
	Required     []string // check / workflow run names that MUST pass ("" = every check must pass)
	CodeScanning bool     // also fail on code scanning alerts reported for this ref
	TimeoutMin   int      // default 40
	IntervalSec  int      // default 20, never below StepPollFloor
	Remote       string   // "" = origin
	Env          Env      // HTTPBase / Lookup overrides (tests)
}

// WaitCIResult is what the step reports.
type WaitCIResult struct {
	Provider string   `json:"provider"`
	Ref      string   `json:"ref"`
	Checks   []string `json:"checks,omitempty"`
	Alerts   int      `json:"code_scanning_alerts"`
}

// WaitCI polls the platform until every check of ref has finished. It fails as soon as a
// required check fails, and it fails with the checks still running when the timeout
// elapses, so the workflow log names what was waited for.
func WaitCI(ctx context.Context, dir string, o WaitCIOptions) (WaitCIResult, error) {
	c, err := platformClient(ctx, dir, o.Remote, o.Env)
	if err != nil {
		return WaitCIResult{}, err
	}
	res := WaitCIResult{Provider: c.Provider, Ref: o.Ref}
	timeout := time.Duration(o.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 40 * time.Minute
	}
	interval := pollInterval(o.IntervalSec, 20*time.Second)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		done, failure, pending, err := c.checkRuns(ctx, o.Ref, o.Required)
		if err != nil {
			return res, err
		}
		if failure != "" {
			return res, fmt.Errorf("wait-ci ref %s: %s", o.Ref, failure)
		}
		if done {
			if o.CodeScanning {
				alerts, detail, err := c.codeScanningAlerts(ctx, o.Ref)
				if err != nil {
					return res, err
				}
				res.Alerts = alerts
				if alerts > 0 {
					return res, fmt.Errorf("wait-ci ref %s: %d new code scanning alert(s): %s", o.Ref, alerts, detail)
				}
			}
			res.Checks = pending
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("wait-ci ref %s: timed out after %s with %d check(s) still running: %s",
				o.Ref, timeout, len(pending), strings.Join(pending, ", "))
		case <-time.After(interval):
		}
	}
}

// WaitAssetsOptions is what `gofer/wait-assets` asks for.
type WaitAssetsOptions struct {
	Tag         string
	Assets      []string // globs of asset file names
	TimeoutMin  int
	IntervalSec int
	Remote      string
	Env         Env
}

// WaitAssetsResult is what the step reports.
type WaitAssetsResult struct {
	Provider string   `json:"provider"`
	Tag      string   `json:"tag"`
	URL      string   `json:"url,omitempty"`
	Assets   []string `json:"assets"`
}

// WaitAssets polls the release of tag until every glob matches an uploaded asset.
func WaitAssets(ctx context.Context, dir string, o WaitAssetsOptions) (WaitAssetsResult, error) {
	c, err := platformClient(ctx, dir, o.Remote, o.Env)
	if err != nil {
		return WaitAssetsResult{}, err
	}
	res := WaitAssetsResult{Provider: c.Provider, Tag: o.Tag}
	if len(o.Assets) == 0 {
		return res, fmt.Errorf("wait-assets tag %s: no assets to wait for", o.Tag)
	}
	timeout := time.Duration(o.TimeoutMin) * time.Minute
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	interval := pollInterval(o.IntervalSec, 20*time.Second)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		names, webURL, err := c.releaseAssets(ctx, o.Tag)
		if err != nil {
			return res, err
		}
		missing, matched := matchAssets(o.Assets, names)
		res.URL = webURL
		if len(missing) == 0 {
			res.Assets = matched
			return res, nil
		}
		select {
		case <-ctx.Done():
			return res, fmt.Errorf("wait-assets tag %s: timed out after %s; missing %s (release has: %s)",
				o.Tag, timeout, strings.Join(missing, ", "), strings.Join(names, ", "))
		case <-time.After(interval):
		}
	}
}

// matchAssets returns the globs no asset name matches, and the names that did match.
func matchAssets(globs, names []string) (missing, matched []string) {
	for _, g := range globs {
		found := ""
		for _, n := range names {
			if ok, err := path.Match(g, n); err == nil && ok {
				found = n
				break
			}
		}
		if found == "" {
			missing = append(missing, g)
			continue
		}
		matched = append(matched, found)
	}
	return missing, matched
}

// pollInterval turns a configured seconds value into the poll interval, honouring
// StepPollFloor (a zero value means the default).
func pollInterval(sec int, def time.Duration) time.Duration {
	d := time.Duration(sec) * time.Second
	if d <= 0 {
		d = def
	}
	if d < StepPollFloor {
		d = StepPollFloor
	}
	return d
}

// platformClient resolves the repository's remote into a platform client. Only GitHub is
// implemented; another platform returns a "not supported yet" error instead of a
// half-working wait.
func platformClient(ctx context.Context, dir, remote string, env Env) (*Client, error) {
	raw, err := RemoteURL(ctx, dir, remote)
	if err != nil {
		return nil, err
	}
	r, err := ParseRemote(raw)
	if err != nil {
		return nil, err
	}
	provider := DetectProvider(r.Host, "")
	switch provider {
	case ProviderGitHub:
	case ProviderGitLab, ProviderGitea:
		return nil, fmt.Errorf("%s: 暂不支持（内置步骤目前只支持 GitHub）", provider)
	default:
		return nil, fmt.Errorf("cannot tell the platform of %s (want a GitHub remote)", raw)
	}
	token := ResolveToken(ctx, provider, "", env)
	if token == "" {
		return nil, fmt.Errorf("%s %s: no API token (set GITHUB_TOKEN or GH_TOKEN on the machine running the step)", provider, r.Path)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Client{Provider: provider, Remote: r, Token: token, APIBase: env.HTTPBase, HTTP: hc}, nil
}

type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}

// checkRuns reads the latest check runs of ref. done is true once every run finished (or
// there is nothing to wait for); failure describes the first refusal; pending lists the
// runs still in flight (the whole finished set when done).
func (c *Client) checkRuns(ctx context.Context, ref string, required []string) (done bool, failure string, pending []string, err error) {
	var out struct {
		CheckRuns []checkRun `json:"check_runs"`
	}
	q := url.Values{"filter": {"latest"}, "per_page": {"100"}}
	if err := c.do(ctx, http.MethodGet, "/repos/"+c.Remote.Path+"/commits/"+url.PathEscape(ref)+"/check-runs?"+q.Encode(), nil, &out); err != nil {
		return false, "", nil, fmt.Errorf("check runs of %s: %w", ref, err)
	}
	for _, r := range out.CheckRuns {
		if r.Status != "completed" {
			pending = append(pending, r.Name+" ("+r.Status+")")
		}
	}
	if len(pending) > 0 {
		return false, "", pending, nil
	}
	byName := make(map[string]checkRun, len(out.CheckRuns))
	for _, r := range out.CheckRuns {
		byName[r.Name] = r
	}
	names := make([]string, 0, len(out.CheckRuns))
	for _, r := range out.CheckRuns {
		names = append(names, r.Name+"="+r.Conclusion)
	}
	if len(required) > 0 {
		for _, want := range required {
			r, ok := byName[want]
			if !ok {
				return false, fmt.Sprintf("required check %q did not run (checks seen: %s)", want, strings.Join(names, ", ")), nil, nil
			}
			if !conclusionOK(r.Conclusion) {
				return false, fmt.Sprintf("required check %q ended %s", want, r.Conclusion), nil, nil
			}
		}
		return true, "", names, nil
	}
	for _, r := range out.CheckRuns {
		if !conclusionOK(r.Conclusion) {
			return false, fmt.Sprintf("check %q ended %s", r.Name, r.Conclusion), nil, nil
		}
	}
	return true, "", names, nil
}

// conclusionOK reports whether a finished check counts as a pass: success, neutral and
// skipped are acceptable; anything else (failure, cancelled, timed_out, action_required,
// a missing conclusion) is not.
func conclusionOK(conclusion string) bool {
	switch conclusion {
	case "success", "neutral", "skipped":
		return true
	}
	return false
}

// codeScanningAlerts counts the OPEN code scanning alerts GitHub reports FOR ref, and
// returns a short listing of the first few. The ref filter is the API's own: alerts are
// reported per analysed ref, so this is "alerts on the released / verified ref", not a
// diff against an earlier baseline (see the step docs).
func (c *Client) codeScanningAlerts(ctx context.Context, ref string) (int, string, error) {
	q := url.Values{"state": {"open"}, "ref": {ref}, "per_page": {"100"}}
	var out []struct {
		Rule struct {
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"rule"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.do(ctx, http.MethodGet, "/repos/"+c.Remote.Path+"/code-scanning/alerts?"+q.Encode(), nil, &out); err != nil {
		return 0, "", fmt.Errorf("code scanning alerts of %s: %w", ref, err)
	}
	details := make([]string, 0, len(out))
	for i, a := range out {
		if i >= 5 {
			details = append(details, fmt.Sprintf("… and %d more", len(out)-5))
			break
		}
		rule := a.Rule.ID
		if rule == "" {
			rule = a.Rule.Description
		}
		details = append(details, rule+" "+a.HTMLURL)
	}
	return len(out), strings.Join(details, "; "), nil
}

// releaseAssets lists the asset names of tag's release (and the release page URL).
func (c *Client) releaseAssets(ctx context.Context, tag string) ([]string, string, error) {
	var out struct {
		HTMLURL string `json:"html_url"`
		Assets  []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	err := c.do(ctx, http.MethodGet, "/repos/"+c.Remote.Path+"/releases/tags/"+url.PathEscape(tag), nil, &out)
	if err != nil {
		// A release that does not exist yet is not an error while waiting: the step polls
		// until its timeout and reports the state then.
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("release %s: %w", tag, err)
	}
	names := make([]string, 0, len(out.Assets))
	for _, a := range out.Assets {
		names = append(names, a.Name)
	}
	return names, out.HTMLURL, nil
}
