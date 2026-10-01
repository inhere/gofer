package job

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// scanSessionStore finds the newest session file created by this job's agent in
// this exact cwd. Both checks matter: another TUI may start concurrently.
// sessionStoreMtimeSlack widens the "written since the job started" check. File
// timestamps come from a coarse kernel clock that can trail time.Now() by a
// scheduler tick (Linux), and some filesystems store whole or 2-second units, so a
// session file written right after start can carry an mtime slightly before it.
const sessionStoreMtimeSlack = 2 * time.Second

func scanSessionStore(glob, idPattern, cwd string, started time.Time) string {
	if glob == "" || idPattern == "" || cwd == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	glob = strings.ReplaceAll(glob, "{{home}}", home)
	glob = strings.ReplaceAll(glob, "{{cwd}}", cwd)
	if !filepath.IsAbs(glob) {
		return ""
	}
	re, err := regexp.Compile(idPattern)
	if err != nil || re.NumSubexp() == 0 {
		return ""
	}
	paths, err := filepath.Glob(glob)
	if err != nil {
		return ""
	}
	var newest time.Time
	var selected string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.ModTime().Before(started.Add(-sessionStoreMtimeSlack)) || info.ModTime().Before(newest) {
			continue
		}
		meta, ok := sessionStoreMetadata(path)
		if !ok || !sameSessionCwd(meta.cwd, cwd) {
			continue
		}
		id := sessionStoreID(re, []byte(meta.id))
		if id == "" {
			id = sessionStoreID(re, []byte(filepath.Base(path)))
		}
		if id == "" {
			id = sessionStoreID(re, meta.raw)
		}
		if id == "" {
			continue
		}
		selected, newest = id, info.ModTime()
	}
	return selected
}

type sessionMetadata struct {
	cwd string
	id  string
	raw []byte
}

func sessionStoreMetadata(path string) (sessionMetadata, bool) {
	f, err := os.Open(path)
	if err != nil {
		return sessionMetadata{}, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 256*1024)
	for i := 0; i < 16 && sc.Scan(); i++ {
		line := sc.Bytes()
		var row struct {
			Cwd     string `json:"cwd"`
			ID      string `json:"id"`
			Payload struct {
				Cwd string `json:"cwd"`
				ID  string `json:"id"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		if row.Cwd != "" {
			return sessionMetadata{row.Cwd, row.ID, bytes.Clone(line)}, true
		}
		if row.Payload.Cwd != "" {
			return sessionMetadata{row.Payload.Cwd, row.Payload.ID, bytes.Clone(line)}, true
		}
	}
	return sessionMetadata{}, false
}

func sameSessionCwd(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func sessionStoreID(re *regexp.Regexp, src []byte) string {
	for _, match := range re.FindAllSubmatch(src, -1) {
		for _, group := range match[1:] {
			if id := acceptableSessionID(string(group)); id != "" {
				return id
			}
		}
	}
	return ""
}

// sessionStoreProbeDelays are the waits between running-job store probes. A CLI
// often writes its session file only after its first exchange, so one early probe
// misses it; the last delay repeats until the job ends or an id is found.
var sessionStoreProbeDelays = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 10 * time.Second}

// probeRunningSessionStore keeps probing the session store with back-off until an
// id is captured or done closes.
func (s *Service) probeRunningSessionStore(done <-chan struct{}, entry *jobEntry, jobID, agentKey, cwd, glob, idRegex string, started time.Time) {
	if glob == "" || idRegex == "" {
		return
	}
	for i := 0; ; i++ {
		delay := sessionStoreProbeDelays[min(i, len(sessionStoreProbeDelays)-1)]
		select {
		case <-time.After(delay):
		case <-done:
			return
		}
		if s.scanRunningSessionStore(entry, jobID, agentKey, cwd, glob, idRegex, started) {
			return
		}
	}
}

// scanRunningSessionStore probes once; it reports whether the job now has a
// session id (found by this probe or already set by another path).
func (s *Service) scanRunningSessionStore(entry *jobEntry, jobID, agentKey, cwd, glob, idRegex string, started time.Time) bool {
	if glob == "" || idRegex == "" {
		return true
	}
	entry.mu.Lock()
	known := entry.result.SessionID != ""
	entry.mu.Unlock()
	if known {
		return true
	}
	id := scanSessionStore(glob, idRegex, cwd, started)
	if id == "" {
		return false
	}
	entry.mu.Lock()
	if entry.result.SessionID != "" {
		entry.mu.Unlock()
		return true
	}
	entry.result.SessionID = id
	entry.storeSessionCandidate = true
	entry.mu.Unlock()
	// Update only the session column: a whole-row upsert could race the
	// runner's status or banner capture while this background scan is finishing.
	_, _ = s.meta.SetJobSessionID(jobID, id)
	s.recordEvent(jobID, EventJobSessionCaptured, map[string]any{
		"agent": agentKey, "by": "session_store", "source": "store",
	})
	return true
}
