package acp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp/acptest"
)

// TestACPPendingRequestUnblocksOnCancel: 挂起的请求不能把取消拖住。agent 收到
// session/prompt 后什么都不回（F12 的真机形状：适配器返回 Authentication required
// 之后卡住），ctx 取消必须让 Prompt 立刻返回 ctx 错误——旧行为是等足 cancelGrace(10s)
// 再返回，叠加一个无界的 cmd.Wait 之后就是一个取消不掉的 job。
func TestACPPendingRequestUnblocksOnCancel(t *testing.T) {
	c, stderr := startFake(t, acptest.Options{Hang: true})
	setupCtx, cancelSetup := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelSetup()

	sid := handshake(t, c, setupCtx)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := c.Prompt(ctx, sid, "keep going", newRecorder())
		done <- result{err}
	}()

	// The request is provably in flight (the fake says so on stderr) before we cancel:
	// a cancellation that raced the write would prove nothing.
	stderr.waitFor(t, "session/prompt left unanswered")

	start := time.Now()
	cancel()
	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Prompt err = %v, want context.Canceled", got.err)
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("Prompt returned %s after the cancel, want it unblocked promptly", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Prompt never returned after ctx cancellation")
	}
}
