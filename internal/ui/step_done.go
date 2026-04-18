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
		case "enter":
			// Reset wizard state and return to menu
			m.step = stepMenu
			m.menuCursor = 0
			m.typeIdx = 0
			m.commitType = ""
			m.breaking = false
			m.scope = ""
			m.msgInput.Reset()
			m.bodyInput.Reset()
			m.showBody = false
			m.bodyFocused = false
			m.pushed = false
			m.pushBranch = ""
			m.gitignoreCached = nil
			m.committing = false
			m.pushing = false
			// Refresh files and graphs
			files, _ := git.GetStatus()
			m.files = files
			m.cursor = 0
			m.fileScroll = 0
			m.RefreshGraphs()
			return m, m.maybeFetch()
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
		{"enter", "menu"},
	}))

	return m.styledBox(b.String())
}
