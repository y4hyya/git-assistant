package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "tab":
			if !m.showBody {
				m.showBody = true
				m.bodyFocused = true
				m.msgInput.Blur()
				m.bodyInput.Focus()
			} else if m.bodyFocused {
				m.bodyFocused = false
				m.bodyInput.Blur()
				m.msgInput.Focus()
			} else {
				m.bodyFocused = true
				m.msgInput.Blur()
				m.bodyInput.Focus()
			}
			return m, nil
		case "enter":
			if m.bodyFocused {
				break
			}
			val := strings.TrimSpace(m.msgInput.Value())
			if val != "" {
				m.step = stepConfirm
			}
			return m, nil
		case "ctrl+d":
			val := strings.TrimSpace(m.msgInput.Value())
			if val != "" {
				m.step = stepConfirm
			}
			return m, nil
		case "esc":
			if m.bodyFocused {
				// Exit body, focus subject
				m.bodyFocused = false
				m.bodyInput.Blur()
				m.msgInput.Focus()
				return m, nil
			}
			m.step = stepScope
			return m, nil
		}
	}

	// Route input to the focused widget
	var cmd tea.Cmd
	if m.bodyFocused {
		m.bodyInput, cmd = m.bodyInput.Update(msg)
	} else {
		m.msgInput, cmd = m.msgInput.Update(msg)
	}
	return m, cmd
}

// buildCommitMessage constructs the full commit message with optional body.
func (m Model) buildCommitMessage(subject string) string {
	msg := m.commitPrefix() + ": " + subject
	if m.showBody {
		body := strings.TrimSpace(m.bodyInput.Value())
		if body != "" {
			msg += "\n\n" + body
		}
	}
	return msg
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
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(renderProgress(m.step))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Write commit message"))
	b.WriteString("\n\n")

	// Summary
	count := 0
	for _, f := range m.files {
		if f.Selected {
			count++
		}
	}
	prefix := m.commitPrefix()
	b.WriteString(fmt.Sprintf("  Type:  %s\n", activeStyle.Render(prefix)))
	b.WriteString(fmt.Sprintf("  Files: %s\n\n", dimStyle.Render(fmt.Sprintf("%d selected", count))))

	// Subject input
	label := "  Subject"
	if !m.bodyFocused {
		label = highlightStyle.Render("  Subject")
	} else {
		label = dimStyle.Render("  Subject")
	}
	b.WriteString(label + "\n")
	b.WriteString("  " + m.msgInput.View() + "\n")

	// Body input (shown when tab is pressed)
	if m.showBody {
		b.WriteString("\n")
		label = "  Body"
		if m.bodyFocused {
			label = highlightStyle.Render("  Body")
		} else {
			label = dimStyle.Render("  Body")
		}
		b.WriteString(label + "\n")
		b.WriteString("  " + m.bodyInput.View() + "\n")
	}

	// Live preview
	val := m.msgInput.Value()
	if val != "" {
		preview := prefix + ": " + val
		b.WriteString("\n  " + previewStyle.Render("→ "+preview) + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	if m.bodyFocused {
		b.WriteString(renderHelp([]helpEntry{
			{"ctrl+d", "next"},
			{"tab", "subject"},
			{"esc", "back"},
		}))
	} else if m.showBody {
		b.WriteString(renderHelp([]helpEntry{
			{"enter", "next"},
			{"tab", "body"},
			{"esc", "back"},
		}))
	} else {
		b.WriteString(renderHelp([]helpEntry{
			{"enter", "next"},
			{"tab", "add body"},
			{"esc", "back"},
		}))
	}

	return boxBorder.Render(b.String())
}
