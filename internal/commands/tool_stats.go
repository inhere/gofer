package commands

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// runToolStatsBackfill runs `gofer tool stats-backfill`: the server computes the
// dashboard's job_metrics rows batch by batch (POST /v1/stats/backfill); this loop only
// parses the flags, follows the cursor and prints the tally. Re-running is safe: jobs
// that already have current metrics are skipped unless --force.
func runToolStatsBackfill(c *gcli.Command, _ []string) error {
	since, err := parseBackfillSince(toolStatsBackfillOpts.since, time.Now())
	if err != nil {
		return toolFail("%v", err)
	}
	limit := toolStatsBackfillOpts.limit
	if limit < 1 || limit > 1000 {
		return toolFail("--limit must be 1-1000")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	return runStatsBackfill(c, cli, since, limit, toolStatsBackfillOpts.force)
}

func runStatsBackfill(c *gcli.Command, cli *client.Client, since int64, limit int, force bool) error {
	req := client.StatsBackfillRequest{Since: since, Limit: limit, Force: force}
	var total client.StatsBackfillResult
	for {
		res, err := cli.StatsBackfill(req)
		if err != nil {
			return toolFail("%v", err)
		}
		total.Scanned += res.Scanned
		total.Written += res.Written
		total.Failed += res.Failed
		total.WithSignal += res.WithSignal
		total.WithGit += res.WithGit
		if res.Scanned > 0 {
			c.Printf("… %d jobs so far\n", total.Scanned)
		}
		if res.Done || res.Scanned == 0 {
			break
		}
		req.AfterEnded, req.AfterID = res.AfterEnded, res.AfterID
	}
	c.Printf("backfill done: scanned %d, written %d, failed %d (turns known %d, git lines known %d)\n",
		total.Scanned, total.Written, total.Failed, total.WithSignal, total.WithGit)
	if total.Failed > 0 {
		return toolFail("%d jobs failed; see the server log", total.Failed)
	}
	return nil
}

// parseBackfillSince accepts "" (everything), a window back from now ("30d", "12h") or
// a date ("2026-09-01", local time) and returns unix seconds.
func parseBackfillSince(raw string, now time.Time) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		if n, err := strconv.Atoi(strings.TrimSuffix(raw, "d")); err == nil && n > 0 {
			return now.Add(-time.Duration(n) * 24 * time.Hour).Unix(), nil
		}
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return now.Add(-d).Unix(), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return t.Unix(), nil
	}
	return 0, fmt.Errorf("invalid --since %q: use a window like 30d / 12h or a date YYYY-MM-DD", raw)
}
