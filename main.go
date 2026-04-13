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

	if !git.IsGitRepo() {
		fmt.Println("✗ Not a git repository")
		os.Exit(1)
	}

	files, err := git.GetStatus()
	if err != nil {
		fmt.Printf("✗ Failed to get status: %v\n", err)
		os.Exit(1)
	}

	if len(files) == 0 {
		fmt.Println("✓ Working tree clean — nothing to commit")
		os.Exit(0)
	}

	branch, _ := git.GetCurrentBranch()

	m := ui.NewModel(files, branch)
	p := tea.NewProgram(m, tea.WithAltScreen())

	if _, err := p.Run(); err != nil {
		fmt.Printf("✗ Error: %v\n", err)
		os.Exit(1)
	}
}
