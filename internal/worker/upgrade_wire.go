package worker

import (
	"context"

	"github.com/inhere/gofer/internal/wsproto"
)

func (cl *Client) handleUpgrade(ctx context.Context, req wsproto.Upgrade) {
	result := wsproto.UpgradeResult{RequestID: req.RequestID}
	if cl.upgradeFn == nil {
		result.Error = "worker: binary upgrade is not wired"
	} else if err := cl.upgradeFn(ctx, req); err != nil {
		result.Error = err.Error()
	} else {
		result.OK = true
		result.Version = req.Version
	}
	_ = cl.writeFrame(ctx, wsproto.TypeUpgradeResult, "", result)
}
