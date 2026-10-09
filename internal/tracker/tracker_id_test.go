package tracker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestTrackerIDFuncs(t *testing.T) {
	re := regexp.MustCompile(`^tracker-[0-9a-f]{10}$`)
	a, b := NewTrackerID(), NewTrackerID()
	if !re.MatchString(a) || a == b {
		t.Fatalf("ids %q %q", a, b)
	}
	legacy := uuid.NewString()
	if !IsLegacyTrackerID(legacy) || IsLegacyTrackerID(a) || IsLegacyTrackerID("") {
		t.Fatal("IsLegacyTrackerID wrong")
	}
	if ShortTrackerID(legacy) != ShortTrackerID(legacy) || !re.MatchString(ShortTrackerID(legacy)) || ShortTrackerID(legacy) == ShortTrackerID(uuid.NewString()) {
		t.Fatal("ShortTrackerID not deterministic/shaped")
	}
}

func TestInitUsesShortTrackerID(t *testing.T) {
	s, _, err := Init(t.TempDir(), "x", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.ReadConfig()
	if !strings.HasPrefix(cfg.TrackerID, "tracker-") || len(cfg.TrackerID) != 18 {
		t.Fatalf("tracker_id=%q", cfg.TrackerID)
	}
}

func TestSyncMigratesLegacyTrackerID(t *testing.T) {
	s, _, err := Init(t.TempDir(), "mig", true)
	if err != nil {
		t.Fatal(err)
	}
	legacy := uuid.NewString()
	if err := s.UpdateConfig(func(c *Config) { c.TrackerID = legacy }); err != nil {
		t.Fatal(err)
	}
	want := ShortTrackerID(legacy)
	var syncIDs []string
	renameOK := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/rename") {
			var body struct {
				New string `json:"new_tracker_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/v1/tracker/repos/"+legacy+"/rename" || body.New != want || !renameOK {
				http.Error(w, "no", http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte(`{"renamed":true}`))
			return
		}
		var body struct {
			TrackerID string `json:"tracker_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		syncIDs = append(syncIDs, body.TrackerID)
		_, _ = w.Write([]byte(`{"issue_cursor":0,"memory_cursor":0,"issues":[],"memories":[]}`))
	}))
	defer ts.Close()

	// Rename refused: sync continues with the old id, config untouched.
	renameOK = false
	if _, err := SyncHTTP(context.Background(), s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.ReadConfig(); cfg.TrackerID != legacy || syncIDs[0] != legacy {
		t.Fatalf("cfg=%q sync=%v", cfg.TrackerID, syncIDs)
	}
	// Rename accepted: config rewritten and the sync uses the new id.
	renameOK = true
	if _, err := SyncHTTP(context.Background(), s, ts.URL); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.ReadConfig(); cfg.TrackerID != want || cfg.Prefix != "mig" || syncIDs[1] != want {
		t.Fatalf("cfg=%+v sync=%v", cfg, syncIDs)
	}
}
