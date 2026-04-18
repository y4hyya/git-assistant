package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

type menuItem struct {
	name string
	desc string
}

func (m Model) menuItems() []menuItem {
	changeCount := 0
	for _, f := range m.files {
		_ = f
		changeCount++
	}

	commitDesc := "no changes"
	if changeCount > 0 {
		commitDesc = fmt.Sprintf("%d changes", changeCount)
	}

	branchCount := len(git.GetAllBranches())

	items := []menuItem{
		{"Commit", commitDesc},
		{"Branch", fmt.Sprintf("%d branches", branchCount)},
		{"Config", "git settings"},
	}
	// Recovery entry: when this local repo has no remote and `gh` is
	// available, offer to create the GitHub repo from here.
	if m.canConnectGH() {
		items = append(items, menuItem{"Connect to GitHub", "create repo + set origin via gh"})
	}
	return items
}

// canConnectGH reports whether the recovery menu entry is applicable.
// Requires: we're in a git repo (always true on menu), no origin set, and
// the gh CLI is installed. Auth is checked later in the init flow.
func (m Model) canConnectGH() bool {
	return !m.hasRemote && git.HasGHCLI()
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateMenu(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while a background fetch is in progress.
	// Non-blocking — keypresses still route normally below, so the user
	// can navigate the menu while fetch runs.
	if m.fetching {
		if _, ok := msg.(spinner.TickMsg); ok {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
	}

	// Forward spinner during sync (blocking — input locked during merge)
	if m.branchMerging {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	items := m.menuItems()

	switch keyMsg.String() {
	case "up", "k":
		if m.menuCursor > 0 {
			m.menuCursor--
		}
	case "down", "j":
		if m.menuCursor < len(items)-1 {
			m.menuCursor++
		}
	case "enter":
		switch m.menuCursor {
		case 0: // Commit
			if len(m.files) == 0 {
				m.err = fmt.Errorf("nothing to commit — working tree clean")
				return m, nil
			}
			m.step = stepFiles
			m.cursor = 0
			m.fileScroll = 0
		case 1: // Branch
			m.branchEntries = git.GetAllBranches()
			m.branchCursor = 0
			m.branchScroll = 0
			m.branchStandalone = false
			m.step = stepBranch
		case 2: // Config
			m.configCursor = 0
			m.configGlobal = false
			m.configEditMode = false
			m.loadConfigItems()
			m.step = stepConfig
		case 3: // Connect to GitHub (only present when canConnectGH)
			if m.canConnectGH() {
				m.step = stepInit
				m.ghReuseMode = true
				m.initCursor = int(initChoiceGHCreate)
				m.initSuccessMsg = ""
				m.initNameInput.SetValue(git.CurrentDirName())
				m.initNameInput.CursorEnd()
				if !git.IsGHAuthed() {
					m.initPhase = initPhaseConfirmGHAuth
				} else {
					m.initNameInput.Focus()
					m.initPhase = initPhaseInputRepoName
				}
			}
		}
	case "s":
		if m.behindMain > 0 {
			// Merge origin/<main> into current — always the freshest source,
			// and consistent with the post-fetch state shown in the graph.
			// git merge accepts a remote-tracking ref directly, so we reuse
			// doMergeBranch instead of introducing a parallel helper.
			m.branchMerging = true
			main := git.ResolveMainBranch()
			return m, tea.Batch(doMergeBranch("origin/"+main), m.spinner.Tick)
		}
	case "p":
		// Manual pull fallback. Opens the sync dialog if there's anything
		// to pull — user may have skipped the startup dialog, or new
		// upstream commits appeared mid-session.
		if m.populateSyncDialog() && m.syncPullCurrent {
			m.syncReturnStep = stepMenu
			m.step = stepSync
		}
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// ── View ────────────────────────────────────────────────

func (m Model) viewMenu() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))

	status := ""
	if len(m.files) == 0 {
		status = successStyle.Render("  clean")
	} else {
		status = modifiedStyle.Render(fmt.Sprintf("  %d changes", len(m.files)))
	}
	b.WriteString(status)
	if m.behindMain > 0 {
		b.WriteString(modifiedStyle.Render(fmt.Sprintf("  %s%d behind main", symArrowDown, m.behindMain)))
	}
	if m.fetching {
		b.WriteString("  " + dimStyle.Render(m.spinner.View()+" syncing"))
	}
	b.WriteString("\n\n")

	// One-shot success banner from the init flow. Cleared on next keypress
	// by the main Update handler, same lifecycle as m.err.
	if m.initSuccessMsg != "" {
		b.WriteString("  " + successStyle.Render(symDone+" "+m.initSuccessMsg) + "\n\n")
	}

	// Menu items
	items := m.menuItems()
	for i, item := range items {
		cursor := "  "
		if i == m.menuCursor {
			cursor = cursorStyle.Render(symCursor + " ")
		}

		name := inactiveStyle.Render(item.name)
		desc := dimStyle.Render(item.desc)
		if i == m.menuCursor {
			name = highlightStyle.Render(item.name)
		}

		// Dim "Commit" when no changes
		if i == 0 && len(m.files) == 0 {
			name = dimStyle.Render(item.name)
			desc = dimStyle.Render(item.desc)
		}

		b.WriteString(fmt.Sprintf("%s%-12s %s\n", cursor, name, desc))
	}

	// Spinner for sync
	if m.branchMerging {
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Syncing with main...") + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	helpEntries := []helpEntry{
		{symArrows, "navigate"},
		{"enter", "select"},
	}
	if m.hasRemote && m.behindOrigin > 0 {
		helpEntries = append(helpEntries, helpEntry{"p", "pull"})
	}
	if m.behindMain > 0 {
		helpEntries = append(helpEntries, helpEntry{"s", "sync"})
	}
	helpEntries = append(helpEntries, helpEntry{"q", "quit"})
	b.WriteString(renderHelp(helpEntries))

	// Graph section
	graphSection := m.renderGraphSection()
	if graphSection != "" {
		b.WriteString("\n\n")
		b.WriteString(graphSection)
	}

	return m.styledBox(b.String())
}
