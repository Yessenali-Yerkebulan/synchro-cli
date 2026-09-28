package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/synchro/synchro-cli/internal/ui"
)

// Build metadata, overridable at link time:
//
//	go build -ldflags "-X github.com/synchro/synchro-cli/internal/cli.Version=1.2.3"
var (
	Version = "0.1.0"
	Commit  = "dev"
)

// Options are the global flags.
type Options struct {
	Home      string
	Workspace string
	Team      string
	Agent     string
	Project   string
	JSON      bool
	NoColor   bool
	Yes       bool
}

type rootState struct {
	opt    Options
	cached *App
}

// Execute builds the command tree and runs it. It returns the process exit code.
func Execute() int {
	st := &rootState{}
	root := newRootCmd(st)

	if err := root.Execute(); err != nil {
		// Cobra already printed the error and usage for flag problems; for
		// runtime errors we print a clean message instead of a usage dump.
		if !isUsageError(err) {
			fmt.Fprintln(os.Stderr, "error: "+err.Error())
		}
		return 1
	}
	return 0
}

func isUsageError(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "unknown command") ||
		strings.Contains(msg, "unknown flag") ||
		strings.Contains(msg, "required flag") ||
		strings.Contains(msg, "invalid argument")
}

func newRootCmd(st *rootState) *cobra.Command {
	root := &cobra.Command{
		Use:   "synchro-cli",
		Short: "An AI team in your terminal",
		Long: strings.TrimSpace(`
synchro-cli runs a team of AI agents from your shell: give it a goal, and a
product manager plans it, a researcher grounds it in real sources, a
developer writes the code, and a reviewer finds the problems.

There is no account, no subscription and no server. Everything lives in
~/.synchro as plain JSON, and the only provider you need is a local Ollama
install - though free cloud keys work too, and nothing is ever blocked.`),
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			app, err := st.app()
			if err != nil {
				return err
			}
			return runREPL(app)
		},
	}

	f := root.PersistentFlags()
	f.StringVar(&st.opt.Home, "home", "", "state directory (default $SYNCHRO_HOME or ~/.synchro)")
	f.StringVarP(&st.opt.Workspace, "workspace", "w", "", "workspace to operate in")
	f.StringVarP(&st.opt.Team, "team", "t", "", "team to operate in")
	f.StringVarP(&st.opt.Agent, "agent", "a", "", "agent to operate as")
	f.StringVarP(&st.opt.Project, "project", "p", "", "project to operate in")
	f.BoolVar(&st.opt.JSON, "json", false, "machine-readable output where supported")
	f.BoolVar(&st.opt.NoColor, "no-color", false, "disable colour")
	f.BoolVarP(&st.opt.Yes, "yes", "y", false, "assume yes for confirmations")

	root.AddCommand(
		newInitCmd(st),
		newDoctorCmd(st),
		newModelsCmd(st),
		newKeysCmd(st),
		newConfigCmd(st),
		newWSCmd(st),
		newTeamCmd(st),
		newAgentCmd(st),
		newProjectCmd(st),
		newTaskCmd(st),
		newRunCmd(st),
		newPipelineCmd(st),
		newCommitCmd(st),
		newFilesCmd(st),
		newWorkflowCmd(st),
		newReportCmd(st),
		newVersionCmd(st),
	)
	return root
}

// app lazily opens the store once flags are parsed.
func (st *rootState) app() (*App, error) {
	if st.cached != nil {
		return st.cached, nil
	}
	a, err := NewApp(st.opt.Home)
	if err != nil {
		return nil, err
	}
	if st.opt.NoColor {
		a.P = ui.NewWriter(os.Stdout, os.Stderr, false)
	}
	a.FlagWorkspace = st.opt.Workspace
	a.FlagTeam = st.opt.Team
	a.FlagAgent = st.opt.Agent
	a.FlagProject = st.opt.Project
	st.cached = a
	return a, nil
}

func newVersionCmd(st *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := st.app()
			if err != nil {
				return err
			}
			if st.opt.JSON {
				a.P.Printf("{\"version\":%q,\"commit\":%q}\n", Version, Commit)
				return nil
			}
			a.P.Printf("synchro-cli %s (%s)\n", Version, Commit)
			return nil
		},
	}
}

// confirm asks for a yes/no answer when --yes was not passed.
func confirm(a *App, opt bool, question string) bool {
	if opt {
		return true
	}
	if !a.P.IsTTY {
		return false
	}
	fmt.Fprintf(a.P.Out, "%s %s [y/N] ", a.P.Yellow("?"), question)
	var answer string
	_, _ = fmt.Fscanln(os.Stdin, &answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}
