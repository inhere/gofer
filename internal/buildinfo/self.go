package buildinfo

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"runtime/debug"
)

// EnvPrintSelf makes the gofer binary print its own Go build identity as JSON and
// exit before any CLI handling. A UPX-packed release binary hides its build info
// section from debug/buildinfo.ReadFile, but once running it unpacks itself and can
// still report it, so the managed upgrade asks the candidate directly.
const EnvPrintSelf = "GOFER_PRINT_BUILDINFO"

// Self is the build identity a binary reports about itself.
type Self struct {
	Path   string `json:"path"`
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
}

// PrintSelfIfRequested writes Self to w and reports true when EnvPrintSelf is "1".
func PrintSelfIfRequested(w io.Writer) bool {
	if os.Getenv(EnvPrintSelf) != "1" {
		return false
	}
	self := Self{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if info, ok := debug.ReadBuildInfo(); ok {
		self.Path = info.Path
	}
	_ = json.NewEncoder(w).Encode(self)
	return true
}

// ParseSelf decodes the output of a binary run with EnvPrintSelf=1.
func ParseSelf(out []byte) (Self, error) {
	var self Self
	if err := json.Unmarshal(out, &self); err != nil {
		return Self{}, err
	}
	if self.Path == "" || self.GOOS == "" || self.GOARCH == "" {
		return Self{}, errors.New("incomplete self-reported build info")
	}
	return self, nil
}
