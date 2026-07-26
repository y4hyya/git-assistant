package main

import (
	"fmt"
	"os"

	"git-assist/internal/git"
	"git-assist/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// Version is set at build time via -ldflags "-X main.Version=…".
// Defaults to "dev" for ad-hoc `go build` invocations.
var Version = "dev"

// usage is the whole command-line surface, hand-formatted. The flag package's
// default output described none of this: the dashboard is the default action,
// `branch` is the only subcommand, and the two flags are not registered with
// flag at all — `git-assist --help` used to print nothing and fall through to
// launching the TUI, which reads as the help being broken.
const usage = `git-assist — an interactive TUI git dashboard

USAGE
  git-assist [command] [flags]

COMMANDS
  (none)      open the dashboard: commit, amend, push, branches, config
  branch      jump straight into the branch manager
  help        show this help

FLAGS
  -h, --help       show this help
  -v, --version    print the version and exit
      --no-color   disable colours (same as NO_COLOR=1 in the environment)

NOTES
  Run it anywhere inside a repository — git-assist moves to the repository root
  itself, so paths and .gitignore resolve the way git reports them.

  Run it outside a repository and it offers first-run setup (git init, connect
  an existing GitHub repo, or create one with the gh CLI) instead of failing.

  Press ? on any screen for the keys that screen responds to.
`

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version", "-v":
			fmt.Printf("git-assist %s\n", Version)
			return
		case "--help", "-h", "help":
			fmt.Print(usage)
			return
		case "--no-color":
			os.Setenv("NO_COLOR", "1")
		}
	}

	// Subcommand: git-assist branch
	subcommand := ""
	for _, arg := range os.Args[1:] {
		if arg != "--no-color" {
			subcommand = arg
			break
		}
	}

	// Every path git reports (status, diff, add pathspecs) is relative to the
	// worktree root, and .gitignore lives there too. Launched from a
	// subdirectory those paths resolve against the wrong cwd, so the
	// dashboard shows a clean tree and file operations misfire. Move to the
	// root up front; if this isn't a repo the call fails and we leave cwd
	// alone for the init flow below.
	if root, err := git.RepoToplevel(); err == nil {
		if err := os.Chdir(root); err != nil {
			fmt.Printf("✗ Cannot enter repository root %s: %v\n", root, err)
			os.Exit(1)
		}
	}

	// Non-git directory → launch first-run init flow instead of exiting.
	// The branch subcommand still requires an existing repo, so skip init
	// for that case and error clearly.
	if !git.IsGitRepo() {
		if subcommand == "branch" {
			fmt.Println("✗ Not a git repository")
			os.Exit(1)
		}
		m := ui.NewInitModel()
		p := tea.NewProgram(m, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if subcommand == "branch" {
		branch, _ := git.GetCurrentBranch()
		m := ui.NewBranchModel(branch)
		p := tea.NewProgram(m, tea.WithAltScreen())
		if _, err := p.Run(); err != nil {
			fmt.Printf("✗ Error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	branch, _ := git.GetCurrentBranch()
	files, _ := git.GetStatus() // nil if clean, that's fine

	// NewModel already takes the one synchronous dashboard reading; a second
	// pass here just doubled the git processes between launch and first frame.
	m := ui.NewModel(files, branch)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("✗ Error: %v\n", err)
		os.Exit(1)
	}
}
