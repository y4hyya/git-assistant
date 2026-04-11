package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	b.WriteString(branchStyle.Render("⎇ " + m.branch))
	b.WriteString("\n\n")

	// Commit summary
	msg := m.commitType + ": " + strings.TrimSpace(m.msgInput.Value())
	b.WriteString("  " + successStyle.Render("✓") + " Committed: " + msg + "\n")

	// Push summary
	if m.pushed {
		b.WriteString("  " + successStyle.Render("✓") + " Pushed to " + branchStyle.Render("origin/"+m.pushBranch) + "\n")
	} else if m.hasRemote {
		b.WriteString("  " + dimStyle.Render("⊘ Push skipped") + "\n")
	}

	b.WriteString("\n  " + successStyle.Render("All done!") + "\n")

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "exit"},
	}))

	return boxBorder.Render(b.String())
}
