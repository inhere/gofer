package memorysynthesis

import (
	"fmt"
	"time"
)

// DayLayout is the format of a business day ("2026-10-28").
const DayLayout = "2006-01-02"

// Window is the half-open interval [Start, End) of one business day in the server's
// time zone, optionally widened backwards by a longer lookback. A source keeps an event
// when Contains(unix) is true, so the day boundaries never double count or drop an event.
type Window struct {
	Day   string
	Start time.Time
	End   time.Time
}

// DayWindow computes the window of `day` in loc. The window always ends at the next local
// midnight and starts at the day's own midnight; a lookback longer than 24h only moves the
// start earlier (End - lookback), a shorter or zero one keeps the plain day. Computing the
// midnights from calendar dates (not by adding 24h) keeps DST days 23 / 25 hours long.
func DayWindow(day string, loc *time.Location, lookback time.Duration) (Window, error) {
	if loc == nil {
		loc = time.Local
	}
	d, err := time.ParseInLocation(DayLayout, day, loc)
	if err != nil {
		return Window{}, fmt.Errorf("memorysynthesis: day %q must be YYYY-MM-DD", day)
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	end := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc)
	if lookback > 24*time.Hour {
		if s := end.Add(-lookback); s.Before(start) {
			start = s
		}
	}
	return Window{Day: day, Start: start, End: end}, nil
}

// Contains reports whether the unix-second timestamp falls inside [Start, End).
func (w Window) Contains(unix int64) bool {
	return unix >= w.Start.Unix() && unix < w.End.Unix()
}

// Timezone is the UTC offset label written into the envelope ("+08:00").
func (w Window) Timezone() string { return w.Start.Format("-07:00") }
