package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gookit/gcli/v3"
)

// `gofer auth grant renew` 的命令层测试（计划 T3）：`--for` 在本地先检查一遍（上限 24 小时，服务端
// 的口径一致），请求打到 POST /v1/auth/grants/{id}/renew，成功后按服务端时钟显示新的到期时间；
// 服务端拒绝（已过期 / 已撤销 / 会话已结束）时原样显示它的理由，因为那正是操作员下一步要看的。

// grantRenewStub serves the renewal route: it records every request it saw and answers with the
// status and body of the case.
type grantRenewStub struct {
	status int
	body   map[string]any
	calls  []grantRenewCall
}

type grantRenewCall struct {
	method string
	path   string
	body   string
}

func (s *grantRenewStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.calls = append(s.calls, grantRenewCall{method: r.Method, path: r.URL.Path, body: string(raw)})
		status := s.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(s.body)
	})
}

// bindGrantRenew builds the `auth grant renew` command the way the app does and points its
// positional argument at one grant id.
func bindGrantRenew(t *testing.T, id string) *gcli.Command {
	t.Helper()
	c := bindCmd(findSub(t, findSub(t, NewAuthCmd(), "grant"), "renew"))
	if id != "" {
		c.Arg("id").WithValue(id)
	}
	return c
}

// TestAuthGrantRenewPrintsTheNewDeadline: `--for 4h` 变成请求体里的 expires_sec=14400，输出是服务端
// 报回的那条（同一条授权，只是到期时间往后挪了），并注明它仍然生效。
func TestAuthGrantRenewPrintsTheNewDeadline(t *testing.T) {
	isolateAgentSessionEnv(t)
	stub := &grantRenewStub{body: map[string]any{
		"id": "g-1a2b3c", "subject": "session:s-abc", "source": "db",
		"pattern": "session.reply.others", "state": "active", "active": true, "effective": true,
		"expires_at": 1760000000, "granted_by": "root", "permissions": []string{"session.reply.others"},
	}}
	stubAuthServer(t, stub.handler())
	// The deadline is printed in the SERVER's clock: pin the zone so the expectation is not the
	// host's (the same pin retry_test uses).
	t.Cleanup(func() { setServerTZ(0, false) })
	setServerTZ(8*3600, true)
	// The flag binding writes its default into the option struct, so --for is set after binding.
	c := bindGrantRenew(t, "g-1a2b3c")
	authGrantOpts.renewFor = 4 * time.Hour
	t.Cleanup(func() { authGrantOpts.renewFor = 0 })

	out := captureOutput(t, func() {
		if err := runGrantRenew(c, mustAuthClient(t)); err != nil {
			t.Fatalf("auth grant renew: %v", err)
		}
	})

	if len(stub.calls) != 1 {
		t.Fatalf("the renew route saw %d requests, want one: %+v", len(stub.calls), stub.calls)
	}
	call := stub.calls[0]
	if call.method != http.MethodPost || call.path != "/v1/auth/grants/g-1a2b3c/renew" {
		t.Fatalf("renewed over %s %s, want POST /v1/auth/grants/g-1a2b3c/renew", call.method, call.path)
	}
	if !strings.Contains(call.body, `"expires_sec":14400`) {
		t.Fatalf("the request body = %s, want the 4h of --for as expires_sec", call.body)
	}
	for _, want := range []string{
		"grant:       g-1a2b3c",
		"subject:     session:s-abc",
		"state:       active (in force)",
		"expires at:  2025-10-09 16:53:20 +08:00 (unix 1760000000)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth grant renew output missing %q:\n%s", want, out)
		}
	}
}

// TestAuthGrantRenewRefusesABadDurationLocally: 超过 24 小时上限、没给 --for、短到不足一秒，都在本地
// 就被拒（带非零退出码），一个请求都不发出去 —— 服务端那边的 400 只是兜底。
func TestAuthGrantRenewRefusesABadDurationLocally(t *testing.T) {
	stub := &grantRenewStub{body: map[string]any{}}
	stubAuthServer(t, stub.handler())
	c := bindGrantRenew(t, "g-1a2b3c")
	t.Cleanup(func() { authGrantOpts.renewFor = 0 })

	for _, tc := range []struct {
		name   string
		forDur time.Duration
		want   string
	}{
		{"over the ceiling", 25 * time.Hour, "at most 24h"},
		{"missing", 0, "--for is required"},
		{"under a second", 500 * time.Millisecond, "at least 1s"},
	} {
		authGrantOpts.renewFor = tc.forDur
		err := runGrantRenew(c, mustAuthClient(t))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: --for %s gave %v, want it refused with %q", tc.name, tc.forDur, err, tc.want)
			continue
		}
		assertCodedExit(t, err)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("an invalid --for still reached the server: %+v", stub.calls)
	}
}

// TestGrantRenewSecondsAcceptsTheCeiling: 24 小时整是允许的（服务端的上限就是它），换算成秒后不丢
// 精度；上限之上一点都不行。
func TestGrantRenewSecondsAcceptsTheCeiling(t *testing.T) {
	if sec, err := grantRenewSeconds(24 * time.Hour); err != nil || sec != 86400 {
		t.Fatalf("--for 24h = %d, %v, want 86400 seconds", sec, err)
	}
	if _, err := grantRenewSeconds(24*time.Hour + time.Second); err == nil {
		t.Fatal("--for 24h1s must be refused locally")
	}
}

// TestAuthGrantRenewShowsTheServerReason: 授权已经不在生效状态时，服务端 409 的理由（哪个条件不满足、
// 下一步该重新授权）必须原样出现在错误里 —— 只说「409」等于没说。
func TestAuthGrantRenewShowsTheServerReason(t *testing.T) {
	stub := &grantRenewStub{status: http.StatusConflict, body: map[string]any{
		"error":  "the grant is not in force",
		"detail": "grant g-1a2b3c was revoked: grant it again instead",
	}}
	stubAuthServer(t, stub.handler())
	c := bindGrantRenew(t, "g-1a2b3c")
	authGrantOpts.renewFor = time.Hour
	t.Cleanup(func() { authGrantOpts.renewFor = 0 })

	err := runGrantRenew(c, mustAuthClient(t))
	if err == nil {
		t.Fatal("a refused renewal must not report success")
	}
	for _, want := range []string{"409", "the grant is not in force", "was revoked: grant it again instead"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal %q does not show %q", err.Error(), want)
		}
	}
	assertCodedExit(t, err)
}

// TestAuthGrantRenewIsMountedUnderAuth: 命令按现有的 auth 子命令组织方式挂在 `auth grant` 下，参数
// 是 <grant-id> 与 --for（G053 登记的那个选项结构）。
func TestAuthGrantRenewIsMountedUnderAuth(t *testing.T) {
	renew := findSub(t, findSub(t, NewAuthCmd(), "grant"), "renew")
	if renew.Func == nil {
		t.Fatal("auth grant renew has no runner")
	}
	c := bindCmd(renew)
	if c.Arg("id") == nil {
		t.Fatal("auth grant renew does not take the grant id")
	}
	if !c.HasOption("for") {
		t.Fatal("auth grant renew does not take --for")
	}
}
