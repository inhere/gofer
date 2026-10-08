//go:build !windows

package servicemgr

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A UPX-packed binary has no readable build info section; the probe falls back
// to asking the binary itself. A script stands in for such a binary here.
func TestReadBuildIdentityFallsBackToSelfReport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "packed-gofer")
	script := "#!/bin/sh\n" +
		"[ \"$GOFER_PRINT_BUILDINFO\" = 1 ] || exit 3\n" +
		"echo '{\"path\":\"github.com/inhere/gofer/cmd/gofer\",\"goos\":\"plan9\",\"goarch\":\"mips\"}'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := readBuildIdentity(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if self.Path != goferMainPath || self.GOOS != "plan9" || self.GOARCH != "mips" {
		t.Fatalf("self report = %+v", self)
	}
	// The platform check still applies to a self-reported identity.
	if _, err := checkCandidate(context.Background(), path); err == nil {
		t.Fatal("a candidate reporting another platform was accepted")
	}
}

func TestReadBuildIdentityFailsWhenNeitherSourceWorks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-gofer")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho usage\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := readBuildIdentity(context.Background(), path); err == nil {
		t.Fatal("an unidentifiable binary was accepted")
	}
}
