package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

// retryTestSec is a fixed instant (2025-09-22 00:13:20 UTC) used by the formatter
// tables; the server zone is pinned to UTC in those tests, so the rendered stamp is
// independent of the host's own timezone.
const retryTestSec int64 = 1758500000

// TestParseRetryFlag: R2/AUTO-03 的 --retry 文法只有两种形态——`<n>`（总尝试次数，含首次，
// 用内置退避表）与 `<n>:<b1,b2,...>`（同一含义 + 显式退避表）。所有非法输入都必须报错并指明
// 是哪个 flag，绝不能"猜一个"：一个写错的退避表会变成 N 次真实重投。
func TestParseRetryFlag(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want *job.RetryPolicy
	}{
		{name: "attempts only uses the built-in table", in: "3", want: &job.RetryPolicy{MaxAttempts: 3}},
		{name: "explicit table", in: "3:60,300", want: &job.RetryPolicy{MaxAttempts: 3, BackoffSec: []int{60, 300}}},
		{name: "single-step table", in: "2:5", want: &job.RetryPolicy{MaxAttempts: 2, BackoffSec: []int{5}}},
		{name: "zero backoff is legal", in: "2:0", want: &job.RetryPolicy{MaxAttempts: 2, BackoffSec: []int{0}}},
		{name: "surrounding spaces tolerated", in: " 2 : 5 , 10 ", want: &job.RetryPolicy{MaxAttempts: 2, BackoffSec: []int{5, 10}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRetryFlag(tc.in)
			if err != nil {
				t.Fatalf("parseRetryFlag(%q) error = %v, want a policy", tc.in, err)
			}
			if got.MaxAttempts != tc.want.MaxAttempts {
				t.Fatalf("parseRetryFlag(%q).MaxAttempts = %d, want %d", tc.in, got.MaxAttempts, tc.want.MaxAttempts)
			}
			if len(got.BackoffSec) != len(tc.want.BackoffSec) {
				t.Fatalf("parseRetryFlag(%q).BackoffSec = %v, want %v", tc.in, got.BackoffSec, tc.want.BackoffSec)
			}
			for i, sec := range tc.want.BackoffSec {
				if got.BackoffSec[i] != sec {
					t.Fatalf("parseRetryFlag(%q).BackoffSec = %v, want %v", tc.in, got.BackoffSec, tc.want.BackoffSec)
				}
			}
			if len(got.OnExitCodes) != 0 {
				t.Fatalf("parseRetryFlag(%q).OnExitCodes = %v, want none from --retry", tc.in, got.OnExitCodes)
			}
		})
	}

	bad := []string{"", "  ", "0", "abc", "3:x", "3:-1", "3:", "3:60,", "-2", "3:60;300"}
	for _, in := range bad {
		t.Run("invalid "+in, func(t *testing.T) {
			got, err := parseRetryFlag(in)
			if err == nil {
				t.Fatalf("parseRetryFlag(%q) = %+v, want an error", in, got)
			}
			if !strings.Contains(err.Error(), "--retry") {
				t.Fatalf("parseRetryFlag(%q) error %q must name the flag", in, err.Error())
			}
		})
	}
}

// TestRetryPolicyFromFlags: 三个 flag 的三态映射。什么都不给 = nil（server 按
// request > project > agent > server 解析）；--no-retry = 显式 max_attempts:1（关掉配置层
// 打开的重试）；--retry 才是本 job 的策略。互相矛盾的组合必须报错——让"后绑定的赢"静默发生，
// 就等于让人以为自己关掉了重试而其实没有。
func TestRetryPolicyFromFlags(t *testing.T) {
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	jobRunOpts = jobRunFlags{}
	if p, err := retryPolicyFromFlags(); err != nil || p != nil {
		t.Fatalf("no flags: policy=%+v err=%v, want nil/nil (let the config layers decide)", p, err)
	}

	jobRunOpts = jobRunFlags{noRetry: true}
	p, err := retryPolicyFromFlags()
	if err != nil || p == nil || p.MaxAttempts != 1 {
		t.Fatalf("--no-retry: policy=%+v err=%v, want an explicit max_attempts:1", p, err)
	}

	jobRunOpts = jobRunFlags{retry: "3:60,300", retryOn: "7,9"}
	p, err = retryPolicyFromFlags()
	if err != nil {
		t.Fatalf("--retry with --retry-on: %v", err)
	}
	if p == nil || p.MaxAttempts != 3 || len(p.BackoffSec) != 2 || len(p.OnExitCodes) != 2 || p.OnExitCodes[0] != 7 || p.OnExitCodes[1] != 9 {
		t.Fatalf("--retry 3:60,300 --retry-on 7,9 -> %+v", p)
	}

	jobRunOpts = jobRunFlags{noRetry: true, retry: "3"}
	if _, err := retryPolicyFromFlags(); err == nil {
		t.Fatal("--no-retry with --retry must be refused, got no error")
	}

	// --retry-on alone cannot express an attempt budget, and a request-level policy
	// replaces the config layers wholesale — so it has nothing to narrow.
	jobRunOpts = jobRunFlags{retryOn: "7"}
	if _, err := retryPolicyFromFlags(); err == nil {
		t.Fatal("--retry-on without --retry must be refused, got no error")
	}

	jobRunOpts = jobRunFlags{retry: "0"}
	if _, err := retryPolicyFromFlags(); err == nil {
		t.Fatal("--retry 0 must be refused, got no error")
	}
}

// TestJobRunRetryFlags proves the flags reach the SUBMITTED request (a flag that is
// registered but never mapped is the failure this catches), through the same app.Run
// path the other job-run flag tests use.
func TestJobRunRetryFlags(t *testing.T) {
	jobRunOpts = jobRunFlags{}
	t.Cleanup(func() { jobRunOpts = jobRunFlags{} })

	app := NewApp("test")
	var got job.JobRequest
	var buildErr error
	runCmd := app.GetCommand("job").GetCommand("run")
	runCmd.Func = func(c *gcli.Command, _ []string) error {
		got, buildErr = buildJobRunRequest(c, nil)
		return buildErr
	}

	runArgs := func(flags ...string) int {
		jobRunOpts = jobRunFlags{}
		buildErr, got = nil, job.JobRequest{}
		args := append([]string{"job", "run", "-p", "self", "-a", "exec"}, flags...)
		return app.Run(append(args, "--", "go", "version"))
	}

	if code := runArgs("--retry", "3:60,300", "--retry-on", "7,9"); code != 0 {
		t.Fatalf("app.Run exit code=%d (err=%v)", code, buildErr)
	}
	if got.Retry == nil || got.Retry.MaxAttempts != 3 || len(got.Retry.BackoffSec) != 2 || len(got.Retry.OnExitCodes) != 2 {
		t.Fatalf("--retry 3:60,300 --retry-on 7,9 -> Retry=%+v", got.Retry)
	}

	if code := runArgs(); code != 0 {
		t.Fatalf("app.Run exit code=%d (err=%v)", code, buildErr)
	}
	if got.Retry != nil {
		t.Fatalf("no retry flag must leave Retry nil, got %+v", got.Retry)
	}

	if code := runArgs("--no-retry"); code != 0 {
		t.Fatalf("app.Run exit code=%d (err=%v)", code, buildErr)
	}
	if got.Retry == nil || got.Retry.MaxAttempts != 1 {
		t.Fatalf("--no-retry must reach the request as max_attempts:1, got %+v", got.Retry)
	}

	if code := runArgs("--no-retry", "--retry", "3"); code == 0 {
		t.Fatalf("--no-retry with --retry must be refused, got exit code 0 (Retry=%+v)", got.Retry)
	}
}

// TestFormatRetries: `job show` 的那一行只关心"还会不会自己再跑、什么时候"。terminal 行
// （done/cancelled）是历史不是待办，单独一条都不打印；多条待发取 attempt 最小的那条并提示
// 还有几条。max_attempts 为 0 的行（没有策略）降级成裸 attempt——不编造分母。
func TestFormatRetries(t *testing.T) {
	t.Cleanup(func() { setServerTZ(0, false) })
	setServerTZ(0, true) // the assertion must not depend on the host's zone

	next := fmtServerTime(retryTestSec)
	cases := []struct {
		name string
		rows []client.Retry
		want string
	}{
		{name: "no rows", rows: nil, want: ""},
		{
			name: "terminal rows are history",
			rows: []client.Retry{
				{ID: "rt-1", Attempt: 2, MaxAttempts: 3, State: jobstore.RetryDone, NewJobID: "job-b"},
				{ID: "rt-2", Attempt: 3, MaxAttempts: 3, State: jobstore.RetryCancelled},
			},
			want: "",
		},
		{
			name: "pending with ceiling",
			rows: []client.Retry{{ID: "rt-1", Attempt: 2, MaxAttempts: 3, State: jobstore.RetryPending, NextRunAt: retryTestSec}},
			want: "attempt 2/3, next at " + next,
		},
		{
			name: "claimed is still waiting",
			rows: []client.Retry{{ID: "rt-1", Attempt: 2, MaxAttempts: 3, State: jobstore.RetryClaimed, NextRunAt: retryTestSec}},
			want: "attempt 2/3, next at " + next,
		},
		{
			name: "row without a policy prints no ceiling",
			rows: []client.Retry{{ID: "rt-1", Attempt: 4, State: jobstore.RetryPending, NextRunAt: retryTestSec}},
			want: "attempt 4, next at " + next,
		},
		{
			name: "lowest attempt wins and the rest are counted",
			rows: []client.Retry{
				{ID: "rt-2", Attempt: 3, MaxAttempts: 4, State: jobstore.RetryPending, NextRunAt: retryTestSec},
				{ID: "rt-done", Attempt: 2, MaxAttempts: 4, State: jobstore.RetryDone, NewJobID: "job-b"},
				{ID: "rt-1", Attempt: 2, MaxAttempts: 4, State: jobstore.RetryPending, NextRunAt: retryTestSec},
			},
			want: "attempt 2/4, next at " + next + " (+1 more)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRetries(tc.rows); got != tc.want {
				t.Fatalf("formatRetries() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJobShowPrintsRetry: `job show` 多出的那一行是"这个 job 还会自己再跑一次吗"的唯一
// 可视信号，所以它必须真的打出来（并且打在 retry: 标签上，宽度与其他行对齐）。多个待发时只
// 报最小的 attempt，其余条数在括号里。
func TestJobShowPrintsRetry(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	t.Cleanup(func() { setServerTZ(0, false) })
	setServerTZ(0, true)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/jobs/job-rt/wakeups":
			_, _ = w.Write([]byte(`{"wakeups":[]}`))
			return
		case "/v1/jobs/job-rt/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-rt","retries":[
				{"id":"rt-2","attempt":3,"max_attempts":3,"state":"pending","reason":"exit_code=7","next_run_at":1758500060},
				{"id":"rt-1","attempt":2,"max_attempts":3,"state":"pending","reason":"exit_code=7","next_run_at":1758500000}
			]}`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v1/jobs/job-rt" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job.JobResult{
			ID: "job-rt", ProjectKey: "self", Status: job.StatusFailed, ExitCode: 7,
		})
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "show", "job-rt", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	want := "retry:      attempt 2/3, next at " + fmtServerTime(retryTestSec) + " (+1 more)"
	if !strings.Contains(out, want) {
		t.Fatalf("job show output missing %q:\n%s", want, out)
	}
}

// TestJobRetryListAndCancel: `job retry ls` 的两种回答（有链 / 没链）与 `job retry
// cancel` 的确认。取消打的是 server 收到的那条 id——不是本地拼的"已取消"。
func TestJobRetryListAndCancel(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	jobConnOpts.server, jobConnOpts.token = "", ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	t.Cleanup(func() { setServerTZ(0, false) })
	setServerTZ(0, true)

	var cancelled string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-rt/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-rt","retries":[
				{"id":"rt-1","attempt":2,"max_attempts":3,"state":"pending","reason":"exit_code=7","next_run_at":1758500000}
			]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/jobs/job-none/retries":
			_, _ = w.Write([]byte(`{"job_id":"job-none","retries":[]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/retries/rt-1":
			cancelled = "rt-1"
			_, _ = w.Write([]byte(`{"status":"cancelled"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer ts.Close()
	jobConnOpts.server = ts.URL

	out := captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "retry", "ls", "job-rt", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	want := "rt-1  attempt 2/3  pending   exit_code=7  next " + fmtServerTime(retryTestSec)
	if !strings.Contains(out, want) {
		t.Fatalf("job retry ls output missing %q:\n%s", want, out)
	}

	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "retry", "ls", "job-none", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if !strings.Contains(out, "job job-none has no retries") {
		t.Fatalf("a job without retries must say so:\n%s", out)
	}

	out = captureOutput(t, func() {
		if code := NewApp("test").Run([]string{"job", "retry", "cancel", "rt-1", "--server", ts.URL}); code != 0 {
			t.Fatalf("app.Run exit code=%d", code)
		}
	})
	if cancelled != "rt-1" {
		t.Fatalf("cancel did not reach the server for rt-1 (cancelled=%q)", cancelled)
	}
	if !strings.Contains(out, "retry rt-1 cancelled") {
		t.Fatalf("job retry cancel output = %q, want a confirmation", out)
	}
}

func TestFormatRetryLine(t *testing.T) {
	t.Cleanup(func() { setServerTZ(0, false) })
	setServerTZ(0, true)

	cases := []struct {
		name string
		row  client.Retry
		want string
	}{
		{
			name: "pending",
			row:  client.Retry{ID: "rt-1a2b", Attempt: 2, MaxAttempts: 3, State: jobstore.RetryPending, Reason: "exit_code=7", NextRunAt: retryTestSec},
			want: "rt-1a2b  attempt 2/3  pending   exit_code=7  next " + fmtServerTime(retryTestSec),
		},
		{
			name: "without a policy",
			row:  client.Retry{ID: "rt-1a2b", Attempt: 2, State: jobstore.RetryPending, Reason: "exit_code=7", NextRunAt: retryTestSec},
			want: "rt-1a2b  attempt 2  pending   exit_code=7  next " + fmtServerTime(retryTestSec),
		},
		{
			name: "done names the job it became",
			row:  client.Retry{ID: "rt-1a2b", Attempt: 2, MaxAttempts: 3, State: jobstore.RetryDone, Reason: "exit_code=7", NewJobID: "20260922-101112-ab12"},
			want: "rt-1a2b  attempt 2/3  done      exit_code=7  submitted as 20260922-101112-ab12",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatRetryLine(tc.row); got != tc.want {
				t.Fatalf("formatRetryLine() = %q, want %q", got, tc.want)
			}
		})
	}
}
