package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/stagenote"
)

// The `gofer team` group (plan 2026-11-26-team-plan.md §2.5): a team is one leader seat plus
// member seats of one project. These commands manage the entity and read the routing /
// evaluation records; what happens to the letters of a team is the server's routing engine.

// teamAddFlags / teamSetFlags are reset each time their command is configured: gcli leaves a
// bound variable alone when its flag is absent and the default is empty, so values from an
// earlier run would carry over.
type teamAddFlags struct {
	leader  string
	brief   string
	project string
}

type teamSetFlags struct {
	leader  string
	brief   string
	project string
}

var teamAddOpts teamAddFlags

var teamRefOpts struct {
	project string
}

var teamSetOpts teamSetFlags

var teamListOpts struct {
	project string
	json    bool
}

var teamShowOpts struct {
	project string
	json    bool
}

// teamRecordOpts is shared by the two record lists (routes / evals): same flags, same shape.
var teamRecordOpts struct {
	project string
	limit   int
	json    bool
}

var teamSayOpts struct {
	project string
}

// NewTeamCmd builds the `team` group: the leader and member seats of a project, the letters
// between them, and the records of what the server decided.
func NewTeamCmd() *gcli.Command {
	return &gcli.Command{
		Name: "team",
		Desc: cliText("cli.team.desc"),
		Subs: []*gcli.Command{
			{
				Name: "add",
				Desc: cliText("cli.team.add.desc"),
				Config: func(c *gcli.Command) {
					teamAddOpts = teamAddFlags{}
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.team.arg.name"), true)
					c.StrOpt(&teamAddOpts.leader, "leader", "", "", cliText("cli.team.flag.leader"))
					c.StrOpt(&teamAddOpts.brief, "brief", "", "", cliText("cli.team.flag.brief"))
					c.StrOpt(&teamAddOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamAdd,
			},
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    cliText("cli.team.ls.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&teamListOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
					c.BoolOpt(&teamListOpts.json, "json", "", false, cliText("cli.team.flag.json"))
				},
				Func: runTeamList,
			},
			{
				Name: "show",
				Desc: cliText("cli.team.show.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.StrOpt(&teamShowOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
					c.BoolOpt(&teamShowOpts.json, "json", "", false, cliText("cli.team.flag.json"))
				},
				Func: runTeamShow,
			},
			{
				Name: "set",
				Desc: cliText("cli.team.set.desc"),
				Config: func(c *gcli.Command) {
					teamSetOpts = teamSetFlags{}
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.StrOpt(&teamSetOpts.leader, "leader", "", "", cliText("cli.team.flag.leader"))
					c.StrOpt(&teamSetOpts.brief, "brief", "", "", cliText("cli.team.flag.brief"))
					c.StrOpt(&teamSetOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamSet,
			},
			{
				Name:    "remove",
				Aliases: []string{"rm"},
				Desc:    cliText("cli.team.rm.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.StrOpt(&teamRefOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamRemove,
			},
			{
				Name: "say",
				Desc: cliText("cli.team.say.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.AddArg("text", cliText("cli.team.arg.text"), true)
					c.StrOpt(&teamSayOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamSay,
			},
			{
				Name: "routes",
				Desc: cliText("cli.team.routes.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.StrOpt(&teamRecordOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
					c.IntOpt(&teamRecordOpts.limit, "limit", "", 0, cliText("cli.team.flag.limit"))
					c.BoolOpt(&teamRecordOpts.json, "json", "", false, cliText("cli.team.flag.json"))
				},
				Func: runTeamRoutes,
			},
			{
				Name: "evals",
				Desc: cliText("cli.team.evals.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.StrOpt(&teamRecordOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
					c.IntOpt(&teamRecordOpts.limit, "limit", "", 0, cliText("cli.team.flag.limit"))
					c.BoolOpt(&teamRecordOpts.json, "json", "", false, cliText("cli.team.flag.json"))
				},
				Func: runTeamEvals,
			},
			newTeamMemberCmd(),
		},
	}
}

// newTeamMemberCmd builds `team member add|rm`: the member seats of a team (the leader is set
// with `team add --leader` / `team set --leader`).
func newTeamMemberCmd() *gcli.Command {
	return &gcli.Command{
		Name: "member",
		Desc: cliText("cli.team.member.desc"),
		Subs: []*gcli.Command{
			{
				Name: "add",
				Desc: cliText("cli.team.member.add.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.AddArg("seat", cliText("cli.team.arg.seat"), true)
					c.StrOpt(&teamRefOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamMemberAdd,
			},
			{
				Name:    "remove",
				Aliases: []string{"rm"},
				Desc:    cliText("cli.team.member.rm.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("team", cliText("cli.team.arg.team"), true)
					c.AddArg("seat", cliText("cli.team.arg.seat"), true)
					c.StrOpt(&teamRefOpts.project, "project", "p", "", cliText("cli.team.flag.project"))
				},
				Func: runTeamMemberRemove,
			},
		},
	}
}

func teamClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// findTeam resolves a team reference to the team row with its roster: `<id>` directly, or a name
// looked up in project (the -p flag), else in the project of the current directory. A reference
// may also carry its project as `name@project`, the spelling seats use.
func findTeam(cli *client.Client, project, in string) (client.Team, error) {
	ref := parseSeatName(in)
	if ref.name == "" {
		return client.Team{}, fmt.Errorf("%s", cliText("cli.team.err.need_name"))
	}
	if ref.project == "" {
		ref.project = strings.TrimSpace(project)
	}
	if ref.project == "" {
		ref.project = currentProjectKey(cli)
	}
	// An id answers on its own; anything else the server does not know falls through to the name.
	if t, err := cli.GetTeam(ref.name); err == nil {
		return t, nil
	} else if client.StatusOf(err) != 404 {
		return client.Team{}, err
	}
	list, err := cli.ListTeams(client.TeamListOpts{Project: ref.project})
	if err != nil {
		return client.Team{}, err
	}
	var hits []client.Team
	for _, t := range list {
		if t.Name == ref.name {
			hits = append(hits, t)
		}
	}
	switch len(hits) {
	case 1:
		return cli.GetTeam(hits[0].ID)
	case 0:
		if ref.project == "" {
			return client.Team{}, fmt.Errorf("%s", cliText("cli.team.err.not_found", "name", ref.name))
		}
		return client.Team{}, fmt.Errorf("%s", cliText("cli.team.err.not_found_in", "name", ref.name, "project", ref.project))
	}
	var b strings.Builder
	for _, t := range hits {
		fmt.Fprintf(&b, "\n  %s@%s", t.Name, t.ProjectKey)
	}
	return client.Team{}, fmt.Errorf("%s", cliText("cli.team.err.ambiguous", "name", ref.name, "n", len(hits), "list", b.String()))
}

// teamBrief resolves the --brief value: a text, or `@<file>` (`@-` reads stdin) holding it.
func teamBrief(v string) (string, error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "@") {
		return v, nil
	}
	path := strings.TrimPrefix(v, "@")
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s", cliText("cli.team.err.brief_file"))
	}
	data, err := readSettingsInput(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", cliText("cli.team.err.brief_read", "path", path), err)
	}
	return strings.TrimSpace(string(data)), nil
}

// teamSeatNames maps the seat ids of a roster to their names, so the record lists can name the
// seats a decision picked instead of showing their ids.
func teamSeatNames(t client.Team) map[string]string {
	out := make(map[string]string, len(t.Roster))
	for _, e := range t.Roster {
		out[e.ID] = e.Name
	}
	return out
}

// teamSeatLabel is a seat id as a person reads it: its name when the roster knows it.
func teamSeatLabel(names map[string]string, id string) string {
	if n, ok := names[id]; ok && n != "" {
		return n
	}
	return dash(id)
}

// teamLeaderName is the leader seat's name (the roster's leader row; the stored id otherwise).
func teamLeaderName(t client.Team) string {
	for _, e := range t.Roster {
		if e.Role == "leader" {
			return e.Name
		}
	}
	return dash(t.LeaderSeatID)
}

func runTeamAdd(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	project := strings.TrimSpace(teamAddOpts.project)
	if project == "" {
		project = currentProjectKey(cli)
	}
	if project == "" {
		return fmt.Errorf("%s", cliText("cli.team.err.need_project"))
	}
	brief, err := teamBrief(teamAddOpts.brief)
	if err != nil {
		return err
	}
	t, err := cli.CreateTeam(client.TeamCreate{
		ProjectKey: project, Name: c.Arg("name").String(), Leader: teamAddOpts.leader, Brief: brief,
	})
	if err != nil {
		return err
	}
	c.Println(cliText("cli.team.add.created", "name", t.Name, "project", t.ProjectKey, "leader", teamLeaderName(t)))
	c.Println(cliText("cli.team.add.hint", "team", teamRefOf(t)))
	return nil
}

// teamRefOf names a team the way the commands take it: bare inside its project, name@project
// across projects.
func teamRefOf(t client.Team) string {
	if t.ProjectKey == "" {
		return t.Name
	}
	return t.Name + "@" + t.ProjectKey
}

func runTeamList(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	list, err := cli.ListTeams(client.TeamListOpts{Project: teamListOpts.project})
	if err != nil {
		return err
	}
	if teamListOpts.json {
		return printTeamJSON(c, list)
	}
	if len(list) == 0 {
		c.Println(cliText("cli.team.ls.empty"))
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %-16s %-8s %-8s %-9s %s\n", "NAME", "PROJECT", "RELAY", "MERGE", "CREATED", "BRIEF")
	for _, t := range list {
		fmt.Fprintf(&b, "%-22s %-16s %-8s %-8s %-9s %s\n", t.Name, t.ProjectKey, t.RelayMode,
			fmt.Sprintf("%ds", t.MergeWindowSec), ago(t.CreatedAt), oneLine(t.Brief, 60))
	}
	c.Print(b.String())
	return nil
}

func runTeamShow(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamShowOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	if teamShowOpts.json {
		return printTeamJSON(c, t)
	}
	c.Printf("team:       %s\nid:         %s\nproject:    %s\nleader:     %s\nrelay:      %s\ncreated:    %s ago\nupdated:    %s ago\n",
		t.Name, t.ID, t.ProjectKey, teamLeaderName(t),
		cliText("cli.team.show.relay", "mode", t.RelayMode, "round", t.RelayRoundLimit, "daily", t.RelayDailyLimit, "merge", t.MergeWindowSec),
		ago(t.CreatedAt), ago(t.UpdatedAt))
	if strings.TrimSpace(t.Brief) != "" {
		c.Printf("\nbrief:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(t.Brief), "\n", "\n  "))
	}
	printTeamRoster(c, t)
	names := teamSeatNames(t)
	routes, err := cli.TeamRoutes(t.ID, 10)
	if err != nil {
		return err
	}
	c.Printf("\n%s\n", cliText("cli.team.show.routes_head", "n", len(routes)))
	printTeamRouteRows(c, routes, names)
	return nil
}

// printTeamRoster renders the roster, leader first: the seat, its role, its state and what the
// sitting session is doing.
func printTeamRoster(c *gcli.Command, t client.Team) {
	c.Printf("\n%s\n", cliText("cli.team.show.roster_head", "n", len(t.Roster)))
	var b strings.Builder
	fmt.Fprintf(&b, "%-8s %-22s %-8s %-9s %-24s %s\n", "ROLE", "SEAT", "KIND", "STATE", "STEP", "ACTIVITY")
	for _, e := range t.Roster {
		waiting := e.State
		if e.Waiting != "" {
			waiting += "/" + e.Waiting
		}
		fmt.Fprintf(&b, "%-8s %-22s %-8s %-9s %-24s %s\n", e.Role, e.Name, e.Kind, waiting,
			teamStageLabel(e.Stage), ago(e.ActivityAt))
	}
	c.Print(b.String())
}

// teamStageLabel is where the flow of the seated session stands: "<step> <n>/<total>", with the
// blocked mark when it is blocked.
func teamStageLabel(x *stagenote.Brief) string {
	if x == nil || !x.InFlow() {
		return "-"
	}
	s := x.Step
	if x.StepTotal > 0 {
		s = fmt.Sprintf("%s %d/%d", x.Step, x.StepNo, x.StepTotal)
	}
	if x.Status == teamStageBlocked {
		s += " (blocked)"
	}
	return s
}

// teamStageBlocked is the blocked mark of a stage brief (stagenote.Brief.Status).
const teamStageBlocked = "blocked"

// printTeamRouteRows renders routing decisions, newest first.
func printTeamRouteRows(c *gcli.Command, rows []client.TeamRoute, names map[string]string) {
	if len(rows) == 0 {
		c.Println(cliText("cli.team.routes.empty"))
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-9s %-16s %-16s %-20s %-4s %s\n", "ID", "WHEN", "RULE", "FROM", "TO", "HOP", "OUTCOME")
	for _, r := range rows {
		to := make([]string, 0, len(r.ToSeatIDs))
		for _, id := range r.ToSeatIDs {
			to = append(to, teamSeatLabel(names, id))
		}
		from := r.FromRef
		if name, ok := names[from]; ok && name != "" {
			from = name
		}
		fmt.Fprintf(&b, "%-6d %-9s %-16s %-16s %-20s %-4d %s\n", r.ID, ago(r.DecidedAt), dash(r.Rule),
			dash(from), dash(strings.Join(to, ",")), r.Hop, r.Outcome)
	}
	c.Print(b.String())
}

func runTeamSet(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamSetOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	p := client.TeamPatch{}
	// "-" clears the brief (the convention `gofer seat set` uses); an empty flag leaves it alone.
	if v, err := teamBrief(teamSetOpts.brief); err != nil {
		return err
	} else if strings.TrimSpace(teamSetOpts.brief) != "" {
		if v == "-" {
			v = ""
		}
		p.Brief = &v
	}
	if v := strings.TrimSpace(teamSetOpts.leader); v != "" {
		p.Leader = &v
	}
	if p.Leader == nil && p.Brief == nil {
		return fmt.Errorf("%s", cliText("cli.team.set.nothing"))
	}
	got, err := cli.PatchTeam(t.ID, p)
	if err != nil {
		return err
	}
	c.Println(cliText("cli.team.set.updated", "name", got.Name, "leader", teamLeaderName(got)))
	return nil
}

func runTeamRemove(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamRefOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	if _, err := cli.ArchiveTeam(t.ID); err != nil {
		return err
	}
	c.Println(cliText("cli.team.rm.archived", "name", t.Name))
	return nil
}

// runTeamSay sends a letter from the person to the team: it is the ordinary mail send to the
// team address (`team:<name>[@<project>]`), so the server's routing rules decide who gets it
// (a letter naming @member goes to those members only, one naming nobody goes to the leader).
func runTeamSay(c *gcli.Command, _ []string) error {
	text := strings.TrimSpace(c.Arg("text").String())
	if text == "" {
		return fmt.Errorf("%s", cliText("cli.team.say.need_text"))
	}
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamSayOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	addr := "team:" + t.Name
	if t.ProjectKey != "" {
		addr += "@" + t.ProjectKey
	}
	res, err := cli.SendMail(addr, text, false)
	if err != nil {
		return err
	}
	c.Print(formatMailSent(cliText("cli.team.say.sent"), res))
	return nil
}

func runTeamMemberAdd(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamRefOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	seat := strings.TrimSpace(c.Arg("seat").String())
	got, err := cli.AddTeamMember(t.ID, seat)
	if err != nil {
		return err
	}
	c.Println(cliText("cli.team.member.add.added", "seat", seat, "team", got.Name))
	return nil
}

func runTeamMemberRemove(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamRefOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	seat := strings.TrimSpace(c.Arg("seat").String())
	if _, err := cli.RemoveTeamMember(t.ID, seat); err != nil {
		return err
	}
	c.Println(cliText("cli.team.member.rm.removed", "seat", seat, "team", t.Name))
	return nil
}

func runTeamRoutes(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamRecordOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	rows, err := cli.TeamRoutes(t.ID, teamRecordOpts.limit)
	if err != nil {
		return err
	}
	if teamRecordOpts.json {
		return printTeamJSON(c, struct {
			Routes []client.TeamRoute `json:"routes"`
		}{Routes: rows})
	}
	printTeamRouteRows(c, rows, teamSeatNames(t))
	return nil
}

func runTeamEvals(c *gcli.Command, _ []string) error {
	cli, err := teamClient()
	if err != nil {
		return err
	}
	t, err := findTeam(cli, teamRecordOpts.project, c.Arg("team").String())
	if err != nil {
		return err
	}
	rows, err := cli.TeamEvals(t.ID, teamRecordOpts.limit)
	if err != nil {
		return err
	}
	if teamRecordOpts.json {
		return printTeamJSON(c, struct {
			Evals []client.TeamEval `json:"evals"`
		}{Evals: rows})
	}
	if len(rows) == 0 {
		c.Println(cliText("cli.team.evals.empty"))
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-6s %-9s %-12s %-16s %s\n", "ID", "WHEN", "OUTCOME", "LEADER", "REASON")
	for _, e := range rows {
		fmt.Fprintf(&b, "%-6d %-9s %-12s %-16s %s\n", e.ID, ago(e.At), dash(e.Outcome), dash(e.LeaderRef), oneLine(e.Reason, 60))
	}
	c.Print(b.String())
	return nil
}

// printTeamJSON prints one team answer as indented JSON.
func printTeamJSON(c *gcli.Command, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	c.Println(string(b))
	return nil
}
