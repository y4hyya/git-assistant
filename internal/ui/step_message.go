package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
)

// focus tracks which input field is active in the message step.
type focus int

const (
	focusScope focus = iota
	focusSubject
	focusBody
)

// ── Update ──────────────────────────────────────────────

func (m Model) messageFocus() focus {
	if m.bodyFocused {
		return focusBody
	}
	// A raw amend has no scope field: the subject is written back verbatim,
	// so there is nothing to assemble a "type(scope): " prefix from.
	if !m.amendRaw && m.scopeInput.Focused() {
		return focusScope
	}
	return focusSubject
}

// leaveMessageStep backs out of the message step. Raw amends skip the type
// picker, so "back" there means the file selector — or the menu, when a
// message-only amend never had one.
func (m Model) leaveMessageStep() (tea.Model, tea.Cmd) {
	m.msgInput.Blur()
	if len(m.files) == 0 {
		cmd := m.returnToMenu()
		return m, cmd
	}
	m.step = stepFiles
	m.cursor = 0
	return m, nil
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
				// Body → Scope (→ Subject when there is no scope field)
				m.bodyInput.Blur()
				m.bodyFocused = false
				if m.amendRaw {
					m.msgInput.Focus()
				} else {
					m.scopeInput.Focus()
				}
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
			if val == "" {
				m.err = fmt.Errorf("subject cannot be empty")
				return m, nil
			}
			m.scope = strings.TrimSpace(m.scopeInput.Value())
			m.enterConfirm()
			return m, nil

		case "ctrl+d":
			val := strings.TrimSpace(m.msgInput.Value())
			if val == "" {
				m.err = fmt.Errorf("subject cannot be empty")
				return m, nil
			}
			m.scope = strings.TrimSpace(m.scopeInput.Value())
			m.enterConfirm()
			return m, nil

		case "up":
			switch f {
			case focusSubject:
				if m.amendRaw {
					return m, nil
				}
				m.msgInput.Blur()
				m.scopeInput.Focus()
			}
			return m, nil

		case "down":
			switch f {
			case focusScope:
				m.scope = strings.TrimSpace(m.scopeInput.Value())
				m.scopeInput.Blur()
				m.msgInput.Focus()
			case focusSubject:
				if m.showBody {
					m.msgInput.Blur()
					m.bodyInput.Focus()
					m.bodyFocused = true
				}
			}
			return m, nil

		case "esc":
			switch f {
			case focusBody:
				m.bodyInput.Blur()
				m.bodyFocused = false
				m.msgInput.Focus()
			case focusSubject:
				if m.amendRaw {
					return m.leaveMessageStep()
				}
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

// subjectLine renders the commit subject exactly as it will be written: the
// conventional prefix plus the typed text, or — in raw amend mode — the
// subject alone. Every preview goes through here so the screen can never
// promise a prefix the commit won't carry (or hide one it will).
func (m Model) subjectLine(subject string) string {
	if m.amendRaw {
		return subject
	}
	return m.commitPrefix() + ": " + subject
}

// buildCommitMessage constructs the full commit message with optional body.
func (m Model) buildCommitMessage(subject string) string {
	msg := m.subjectLine(subject)
	if m.showBody {
		body := strings.TrimSpace(m.bodyInput.Value())
		if body != "" {
			msg += "\n\n" + body
		}
	}
	return msg
}

func doCommit(files []types.FileEntry, cachedPaths []string, message string) tea.Cmd {
	return func() tea.Msg {
		err := git.Commit(files, cachedPaths, message)
		return commitResultMsg{err: err}
	}
}

// doAmend stages the given paths on top of the existing index and re-runs
// the last commit with a new message. Reuses commitResultMsg — the handler
// in model.go branches on m.amendMode to skip the push step and route
// straight to Done.
func doAmend(files []types.FileEntry, message string) tea.Cmd {
	return func() tea.Msg {
		err := git.Amend(files, message)
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
	if m.amendRaw {
		b.WriteString(fmt.Sprintf("  Type:  %s\n", dimStyle.Render("(keeping original format)")))
	} else {
		b.WriteString(fmt.Sprintf("  Type:  %s\n", activeStyle.Render(m.commitPrefix())))
	}
	b.WriteString(fmt.Sprintf("  Files: %s\n\n", dimStyle.Render(fmt.Sprintf("%d selected", count))))

	// Scope input (inline, optional). Hidden for raw amends — the subject is
	// written back verbatim, so there is no prefix to put a scope into.
	if !m.amendRaw {
		scopeLabel := dimStyle.Render("  Scope (optional)")
		if f == focusScope {
			scopeLabel = highlightStyle.Render("  Scope") + " " + dimStyle.Render("(optional)")
		}
		b.WriteString(scopeLabel + "\n")
		b.WriteString("  " + m.scopeInput.View() + "\n\n")
	}

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
		preview := val
		if !m.amendRaw {
			// Build preview with current scope input
			scopeVal := m.scopeInput.Value()
			previewPrefix := m.commitType
			if scopeVal != "" {
				previewPrefix += "(" + scopeVal + ")"
			}
			if m.breaking {
				previewPrefix += "!"
			}
			preview = previewPrefix + ": " + val
		}
		b.WriteString("\n  " + previewStyle.Render(symArrowRight+" "+preview) + "\n")
		if m.amendRaw {
			b.WriteString("  " + dimStyle.Render("(keeping original format)") + "\n")
		}
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
			{symArrows, "navigate"},
			{"enter", "subject"},
			{"esc", "back"},
		}))
	case focusBody:
		tabTarget := "scope"
		if m.amendRaw {
			tabTarget = "subject"
		}
		b.WriteString(renderHelp([]helpEntry{
			{"ctrl+d", "next"},
			{"tab", tabTarget},
			{"esc", "subject"},
		}))
	default:
		// Raw amends have no scope field, so esc leaves the step entirely.
		escTarget := "scope"
		if m.amendRaw {
			escTarget = "back"
		}
		tabTarget := "add body"
		if m.showBody {
			tabTarget = "body"
		}
		b.WriteString(renderHelp([]helpEntry{
			{symArrows, "navigate"},
			{"enter", "next"},
			{"tab", tabTarget},
			{"esc", escTarget},
		}))
	}

	return m.styledBox(b.String())
}
