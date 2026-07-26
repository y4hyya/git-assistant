package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
	"git-assist/internal/ui"
)

// Version is set at build time via -ldflags "-X main.Version=…".
// Defaults to "dev" for ad-hoc `go build` invocations.
var Version = "dev"

func main() {
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--version", "-v":
			fmt.Printf("git-assist %s\n", Version)
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

	m := ui.NewModel(files, branch)
	m.RefreshGraphs()
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("✗ Error: %v\n", err)
		os.Exit(1)
	}
}
