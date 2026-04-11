package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Block input while committing
	if m.committing {
		return m, nil
	}

	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			val := strings.TrimSpace(m.msgInput.Value())
			if val != "" {
				m.committing = true
				fullMsg := m.commitType + ": " + val
				var paths []string
				for _, f := range m.files {
					if f.Selected {
						paths = append(paths, f.Path)
					}
				}
				return m, doCommit(paths, m.gitignoreCached, fullMsg)
			}
			return m, nil
		case "esc":
			m.step = stepType
			return m, nil
		}
	}

	// Pass everything else to the text input (typing, blink, etc.)
	var cmd tea.Cmd
	m.msgInput, cmd = m.msgInput.Update(msg)
	return m, cmd
}

func doCommit(paths []string, cachedPaths []string, message string) tea.Cmd {
	return func() tea.Msg {
		err := git.Commit(paths, cachedPaths, message)
		return commitResultMsg{err: err}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewMessage() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render("⎇ " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Step 3/4 · Write commit message"))
	b.WriteString("\n\n")

	// Summary
	count := 0
	for _, f := range m.files {
		if f.Selected {
			count++
		}
	}
	b.WriteString(fmt.Sprintf("  Type:  %s\n", activeStyle.Render(m.commitType)))
	b.WriteString(fmt.Sprintf("  Files: %s\n\n", dimStyle.Render(fmt.Sprintf("%d selected", count))))

	// Input
	b.WriteString("  " + m.msgInput.View() + "\n")

	// Live preview
	val := m.msgInput.Value()
	if val != "" {
		preview := m.commitType + ": " + val
		b.WriteString("\n  " + previewStyle.Render("→ "+preview) + "\n")
	}

	// Loading state
	if m.committing {
		b.WriteString("\n  " + dimStyle.Render("⟳ Committing...") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + errorStyle.Render("Error: "+m.err.Error()) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"enter", "commit"},
		{"esc", "back"},
	}))

	return boxBorder.Render(b.String())
}
