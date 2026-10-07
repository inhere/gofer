package commands

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/servicemgr"
)

var upgradeHelperReceiptPath string

// newServeUpgradeHelperCmd is private process plumbing. The public upgrade
// command is integrated separately after the managed transaction is tested.
func newServeUpgradeHelperCmd() *gcli.Command {
	return &gcli.Command{
		Name: "upgrade-helper", Hidden: true,
		Desc: "Resume a registered server upgrade transaction",
		Config: func(c *gcli.Command) {
			c.StrOpt(&upgradeHelperReceiptPath, "receipt", "", "", "absolute managed upgrade receipt path")
		},
		Func: func(_ *gcli.Command, _ []string) error {
			if upgradeHelperReceiptPath == "" {
				return errors.New("--receipt is required")
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return servicemgr.RunUpgradeHelper(ctx, upgradeHelperReceiptPath)
		},
	}
}
