package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateConfirm(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while committing
	if m.committing {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter":
		m.committing = true
		val := strings.TrimSpace(m.msgInput.Value())
		fullMsg := m.buildCommitMessage(val)
		var paths []string
		for _, f := range m.files {
			if f.Selected {
				paths = append(paths, f.Path)
			}
		}
		return m, tea.Batch(doCommit(paths, m.gitignoreCached, fullMsg), m.spinner.Tick)
	case "esc":
		m.step = stepMessage
		m.bodyFocused = false
		m.bodyInput.Blur()
		m.msgInput.Focus()
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewConfirm() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Review before committing"))
	b.WriteString("\n\n")

	// Full commit message preview
	val := strings.TrimSpace(m.msgInput.Value())
	fullMsg := m.commitPrefix() + ": " + val
	b.WriteString("  " + highlightStyle.Render(fullMsg) + "\n")

	// Body if present
	if m.showBody {
		body := strings.TrimSpace(m.bodyInput.Value())
		if body != "" {
			b.WriteString("\n")
			for _, line := range strings.Split(body, "\n") {
				b.WriteString("  " + dimStyle.Render(line) + "\n")
			}
		}
	}

	// Selected files
	b.WriteString("\n")
	var selected []int
	for i, f := range m.files {
		if f.Selected {
			selected = append(selected, i)
		}
	}
	b.WriteString(fmt.Sprintf("  %s\n", dimStyle.Render(fmt.Sprintf("%d file(s):", len(selected)))))

	maxShow := 5
	for j, idx := range selected {
		if j >= maxShow {
			remaining := len(selected) - maxShow
			b.WriteString(fmt.Sprintf("    %s\n", dimStyle.Render(fmt.Sprintf("... and %d more", remaining))))
			break
		}
		f := m.files[idx]
		status := fileStatusStyle(f.Status).Render(fmt.Sprintf("%-2s", f.Status.Symbol()))
		b.WriteString(fmt.Sprintf("    %s  %s\n", status, filePathStyle.Render(f.Path)))
	}

	// Committing spinner
	if m.committing {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Committing...") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "commit"},
		{"esc", "back"},
		{"q", "quit"},
	}))

	return boxBorder.Render(b.String())
}
