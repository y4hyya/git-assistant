package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
	"git-assist/internal/ui"
)

func main() {
	for _, arg := range os.Args[1:] {
		if arg == "--no-color" {
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
