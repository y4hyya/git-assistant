package ui

import (
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateDone(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter", "esc":
			m.menuCursor = 0
			m.committing = false
			m.pushing = false
			// Wizard reset, fresh file list, and graph refresh all live in
			// returnToMenu so no exit path can skip one of them.
			cmd := m.returnToMenu()
			return m, cmd
		case "q":
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

	// Commit / amend summary
	msg := m.subjectLine(strings.TrimSpace(m.msgInput.Value()))
	verb := "Committed"
	if m.amendMode {
		verb = "Amended"
	}
	b.WriteString("  " + successStyle.Render(symDone) + " " + verb + ": " + msg + "\n")

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
		b.WriteString("  " + dimStyle.Render(symSkip+" Push skipped") + "\n")
	}

	b.WriteString("\n  " + successStyle.Render("All done!") + "\n")

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "menu"},
		{"q", "quit"},
	}))

	return m.styledBox(b.String())
}
