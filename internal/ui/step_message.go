package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// focus tracks which input field is active in the message step.
type focus int

const (
	focusScope   focus = iota
	focusSubject
	focusBody
)

// ── Update ──────────────────────────────────────────────

func (m Model) messageFocus() focus {
	if m.bodyFocused {
		return focusBody
	}
	if m.scopeInput.Focused() {
		return focusScope
	}
	return focusSubject
}

func (m Model) updateMessage(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		f := m.messageFocus()

		switch keyMsg.String() {
		case "tab":
			switch f {
			case focusScope:
				// Scope → Subject
				m.scope = strings.TrimSpace(m.scopeInput.Value())
				m.scopeInput.Blur()
				m.msgInput.Focus()
				m.bodyFocused = false
			case focusSubject:
				// Subject → Body
				if !m.showBody {
					m.showBody = true
				}
				m.msgInput.Blur()
				m.bodyInput.Focus()
				m.bodyFocused = true
			case focusBody:
				// Body → Scope
				m.bodyInput.Blur()
				m.bodyFocused = false
				m.scopeInput.Focus()
			}
			return m, nil

		case "enter":
			if f == focusBody {
				break // newline in body textarea
			}
			if f == focusScope {
				// Scope → Subject (enter advances within step)
				m.scope = strings.TrimSpace(m.scopeInput.Value())
				m.scopeInput.Blur()
				m.msgInput.Focus()
				return m, nil
			}
			// Subject → confirm
			val := strings.TrimSpace(m.msgInput.Value())
			if val != "" {
				m.scope = strings.TrimSpace(m.scopeInput.Value())
				m.step = stepConfirm
			}
			return m, nil

		case "ctrl+d":
			val := strings.TrimSpace(m.msgInput.Value())
			if val != "" {
				m.scope = strings.TrimSpace(m.scopeInput.Value())
				m.step = stepConfirm
			}
			return m, nil

		case "esc":
			switch f {
			case focusBody:
				// Body → Subject
				m.bodyInput.Blur()
				m.bodyFocused = false
				m.msgInput.Focus()
			case focusSubject:
				// Subject → Scope
				m.msgInput.Blur()
				m.scopeInput.Focus()
			case focusScope:
				// Back to type step
				m.scope = ""
				m.scopeInput.Reset()
				m.step = stepType
			}
			return m, nil
		}
	}

	// Route input to the focused widget
	var cmd tea.Cmd
	switch m.messageFocus() {
	case focusScope:
		m.scopeInput, cmd = m.scopeInput.Update(msg)
	case focusBody:
		m.bodyInput, cmd = m.bodyInput.Update(msg)
	default:
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
	f := m.messageFocus()

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

	// Scope input (inline, optional)
	scopeLabel := "  Scope " + dimStyle.Render("(optional)")
	if f == focusScope {
		scopeLabel = highlightStyle.Render("  Scope") + " " + dimStyle.Render("(optional)")
	} else {
		scopeLabel = dimStyle.Render("  Scope (optional)")
	}
	b.WriteString(scopeLabel + "\n")
	b.WriteString("  " + m.scopeInput.View() + "\n\n")

	// Subject input
	subjectLabel := "  Subject"
	if f == focusSubject {
		subjectLabel = highlightStyle.Render("  Subject")
	} else {
		subjectLabel = dimStyle.Render("  Subject")
	}
	b.WriteString(subjectLabel + "\n")
	b.WriteString("  " + m.msgInput.View() + "\n")

	// Body input (shown when tab reaches it)
	if m.showBody {
		b.WriteString("\n")
		bodyLabel := "  Body"
		if f == focusBody {
			bodyLabel = highlightStyle.Render("  Body")
		} else {
			bodyLabel = dimStyle.Render("  Body")
		}
		b.WriteString(bodyLabel + "\n")
		b.WriteString("  " + m.bodyInput.View() + "\n")
	}

	// Live preview
	val := m.msgInput.Value()
	if val != "" {
		// Build preview with current scope input
		scopeVal := m.scopeInput.Value()
		previewPrefix := m.commitType
		if scopeVal != "" {
			previewPrefix += "(" + scopeVal + ")"
		}
		if m.breaking {
			previewPrefix += "!"
		}
		preview := previewPrefix + ": " + val
		b.WriteString("\n  " + previewStyle.Render("→ "+preview) + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	switch f {
	case focusScope:
		b.WriteString(renderHelp([]helpEntry{
			{"enter", "subject"},
			{"tab", "subject"},
			{"esc", "back"},
		}))
	case focusBody:
		b.WriteString(renderHelp([]helpEntry{
			{"ctrl+d", "next"},
			{"tab", "scope"},
			{"esc", "subject"},
		}))
	default:
		if m.showBody {
			b.WriteString(renderHelp([]helpEntry{
				{"enter", "next"},
				{"tab", "body"},
				{"esc", "scope"},
			}))
		} else {
			b.WriteString(renderHelp([]helpEntry{
				{"enter", "next"},
				{"tab", "add body"},
				{"esc", "scope"},
			}))
		}
	}

	return boxBorder.Render(b.String())
}
