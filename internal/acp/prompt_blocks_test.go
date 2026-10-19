package acp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/acp"
	"github.com/inhere/gofer/internal/acp/acptest"
)

// The fake agent advertises promptCapabilities.image per option, and PromptBlocks
// delivers text first, then the images in order.
func TestPromptBlocksOrderAndCapability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, capable := range []bool{false, true} {
		c, stderr := startFake(t, acptest.Options{ImageCapable: capable, RecordBlocks: true})
		init, err := c.Initialize(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got := init.AgentCapabilities.PromptImage(); got != capable {
			t.Fatalf("ImageCapable=%v: PromptImage() = %v", capable, got)
		}
		if !capable {
			continue
		}
		sess, err := c.NewSession(ctx, acp.SessionNewParams{Cwd: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		blocks := []acp.ContentBlock{
			acp.TextBlock("look at these"),
			acp.ImageBlock("image/png", []byte{1, 2, 3}),
			acp.ImageBlock("image/jpeg", []byte{1, 2, 3, 4, 5}),
		}
		if _, err := c.PromptBlocks(ctx, sess.SessionID, blocks, newRecorder()); err != nil {
			t.Fatalf("PromptBlocks: %v", err)
		}
		got := stderr.waitFor(t, "acptest: prompt-blocks ")
		if !strings.Contains(got, "acptest: prompt-blocks text|image/png:3|image/jpeg:5\n") {
			t.Fatalf("agent saw different blocks:\n%s", got)
		}
	}
}

// Prompt stays a text-only wrapper: exactly one text block.
func TestPromptWrapperSendsOneTextBlock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, stderr := startFake(t, acptest.Options{RecordBlocks: true})
	sid := handshake(t, c, ctx)
	if _, err := c.Prompt(ctx, sid, "plain", newRecorder()); err != nil {
		t.Fatal(err)
	}
	if got := stderr.waitFor(t, "acptest: prompt-blocks text\n"); !strings.Contains(got, "prompt-blocks text\n") {
		t.Fatalf("unexpected blocks:\n%s", got)
	}
}
