package tracker

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

const trackerIDPrefix = "tracker-"

var legacyTrackerIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// NewTrackerID returns a fresh short tracker id: "tracker-" + 10 lowercase hex chars.
func NewTrackerID() string {
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("tracker: crypto/rand failed: " + err.Error())
	}
	return trackerIDPrefix + hex.EncodeToString(b[:])
}

// IsLegacyTrackerID reports whether id has the old UUID form.
func IsLegacyTrackerID(id string) bool { return legacyTrackerIDRe.MatchString(id) }

// ShortTrackerID derives the short id of a legacy tracker id. It is deterministic so
// every clone of a repository (which all carry the same committed legacy UUID) derives
// the same new id.
//
// DEPRECATED(v0.126): remove in v0.129 (legacy UUID tracker_id migration).
func ShortTrackerID(legacy string) string {
	sum := sha256.Sum256([]byte(legacy))
	return trackerIDPrefix + hex.EncodeToString(sum[:])[:10]
}
