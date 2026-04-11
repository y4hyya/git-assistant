package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"git-assist/internal/git"
	"git-assist/internal/types"
)

type gitignoreResultMsg struct{ err error }

func fileStatusStyle(s types.FileStatus) lipgloss.Style {
	switch s {
	case types.StatusModified:
		return modifiedStyle
	case types.StatusAdded:
		return addedStyle
	case types.StatusDeleted:
		return deletedStyle
	case types.StatusRenamed:
		return renamedStyle
	case types.StatusUntracked:
		return untrackedStyle
	default:
		return modifiedStyle
	}
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateFiles(msg tea.Msg) (tea.Model, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.files)-1 {
			m.cursor++
		}
	case " ":
		m.files[m.cursor].Gitignored = false
		m.files[m.cursor].Selected = !m.files[m.cursor].Selected
	case "g":
		m.files[m.cursor].Selected = false
		m.files[m.cursor].Gitignored = !m.files[m.cursor].Gitignored
	case "a":
		allSelected := true
		for _, f := range m.files {
			if !f.Selected {
				allSelected = false
				break
			}
		}
		for i := range m.files {
			m.files[i].Selected = !allSelected
		}
	case "enter":
		hasSelected := false
		hasGitignored := false
		for _, f := range m.files {
			if f.Selected {
				hasSelected = true
			}
			if f.Gitignored {
				hasGitignored = true
			}
		}
		if !hasSelected && !hasGitignored {
			return m, nil
		}
		// Process gitignored files if any
		var ignorePaths []string
		var cachedPaths []string
		for _, f := range m.files {
			if f.Gitignored {
				ignorePaths = append(ignorePaths, f.Path)
				if f.Status != types.StatusUntracked {
					cachedPaths = append(cachedPaths, f.Path)
				}
			}
		}
		m.gitignoreCached = cachedPaths
		if len(ignorePaths) > 0 {
			return m, doGitignore(ignorePaths, cachedPaths)
		}
		m.step = stepType
		m.cursor = 0
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func doGitignore(ignorePaths, cachedPaths []string) tea.Cmd {
	return func() tea.Msg {
		if err := git.AddToGitignore(ignorePaths); err != nil {
			return gitignoreResultMsg{err: err}
		}
		if err := git.RemoveCached(cachedPaths); err != nil {
			return gitignoreResultMsg{err: err}
		}
		return gitignoreResultMsg{}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewFiles() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render("⎇ " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Step 1/4 · Select files to commit"))
	b.WriteString("\n\n")

	// File list
	selected := 0
	ignored := 0
	for i, f := range m.files {
		// Cursor arrow
		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		// Checkbox
		check := unselectedCheck.Render("○")
		if f.Gitignored {
			check = gitignoreCheck.Render("●")
			ignored++
		} else if f.Selected {
			check = selectedCheck.Render("●")
			selected++
		}

		// Status badge
		status := fileStatusStyle(f.Status).Render(fmt.Sprintf("%-2s", f.Status.Symbol()))

		// File path (highlighted when cursor is on it)
		path := filePathStyle.Render(f.Path)
		if i == m.cursor {
			path = highlightStyle.Render(f.Path)
		}

		b.WriteString(fmt.Sprintf("%s%s  %s  %s\n", cursor, check, status, path))
	}

	// Counter
	counter := fmt.Sprintf("%d/%d selected", selected, len(m.files))
	if ignored > 0 {
		counter += fmt.Sprintf(" · %d gitignored", ignored)
	}
	b.WriteString(fmt.Sprintf("\n  %s\n", dimStyle.Render(counter)))

	// Error
	if m.err != nil {
		b.WriteString("\n  " + errorStyle.Render("Error: "+m.err.Error()) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	b.WriteString(renderHelp([]helpEntry{
		{"↑↓", "navigate"},
		{"space", "select"},
		{"g", "gitignore"},
		{"a", "all"},
		{"enter", "next"},
		{"q", "quit"},
	}))

	return boxBorder.Render(b.String())
}

// ── Helpers ─────────────────────────────────────────────

type helpEntry struct {
	key  string
	desc string
}

func renderHelp(entries []helpEntry) string {
	var parts []string
	for _, e := range entries {
		parts = append(parts, helpKeyStyle.Render(e.key)+" "+helpStyle.Render(e.desc))
	}
	return "  " + strings.Join(parts, "    ")
}
