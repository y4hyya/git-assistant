package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "q":
			m.quitting = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewDone() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n\n")

	// Commit summary
	msg := m.commitPrefix() + ": " + strings.TrimSpace(m.msgInput.Value())
	b.WriteString("  " + successStyle.Render(symDone) + " Committed: " + msg + "\n")

	// Commit hash and stats
	hash := git.GetLastCommitHash()
	stats := git.GetCommitStats()
	if hash != "" || stats != "" {
		detail := "    "
		if hash != "" {
			detail += dimStyle.Render(hash)
		}
		if hash != "" && stats != "" {
			detail += dimStyle.Render(" · ")
		}
		if stats != "" {
			detail += dimStyle.Render(stats)
		}
		b.WriteString(detail + "\n")
	}

	// Push summary
	if m.pushed {
		b.WriteString("  " + successStyle.Render(symDone) + " Pushed to " + branchStyle.Render("origin/"+m.pushBranch) + "\n")
	} else if m.hasRemote {
		b.WriteString("  " + dimStyle.Render(symSkip + " Push skipped") + "\n")
	}

	b.WriteString("\n  " + successStyle.Render("All done!") + "\n")

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "exit"},
	}))

	return m.styledBox(b.String())
}
