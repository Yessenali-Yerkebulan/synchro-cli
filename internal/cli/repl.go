package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/peterh/liner"
	"github.com/synchro/synchro-cli/internal/agents"
	"github.com/synchro/synchro-cli/internal/llm"
	"github.com/synchro/synchro-cli/internal/model"
	"github.com/synchro/synchro-cli/internal/repo"
	"github.com/synchro/synchro-cli/internal/store"
	"github.com/synchro/synchro-cli/internal/ui"
)

// runREPL is the interactive shell: the default when synchro is run with no
// arguments on a terminal.
func runREPL(a *App) error {
	// runCtl owns the cancellable context for the command in flight. The
	// signal goroutine and the read loop both touch it, so every access goes
	// through the mutex rather than through bare variables.
	rc := newRunCtl(a.Ctx)
	defer rc.dispose()
	rc.attach(a)

	var running atomic.Bool

	// Ctrl+C cancels a run in progress; a second one, or Ctrl+C at the prompt,
	// leaves the shell.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	go func() {
		for range sig {
			if running.Load() {
				if rc.cancelRun() {
					a.P.Printf("\n%s %s\n", a.P.Yellow("!"), a.P.Yellow("cancelled - Ctrl+C again to exit"))
				}
				continue
			}
			a.P.Println("")
			leave()
		}
	}()

	// The logo goes first: it is the thing you see when the shell opens, and
	// the welcome text reads as a caption under it.
	printBanner(a)

	if len(a.Store.Workspaces()) == 0 {
		printWelcome(a)
		if a.P.IsTTY {
			if confirm(a, false, "Set up now? (creates a workspace and a starter team)") {
				if err := quickSetup(a); err != nil {
					return err
				}
			}
		}
	}

	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)
	line.SetMultiLineMode(true)
	line.SetTabCompletionStyle(liner.TabPrints)
	line.SetCompleter(func(l string) []string { return completer(a, l) })

	hist := newHistory(a)
	for _, h := range hist.lines {
		line.AppendHistory(h)
	}
	defer hist.save()

	printContextBar(a)

	for {
		input, err := line.Prompt(promptFor(a))
		if err != nil {
			if err == liner.ErrPromptAborted {
				// Ctrl+C at an empty prompt is a request to exit.
				if strings.TrimSpace(input) == "" {
					printGoodbye(a)
					return nil
				}
				continue
			}
			if errors.Is(err, io.EOF) {
				printGoodbye(a)
				return nil
			}
			return err
		}
		hist.add(input)
		line.AppendHistory(input)

		text := strings.TrimSpace(input)
		if text == "" {
			continue
		}
		// A trailing backslash continues the line, for long prompts.
		for strings.HasSuffix(text, "\\") {
			text = strings.TrimSuffix(text, "\\")
			cont, err := line.Prompt(a.P.Gray("    "))
			if err != nil {
				break
			}
			hist.add(cont)
			line.AppendHistory(cont)
			text += " " + strings.TrimSpace(cont)
		}

		// A fresh context per command: a cancelled command must not poison
		// the next one.
		rc.begin(a)

		running.Store(true)
		stop, err := dispatch(a, text)
		running.Store(false)
		if err != nil {
			if rc.wasCancelled() {
				a.P.Info("cancelled")
			} else {
				a.P.Error("%v", err)
			}
		}
		rc.end(a)
		if stop {
			printGoodbye(a)
			return nil
		}
	}
}

// runCtl holds the cancellable context used by the command currently running.
// The signal handler cancels it; the read loop replaces it between commands.
// Both sides can be active at once, hence the mutex.
type runCtl struct {
	base      context.Context
	mu        sync.Mutex
	cancel    context.CancelFunc
	cancelled bool
}

func newRunCtl(base context.Context) *runCtl {
	if base == nil {
		base = context.Background()
	}
	return &runCtl{base: base}
}

// begin arms a new context for the next command.
func (c *runCtl) begin(a *App) {
	c.mu.Lock()
	c.cancelled = false
	c.cancel = func() {}
	c.mu.Unlock()
	c.attach(a)
}

// end tears the context down so nothing leaks between commands.
func (c *runCtl) end(a *App) {
	c.mu.Lock()
	if c.cancel != nil {
		c.cancel()
	}
	c.cancel = nil
	c.mu.Unlock()
	c.attach(a)
}

func (c *runCtl) attach(a *App) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		ctx, cancel := context.WithCancel(c.base)
		c.cancel = cancel
		a.SetContext(ctx)
		return
	}
	// Between commands the shell runs on a plain background context, so a
	// stray Ctrl+C cannot cancel the next command.
	a.SetContext(c.base)
}

// cancelRun cancels the in-flight command. It reports whether there was one.
func (c *runCtl) cancelRun() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel == nil {
		return false
	}
	c.cancelled = true
	c.cancel()
	return true
}

func (c *runCtl) wasCancelled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cancelled
}

// dispose cancels anything still outstanding, so nothing outlives the shell.
func (c *runCtl) dispose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

// dispatch routes one line of input. It returns true when the shell should exit.
func dispatch(a *App, text string) (bool, error) {
	if !strings.HasPrefix(text, "/") {
		return false, runFreeform(a, text)
	}

	fields := strings.Fields(text)
	cmd := strings.ToLower(fields[0])
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))

	switch cmd {
	case "/exit", "/quit", "/q":
		return true, nil
	case "/help", "/?", "/h":
		printHelp(a)
		return false, nil
	case "/clear", "/cls":
		a.P.Printf("\x1b[2J\x1b[H")
		return false, nil
	case "/context", "/ctx":
		printContextBar(a)
		return false, nil
	case "/banner":
		printBanner(a)
		return false, nil

	case "/ws", "/workspaces", "/workspace":
		return false, replSwitchWorkspace(a, rest)
	case "/team", "/teams":
		return false, replSwitchTeam(a, rest)
	case "/agents", "/agent":
		return false, replSwitchAgent(a, rest)
	case "/projects", "/project", "/proj":
		return false, replSwitchProject(a, rest)

	case "/run":
		if rest == "" {
			return false, fmt.Errorf("/run needs a task, e.g. /run write a README")
		}
		return false, runFreeform(a, rest)
	case "/pipeline", "/pipe", "/p":
		return false, replPipeline(a, rest)
	case "/mode":
		return false, replSetMode(a, rest)
	case "/wf", "/flow", "/workflow":
		return false, replWorkflow(a, rest)

	case "/tasks":
		return false, replTasks(a)
	case "/commit":
		return false, replCommit(a, rest)
	case "/files", "/code":
		return false, replFiles(a)
	case "/report":
		return false, replReport(a)
	case "/models", "/model":
		return false, replModels(a, rest)
	case "/keys":
		return false, replKeys(a)
	case "/config":
		return false, replConfig(a)
	case "/provider":
		return false, replProvider(a, rest)
	case "/status", "/stats":
		return false, replStatus(a)
	case "/doctor":
		return false, replDoctor(a)
	case "/new":
		return false, replNewProject(a, rest)
	case "/history":
		return false, replHistory(a)
	case "/reset":
		return false, replReset(a)
	}

	// Unknown slash command: try it as a task, since "/summarise this" is a
	// perfectly reasonable thing to type.
	if looksLikeTask(text) {
		return false, runFreeform(a, strings.TrimPrefix(text, "/"))
	}
	return false, fmt.Errorf("unknown command %s. Try /help", cmd)
}

// looksLikeTask is a small heuristic for "did they mean a command or a task".
func looksLikeTask(s string) bool {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return false
	}
	switch strings.ToLower(fields[0]) {
	case "/a", "/add", "/do", "/make", "/write", "/build", "/create", "/fix", "/explain", "/review", "/analyze", "/analyse":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// running tasks
// ---------------------------------------------------------------------------

// runFreeform treats the line as a task for the active agent.
func runFreeform(a *App, text string) error {
	ag, err := a.RequireAgent()
	if err != nil {
		return err
	}
	var projectID string
	if p, perr := a.resolveProject(); perr == nil && p != nil {
		projectID = p.ID
	} else if ag.Role == model.RoleDeveloper {
		return fmt.Errorf("a developer agent needs a project to commit code into. Create one: /new <name>")
	}

	t := &model.Task{
		ProjectID:       projectID,
		AssignedAgentID: ag.ID,
		Title:           summarize(text),
		Description:     text,
	}
	if projectID != "" {
		if err := a.Store.CreateTask(t); err != nil {
			return err
		}
	}
	_, err = a.ExecuteTask(t, ag, t.Title)
	return err
}

// ---------------------------------------------------------------------------
// context switching
// ---------------------------------------------------------------------------

func replSwitchWorkspace(a *App, rest string) error {
	list := a.Store.Workspaces()
	if rest == "" {
		return printIndexed(a, "WORKSPACE", "/ws", list, func(w model.Workspace) string {
			if w.Description != "" {
				return w.Description
			}
			return fmt.Sprintf("%d teams, %d projects", len(a.Store.Teams(w.ID)), len(a.Store.Projects(w.ID)))
		})
	}
	w, err := a.Store.Workspace(rest)
	if err != nil {
		return fmt.Errorf("no workspace called %q", rest)
	}
	if err := a.SetActive(w.ID, "", "", ""); err != nil {
		return err
	}
	a.P.Success("workspace -> %s", a.P.Bold(w.Name))
	printContextBar(a)
	return nil
}

func replSwitchTeam(a *App, rest string) error {
	ws, err := a.RequireWorkspace()
	if err != nil {
		return err
	}
	if rest == "" {
		list := a.Store.Teams(ws.ID)
		return printIndexed(a, "TEAM", "/team", list, func(t model.Team) string {
			return fmt.Sprintf("%d agents, %d workflows", len(a.Store.Agents(t.ID)), len(a.Store.Workflows(t.ID)))
		})
	}
	t, err := a.Store.Team(rest, ws.ID)
	if err != nil {
		return fmt.Errorf("no team called %q in %s", rest, ws.Name)
	}
	if err := a.SetActive(ws.ID, t.ID, "", ""); err != nil {
		return err
	}
	a.P.Success("team -> %s", a.P.Bold(t.Name))
	printContextBar(a)
	return nil
}

func replSwitchAgent(a *App, rest string) error {
	team, err := a.RequireTeam()
	if err != nil {
		return err
	}
	list := a.Store.Agents(team.ID)
	if rest == "" {
		return printIndexed(a, "AGENT", "/agent", list, func(ag model.Agent) string {
			return fmt.Sprintf("%s - %s/%s", ag.Role, ag.Provider, ag.Model)
		})
	}
	ag, err := a.Store.Agent(rest, team.ID)
	if err != nil {
		return fmt.Errorf("no agent called %q in %s", rest, team.Name)
	}
	if err := a.SetActive("", "", ag.ID, ""); err != nil {
		return err
	}
	a.P.Success("agent -> %s %s", a.P.Bold(ag.Name), a.P.Gray(fmt.Sprintf("(%s, %s/%s)", ag.Role, ag.Provider, ag.Model)))
	return nil
}

func replSwitchProject(a *App, rest string) error {
	ws, err := a.RequireWorkspace()
	if err != nil {
		return err
	}
	if rest == "" {
		list := a.Store.Projects(ws.ID)
		return printIndexed(a, "PROJECT", "/project", list, func(p model.Project) string {
			return fmt.Sprintf("%d tasks, %d commits", len(a.Store.Tasks(p.ID)), len(repo.Log(a.Store.ReposDir(), p.ID, 1000)))
		})
	}
	if strings.EqualFold(rest, "none") {
		if err := a.ClearActive("project"); err != nil {
			return err
		}
		a.P.Success("project -> none")
		return nil
	}
	p, err := a.Store.Project(rest, ws.ID)
	if err != nil {
		return fmt.Errorf("no project called %q", rest)
	}
	if err := a.SetActive(ws.ID, "", "", p.ID); err != nil {
		return err
	}
	a.P.Success("project -> %s", a.P.Bold(p.Name))
	return nil
}

func replNewProject(a *App, rest string) error {
	if rest == "" {
		return fmt.Errorf("give the project a name: /new habit tracker")
	}
	ws, err := a.RequireWorkspace()
	if err != nil {
		return err
	}
	team, err := a.RequireTeam()
	if err != nil {
		return err
	}
	p := &model.Project{WorkspaceID: ws.ID, TeamID: team.ID, Name: rest}
	if err := a.Store.CreateProject(p); err != nil {
		return err
	}
	if err := a.SetActive(ws.ID, team.ID, "", p.ID); err != nil {
		return err
	}
	a.P.Success("project %s created - code will be committed to %s", a.P.Bold(p.Name), a.P.Gray(filepath.Join(a.Store.ReposDir(), p.ID)))
	return nil
}

// ---------------------------------------------------------------------------
// pipelines and workflows
// ---------------------------------------------------------------------------

// replPipeline runs a multi-agent chain. /pipeline <mode> <text>, defaulting to
// the mode set with /mode.
func replPipeline(a *App, rest string) error {
	if rest == "--list" || rest == "list" || rest == "?" {
		return listPipelineModes(a)
	}
	// Accept the same --mode flag the CLI command takes, so muscle memory
	// carries over between the shell and one-shot invocations.
	mode := defaultPipelineMode(a.Store.Config())
	trimmed := strings.TrimSpace(rest)
	// Both flag spellings are accepted, and the goal is whatever follows the
	// mode. "--mode=idea" and "--mode idea" behave the same.
	if trimmed == "--mode" {
		return fmt.Errorf("/pipeline --mode needs a mode name, e.g. /pipeline --mode review <what to do>")
	}
	if strings.HasPrefix(trimmed, "--mode=") || strings.HasPrefix(trimmed, "--mode ") {
		fields := strings.Fields(trimmed)
		if len(fields) < 2 {
			return fmt.Errorf("/pipeline --mode needs a mode name, e.g. /pipeline --mode review <what to do>")
		}
		if value, ok := strings.CutPrefix(fields[0], "--mode="); ok {
			// The value shares the first token, so the goal starts at the
			// second one.
			mode = value
			rest = strings.Join(fields[1:], " ")
		} else {
			mode = fields[1]
			rest = strings.Join(fields[2:], " ")
		}
		rest = strings.TrimSpace(rest)
	} else if fields := strings.Fields(trimmed); len(fields) > 0 {
		// A leading bare word that names a mode selects it; anything else is
		// the goal.
		if pm, ok := agents.PipelineModeByID(fields[0]); ok {
			mode = pm.ID
			rest = strings.TrimSpace(trimmed[len(fields[0]):])
		}
	}
	if rest == "" {
		return fmt.Errorf("/pipeline needs something to work on, e.g. /pipeline a habit tracker with a web UI")
	}
	pm, ok := agents.PipelineModeByID(mode)
	if !ok {
		return fmt.Errorf("unknown mode %q. Try /pipeline --list", mode)
	}
	team, err := a.RequireTeam()
	if err != nil {
		return err
	}
	p, err := a.requireProjectForTask()
	if err != nil {
		return err
	}
	stages := agents.BuildStages(a.Store.Agents(team.ID), pm, rest)
	return a.runPipeline(p, pm, stages, rest)
}

func replSetMode(a *App, rest string) error {
	if rest == "" {
		a.P.Info("default pipeline mode is %q. Change it with: /mode <id>", defaultPipelineMode(a.Store.Config()))
		return listPipelineModes(a)
	}
	pm, ok := agents.PipelineModeByID(rest)
	if !ok {
		return fmt.Errorf("unknown mode %q. Run /pipeline --list", rest)
	}
	if err := a.Store.SetConfig(func(c *store.Config) { c.PipelineMode = pm.ID }); err != nil {
		return err
	}
	a.P.Success("default pipeline mode -> %s (%s)", pm.ID, pm.Title)
	return nil
}

func defaultPipelineMode(cfg store.Config) string {
	if cfg.PipelineMode != "" {
		return cfg.PipelineMode
	}
	return "idea"
}

// replWorkflow lists workflows, or runs one: /wf <name> <input>.
func replWorkflow(a *App, rest string) error {
	team, err := a.RequireTeam()
	if err != nil {
		return err
	}
	if rest == "" {
		list := a.Store.Workflows(team.ID)
		if len(list) == 0 {
			a.P.Info("no workflows in %s yet.", team.Name)
			a.P.Info("create one:  synchro-cli wf new \"<name>\" --agents researcher,developer,qa")
			return nil
		}
		rows := make([][]string, 0, len(list))
		for _, w := range list {
			rows = append(rows, []string{w.Name, w.ID, fmt.Sprintf("%d nodes", len(w.Graph.Nodes)), fmt.Sprintf("%d runs", len(a.Store.Executions(w.ID)))})
		}
		a.P.Table([]string{"WORKFLOW", "ID", "GRAPH", "HISTORY"}, rows)
		a.P.Blank()
		a.P.Hint("run one: /wf <name> <what to work on>")
		return nil
	}
	fields := strings.Fields(rest)
	wf, err := a.Store.Workflow(fields[0], team.ID)
	if err != nil {
		return fmt.Errorf("no workflow called %q in %s", fields[0], team.Name)
	}
	input := strings.TrimSpace(strings.TrimPrefix(rest, fields[0]))
	if input == "" {
		return fmt.Errorf("/wf %s needs something to work on", wf.Name)
	}
	return a.runWorkflow(wf, input)
}

// ---------------------------------------------------------------------------
// read-only helpers
// ---------------------------------------------------------------------------

func replTasks(a *App) error {
	project, err := a.resolveProject()
	if err != nil {
		return err
	}
	var tasks []model.Task
	if project != nil {
		tasks = a.Store.Tasks(project.ID)
	} else {
		tasks = a.Store.Tasks("")
	}
	if len(tasks) == 0 {
		a.P.Info("no tasks yet.")
		return nil
	}
	rows := make([][]string, 0, len(tasks))
	for i, t := range tasks {
		agentName := "—"
		if ag, err := a.Store.Agent(t.AssignedAgentID, ""); err == nil {
			agentName = ag.Name
		}
		extra := ""
		if t.Result != nil {
			if n := len(t.Result.Files); n > 0 {
				extra = fmt.Sprintf("%d files", n)
			}
			if t.Result.Repo != nil {
				extra = "committed " + t.Result.Repo.Commit
			}
		}
		rows = append(rows, []string{strconv.Itoa(i + 1), t.ID, string(t.Status), agentName, ui.Truncate(t.Title, 40), extra})
	}
	a.P.Table([]string{"#", "ID", "STATUS", "AGENT", "TITLE", ""}, rows)
	a.P.Blank()
	a.P.Hint("read one: synchro-cli task show <id>   -   commit its files: /commit <id>")
	return nil
}

func replCommit(a *App, rest string) error {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return fmt.Errorf("/commit needs a task id. List them with /tasks")
	}
	t, err := a.Store.Task(fields[0])
	if err != nil {
		return fmt.Errorf("no task with id or title %q", fields[0])
	}
	e := a.NewEngine()
	info, err := e.CommitFiles(t.ID)
	if err != nil {
		return err
	}
	a.P.Success("committed %d file(s) as %s", len(t.Result.Files), info.Commit)
	return nil
}

func replFiles(a *App) error {
	p, err := a.resolveProject()
	if err != nil || p == nil {
		return fmt.Errorf("no project selected. Create one with /new <name>")
	}
	files := repo.ListFiles(a.Store.ReposDir(), p.ID)
	if len(files) == 0 {
		a.P.Info("no committed code in %s yet.", p.Name)
		return nil
	}
	for _, f := range files {
		a.P.Printf("  %s\n", f)
	}
	a.P.Blank()
	a.P.Hint("repo: %s", filepath.Join(a.Store.ReposDir(), p.ID))
	return nil
}

func replReport(a *App) error {
	p, err := a.resolveProject()
	if err != nil || p == nil {
		return fmt.Errorf("no project selected. Create one with /new <name>")
	}
	team, err := a.Store.Team(p.TeamID, "")
	if err != nil {
		return fmt.Errorf("this project's team is gone: %w", err)
	}
	teamAgents := a.Store.Agents(team.ID)
	if len(teamAgents) == 0 {
		return fmt.Errorf("this project's team has no agents to write the report")
	}
	// A PM writes a client-facing document better than a developer.
	reporter := &teamAgents[0]
	for i := range teamAgents {
		if teamAgents[i].Role == model.RoleProductManager {
			reporter = &teamAgents[i]
			break
		}
	}
	t := &model.Task{
		ProjectID:       p.ID,
		AssignedAgentID: reporter.ID,
		Title:           "Delivery report for " + p.Name,
		Description:     buildReportPrompt(a, p, teamAgents),
	}
	if err := a.Store.CreateTask(t); err != nil {
		return err
	}
	res, err := a.ExecuteTaskQuiet(t, reporter, t.Title)
	if err != nil {
		return err
	}
	p.DeliveryReport = res.Output
	p.DeliveryReportAt = nowUTC()
	if err := a.Store.UpdateProject(p); err != nil {
		return err
	}
	a.P.Blank()
	a.P.Markdown(res.Output)
	a.P.Blank()
	a.P.Hint("saved to the project. Write it to a file with: synchro-cli report %s --save", p.Name)
	return nil
}

func replModels(a *App, rest string) error {
	return printModelTable(a, rest)
}

func replKeys(a *App) error   { return listKeys(a) }
func replConfig(a *App) error { return showConfig(a) }
func replDoctor(a *App) error { return runDoctor(a) }

func replStatus(a *App) error {
	cfg := a.Store.Config()
	scope := a.Resolve()
	a.P.Title("status")
	if scope.Workspace != nil {
		a.P.KeyValue("workspace", scope.Workspace.Name)
	}
	if scope.Team != nil {
		a.P.KeyValue("team", scope.Team.Name)
	}
	if scope.Agent != nil {
		a.P.KeyValue("agent", fmt.Sprintf("%s (%s)", scope.Agent.Name, scope.Agent.Role))
		a.P.KeyValue("model", fmt.Sprintf("%s/%s", scope.Agent.Provider, scope.Agent.Model))
		a.P.KeyValue("free", boolStr(llm.IsFreeModel(scope.Agent.Provider, scope.Agent.Model)))
	}
	if scope.Project != nil {
		a.P.KeyValue("project", fmt.Sprintf("%s (%s)", scope.Project.Name, scope.Project.ID))
		a.P.KeyValue("tasks", fmt.Sprintf("%d", len(a.Store.Tasks(scope.Project.ID))))
		commits := repo.Log(a.Store.ReposDir(), scope.Project.ID, 1000)
		a.P.KeyValue("commits", fmt.Sprintf("%d", len(commits)))
		if scope.Project.SpentUSD > 0 {
			a.P.KeyValue("estimated spend", money(scope.Project.SpentUSD))
		}
	}
	a.P.KeyValue("pipeline mode", defaultPipelineMode(cfg))
	a.P.KeyValue("state", a.Store.Dir())
	a.P.Blank()
	a.P.Hint("nothing in synchro is ever billed to you. Any spend is between you and your model provider.")
	return nil
}

func replHistory(a *App) error {
	ws, err := a.RequireWorkspace()
	if err != nil {
		return err
	}
	tasks := a.Store.Tasks("")
	filtered := make([]model.Task, 0, len(tasks))
	for _, t := range tasks {
		var project *model.Project
		if t.ProjectID != "" {
			project, _ = a.Store.Project(t.ProjectID, "")
		}
		if project == nil || project.WorkspaceID == ws.ID {
			filtered = append(filtered, t)
		}
	}
	if len(filtered) == 0 {
		a.P.Info("no tasks yet in %s.", ws.Name)
		return nil
	}
	rows := make([][]string, 0, len(filtered))
	for _, t := range filtered {
		when := t.CreatedAt.Local().Format("01-02 15:04")
		agentName := "—"
		if ag, err := a.Store.Agent(t.AssignedAgentID, ""); err == nil {
			agentName = ag.Name
		}
		rows = append(rows, []string{t.ID, when, string(t.Status), agentName, ui.Truncate(t.Title, 44)})
	}
	a.P.Table([]string{"ID", "WHEN", "STATUS", "AGENT", "TITLE"}, rows)
	a.P.Blank()
	a.P.Hint("re-run one: synchro-cli task run <id>")
	return nil
}

// replReset clears the terminal selection so the shell auto-resolves again.
func replReset(a *App) error {
	if err := a.Store.SetConfig(func(c *store.Config) {
		c.ActiveWorkspaceID = ""
		c.ActiveTeamID = ""
		c.ActiveAgentID = ""
		c.ActiveProjectID = ""
	}); err != nil {
		return err
	}
	a.P.Success("context cleared — synchro will auto-select again")
	printContextBar(a)
	return nil
}

// replProvider changes the active agent's provider and model.
func replProvider(a *App, rest string) error {
	ag, err := a.RequireAgent()
	if err != nil {
		return err
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		a.P.KeyValue("current", fmt.Sprintf("%s/%s", ag.Provider, ag.Model))
		a.P.Info("change it: /provider <name> [model]")
		return nil
	}
	if _, ok := llm.Lookup(fields[0]); !ok {
		return fmt.Errorf("unknown provider %q. Known: %s", fields[0], strings.Join(llm.ProviderNames(), ", "))
	}
	ag.Provider = strings.ToLower(fields[0])
	if len(fields) > 1 {
		ag.Model = fields[1]
	} else {
		free := llm.CuratedModels(ag.Provider)
		if len(free) > 0 {
			ag.Model = free[0].ID
		}
	}
	if err := a.Store.UpdateAgent(ag); err != nil {
		return err
	}
	msg := fmt.Sprintf("%s -> %s/%s", ag.Name, ag.Provider, ag.Model)
	if llm.IsFreeModel(ag.Provider, ag.Model) {
		a.P.Success("%s (free)", msg)
	} else {
		a.P.Success("%s", msg)
		a.P.Hint("this model is billable to your own key - see: /models %s", ag.Provider)
	}
	return nil
}

// ---------------------------------------------------------------------------
// chrome
// ---------------------------------------------------------------------------

func printBanner(a *App) {
	p := a.P
	p.Wordmark(fmt.Sprintf("v%s  ·  free, local, open source", Version))
	p.Printf("  %s\n", p.Gray("No account, no subscription, no server. Your data lives in "+a.Store.Dir()))
	p.Println()
}

func printWelcome(a *App) {
	p := a.P
	p.Printf("\n  %s\n", p.Bold(p.Magenta("An AI team in your terminal.")))
	p.Printf("\n  %s\n\n", p.Gray("Give it a goal and the agents do the work:"))
	p.Printf("    %s  %s\n", p.Bold("research"), p.Gray("grounds the plan in real sources"))
	p.Printf("    %s  %s\n", p.Bold("spec    "), p.Gray("turns a rough idea into something buildable"))
	p.Printf("    %s  %s\n", p.Bold("build   "), p.Gray("writes real files, committed to git"))
	p.Printf("    %s  %s\n\n", p.Bold("review  "), p.Gray("finds the bugs before you do"))
	p.Printf("  %s\n", p.Gray("It runs against a local Ollama install by default - no key, no cost, works offline."))
	p.Printf("  %s\n\n", p.Gray("Type /help for commands."))
}

func printGoodbye(a *App) {
	a.P.Printf("\n%s\n", a.P.Gray("bye"))
}

func leave() {
	fmt.Fprintln(os.Stdout, "")
	os.Exit(0)
}

// promptFor renders the context line before the input cursor, so the active
// workspace, team and agent are always visible.
func promptFor(a *App) string {
	p := a.P
	s := a.Resolve()
	parts := []string{}
	if s.Workspace != nil {
		parts = append(parts, p.Cyan(s.Workspace.Name))
	}
	if s.Team != nil {
		parts = append(parts, p.Blue(s.Team.Name))
	}
	if s.Agent != nil {
		parts = append(parts, p.Magenta(s.Agent.Name))
	}
	if s.Project != nil {
		parts = append(parts, p.Green(s.Project.Name))
	}
	if len(parts) == 0 {
		return p.Bold("synchro") + " " + p.Gray("> ")
	}
	return p.Bold("synchro") + " " + p.Gray("> ") + strings.Join(parts, p.Gray(" > ")) + " " + p.Gray("> ")
}

// printContextBar shows the resolved context and what to do next.
func printContextBar(a *App) {
	p := a.P
	s := a.Resolve()
	if s.Workspace == nil {
		p.Hint("no workspace yet - run: synchro-cli init")
		return
	}
	sep := p.Gray(" > ")
	line := "  " + p.Cyan(s.Workspace.Name)
	if s.Team != nil {
		line += sep + p.Blue(s.Team.Name)
	}
	if s.Agent != nil {
		line += sep + p.Magenta(s.Agent.Name) + p.Gray(" ("+string(s.Agent.Role)+", "+s.Agent.Provider+"/"+s.Agent.Model+")")
	}
	if s.Project != nil {
		line += sep + p.Green(s.Project.Name)
	} else {
		line += sep + p.Gray("(no project)")
	}
	p.Printf("%s\n", line)
	switch {
	case s.Agent == nil:
		p.Hint("pick an agent: /agents     -     or create a team: /help")
	case s.Project == nil:
		p.Hint("create a project for generated code: /new <name>")
	default:
		p.Hint("type a task, or /help for commands")
	}
	p.Println("")
}

type helpEntry struct{ cmd, desc string }

var helpTable = []helpEntry{
	{"/help", "show this list"},
	{"/agents, /agent <n>", "list agents, or switch to one"},
	{"/team, /team <n>", "list teams, or switch to one"},
	{"/ws, /ws <name>", "list workspaces, or switch to one"},
	{"/project, /project <n>", "list projects, or switch to one"},
	{"/new <name>", "create a project and make it active"},
	{"/tasks", "list tasks in the current project"},
	{"/commit <id>", "commit a task's generated files to git"},
	{"/files", "list the code generated so far"},
	{"/report", "write a Markdown delivery report"},
	{"", ""},
	{"/pipeline <text>", "run the whole team over a goal"},
	{"/pipeline <mode> <text>", "run a specific chain (idea, build, review, ...)"},
	{"/mode <id>", "set the default pipeline mode"},
	{"/wf <name> <input>", "run a saved multi-agent workflow"},
	{"/wf", "list workflows"},
	{"", ""},
	{"/provider <name> [model]", "switch the active agent's model"},
	{"/models [provider]", "see which models are free"},
	{"/keys", "see which providers are ready"},
	{"/config", "show settings"},
	{"/status", "current context and spend estimate"},
	{"/doctor", "check the setup"},
	{"/context", "redraw the context line"},
	{"/history", "list past tasks"},
	{"/reset", "clear the remembered context"},
	{"/clear", "clear the screen"},
	{"/exit", "quit"},
}

func printHelp(a *App) {
	p := a.P
	p.Title("commands")
	rows := make([][]string, 0, len(helpTable))
	for _, h := range helpTable {
		if h.cmd == "" {
			rows = append(rows, []string{"", ""})
			continue
		}
		rows = append(rows, []string{h.cmd, h.desc})
	}
	p.Table([]string{"COMMAND", "WHAT IT DOES"}, rows)
	p.Blank()
	p.Printf("  %s %s\n", p.Bold("anything else"), p.Gray("is treated as a task for the active agent"))
	p.Printf("  %s %s\n\n", p.Bold("Ctrl+C"), p.Gray("cancels a run; press it again at the prompt to quit"))
	p.Hint("full docs: synchro-cli <command> --help")
}

// printIndexed renders a numbered list for the /switch commands, plus the one
// line that says how to pick from it.
func printIndexed[T any](a *App, label, cmd string, list []T, desc func(T) string) error {
	if len(list) == 0 {
		a.P.Info("none yet")
		return nil
	}
	rows := make([][]string, 0, len(list))
	for i, item := range list {
		rows = append(rows, []string{strconv.Itoa(i + 1), anyName(item), desc(item)})
	}
	a.P.Table([]string{"#", label, ""}, rows)
	a.P.Blank()
	a.P.Hint("switch with: %s <number>", cmd)
	return nil
}

// anyName pulls the Name field out of the three entity types above.
func anyName(v any) string {
	switch t := v.(type) {
	case model.Workspace:
		return t.Name
	case model.Team:
		return t.Name
	case model.Agent:
		return t.Name
	case model.Project:
		return t.Name
	}
	return ""
}

// completer offers slash commands and entity names as tab completions. It
// needs the app to look up names, so it is built as a closure over the session.
func completer(a *App, line string) []string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	// Completing the first word: slash commands.
	if !strings.HasPrefix(line, "/") && len(fields) == 1 {
		return nil
	}
	if strings.HasPrefix(line, "/") && len(fields) == 1 {
		var out []string
		for _, h := range helpTable {
			if h.cmd == "" {
				continue
			}
			if strings.HasPrefix(h.cmd, line) {
				out = append(out, h.cmd)
			}
		}
		sort.Strings(out)
		return out
	}
	// Completing an argument: entity names from the current context.
	prefix := fields[len(fields)-1]
	argCmd := fields[0]
	var out []string
	add := func(name string) {
		if name != "" && strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			out = append(out, name)
		}
	}
	switch argCmd {
	case "/agent", "/agents", "/a":
		if team, err := a.RequireTeam(); err == nil {
			for _, ag := range a.Store.Agents(team.ID) {
				add(ag.Name)
			}
		}
	case "/team", "/teams":
		if ws, err := a.RequireWorkspace(); err == nil {
			for _, t := range a.Store.Teams(ws.ID) {
				add(t.Name)
			}
		}
	case "/ws", "/workspace", "/workspaces":
		for _, w := range a.Store.Workspaces() {
			add(w.Name)
		}
	case "/project", "/projects", "/proj":
		if ws, err := a.RequireWorkspace(); err == nil {
			for _, p := range a.Store.Projects(ws.ID) {
				add(p.Name)
			}
		}
	case "/wf", "/workflow", "/flow":
		if team, err := a.RequireTeam(); err == nil {
			for _, w := range a.Store.Workflows(team.ID) {
				add(w.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

func historyPath(a *App) string { return filepath.Join(a.Store.Dir(), "history") }

// history holds the lines typed this session. liner has no way to read its own
// history back, so we keep the list ourselves and persist it on exit.
type history struct {
	path  string
	lines []string
}

// newHistory seeds the session list from the file written last time, so a
// restart does not lose what was already there.
func newHistory(a *App) *history {
	h := &history{path: historyPath(a)}
	b, err := os.ReadFile(h.path)
	if err != nil {
		return h
	}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line != "" {
			h.lines = append(h.lines, line)
		}
	}
	return h
}

// add records a line, skipping consecutive duplicates so a repeated command
// does not fill the file.
func (h *history) add(line string) {
	line = strings.TrimRight(line, " \t")
	if line == "" {
		return
	}
	if n := len(h.lines); n > 0 && h.lines[n-1] == line {
		return
	}
	h.lines = append(h.lines, line)
}

// save writes the most recent entries. The file is user-only readable.
func (h *history) save() {
	lines := h.lines
	if len(lines) > 500 {
		lines = lines[len(lines)-500:]
	}
	_ = os.WriteFile(h.path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// quickSetup creates the minimum needed to run a task, without the full wizard.
func quickSetup(a *App) error {
	ws := &model.Workspace{Name: "default"}
	if err := a.Store.CreateWorkspace(ws); err != nil {
		return err
	}
	tpl, _ := agents.TemplateByID("mvp")
	if err := createTeamFromTemplate(a, ws, tpl, tpl.Name); err != nil {
		return err
	}
	a.P.Blank()
	a.P.Success("ready. Type a task and press enter.")
	a.P.Hint("or try: /pipeline build a CLI that turns shell recordings into docs")
	return nil
}
