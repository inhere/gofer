package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/install"
)

// The one-line worker installer (design 2026-11-14-traceability-and-machine-onboarding
// §3, plan R19): GET /install.sh, GET /install.ps1 and GET /install/manifest. All three
// sit OUTSIDE the /v1 auth group and outside any credential: a machine that is not a
// worker yet has no token, and the scripts carry none — joining is the anonymous
// self-enrollment flow a person approves in the console. They are read-only and say
// nothing about this server beyond its address, its version and where to download it.

// handleInstallScript serves one embedded installer. The handler only fills in the
// server address (install.Script) and names the media type.
func (s *Server) handleInstallScript(name string) rux.HandlerFunc {
	return func(c *rux.Context) {
		body, err := install.Script(name, s.installServerURL(c))
		if err != nil {
			writeError(c, http.StatusInternalServerError, "installer unavailable", err.Error())
			return
		}
		c.Resp.Header().Set("Content-Type", install.ContentType(name))
		c.Resp.Header().Set("Cache-Control", "no-store")
		_, _ = c.Resp.Write(body)
	}
}

// handleInstallManifest answers where to download THIS server's version for a platform,
// and its sha256 (install.ManifestFor decides that; the update source supplies both). A
// server whose release carries no checksum answers 404 with the reason, so the installer
// fails instead of installing an unverified binary.
func (s *Server) handleInstallManifest(c *rux.Context) {
	if s.updater == nil {
		writeError(c, http.StatusServiceUnavailable, "no download source",
			"this server has no release checker, so it cannot name a binary to install")
		return
	}
	// A server that has not checked its source yet (fresh start, or a failing source)
	// could not resolve the latest release; one check now makes an install right after
	// startup work.
	if st := s.updater.Status(); st.Latest == "" || st.Error != "" {
		s.updater.Check(c.Req.Context())
	}
	m, err := install.ManifestFor(c.Req.Context(), s.updater, install.Request{
		Version: s.build.Version,
		OS:      c.Query("os"),
		Arch:    c.Query("arch"),
		Server:  s.installServerURL(c),
	})
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, install.ErrNoChecksum) || errors.Is(err, install.ErrNoReleaseVersion) {
			status = http.StatusNotFound
		}
		writeError(c, status, "no verifiable install", err.Error())
		return
	}
	c.JSON(http.StatusOK, m)
}

// installServerURL is the address the installer scripts call back to: the configured
// public console address (server.web_base_url) when set, else the address this request
// arrived at. The scripts and the pairing flow talk to the same address a browser uses.
func (s *Server) installServerURL(c *rux.Context) string {
	if cfg := s.serverCfg(); cfg != nil {
		if base := strings.TrimRight(strings.TrimSpace(cfg.WebBaseURL), "/"); base != "" {
			return base
		}
	}
	scheme := "http"
	if c.Req.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Req.Host
}
