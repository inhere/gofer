package buildinfo

import (
	"bytes"
	"runtime"
	"testing"
)

func TestPrintSelfIfRequested(t *testing.T) {
	var out bytes.Buffer
	t.Setenv(EnvPrintSelf, "")
	if PrintSelfIfRequested(&out) || out.Len() != 0 {
		t.Fatal("printed without the probe variable")
	}
	t.Setenv(EnvPrintSelf, "1")
	if !PrintSelfIfRequested(&out) {
		t.Fatal("probe variable ignored")
	}
	var self Self
	self, err := ParseSelf(out.Bytes())
	if err != nil {
		t.Fatalf("self report did not parse: %v (%q)", err, out.String())
	}
	if self.GOOS != runtime.GOOS || self.GOARCH != runtime.GOARCH {
		t.Fatalf("platform = %s/%s", self.GOOS, self.GOARCH)
	}
}

func TestParseSelfRejectsIncompleteOutput(t *testing.T) {
	for _, out := range []string{"", "Usage: gofer ...", `{"path":"x","goos":"linux"}`} {
		if _, err := ParseSelf([]byte(out)); err == nil {
			t.Fatalf("accepted %q", out)
		}
	}
}
