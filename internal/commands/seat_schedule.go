package commands

import (
	"fmt"
	"strings"

	"github.com/gookit/cliui/show/table"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

var seatScheduleOpts = struct {
	cron    string
	text    string
	title   string
	project string
	ls      bool
}{}

// newSeatScheduleCmd builds `seat schedule`: it creates a schedule whose target is the seat
// (the server delivers its text to the session sitting there at each due time), or with --ls
// lists the schedules that deliver to the seat. Removing one is the ordinary `schedule rm`.
func newSeatScheduleCmd() *gcli.Command {
	return &gcli.Command{
		Name: "schedule",
		Desc: cliText("cli.seat.schedule.desc"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("name", cliText("cli.seat.arg.seat"), true)
			c.StrOpt(&seatScheduleOpts.cron, "cron", "", "", cliText("cli.seat.schedule.flag.cron"))
			c.StrOpt(&seatScheduleOpts.text, "text", "", "", cliText("cli.seat.schedule.flag.text"))
			c.StrOpt(&seatScheduleOpts.title, "title", "", "", cliText("cli.seat.schedule.flag.title"))
			c.StrOpt(&seatScheduleOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
			c.BoolOpt(&seatScheduleOpts.ls, "ls", "", false, cliText("cli.seat.schedule.flag.ls"))
		},
		Func: runSeatSchedule,
	}
}

func runSeatSchedule(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatScheduleOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	if seatScheduleOpts.ls {
		return listSeatSchedules(c, cli, x)
	}
	cronExpr, text := strings.TrimSpace(seatScheduleOpts.cron), strings.TrimSpace(seatScheduleOpts.text)
	if cronExpr == "" || text == "" {
		return fmt.Errorf("%s", cliText("cli.seat.schedule.need"))
	}
	title := strings.TrimSpace(seatScheduleOpts.title)
	if title == "" {
		title = x.Name
	}
	got, err := cli.CreateSchedule(client.CreateScheduleRequest{
		Name: title, Cron: cronExpr, Target: jobstore.ScheduleTargetSeat(x.ID),
		Request: job.JobRequest{ProjectKey: x.ProjectKey, Prompt: text},
	})
	if err != nil {
		return err
	}
	c.Println(cliText("cli.seat.schedule.created", "id", got.ID, "seat", x.Name, "cron", cronExpr))
	c.Println(cliText("cli.seat.schedule.note"))
	return nil
}

func listSeatSchedules(c *gcli.Command, cli *client.Client, x client.Seat) error {
	list, err := cli.ListSchedules(x.ProjectKey)
	if err != nil {
		return err
	}
	tb := table.New("", table.WithColMaxWidth(40))
	tb.SetHeads("ID", "NAME", "CRON", "NEXT_RUN", "ENABLED", "LAST_RESULT", "TEXT")
	rows := 0
	for _, s := range list {
		if s.Target != jobstore.ScheduleTargetSeat(x.ID) {
			continue
		}
		tb.AddRow(s.ID, s.Name, emptyDash(s.Cron), formatScheduleListTime(s.NextRunAt), scheduleEnabledText(s), emptyDash(s.LastResult), s.Request.Prompt)
		rows++
	}
	if rows == 0 {
		c.Println(cliText("cli.seat.schedule.empty", "seat", x.Name))
		return nil
	}
	c.Print(tb.Render())
	return nil
}
