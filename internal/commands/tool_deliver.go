package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/delivery"
)

// deliverToolTimeout bounds the delivery inside the internal job.
const deliverToolTimeout = 4 * time.Minute

// runToolDeliver executes the delivery plan the server pipes in (GIT-02): the
// internal job that hands a worker's worktree job over runs it on that worker, where
// the worktree is. The outcome is the last stdout line, as JSON; a failed delivery
// also exits non-zero.
func runToolDeliver(c *gcli.Command, _ []string) error {
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
	if err != nil {
		return err
	}
	var plan delivery.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("read delivery plan: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), deliverToolTimeout)
	defer cancel()
	res := delivery.Execute(ctx, plan, delivery.Env{})
	b, _ := json.Marshal(res)
	c.Println(string(b))
	if res.State == delivery.StateFailed {
		return fmt.Errorf("delivery failed: %s", res.Error)
	}
	return nil
}
