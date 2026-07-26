package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── Update ──────────────────────────────────────────────

func (m Model) updateType(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	totalItems := len(types.CommitTypes) + 1 // +1 for "custom"

	switch keyMsg.String() {
	case "up", "k":
		if m.typeIdx > 0 {
			m.typeIdx--
		}
	case "down", "j":
		if m.typeIdx < totalItems-1 {
			m.typeIdx++
		}
	case "!":
		m.breaking = !m.breaking
	case "enter":
		if m.typeIdx == len(types.CommitTypes) {
			// Custom type
			m.step = stepCustom
			m.customInput.Focus()
			return m, nil
		}
		m.commitType = types.CommitTypes[m.typeIdx].Name
		m.step = stepMessage
		m.scopeInput.Focus()
		return m, nil
	case "esc":
		// A message-only amend (clean tree) never passed through the file
		// selector — "back" there would land on an empty list, so return to
		// the menu instead.
		if m.amendMode && len(m.files) == 0 {
			cmd := m.returnToMenu()
			return m, cmd
		}
		m.step = stepFiles
		m.cursor = 0
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewType() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(m.renderProgress())
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Choose commit type"))
	if m.breaking {
		b.WriteString("   " + errorStyle.Render("! BREAKING CHANGE"))
	}
	b.WriteString("\n\n")

	// Commit type list
	for i, ct := range types.CommitTypes {
		cursor := "  "
		if i == m.typeIdx {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		radio := inactiveStyle.Render(symUnselected)
		name := inactiveStyle.Render(ct.Name)
		desc := dimStyle.Render(ct.Description)
		if i == m.typeIdx {
			radio = activeStyle.Render(symSelected)
			name = activeStyle.Render(ct.Name)
			desc = lipgloss.NewStyle().Foreground(lightGray).Render(ct.Description)
		}

		b.WriteString(fmt.Sprintf("%s%s  %-12s %s\n", cursor, radio, name, desc))
	}

	// Custom option
	customIdx := len(types.CommitTypes)
	cursor := "  "
	if m.typeIdx == customIdx {
		cursor = cursorStyle.Render(symCursor + " ")
	}
	radio := inactiveStyle.Render(symUnselected)
	name := inactiveStyle.Render("custom")
	desc := dimStyle.Render("Write your own...")
	if m.typeIdx == customIdx {
		radio = activeStyle.Render(symSelected)
		name = activeStyle.Render("custom")
		desc = lipgloss.NewStyle().Foreground(lightGray).Render("Write your own...")
	}
	b.WriteString(fmt.Sprintf("%s%s  %-12s %s\n", cursor, radio, name, desc))

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))

	return m.styledBox(b.String())
}

// ── Custom type input ───────────────────────────────────

// customTypeLimit caps the custom commit type. It is also what the input's
// CharLimit is set to (see NewModel), so the counter on screen and the point
// where keystrokes stop being accepted are the same number.
const customTypeLimit = 40

// customTypeCounterAt is when the remaining-characters counter appears. Showing
// it from the first keystroke is noise; showing it only after keys have silently
// stopped registering is too late.
const customTypeCounterAt = 30

// validateCustomType checks a typed commit type and returns it in the form that
// will actually be committed. The prefix goes into the commit message verbatim,
// so "Bug Fix" produced `Bug Fix: subject` — not a conventional commit at all,
// and nothing on the screen had said a word about it.
func validateCustomType(raw string) (string, error) {
	val := strings.TrimSpace(raw)
	if val == "" {
		return "", fmt.Errorf("type cannot be empty — try fix, hotfix or chore")
	}
	if strings.ContainsAny(val, " \t") {
		return "", fmt.Errorf("a commit type is a single word, e.g. hotfix")
	}
	// Conventional commit types are lowercase. Correcting it beats rejecting it:
	// "FEAT" is unambiguous about what was meant.
	return strings.ToLower(val), nil
}

func (m Model) updateCustom(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		switch keyMsg.String() {
		case "enter":
			val, err := validateCustomType(m.customInput.Value())
			if err != nil {
				m.err = err
				return m, nil
			}
			m.commitType = val
			m.customInput.SetValue(val)
			m.step = stepMessage
			m.scopeInput.Focus()
			return m, nil
		case "esc":
			m.step = stepType
			m.customInput.Reset()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.customInput, cmd = m.customInput.Update(msg)
	return m, cmd
}

func (m Model) viewCustom() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(m.renderProgress())
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Enter custom commit type"))
	b.WriteString("\n\n")

	b.WriteString("  " + m.customInput.View() + "\n")

	// Character counter, close to the limit only — the input simply stops
	// accepting keystrokes there, and it used to do so with nothing on screen
	// to explain why the word had stopped growing.
	if used := len([]rune(m.customInput.Value())); used >= customTypeCounterAt {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%d/%d characters", used, customTypeLimit)) + "\n")
	}

	// Error — this screen used to render none at all, so a rejected enter was
	// indistinguishable from a dead key.
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))

	return m.styledBox(b.String())
}
