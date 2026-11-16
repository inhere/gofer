// Package install serves the one-line worker installer (design
// 2026-11-14-traceability-and-machine-onboarding-design.md §3): the public scripts
// /install.sh and /install.ps1, and the manifest that names this server's own version
// for a platform together with its sha256.
//
// The scripts carry NO credential — only the server's address. Joining is the existing
// anonymous self-enrollment flow (`gofer worker join`): the machine asks to be admitted,
// prints a pairing code, and a person approves it in the console. The installer's own
// integrity protection is the checksum from the manifest: a missing checksum is a hard
// failure, never a silent unverified install.
package install

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path"
	"runtime"
	"strings"

	"github.com/inhere/gofer/internal/updatecheck"
)

// Script names a template ships under. They are also the route suffixes (/install.sh).
const (
	ScriptSh       = "install.sh"
	ScriptPS1      = "install.ps1"
	placeholder    = "{{SERVER}}"
	shContentType  = "text/x-shellscript; charset=utf-8"
	ps1ContentType = "text/plain; charset=utf-8"
)

// templates holds the scripts verbatim (LF endings, no build-time rewriting).
//
//go:embed install.sh install.ps1
var templates embed.FS

// Script returns the installer script named name with the server address filled in.
// The result holds exactly the server's address: no credential, no version, no path of
// this machine.
func Script(name, server string) ([]byte, error) {
	server = strings.TrimRight(strings.TrimSpace(server), "/")
	if server == "" {
		return nil, errors.New("no server address to put into the installer")
	}
	if strings.ContainsAny(server, "\"' \t\r\n") {
		// The address is spliced into a quoted shell string: an address with a quote or
		// a blank would end it and turn the rest into code.
		return nil, fmt.Errorf("server address %q is not a plain URL", server)
	}
	if _, err := url.Parse(server); err != nil {
		return nil, fmt.Errorf("server address %q: %v", server, err)
	}
	body, err := templates.ReadFile(name)
	if err != nil {
		return nil, err
	}
	out := strings.ReplaceAll(string(body), placeholder, server)
	if strings.Contains(out, placeholder) {
		return nil, fmt.Errorf("%s: the placeholder was not fully replaced", name)
	}
	return []byte(out), nil
}

// ContentType is the media type a script is served with.
func ContentType(name string) string {
	if name == ScriptPS1 {
		return ps1ContentType
	}
	return shContentType
}

// Resolver is the release source the manifest resolves this server's version from
// (*updatecheck.Checker in the server).
type Resolver interface {
	AssetForVersion(ctx context.Context, goos, goarch, version string) (updatecheck.Asset, error)
}

// Errors the manifest answers 404 for: nothing is wrong with the request, but this
// source cannot supply a verifiable binary.
var (
	// ErrNoReleaseVersion — the server runs a build that is not a release (a "dev" or
	// bare-commit build), so no release carries its exact version.
	ErrNoReleaseVersion = errors.New("this server's build is not a release version")
	// ErrNoChecksum — the source has no sha256 for that binary: the download could not be
	// verified, so the installer refuses to run, and the manifest refuses to offer it.
	ErrNoChecksum = errors.New("the update source published no sha256 for that binary")
)

// Request is what a manifest is resolved for. An empty OS / Arch means this server's own
// platform.
type Request struct {
	Version string // this server's build version (buildinfo.Info.Version)
	OS      string
	Arch    string
	Server  string // the address the scripts call back to
}

// Manifest is the answer of GET /install/manifest: where to download THIS server's
// version for one platform, its sha256, and the file name the URL serves.
type Manifest struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// URL is the binary (or its release archive) on the configured update source.
	URL string `json:"url"`
	// Name is the file the URL serves; an archive is one of the .tar.gz / .zip layouts
	// Download knows, and the scripts unpack those before hashing.
	Name string `json:"name"`
	// SHA256 is the published checksum of the BINARY inside Name.
	SHA256 string `json:"sha256"`
	// Server is the address the scripts join (server.web_base_url, else the request's).
	Server string `json:"server"`
}

// ManifestFor resolves the manifest: this server's release version for the platform,
// from the update source's own metadata (the mirror's latest.yaml, or the GitHub
// release's). It fails rather than offering a download nothing can verify.
func ManifestFor(ctx context.Context, src Resolver, req Request) (Manifest, error) {
	version := updatecheck.ReleaseVersion(req.Version)
	if version == "" {
		return Manifest{}, fmt.Errorf("%w (%q): only a released build has a version to download", ErrNoReleaseVersion, req.Version)
	}
	goos, goarch := strings.TrimSpace(req.OS), strings.TrimSpace(req.Arch)
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	a, err := src.AssetForVersion(ctx, goos, goarch, version)
	if err != nil {
		return Manifest{}, err
	}
	if strings.TrimSpace(a.SHA256) == "" {
		return Manifest{}, fmt.Errorf("%w (%s/%s of %s), so it cannot be installed with a verified download", ErrNoChecksum, goos, goarch, version)
	}
	name := strings.TrimSpace(a.Name)
	if name == "" {
		name = path.Base(a.URL)
		if u, err := url.Parse(a.URL); err == nil {
			name = path.Base(u.Path)
		}
	}
	return Manifest{
		Version: a.Version,
		OS:      goos,
		Arch:    goarch,
		URL:     a.URL,
		Name:    name,
		SHA256:  strings.ToLower(strings.TrimSpace(a.SHA256)),
		Server:  strings.TrimRight(strings.TrimSpace(req.Server), "/"),
	}, nil
}
