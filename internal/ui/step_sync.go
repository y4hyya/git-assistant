package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// syncCommitSample caps how many incoming commit subjects the dialog lists.
// The true count is read separately so the header never understates it.
const syncCommitSample = 10

// ── Async commands ─────────────────────────────────────

// doPullCurrent merges origin/<branch> into the current branch, allowing
// fast-forward. Used for catching up on the same branch (no artificial merge
// commits when you could have just advanced the branch pointer).
func doPullCurrent(branch string) tea.Cmd {
	return func() tea.Msg {
		// Auto-stash if dirty, mirroring branch-switch semantics.
		stashed := false
		stashRef := ""
		dirty, err := git.HasUncommittedChanges()
		if err != nil {
			return pullResultMsg{err: err, kind: pullKindCurrent}
		}
		if dirty {
			ref, stashErr := git.StashChanges()
			if stashErr != nil {
				return pullResultMsg{err: stashErr, kind: pullKindCurrent}
			}
			stashed = true
			stashRef = ref
		}
		upToDate, err := git.MergeFromOrigin(branch, false)
		if err != nil {
			// Restoring the stash is the handler's job: on a conflict it has
			// to abort the merge first, and only then does the tree sit at
			// the commit the stash applies cleanly onto.
			return pullResultMsg{
				err:           err,
				conflictFiles: git.GetConflictFiles(),
				kind:          pullKindCurrent,
				stashed:       stashed,
				stashRef:      stashRef,
			}
		}
		if stashed {
			if err := git.StashPop(); err != nil {
				git.CleanupFailedStashPop()
				return pullResultMsg{
					err: recoveryError{fmt.Errorf("pulled, but restoring your uncommitted changes conflicted — the working tree was reset clean and nothing was lost. %s", stashRecoveryHint(stashRef))},
					// Already handled here — don't let the handler pop twice.
					kind:          pullKindCurrent,
					stashOrphaned: true,
				}
			}
		}
		// stashRestored is reported, not inferred: the note has to disclose a
		// round trip the user never saw happen.
		return pullResultMsg{kind: pullKindCurrent, upToDate: upToDate, stashRestored: stashed}
	}
}

// doSyncMain merges origin/<main> into current with --no-ff (explicit merge
// commit, since this is an upstream integration, not catch-up).
func doSyncMain(mainBranch string) tea.Cmd {
	return func() tea.Msg {
		stashed := false
		stashRef := ""
		dirty, err := git.HasUncommittedChanges()
		if err != nil {
			return pullResultMsg{err: err, kind: pullKindMain}
		}
		if dirty {
			ref, stashErr := git.StashChanges()
			if stashErr != nil {
				return pullResultMsg{err: stashErr, kind: pullKindMain}
			}
			stashed = true
			stashRef = ref
		}
		upToDate, err := git.MergeFromOrigin(mainBranch, true)
		if err != nil {
			// See doPullCurrent: the handler owns stash recovery because it
			// aborts the merge first.
			return pullResultMsg{
				err:           err,
				conflictFiles: git.GetConflictFiles(),
				kind:          pullKindMain,
				stashed:       stashed,
				stashRef:      stashRef,
			}
		}
		if stashed {
			if err := git.StashPop(); err != nil {
				git.CleanupFailedStashPop()
				return pullResultMsg{
					err:           recoveryError{fmt.Errorf("merged, but restoring your uncommitted changes conflicted — the working tree was reset clean and nothing was lost. %s", stashRecoveryHint(stashRef))},
					kind:          pullKindMain,
					stashOrphaned: true,
				}
			}
		}
		return pullResultMsg{kind: pullKindMain, upToDate: upToDate, stashRestored: stashed}
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
		return m.exitSyncDialog()
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
func (m Model) exitSyncDialog() (Model, tea.Cmd) {
	m.clearSyncDialog()
	if m.syncReturnStep == stepMenu {
		// Back to the dashboard through the one shared door: a pull moves
		// HEAD and can restore a stash, so status and graphs must be re-read.
		// (The dialog only ever returns to the menu from outside the wizard,
		// so the reset returnToMenu performs has nothing to discard.)
		cmd := m.returnToMenu()
		return m, cmd
	}
	if m.syncReturnStep == stepPush {
		// The push screen states exactly what the push will send, and every
		// number on it was read by enterPush before the dialog opened. A pull
		// invalidates all of them at once — behind goes to zero, the merge
		// commit it may have created joins the outgoing list, the lease moves —
		// so re-read rather than re-render: the screen used to come back
		// warning about commits it had just pulled and offering to pull them
		// again, on a step whose next enter pushed immediately.
		m.enterPush(m.pushReturnToMenu)
		// The offer has been made and answered, whichever way. Re-running it
		// would bounce a declined pull straight back into this dialog.
		m.pushCheckDone = true
		return m, nil
	}
	m.step = m.syncReturnStep
	return m, nil
}

// clearSyncDialog drops the dialog's contents without deciding where to go
// next. Split out of exitSyncDialog for the one caller that has its own
// destination: a conflicting pull routes to the resolver, and it must not pick
// up exitSyncDialog's return-to-menu command — dropping that command would
// leave refreshing latched with no refresh in flight, and every later dashboard
// read would coalesce into a request that never runs.
func (m *Model) clearSyncDialog() {
	m.syncPullCurrent = false
	m.syncSyncMain = false
	m.syncRewriteHold = false
	m.syncIncomingCurr = nil
	m.syncIncomingMain = nil
	m.syncCurrTotal = 0
	m.syncMainTotal = 0
	m.syncAhead = 0
	m.syncDiverged = false
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
	if m.syncRewriteHold {
		b.WriteString("  " + highlightStyle.Render("origin/"+m.branch+" still has the commit you rewrote") + "\n\n")
	} else if m.syncDiverged {
		b.WriteString("  " + highlightStyle.Render("Your branch and origin/"+m.branch+" have diverged") + "\n\n")
	} else if m.syncPullCurrent && m.syncSyncMain {
		b.WriteString("  " + highlightStyle.Render("Your branch is out of sync") + "\n\n")
	} else if m.syncPullCurrent {
		b.WriteString("  " + highlightStyle.Render("origin/"+m.branch+" has new commits") + "\n\n")
	} else if m.syncSyncMain {
		b.WriteString("  " + highlightStyle.Render(m.syncMainBranchName+" has new commits") + "\n\n")
	}

	// The rewrite hold: no pull on offer, and the reason spelled out. Silence
	// here would read as "there is nothing to do", on a screen the user opened
	// precisely because the dashboard said they were behind.
	if m.syncRewriteHold {
		b.WriteString("  " + modifiedStyle.Render(fmt.Sprintf("%s pulling would bring back the commit you amended or undid",
			symWarn)) + "\n")
		b.WriteString("  " + dimStyle.Render("origin/"+m.branch+" is \"ahead\" only because it still holds that commit, so a") + "\n")
		b.WriteString("  " + dimStyle.Render("pull here silently undoes the rewrite. It is not offered.") + "\n\n")
		b.WriteString("  " + dimStyle.Render("Open Push from the menu instead: it offers a force-with-lease, which") + "\n")
		b.WriteString("  " + dimStyle.Render("replaces origin's copy only if nobody else has pushed meanwhile.") + "\n\n")
	}

	// Diverged — pulling would undo a history rewrite
	if m.syncDiverged {
		b.WriteString("  " + modifiedStyle.Render(fmt.Sprintf("%s diverged from origin (%d ahead / %d behind)",
			symWarn, m.syncAhead, m.syncCurrTotal)) + "\n")
		b.WriteString("  " + dimStyle.Render("Pulling will merge the old commits back in. If you rewrote history") + "\n")
		b.WriteString("  " + dimStyle.Render("(amend or undo), skip this and open Push from the menu — it offers") + "\n")
		b.WriteString("  " + dimStyle.Render("a force-with-lease, which replaces origin's copy safely.") + "\n\n")
	}

	// Pull current — commit list
	if m.syncPullCurrent && len(m.syncIncomingCurr) > 0 {
		renderIncoming(&b, "origin/"+m.branch, m.syncIncomingCurr, m.syncCurrTotal)
	}

	// Sync main — commit list
	if m.syncSyncMain && len(m.syncIncomingMain) > 0 {
		renderIncoming(&b, m.syncMainBranchName, m.syncIncomingMain, m.syncMainTotal)
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
	b.WriteString(renderHelpRows(m.helpRows()))

	return m.styledBox(b.String())
}

// ── Helpers invoked from other steps ───────────────────

// populateSyncDialog inspects remote state and sets the dialog's fields.
// Returns true if either condition fired (i.e., the dialog is worth showing).
// Does NOT change m.step — caller decides whether to transition.
func (m *Model) populateSyncDialog() bool {
	// A detached HEAD tracks nothing and merges into nothing: every question
	// below would be asked about the branch we are NOT on. A merge already in
	// progress rules it out for a sharper reason — both offers here START a
	// merge, git refuses to run one while MERGE_HEAD exists, and this is the
	// function the startup auto-show calls, so the dialog would otherwise open
	// itself on top of an unresolved conflict.
	if !m.hasRemote || m.detached || m.mergeInProgress {
		return false
	}
	main := git.ResolveMainBranch()
	m.syncMainBranchName = main

	// Pull current: current branch is behind its upstream. A branch with no
	// upstream has nothing to pull.
	ahead, behind, hasUpstream := git.GetAheadBehind(m.branch)
	pullCurrent := hasUpstream && behind > 0
	var incomingCurr []string
	if pullCurrent {
		incomingCurr = git.GetIncomingCommits(m.branch, "origin/"+m.branch, syncCommitSample)
	}

	// Sync main: only meaningful when current branch is not main itself.
	syncMain := false
	mainTotal := 0
	var incomingMain []string
	if m.branch != main {
		mainTotal = git.CountIncomingCommits(m.branch, "origin/"+main)
		if mainTotal > 0 {
			incomingMain = git.GetIncomingCommits(m.branch, "origin/"+main, syncCommitSample)
			syncMain = len(incomingMain) > 0
		}
	}

	// A rewrite this session made, that origin is still holding the other half
	// of: pulling here re-merges the commit the user deliberately amended or
	// undid, and on the pure-deletion case (undo a pushed commit, don't
	// re-commit) it fast-forwards it straight back with no divergence warning to
	// even hint at what happened. The pull is not offered at all in that state —
	// the force-with-lease on the push screen is the operation that means what
	// the user asked for.
	rewriteHold := pullCurrent && m.historyRewritten && m.rewriteBaseSHA != "" &&
		git.RemoteTrackingSHA(m.branch) == m.rewriteBaseSHA
	if rewriteHold {
		pullCurrent = false
		incomingCurr = nil
	}

	m.syncPullCurrent = pullCurrent
	m.syncSyncMain = syncMain
	m.syncRewriteHold = rewriteHold
	m.syncIncomingCurr = incomingCurr
	m.syncIncomingMain = incomingMain
	m.syncCurrTotal = behind
	m.syncMainTotal = mainTotal
	m.syncAhead = ahead
	// Ahead AND behind at once: the branch forked from its upstream, or its
	// history was rewritten under it (amend/undo of a pushed commit). Pulling
	// merges the pre-rewrite commits back in — the exact thing the rewrite
	// was undoing — so the offer needs a warning attached. Under the hold there
	// is no pull to warn about; the hold's own block says more, and precisely.
	m.syncDiverged = !rewriteHold && hasUpstream && ahead > 0 && behind > 0

	return pullCurrent || syncMain || rewriteHold
}

// renderIncoming writes one "↓ <ref> (N new):" section with up to
// syncCommitSample subjects and an explicit "... and N more" when the sample
// is short of the real count. Reading the header off len(sample) alone used to
// render a 25-commit backlog as "(10 new)" with no hint that it was truncated.
func renderIncoming(b *strings.Builder, label string, subjects []string, total int) {
	if total < len(subjects) {
		total = len(subjects)
	}
	b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%s %s (%d new):", symArrowDown, label, total)) + "\n")
	for _, subj := range subjects {
		b.WriteString("    " + dimStyle.Render(symBullet) + " " + subj + "\n")
	}
	if total > len(subjects) {
		b.WriteString("    " + dimStyle.Render(fmt.Sprintf("... and %d more", total-len(subjects))) + "\n")
	}
	b.WriteString("\n")
}
