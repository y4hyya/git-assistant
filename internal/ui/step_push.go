package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updatePush(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while pushing
	if m.pushing {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if m.branchIdx > 0 {
			m.branchIdx--
		}
	case "down", "j":
		if m.branchIdx < len(m.branches)-1 {
			m.branchIdx++
		}
	case "enter":
		branch := m.branches[m.branchIdx]
		m.pushBranch = branch
		m.pushing = true
		return m, tea.Batch(doPush(m.branch, branch), m.spinner.Tick)
	case "n", "esc":
		m.step = stepDone
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func doPush(currentBranch, targetBranch string) tea.Cmd {
	return func() tea.Msg {
		err := git.Push(currentBranch, targetBranch)
		return pushResultMsg{err: err}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewPush() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Push to remote"))
	b.WriteString("\n\n")

	// Commit success
	msg := m.commitPrefix() + ": " + strings.TrimSpace(m.msgInput.Value())
	b.WriteString("  " + successStyle.Render(symDone) + " Committed: " + msg + "\n")

	stats := git.GetCommitStats()
	if stats != "" {
		b.WriteString("  " + dimStyle.Render(stats) + "\n")
	}
	b.WriteString("\n")

	// Branch picker
	b.WriteString("  Select branch to push:\n\n")
	for i, branch := range m.branches {
		cursor := "  "
		if i == m.branchIdx {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		radio := inactiveStyle.Render(symUnselected)
		name := inactiveStyle.Render(branch)
		if i == m.branchIdx {
			radio = activeStyle.Render(symSelected)
			name = activeStyle.Render(branch)
		}

		label := ""
		if branch == m.branch {
			label = dimStyle.Render(" (current)")
		}

		b.WriteString(fmt.Sprintf("%s%s  %s%s\n", cursor, radio, name, label))
	}

	// Loading
	if m.pushing {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Pushing...") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{symArrows, "navigate"},
		{"enter", "push"},
		{"n", "skip"},
		{"q", "quit"},
	}))

	return m.styledBox(b.String())
}
