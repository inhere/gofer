package commands

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// gcli binds every flag straight into a package-level option struct and never
// resets it, so a value one test passes on the command line is still there for
// the next test that builds a fresh NewApp (gofer-r7am, design
// docs/design/2026-10-10-flaky-tests-root-cause.md §2.6, scheme B: test-only).
// A CLI process runs a single command, so production needs no reset.
//
// flagGlobals lists every such struct. TestMain snapshots them before any test
// runs; resetFlagGlobals puts the snapshot back. A new option struct must be
// added here (TestFlagGlobalsRegistryComplete fails if one is missed).
func flagGlobals() map[string]any {
	return map[string]any{
		"agentListOpts":         &agentListOpts,
		"agentProbeOpts":        &agentProbeOpts,
		"agentRuleOpts":         &agentRuleOpts,
		"agentSkillOpts":        &agentSkillOpts,
		"briefOpts":             &briefOpts,
		"hookOpts":              &hookOpts,
		"initOpts":              &initOpts,
		"jobAcceptOpts":         &jobAcceptOpts,
		"jobApproveOpts":        &jobApproveOpts,
		"jobCommonOpts":         &jobCommonOpts,
		"jobConnOpts":           &jobConnOpts,
		"jobDeleteOpts":         &jobDeleteOpts,
		"jobFindingsOpts":       &jobFindingsOpts,
		"jobListOpts":           &jobListOpts,
		"jobRedactOpts":         &jobRedactOpts,
		"jobRejectOpts":         &jobRejectOpts,
		"jobRerunOpts":          &jobRerunOpts,
		"jobResumeOpts":         &jobResumeOpts,
		"jobReviewOpts":         &jobReviewOpts,
		"jobRunOpts":            &jobRunOpts,
		"jobSecretScanOpts":     &jobSecretScanOpts,
		"jobSetOpts":            &jobSetOpts,
		"jobWakeupOpts":         &jobWakeupOpts,
		"jobWatchOpts":          &jobWatchOpts,
		"jobWorktreeOpts":       &jobWorktreeOpts,
		"logsOpts":              &logsOpts,
		"manageOpts":            &manageOpts,
		"mcpOpts":               &mcpOpts,
		"memoryCandidateOpts":   &memoryCandidateOpts,
		"planAddTodoOpts":       &planAddTodoOpts,
		"planAnswerOpts":        &planAnswerOpts,
		"planAskOpts":           &planAskOpts,
		"planCommentOpts":       &planCommentOpts,
		"planCreateOpts":        &planCreateOpts,
		"planDecisionsOpts":     &planDecisionsOpts,
		"planHandoffOpts":       &planHandoffOpts,
		"planImportOpts":        &planImportOpts,
		"planListOpts":          &planListOpts,
		"planSetOpts":           &planSetOpts,
		"planSetTodoOpts":       &planSetTodoOpts,
		"presenceInboxOpts":     &presenceInboxOpts,
		"presenceLsOpts":        &presenceLsOpts,
		"presenceSendOpts":      &presenceSendOpts,
		"projectAddOpts":        &projectAddOpts,
		"projectListOpts":       &projectListOpts,
		"registerOpts":          &registerOpts,
		"scheduleOpts":          &scheduleOpts,
		"serveOpts":             &serveOpts,
		"serveReloadOpts":       &serveReloadOpts,
		"sessionListOpts":       &sessionListOpts,
		"sessionNudgeOpts":      &sessionNudgeOpts,
		"sessionRelayOpts":      &sessionRelayOpts,
		"sessionResumeOpts":     &sessionResumeOpts,
		"sessionSayOpts":        &sessionSayOpts,
		"sessionWatchOpts":      &sessionWatchOpts,
		"stewardOpts":           &stewardOpts,
		"templateLsOpts":        &templateLsOpts,
		"templateShowOpts":      &templateShowOpts,
		"toolCertOpts":          &toolCertOpts,
		"toolCpOpts":            &toolCpOpts,
		"toolStatsBackfillOpts": &toolStatsBackfillOpts,
		"toolXferLsOpts":        &toolXferLsOpts,
		"tunnelOpts":            &tunnelOpts,
		"upgradeOpts":           &upgradeOpts,
		"upgradeStatusOpts":     &upgradeStatusOpts,
		"wfEventsOpts":          &wfEventsOpts,
		"wfExportOpts":          &wfExportOpts,
		"wfListOpts":            &wfListOpts,
		"wfPickOpts":            &wfPickOpts,
		"wfRunOpts":             &wfRunOpts,
		"workOpts":              &workOpts,
		"workerDoctorOpts":      &workerDoctorOpts,
		"workerInitOpts":        &workerInitOpts,
		"workerManageOpts":      &workerManageOpts,
		"workerOpts":            &workerOpts,
		"workerReloadOpts":      &workerReloadOpts,
		"workerStopOpts":        &workerStopOpts,
		"workerUpgradeOpts":     &workerUpgradeOpts,
		"config.InputCfgFile":   &config.InputCfgFile,
	}
}

var flagSnapshots = map[string]reflect.Value{}

func snapshotFlagGlobals() {
	for name, p := range flagGlobals() {
		v := reflect.ValueOf(p)
		cp := reflect.New(v.Elem().Type())
		cp.Elem().Set(v.Elem())
		flagSnapshots[name] = cp
	}
}

// resetFlagGlobals restores every registered option struct to its pristine value.
func resetFlagGlobals() {
	for name, p := range flagGlobals() {
		reflect.ValueOf(p).Elem().Set(flagSnapshots[name].Elem())
	}
}

// TestFlagGlobalsRegistryComplete fails when a package-level *Opts variable is
// added without being registered in flagGlobals.
func TestFlagGlobalsRegistryComplete(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	reg := flagGlobals()
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				g, ok := d.(*ast.GenDecl)
				if !ok || g.Tok != token.VAR {
					continue
				}
				for _, spec := range g.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						if strings.HasSuffix(n.Name, "Opts") {
							if _, ok := reg[n.Name]; !ok {
								t.Errorf("option struct %s is not registered in flagGlobals (flagreset_test.go)", n.Name)
							}
						}
					}
				}
			}
		}
	}
}
