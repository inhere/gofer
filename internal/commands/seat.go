package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/hookrelay"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/seat"
)

// seatAddFlags is reset each time the command is configured: gcli leaves a bound variable alone
// when its flag is absent and the default is empty, so values from an earlier run would carry over.
type seatAddFlags struct {
	project  string
	purpose  string
	charter  string
	template string
	kind     string
	agent    string
	cwd      string
}

var seatAddOpts seatAddFlags

var seatRefOpts struct {
	project string
}

var seatSetOpts struct {
	project  string
	purpose  string
	charter  string
	template string
	agent    string
}

var seatBindOpts struct {
	project string
	ref     string
	replace bool
}

var seatOpenOpts struct {
	project string
	shell   string
	restart bool
}

var seatListOpts struct {
	project  string
	archived bool
	all      bool
}

// NewSeatCmd builds the `seat` group: the roster of long-lived working positions of the current
// machine's caller (design 2026-11-18 resident-sessions §4). A seat outlives the sessions that
// sit in it; the session sitting there gets its charter injected on every start.
func NewSeatCmd() *gcli.Command {
	return &gcli.Command{
		Name: "seat",
		Desc: cliText("cli.seat.desc"),
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    cliText("cli.seat.ls.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&seatListOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
					c.BoolOpt(&seatListOpts.archived, "archived", "", false, cliText("cli.seat.flag.archived"))
					c.BoolOpt(&seatListOpts.all, "all", "", false, cliText("cli.seat.flag.all"))
				},
				Func: runSeatList,
			},
			{
				Name: "add",
				Desc: cliText("cli.seat.add.desc"),
				Config: func(c *gcli.Command) {
					seatAddOpts = seatAddFlags{}
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.name"), true)
					c.StrOpt(&seatAddOpts.purpose, "purpose", "", "", cliText("cli.seat.flag.purpose"))
					c.StrOpt(&seatAddOpts.charter, "charter", "", "", cliText("cli.seat.flag.charter"))
					c.StrOpt(&seatAddOpts.template, "charter-template", "", "", cliText("cli.seat.flag.charter_template"))
					c.StrOpt(&seatAddOpts.kind, "kind", "", jobstore.SeatKindTerminal, cliText("cli.seat.flag.kind"))
					c.StrOpt(&seatAddOpts.agent, "agent", "a", "", cliText("cli.seat.flag.agent"))
					c.StrOpt(&seatAddOpts.cwd, "cwd", "", "", cliText("cli.seat.flag.cwd"))
					c.StrOpt(&seatAddOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
				},
				Func: runSeatAdd,
			},
			{
				Name: "show",
				Desc: cliText("cli.seat.show.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatRefOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
				},
				Func: runSeatShow,
			},
			{
				Name: "set",
				Desc: cliText("cli.seat.set.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatSetOpts.purpose, "purpose", "", "", cliText("cli.seat.flag.purpose"))
					c.StrOpt(&seatSetOpts.charter, "charter", "", "", cliText("cli.seat.flag.charter"))
					c.StrOpt(&seatSetOpts.template, "charter-template", "", "", cliText("cli.seat.flag.charter_template"))
					c.StrOpt(&seatSetOpts.agent, "agent", "a", "", cliText("cli.seat.flag.agent"))
					c.StrOpt(&seatSetOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
				},
				Func: runSeatSet,
			},
			{
				Name:    "remove",
				Aliases: []string{"rm"},
				Desc:    cliText("cli.seat.rm.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatRefOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
				},
				Func: runSeatRemove,
			},
			{
				Name: "bind",
				Desc: cliText("cli.seat.bind.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatBindOpts.ref, "ref", "", "", cliText("cli.seat.flag.ref"))
					c.StrOpt(&seatBindOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
					c.BoolOpt(&seatBindOpts.replace, "replace", "", false, cliText("cli.seat.flag.replace"))
				},
				Func: runSeatBind,
			},
			{
				Name: "unbind",
				Desc: cliText("cli.seat.unbind.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatRefOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
				},
				Func: runSeatUnbind,
			},
			newSeatScheduleCmd(),
			{
				Name: "open",
				Desc: cliText("cli.seat.open.desc"),
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("name", cliText("cli.seat.arg.seat"), true)
					c.StrOpt(&seatOpenOpts.shell, "shell", "", "", cliText("cli.seat.flag.shell"))
					c.StrOpt(&seatOpenOpts.project, "project", "p", "", cliText("cli.seat.flag.project"))
					c.BoolOpt(&seatOpenOpts.restart, "restart", "", false, cliText("cli.seat.flag.restart"))
				},
				Func: runSeatOpen,
			},
		},
	}
}

func seatClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// seatName is a seat reference on the command line: `name` looks in the project of the current
// directory, `name@project` anywhere.
type seatName struct {
	name    string
	project string
}

func parseSeatName(in string) seatName {
	in = strings.TrimSpace(in)
	if i := strings.LastIndex(in, "@"); i > 0 {
		return seatName{name: in[:i], project: in[i+1:]}
	}
	return seatName{name: in}
}

// currentProjectKey is the project of the current directory ("" when the cwd is in none).
func currentProjectKey(cli *client.Client) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	projects, err := cli.ListProjects()
	if err != nil {
		return ""
	}
	key, _, ok := client.MatchProjectPath(projects, cwd)
	if !ok {
		return ""
	}
	return key
}

// findSeat resolves a seat reference to its row. A bare name is looked up in project (the -p flag)
// when given, else in the project of the current directory (else anywhere the name is unique).
func findSeat(cli *client.Client, project, in string) (client.Seat, error) {
	ref := parseSeatName(in)
	if ref.name == "" {
		return client.Seat{}, fmt.Errorf("seat name required")
	}
	if ref.project == "" {
		ref.project = strings.TrimSpace(project)
	}
	if ref.project == "" {
		ref.project = currentProjectKey(cli)
	}
	list, err := cli.ListSeats(client.SeatListOpts{Project: ref.project})
	if err != nil {
		return client.Seat{}, err
	}
	var hits []client.Seat
	for _, x := range list {
		if x.Name == ref.name {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		if ref.project == "" {
			return client.Seat{}, fmt.Errorf("no seat named %q (see `gofer seat ls`)", ref.name)
		}
		return client.Seat{}, fmt.Errorf("no seat named %q in project %q (see `gofer seat ls`)", ref.name, ref.project)
	}
	var b strings.Builder
	for _, x := range hits {
		fmt.Fprintf(&b, "\n  %s@%s", x.Name, x.ProjectKey)
	}
	return client.Seat{}, fmt.Errorf("seat %q exists in %d projects; name one of them:%s", ref.name, len(hits), b.String())
}

func runSeatList(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	list, err := cli.ListSeats(client.SeatListOpts{Project: seatListOpts.project, IncludeArchived: seatListOpts.archived, All: seatListOpts.all})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println(cliText("cli.seat.ls.empty"))
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %-16s %-8s %-9s %-9s %-30s %s\n", "NAME", "PROJECT", "KIND", "AGENT", "STATE", "CURRENT", "PURPOSE")
	for _, x := range list {
		agent, current := x.Agent, seatCurrentRef(x)
		if agent == "" {
			agent = "-"
		}
		if x.ArchivedAt > 0 {
			current += " (archived)"
		}
		fmt.Fprintf(&b, "%-22s %-16s %-8s %-9s %-9s %-30s %s\n",
			x.Name, x.ProjectKey, x.Kind, agent, x.State, current, oneLine(x.Purpose, 60))
	}
	c.Print(b.String())
	return nil
}

// seatCharter resolves the charter flag pair: --charter-template <name> takes the embedded text,
// otherwise --charter takes a text or an "@<repository-relative path>".
func seatCharter(charter, template string) (string, error) {
	if charter != "" && template != "" {
		return "", fmt.Errorf("--charter and --charter-template are mutually exclusive")
	}
	if template == "" {
		return charter, nil
	}
	text, ok := seat.CharterTemplate(template)
	if !ok {
		return "", fmt.Errorf("unknown charter template %q (available: %s)", template, strings.Join(seat.CharterTemplateNames(), ", "))
	}
	return text, nil
}

func runSeatAdd(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	charter, err := seatCharter(seatAddOpts.charter, seatAddOpts.template)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if in := strings.TrimSpace(seatAddOpts.cwd); in != "" {
		cwd = in
	}
	if cwd, err = absoluteSeatCwd(cwd); err != nil {
		return err
	}
	project := strings.TrimSpace(seatAddOpts.project)
	rel := ""
	if project == "" {
		projects, err := cli.ListProjects()
		if err != nil {
			return err
		}
		key, root, ok := client.MatchProjectPath(projects, cwd)
		if !ok {
			return fmt.Errorf("cannot tell which project %s belongs to; pass --project <key>", cwd)
		}
		project = key
		rel = relToRoot(cwd, root)
	} else if p, ok := projectOf(cli, project); ok {
		if r, under := inRoot(cwd, p.HostPath); under {
			rel = r
		} else if r, under := inRoot(cwd, p.ContainerPath); under {
			rel = r
		}
	}
	x, err := cli.CreateSeat(client.SeatCreate{
		ProjectKey: project, Name: c.Arg("name").String(), RelCwd: rel, Purpose: seatAddOpts.purpose,
		Charter: charter, Kind: strings.TrimSpace(seatAddOpts.kind), Agent: seatAddOpts.agent,
	})
	if err != nil {
		return err
	}
	c.Printf("seat %s created in %s (kind %s, %s)\n", x.Name, x.ProjectKey, x.Kind, x.State)
	if x.RelCwd != "" {
		c.Printf("  directory: %s\n", x.RelCwd)
	}
	c.Printf("  bind a session to it with `gofer seat bind %s`\n", seatRef(x))
	return nil
}

// absoluteSeatCwd resolves a relative directory beside the current one, so the project it belongs
// to can be found; a rooted path (a host path, or the container's /d/... spelling) is kept exactly
// as typed.
func absoluteSeatCwd(cwd string) (string, error) {
	if filepath.IsAbs(cwd) || strings.HasPrefix(cwd, "/") {
		return cwd, nil
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve --cwd %s: %w", cwd, err)
	}
	return abs, nil
}

// seatCurrentRef is how a seat names what sits in it: the terminal session id shortened to its
// readable prefix, the job id of an acp seat in full (it is the handle a person copies into
// `gofer job show`). An empty seat is "-".
func seatCurrentRef(x client.Seat) string { return seatRefLabel(x.Kind, x.CurrentRef) }

// seatRefLabel is seatCurrentRef for a kind and a ref that need not come from the same row (a
// fresh bind answers with the new ref).
func seatRefLabel(kind, ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "-"
	}
	if kind == jobstore.SeatKindACP {
		return ref
	}
	return shortSID(ref)
}

func runSeatShow(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	c.Printf("seat:       %s\nproject:    %s\nkind:       %s\nagent:      %s\ndir:        %s\nstate:      %s\npurpose:    %s\ncharter by: %s\ncreated:    %s ago\nupdated:    %s ago\n",
		x.Name, x.ProjectKey, x.Kind, dash(x.Agent), dash(x.RelCwd), x.State, dash(x.Purpose), dash(x.CharterBy),
		ago(x.CreatedAt), ago(x.UpdatedAt))
	if x.CurrentRef != "" {
		c.Printf("current:    %s (%s)\n", seatCurrentRef(x), x.State)
	} else {
		c.Println("current:    - (vacant)")
	}
	if x.ThreadID != "" {
		c.Printf("thread:     %s\n", x.ThreadID)
	}
	if x.Charter != "" {
		c.Printf("\ncharter:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(x.Charter), "\n", "\n  "))
	}
	return nil
}

func runSeatSet(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	p := client.SeatPatch{}
	// "-" clears a field (the convention `gofer memory set` uses); an empty flag leaves it alone.
	if v := strings.TrimSpace(seatSetOpts.purpose); v != "" {
		if v == "-" {
			v = ""
		}
		p.Purpose = &v
	}
	if v := strings.TrimSpace(seatSetOpts.agent); v != "" {
		if v == "-" {
			v = ""
		}
		p.Agent = &v
	}
	charter, err := seatCharter(seatSetOpts.charter, seatSetOpts.template)
	if err != nil {
		return err
	}
	if charter != "" {
		p.Charter = &charter
	}
	if p.Purpose == nil && p.Agent == nil && p.Charter == nil {
		return fmt.Errorf("nothing to change: pass --purpose / --charter / --charter-template / --agent")
	}
	got, err := cli.PatchSeat(x.ID, p)
	if err != nil {
		return err
	}
	c.Printf("seat %s updated (charter_by %s)\n", got.Name, got.CharterBy)
	return nil
}

func runSeatRemove(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	archived := true
	if _, err := cli.PatchSeat(x.ID, client.SeatPatch{Archived: &archived}); err != nil {
		return err
	}
	c.Printf("seat %s archived (the name is free again; history is kept)\n", x.Name)
	return nil
}

// runSeatBind puts a session into the seat: --ref names one, otherwise the session of the
// current directory. It then says whether that session's agent has the relay hook installed,
// because the charter only reaches a session whose SessionStart hook runs.
func runSeatBind(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(seatBindOpts.ref)
	var a client.AgentSession
	if ref == "" {
		sid, err := resolveCurrentSession(cli)
		if err != nil {
			return err
		}
		ref = sid
	} else if x.Kind != jobstore.SeatKindACP {
		if ref, err = resolveSessionID(cli, ref); err != nil {
			return err
		}
	}
	got, err := cli.BindSeat(x.ID, ref, seatBindOpts.replace)
	if err != nil {
		if client.StatusOf(err) == 409 {
			return fmt.Errorf("%w\n  the seat is held by another session, or the session sits in another seat; add --replace to take it over", err)
		}
		return err
	}
	c.Printf("seat %s now holds session %s\n", got.Name, seatRefLabel(x.Kind, got.CurrentRef))
	if x.Kind == jobstore.SeatKindACP {
		// An acp seat is a gofer session job: its opening prompt carries the seat block, so no
		// terminal relay hook is involved and there is nothing to warn about. A job that was
		// still live at bind time got the charter as a message right away (or why it did not).
		switch {
		case got.CharterSent:
			c.Printf("  %s\n", cliText("cli.seat.bind.charter_sent"))
		case got.CharterError != "":
			c.Printf("  %s\n", cliText("cli.seat.bind.charter_failed", "reason", got.CharterError))
		}
		return nil
	}
	c.Printf("  charter injection needs the relay hook: `gofer init hooks`\n")
	if d, derr := cli.GetSession(ref, 0, ""); derr == nil {
		a = d.Session
		seatBindRelayHint(c, cli, x, a)
	}
	return nil
}

// seatBindRelayHint says whether the seat's charter will reach the session. It looks where gofer
// installs relay hooks — under the project root and at the user level — because a session's
// SessionStart fires from either one, and it says "cannot tell" instead of claiming a missing
// hook when the check cannot see the session's machine or the agent has no gofer hook template.
func seatBindRelayHint(c *gcli.Command, cli *client.Client, x client.Seat, a client.AgentSession) {
	agent := strings.TrimSpace(a.Agent)
	if r := strings.TrimSpace(a.Runner); r != "" && !config.IsLocalRunnerName(r) {
		c.Printf("  the session runs on %s: gofer cannot check its hooks from here (run `gofer init hooks` there if the charter does not arrive)\n", r)
		return
	}
	level, known := hookrelay.FindRelayHooks(agent, seatRootForAgent(cli, x))
	switch {
	case !known:
		c.Printf("  gofer cannot check the %s hook config; if the charter does not arrive, install it with `gofer init hooks`\n", dash(agent))
	case level != "":
		c.Printf("  relay hook installed for %s (%s level): the charter reaches it on the next start\n", agent, level)
	default:
		c.Printf("  WARNING: %s has no gofer relay hook in this project or at the user level, so the charter will NOT be injected; install it with `gofer init hooks`\n", dash(agent))
	}
}

func runSeatUnbind(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	got, err := cli.UnbindSeat(x.ID)
	if err != nil {
		return err
	}
	c.Printf("seat %s is vacant again\n", got.Name)
	return nil
}

// runSeatOpen prints how to get back into the seat: the command to type and on which machine.
// An acp seat is opened as a workbench thread instead; an ended ACP session is started again
// with --restart.
func runSeatOpen(c *gcli.Command, _ []string) error {
	cli, err := seatClient()
	if err != nil {
		return err
	}
	x, err := findSeat(cli, seatRefOpts.project, c.Arg("name").String())
	if err != nil {
		return err
	}
	if seatOpenOpts.restart {
		p, err := cli.RestartSeat(x.ID)
		if err != nil {
			return err
		}
		c.Printf("restarted seat %s as job %s (session %s)\n", x.Name, p.JobID, dash(p.ThreadID))
		if p.ThreadID != "" {
			c.Printf("  workbench thread: %s\n", p.ThreadID)
		}
		return nil
	}
	p, err := cli.SeatOpenPlan(x.ID)
	if err != nil {
		return err
	}
	if x.Kind == jobstore.SeatKindACP {
		printSeatACP(c, p)
		return nil
	}
	printSeatTerminal(c, p)
	return nil
}

func printSeatACP(c *gcli.Command, p client.SeatOpenPlan) {
	if p.ThreadID != "" {
		c.Printf("seat thread: %s\n", p.ThreadID)
		c.Printf("  open it in the gofer web console (workbench) on the server\n")
	}
	if p.NeedsRestart {
		c.Printf("its session job has ended; start it again with `gofer seat open <name> --restart`\n")
	}
}

func printSeatTerminal(c *gcli.Command, p client.SeatOpenPlan) {
	if p.Resumable {
		c.Printf("run this on %s (cwd %s):\n", dash(p.Runner), dash(p.Cwd))
		if p.Alive {
			c.Println("  note: that session is still running there; continuing it starts a second copy of the conversation")
		}
		switch seatOpenOpts.shell {
		case "sh":
			c.Printf("  %s\n", p.Sh)
		case "ps", "powershell":
			c.Printf("  %s\n", p.PowerShell)
		default:
			if runtime.GOOS == "windows" {
				c.Printf("  %s\n", p.PowerShell)
				c.Printf("  (sh) %s\n", p.Sh)
			} else {
				c.Printf("  %s\n", p.Sh)
				c.Printf("  (PowerShell) %s\n", p.PowerShell)
			}
		}
		return
	}
	c.Printf("this seat cannot be resumed (%s); start a fresh session instead:\n", p.Reason)
	if p.Cwd != "" {
		c.Printf("  cwd: %s\n", p.Cwd)
	}
	sh, ps := p.NewSh, p.NewPowerShell
	switch seatOpenOpts.shell {
	case "sh":
		c.Printf("  %s\n", sh)
	case "ps", "powershell":
		c.Printf("  %s\n", ps)
	default:
		if runtime.GOOS == "windows" {
			c.Printf("  %s\n", ps)
			c.Printf("  (sh) %s\n", sh)
		} else {
			c.Printf("  %s\n", sh)
			c.Printf("  (PowerShell) %s\n", ps)
		}
	}
}

// seatRef is how a seat is named in the commands that accept one: bare in the current project,
// <name>@<project> across projects.
func seatRef(x client.Seat) string {
	if x.ProjectKey == "" {
		return x.Name
	}
	return x.Name + "@" + x.ProjectKey
}

func dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// seatRootForAgent is the project root the relay hooks of the seat's project live in (host
// path, the machine the CLI and the hooks run on).
func seatRootForAgent(cli *client.Client, x client.Seat) string {
	if p, ok := projectOf(cli, x.ProjectKey); ok {
		if p.HostPath != "" {
			return p.HostPath
		}
		return p.ContainerPath
	}
	return ""
}

func projectOf(cli *client.Client, key string) (client.ProjectMeta, bool) {
	projects, err := cli.ListProjects()
	if err != nil {
		return client.ProjectMeta{}, false
	}
	for _, p := range projects {
		if p.Key == key {
			return p, true
		}
	}
	return client.ProjectMeta{}, false
}

// relToRoot is the slash-separated directory of cwd relative to root ("" = cwd is the root);
// "" also when cwd is outside root.
func relToRoot(cwd, root string) string {
	rel, ok := inRoot(cwd, root)
	if !ok {
		return ""
	}
	return rel
}

// inRoot reports cwd relative to root in slash form, and whether cwd really is under root.
func inRoot(cwd, root string) (string, bool) {
	root = strings.TrimRight(strings.ReplaceAll(root, "\\", "/"), "/")
	c := strings.TrimRight(strings.ReplaceAll(cwd, "\\", "/"), "/")
	if root == "" || c == "" {
		return "", false
	}
	if c == root {
		return "", true
	}
	if !strings.HasPrefix(c, root+"/") {
		return "", false
	}
	return strings.TrimPrefix(c, root+"/"), true
}
