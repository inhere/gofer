package healthcheck

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Disk floors (design §5): the data directory counts as out of space below 1GB free OR
// below 5% of the volume's capacity, whichever is reached first. Both are floors, not
// targets: a 20GB volume fails at 1GB, a 10TB volume only at 5%.
const (
	MinFreeBytes = uint64(1) << 30 // 1 GiB
	MinFreeRatio = 0.05
)

// ProbeWriter is the slice of the job store the storage probe writes through
// (jobstore.Store.HealthProbeWrite).
type ProbeWriter interface {
	HealthProbeWrite(now int64) error
}

// StorageProbe checks that the database accepts writes by upserting the single
// health_probe row. A read-only file, a full disk or a broken WAL all fail here, which
// is exactly the class of problem a plain SELECT would miss.
func StorageProbe(w ProbeWriter, now func() time.Time) func() error {
	if now == nil {
		now = time.Now
	}
	return func() error {
		if w == nil {
			return fmt.Errorf("storage probe: no store")
		}
		return w.HealthProbeWrite(now().Unix())
	}
}

// freeSpaceFor is the platform free-space read; a package var so a test can pin the
// numbers instead of depending on the machine's real volume.
var freeSpaceFor = freeSpace

// DiskProbe checks the free space of the volume holding dir (the data directory). The
// platform free-space read lives in disk_free_windows.go / disk_free_other.go.
func DiskProbe(dir string) func() error {
	return func() error {
		total, free, err := freeSpaceFor(dir)
		if err != nil {
			return err
		}
		if free < MinFreeBytes {
			return fmt.Errorf("low disk space: %s free, floor %s", humanBytes(free), humanBytes(MinFreeBytes))
		}
		if total > 0 && float64(free) < MinFreeRatio*float64(total) {
			return fmt.Errorf("low disk space: %s free of %s (below %d%%)",
				humanBytes(free), humanBytes(total), int(MinFreeRatio*100))
		}
		return nil
	}
}

// SweepProbe checks that the server's periodic sweeper still ticks: last is the sweeper's
// last pass stamp and maxAge is how long a pass may be overdue. A past stamp far enough
// behind means the loop stalled (a blocked database, a stuck submission), which no
// single check of the tables would reveal.
func SweepProbe(last func() time.Time, maxAge time.Duration, now func() time.Time) func() error {
	if now == nil {
		now = time.Now
	}
	return func() error {
		if last == nil {
			return fmt.Errorf("sweep probe: no sweeper")
		}
		at := last()
		if at.IsZero() {
			return fmt.Errorf("sweeper has not run yet")
		}
		if age := now().Sub(at); age > maxAge {
			return fmt.Errorf("sweeper last ran %s ago (limit %s)", age.Round(time.Second), maxAge)
		}
		return nil
	}
}

// NotifyProbe checks the outbound notification channels: fails is the dispatcher's
// per-channel streak (channel name → consecutive delivery failures) and threshold is how
// many in a row count as failing. One failing channel fails the item, and every failing
// channel is named, so the reported error says which one to look at.
func NotifyProbe(fails func() map[string]int, threshold int) func() error {
	return func() error {
		if fails == nil {
			return nil // no dispatcher wired (notification off): nothing to check
		}
		if threshold <= 0 {
			threshold = DefaultFailThreshold
		}
		streaks := fails()
		var bad []string
		for name, n := range streaks {
			if n >= threshold {
				bad = append(bad, fmt.Sprintf("%s (%d in a row)", name, n))
			}
		}
		if len(bad) == 0 {
			return nil
		}
		sort.Strings(bad)
		return fmt.Errorf("delivery failing: %s", strings.Join(bad, ", "))
	}
}

// humanBytes formats a byte count for the probe's error text.
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(n)/float64(div), "KMGTP"[exp])
}
