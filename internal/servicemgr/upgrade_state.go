package servicemgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/inhere/gofer/internal/daemon"
)

const UpgradeSchema = 1

type UpgradePhase string

const (
	UpgradeDraining   UpgradePhase = "draining"
	UpgradeAccepted   UpgradePhase = "accepted"
	UpgradeSwitching  UpgradePhase = "switching"
	UpgradeSucceeded  UpgradePhase = "succeeded"
	UpgradeRolledBack UpgradePhase = "rolled_back"
	UpgradeFailed     UpgradePhase = "failed"
)

var upgradeIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// UpgradeControl is the local admission/drain gate. SourceJobID is informational
// until the job bridge checks it against trusted current-job metadata; the
// helper never treats a caller-supplied ID as authorization to ignore a job.
type UpgradeControl struct {
	SchemaVersion    int          `json:"schema_version"`
	UpgradeID        string       `json:"upgrade_id"`
	Name             string       `json:"name"`
	SourceJobID      string       `json:"source_job_id,omitempty"`
	InitiatorPID     int          `json:"initiator_pid"`
	InitiatorStartID string       `json:"initiator_start_id"`
	Deadline         time.Time    `json:"deadline"`
	Phase            UpgradePhase `json:"phase"`
}

func (c UpgradeControl) Validate() error {
	if c.SchemaVersion != UpgradeSchema || !upgradeIDPattern.MatchString(c.UpgradeID) || !validName(c.Name) || c.InitiatorPID <= 0 || c.InitiatorStartID == "" || c.Deadline.IsZero() {
		return fmt.Errorf("invalid upgrade control identity or deadline")
	}
	if c.Phase != UpgradeDraining && c.Phase != UpgradeAccepted && c.Phase != UpgradeSwitching {
		return fmt.Errorf("invalid upgrade control phase %q", c.Phase)
	}
	return nil
}

func (m *Manager) UpgradeControlPath() string {
	return filepath.Join(m.serviceDir(), m.Name+".upgrade-control.json")
}

func (m *Manager) LoadUpgradeControl() (UpgradeControl, error) {
	var control UpgradeControl
	data, err := os.ReadFile(m.UpgradeControlPath())
	if err != nil {
		return control, err
	}
	if err := json.Unmarshal(data, &control); err != nil {
		return control, err
	}
	if err := control.Validate(); err != nil {
		return control, err
	}
	if control.Name != m.Name {
		return control, ErrIdentityMismatch
	}
	return control, nil
}

// SaveUpgradeControl atomically updates the gate. The caller holds m.Acquire
// while creating or transitioning a transaction; readers never see half JSON.
func (m *Manager) SaveUpgradeControl(control UpgradeControl) error {
	if err := control.Validate(); err != nil {
		return err
	}
	if control.Name != m.Name {
		return ErrIdentityMismatch
	}
	return writeJSONAtomic(m.UpgradeControlPath(), control)
}

// UpgradeReceipt survives process restarts and is the source of truth for an
// upgrade outcome. An interrupted nonterminal phase is never reported as done.
type UpgradeReceipt struct {
	SchemaVersion    int                    `json:"schema_version"`
	UpgradeID        string                 `json:"upgrade_id"`
	Name             string                 `json:"name"`
	Phase            UpgradePhase           `json:"phase"`
	SourceJobID      string                 `json:"source_job_id,omitempty"`
	CandidateSHA256  string                 `json:"candidate_sha256"`
	CandidateVersion string                 `json:"candidate_version"`
	PreviousVersion  string                 `json:"previous_version,omitempty"`
	CandidatePath    string                 `json:"candidate_path"`
	Helper           daemon.ProcessIdentity `json:"helper,omitempty"`
	StartedAt        time.Time              `json:"started_at"`
	AcceptedAt       *time.Time             `json:"accepted_at,omitempty"`
	FinishedAt       *time.Time             `json:"finished_at,omitempty"`
	Error            string                 `json:"error,omitempty"`
}

func (r UpgradeReceipt) Validate() error {
	if r.SchemaVersion != UpgradeSchema || !upgradeIDPattern.MatchString(r.UpgradeID) || !validName(r.Name) || r.StartedAt.IsZero() {
		return fmt.Errorf("invalid upgrade receipt identity")
	}
	if r.Phase != UpgradeDraining && r.Phase != UpgradeAccepted && r.Phase != UpgradeSwitching && r.Phase != UpgradeSucceeded && r.Phase != UpgradeRolledBack && r.Phase != UpgradeFailed {
		return fmt.Errorf("invalid upgrade receipt phase %q", r.Phase)
	}
	return nil
}

func (m *Manager) UpgradeReceiptPath(id string) (string, error) {
	if !upgradeIDPattern.MatchString(id) {
		return "", fmt.Errorf("invalid upgrade id")
	}
	return filepath.Join(m.ConfigDir, "run", "upgrade", id+".json"), nil
}

func (m *Manager) LoadUpgradeReceipt(id string) (UpgradeReceipt, error) {
	var receipt UpgradeReceipt
	path, err := m.UpgradeReceiptPath(id)
	if err != nil {
		return receipt, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return receipt, err
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		return receipt, err
	}
	if err := receipt.Validate(); err != nil {
		return receipt, err
	}
	if receipt.Name != m.Name || receipt.UpgradeID != id {
		return receipt, ErrIdentityMismatch
	}
	return receipt, nil
}

func (m *Manager) SaveUpgradeReceipt(receipt UpgradeReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if receipt.Name != m.Name {
		return ErrIdentityMismatch
	}
	path, err := m.UpgradeReceiptPath(receipt.UpgradeID)
	if err != nil {
		return err
	}
	return writeJSONAtomic(path, receipt)
}

// AcceptUpgradeControl is the durable CAS used only after the server bridge
// has verified the source job and drained all other work. The helper has to
// prove independent liveness first; it never grants itself this transition.
func (m *Manager) AcceptUpgradeControl(id, verifiedSourceJobID string) error {
	lock, err := m.Acquire()
	if err != nil {
		return err
	}
	defer lock.Release()
	control, err := m.LoadUpgradeControl()
	if err != nil {
		return err
	}
	if control.UpgradeID != id || control.Phase != UpgradeDraining {
		return fmt.Errorf("upgrade control is not awaiting acceptance")
	}
	if control.SourceJobID != verifiedSourceJobID {
		return fmt.Errorf("verified source job does not match upgrade claim")
	}
	receipt, err := m.LoadUpgradeReceipt(id)
	if err != nil {
		return err
	}
	if receipt.Helper.PID <= 0 {
		return fmt.Errorf("upgrade helper has not taken over")
	}
	spec, err := m.LoadSpec()
	if err != nil {
		return err
	}
	live, err := inspectIndependentHelper(m, spec, id, receipt.Helper)
	if err != nil || !live {
		return fmt.Errorf("upgrade helper identity or isolation is invalid: %v", err)
	}
	control.SourceJobID = verifiedSourceJobID
	control.Phase = UpgradeAccepted
	return m.SaveUpgradeControl(control)
}
