package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
)

// ── Async commands ─────────────────────────────────────

// doPullCurrent merges origin/<branch> into the current branch, allowing
// fast-forward. Used for catching up on the same branch (no artificial merge
// commits when you could have just advanced the branch pointer).
func doPullCurrent(branch string) tea.Cmd {
	return func() tea.Msg {
		// Auto-stash if dirty, mirroring branch-switch semantics.
		stashed := false
		if git.HasUncommittedChanges() {
			if err := git.StashChanges(); err != nil {
				return pullResultMsg{err: err, kind: pullKindCurrent}
			}
			stashed = true
		}
		if err := git.MergeFromOrigin(branch, false); err != nil {
			conflicts := git.GetConflictFiles()
			// If merge failed cleanly (no conflicts), try to restore stash
			if len(conflicts) == 0 && stashed {
				git.StashPop()
			}
			return pullResultMsg{err: err, conflictFiles: conflicts, kind: pullKindCurrent}
		}
		if stashed {
			if err := git.StashPop(); err != nil {
				git.CleanupFailedStashPop()
				return pullResultMsg{
					err:  fmt.Errorf("pulled, but stash-pop failed: %s", err.Error()),
					kind: pullKindCurrent,
				}
			}
		}
		return pullResultMsg{kind: pullKindCurrent}
	}
}

// doSyncMain merges origin/<main> into current with --no-ff (explicit merge
// commit, since this is an upstream integration, not catch-up).
func doSyncMain(mainBranch string) tea.Cmd {
	return func() tea.Msg {
		stashed := false
		if git.HasUncommittedChanges() {
			if err := git.StashChanges(); err != nil {
				return pullResultMsg{err: err, kind: pullKindMain}
			}
			stashed = true
		}
		if err := git.MergeFromOrigin(mainBranch, true); err != nil {
			conflicts := git.GetConflictFiles()
			if len(conflicts) == 0 && stashed {
				git.StashPop()
			}
			return pullResultMsg{err: err, conflictFiles: conflicts, kind: pullKindMain}
		}
		if stashed {
			if err := git.StashPop(); err != nil {
				git.CleanupFailedStashPop()
				return pullResultMsg{
					err:  fmt.Errorf("merged, but stash-pop failed: %s", err.Error()),
					kind: pullKindMain,
				}
			}
		}
		return pullResultMsg{kind: pullKindMain}
	}
}

// ── Update ─────────────────────────────────────────────

func (m Model) updateSync(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks during a pull (blocks input).
	if m.pulling {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "p":
		if !m.syncPullCurrent {
			return m, nil
		}
		m.pulling = true
		m.pullingKind = pullKindCurrent
		return m, tea.Batch(doPullCurrent(m.branch), m.spinner.Tick)
	case "s":
		if !m.syncSyncMain {
			return m, nil
		}
		m.pulling = true
		m.pullingKind = pullKindMain
		return m, tea.Batch(doSyncMain(m.syncMainBranchName), m.spinner.Tick)
	case "enter", "esc", "n":
		// Skip — return to caller without action.
		return m.exitSyncDialog(), nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

// exitSyncDialog resets dialog state and routes back to the step that launched
// it. Centralizes the cleanup so success, skip, and error paths stay in sync.
// The zero value of syncReturnStep is stepMenu, which is the correct default
// when the dialog was fired without an explicit return context.
func (m Model) exitSyncDialog() Model {
	m.step = m.syncReturnStep
	m.syncPullCurrent = false
	m.syncSyncMain = false
	m.syncIncomingCurr = nil
	m.syncIncomingMain = nil
	return m
}

// ── View ───────────────────────────────────────────────

func (m Model) viewSync() string {
	var b strings.Builder

	// Header
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Remote Sync"))
	b.WriteString("\n\n")

	// Heading
	if m.syncPullCurrent && m.syncSyncMain {
		b.WriteString("  " + highlightStyle.Render("Your branch is out of sync") + "\n\n")
	} else if m.syncPullCurrent {
		b.WriteString("  " + highlightStyle.Render("origin/"+m.branch+" has new commits") + "\n\n")
	} else if m.syncSyncMain {
		b.WriteString("  " + highlightStyle.Render(m.syncMainBranchName+" has new commits") + "\n\n")
	}

	// Pull current — commit list
	if m.syncPullCurrent && len(m.syncIncomingCurr) > 0 {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%s origin/%s (%d new):",
			symArrowDown, m.branch, len(m.syncIncomingCurr))) + "\n")
		for _, subj := range m.syncIncomingCurr {
			b.WriteString("    " + dimStyle.Render("•") + " " + subj + "\n")
		}
		b.WriteString("\n")
	}

	// Sync main — commit list
	if m.syncSyncMain && len(m.syncIncomingMain) > 0 {
		b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%s %s (%d new):",
			symArrowDown, m.syncMainBranchName, len(m.syncIncomingMain))) + "\n")
		for _, subj := range m.syncIncomingMain {
			b.WriteString("    " + dimStyle.Render("•") + " " + subj + "\n")
		}
		b.WriteString("\n")
	}

	// Spinner while operation is in flight
	if m.pulling {
		verb := "Pulling..."
		if m.pullingKind == pullKindMain {
			verb = "Syncing with " + m.syncMainBranchName + "..."
		}
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render(verb) + "\n")
	}

	// Error
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	// Help bar
	b.WriteString("\n")
	entries := []helpEntry{}
	if m.syncPullCurrent {
		entries = append(entries, helpEntry{"p", "pull"})
	}
	if m.syncSyncMain {
		entries = append(entries, helpEntry{"s", "sync with " + m.syncMainBranchName})
	}
	entries = append(entries, helpEntry{"enter", "skip"})
	b.WriteString(renderHelp(entries))

	return m.styledBox(b.String())
}

// ── Helpers invoked from other steps ───────────────────

// populateSyncDialog inspects remote state and sets the dialog's fields.
// Returns true if either condition fired (i.e., the dialog is worth showing).
// Does NOT change m.step — caller decides whether to transition.
func (m *Model) populateSyncDialog() bool {
	if !m.hasRemote {
		return false
	}
	main := git.ResolveMainBranch()
	m.syncMainBranchName = main

	// Pull current: current branch is behind origin/<current>
	ahead, behind := git.GetAheadBehind(m.branch)
	_ = ahead
	pullCurrent := behind > 0
	var incomingCurr []string
	if pullCurrent {
		incomingCurr = git.GetIncomingCommits(m.branch, "origin/"+m.branch, 10)
	}

	// Sync main: only meaningful when current branch is not main itself.
	syncMain := false
	var incomingMain []string
	if m.branch != main {
		mainBehind := git.GetIncomingCommits(m.branch, "origin/"+main, 10)
		if len(mainBehind) > 0 {
			syncMain = true
			incomingMain = mainBehind
		}
	}

	m.syncPullCurrent = pullCurrent
	m.syncSyncMain = syncMain
	m.syncIncomingCurr = incomingCurr
	m.syncIncomingMain = incomingMain

	return pullCurrent || syncMain
}
