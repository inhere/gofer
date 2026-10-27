package commands

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
)

func TestInitWorkerWizardRedirect(t *testing.T) {
	old := initOpts
	t.Cleanup(func() { initOpts = old })
	c := bindCmd(NewInitCmd(buildinfo.Info{}))
	c.Arg("target").WithValue("worker")
	initOpts.server = "http://server.example"
	initOpts.adminToken = "admin-token"
	initOpts.id = "builder-1"
	initOpts.projects = []string{"app"}
	initOpts.yes = true

	err := runInit(c, buildinfo.Info{})
	if err == nil {
		t.Fatal("init worker wizard flags must fail")
	}
	assertCodedExit(t, err)
	want := "gofer worker init --server http://server.example --admin-token admin-token --id builder-1 --project app --yes"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("redirect error = %q, want command %q", err, want)
	}
}
