package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

func runServeUpgrade(c *gcli.Command, _ []string) error {
	if upgradeOpts.binary == "" {
		return errors.New("--binary is required")
	}
	m, err := managerFor(upgradeOpts.name)
	if err != nil {
		return err
	}
	if _, err := m.LoadSpec(); err != nil {
		return err
	}
	path, err := filepath.Abs(upgradeOpts.binary)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, f)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return err
	}
	if size <= 0 {
		return errors.New("candidate binary is empty")
	}
	helperExecutable, err := os.Executable()
	if err != nil {
		return err
	}
	helperExecutable, err = filepath.Abs(helperExecutable)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(5 * time.Minute)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	sourceJobID := strings.TrimSpace(os.Getenv("GOFER_JOB_ID")) // untrusted until the server validates the direct local process
	receipt, err := servicemgr.BeginUpgrade(ctx, m, servicemgr.UpgradeRequest{
		CandidatePath: path, HelperExecutable: helperExecutable, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil)), Deadline: deadline,
		SourceJobID: sourceJobID,
	})
	if err != nil {
		return err
	}
	accepted, err := servicemgr.WaitUpgradeAccepted(ctx, m, receipt.UpgradeID)
	if err != nil {
		return fmt.Errorf("upgrade %s was not accepted: %w", receipt.UpgradeID, err)
	}
	if accepted.Phase == servicemgr.UpgradeFailed {
		return fmt.Errorf("upgrade %s failed before acceptance: %s", receipt.UpgradeID, accepted.Error)
	}
	c.Printf("upgrade_id=%s phase=accepted\n", receipt.UpgradeID)
	if sourceJobID != "" || upgradeOpts.noWait {
		return nil
	}
	finalCtx, done := context.WithTimeout(context.Background(), 10*time.Minute)
	defer done()
	final, err := waitManagedUpgradeFinal(finalCtx, m, receipt.UpgradeID)
	if err != nil {
		return fmt.Errorf("upgrade %s outcome is unconfirmed: %w; inspect serve upgrade status %s", receipt.UpgradeID, err, receipt.UpgradeID)
	}
	c.Printf("upgrade_id=%s phase=%s\n", receipt.UpgradeID, final.Phase)
	if final.Phase != servicemgr.UpgradeSucceeded {
		return fmt.Errorf("upgrade %s %s: %s", receipt.UpgradeID, final.Phase, final.Error)
	}
	return nil
}

func waitManagedUpgradeFinal(ctx context.Context, m *servicemgr.Manager, id string) (servicemgr.UpgradeReceipt, error) {
	for {
		receipt, err := m.LoadUpgradeReceipt(id)
		if err != nil {
			return receipt, err
		}
		switch receipt.Phase {
		case servicemgr.UpgradeSucceeded, servicemgr.UpgradeRolledBack, servicemgr.UpgradeFailed:
			return receipt, nil
		}
		select {
		case <-ctx.Done():
			return receipt, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

type upgradeStatusOutput struct {
	servicemgr.UpgradeReceipt
	Interrupted bool `json:"interrupted"`
}

func readUpgradeStatus(m *servicemgr.Manager, id string) (upgradeStatusOutput, error) {
	receipt, err := m.LoadUpgradeReceipt(id)
	if err != nil {
		return upgradeStatusOutput{}, err
	}
	out := upgradeStatusOutput{UpgradeReceipt: receipt}
	switch receipt.Phase {
	case servicemgr.UpgradeSucceeded, servicemgr.UpgradeRolledBack, servicemgr.UpgradeFailed:
		return out, nil
	}
	if receipt.Helper.PID == 0 {
		out.Interrupted = time.Since(receipt.StartedAt) > 8*time.Second
	} else {
		live, inspectErr := daemon.InspectProcess(receipt.Helper.PID)
		out.Interrupted = inspectErr != nil || live != receipt.Helper
	}
	return out, nil
}

func runServeUpgradeStatus(c *gcli.Command, args []string) error {
	id := argString(c, "upgrade_id")
	if id == "" && len(args) == 1 {
		id = args[0]
	}
	if id == "" {
		return errors.New("upgrade status requires one upgrade_id")
	}
	m, err := managerFor(upgradeStatusOpts.name)
	if err != nil {
		return err
	}
	out, err := readUpgradeStatus(m, id)
	if err != nil {
		return err
	}
	if upgradeStatusOpts.asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	c.Printf("upgrade_id=%s phase=%s candidate_version=%s previous_version=%s interrupted=%t\n",
		out.UpgradeID, out.Phase, out.CandidateVersion, out.PreviousVersion, out.Interrupted)
	if out.Error != "" {
		c.Printf("error: %s\n", out.Error)
	}
	return nil
}
