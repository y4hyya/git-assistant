package ui

import (
	"fmt"
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
)

// pushCommitSample caps how many outgoing subjects the screen lists. The true
// count is read separately, so the header never understates what is being sent.
const pushCommitSample = 8

// ── Entering the step ──────────────────────────────────

// enterPush reads everything the push screen shows and routes into it.
//
// Everything, once, here — the same contract as enterConfirm. A View func runs
// on every keypress, every resize and every spinner tick, so `git log`,
// `rev-list` and the upstream probe cannot live there.
//
// fromMenu records where the user came from. The wizard reaches this step by
// finishing a commit and has a Done screen waiting behind it; the dashboard
// reaches it directly and wants a status note and its own screen back.
func (m *Model) enterPush(fromMenu bool) {
	m.pushReturnToMenu = fromMenu
	m.pushBranch = m.branch
	// A fresh visit: the behind-origin check gets one shot (declining the
	// offered pull must not make pushing unreachable), and any verdict from a
	// previous push belongs to that push. These used to be cleared only by
	// resetWizard, which a dashboard-initiated push never goes through — so the
	// second push of a session opened on the first one's error.
	m.pushCheckDone = false
	m.pushed = false
	m.pushFailed = false
	m.pushErr = nil

	ahead, behind, hasUpstream := git.GetAheadBehind(m.branch)
	m.pushAhead = ahead
	m.pushBehind = behind
	m.pushHasUpstream = hasUpstream

	// Measured against origin/<branch>, which is where the push goes — not
	// against the configured upstream, which a hand-edited config can point
	// somewhere else entirely.
	m.pushOutgoingTotal = git.CountOutgoingCommits(m.branch)
	m.pushOutgoing = nil
	if m.pushOutgoingTotal > 0 {
		m.pushOutgoing = git.GetOutgoingCommits(m.branch, pushCommitSample)
	}

	// The commit a force push would replace, as of the moment this screen was
	// built. It becomes the lease (see git.PushForceWithLease): the app's own
	// background fetch would otherwise be able to update the default lease
	// underneath the user and turn "replace the commit I showed you" into
	// "replace whatever is there now".
	m.pushLeaseSHA = git.RemoteTrackingSHA(m.branch)

	m.pushForce = m.forcePushRequired()
	m.step = stepPush
}

// forcePushRequired reports whether the only push that can land here is a
// force-with-lease.
//
// historyRewritten is the first gate, and it is set only when THIS session
// amended or undid a commit origin already had. A branch that is merely behind
// has not been rewritten — someone else pushed, the answer is a pull, and
// forcing would delete their commits. That case must never reach the `f` key.
func (m Model) forcePushRequired() bool {
	if !m.historyRewritten || !m.originHoldsTheRewrittenCommit() {
		return false
	}
	// Diverged: origin holds the pre-rewrite commit, and we have its
	// replacement to put there.
	if m.pushAhead > 0 && m.pushBehind > 0 {
		return true
	}
	// The amend flow knows what it just did without consulting the counts: it
	// rewrote a commit IsLastCommitPushed() reported as already on a remote.
	return m.amendPushed && m.pushHasUpstream
}

// originHoldsTheRewrittenCommit reports whether origin's tip is still exactly
// the commit the rewrite replaced.
//
// This is the second gate, and it is not theoretical. "Nobody else pushed
// meanwhile" cannot be left to git's default lease, because the lease is taken
// against the remote-tracking ref and THIS APP updates that ref on its own: a
// background fetch pulls the other person's commit into origin/<branch>, the
// lease then happily expects it, and the force deletes their work. Comparing
// against the commit we actually rewrote closes that: someone pushing on top of
// it moves origin's tip somewhere we never promised to replace.
//
// Unknown base (a rewrite recorded before this reading existed) falls back to
// the older behaviour rather than blocking a legitimate push.
func (m Model) originHoldsTheRewrittenCommit() bool {
	if m.rewriteBaseSHA == "" {
		return true
	}
	return m.pushLeaseSHA == m.rewriteBaseSHA
}

// forceLeaseSHA is the commit git must find on origin for the force to go
// through. The rewritten commit when we know it, otherwise whatever origin held
// when this screen was built.
func (m Model) forceLeaseSHA() string {
	if m.rewriteBaseSHA != "" {
		return m.rewriteBaseSHA
	}
	return m.pushLeaseSHA
}

// isNonFastForward reports whether git refused a push because the remote branch
// is not an ancestor of what we tried to send.
func isNonFastForward(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first") ||
		strings.Contains(msg, "rejected")
}

// ── Update ──────────────────────────────────────────────

func (m Model) updatePush(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Forward spinner ticks while pushing
	if m.pushing {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch keyMsg.String() {
	case "enter", "y":
		// In force mode enter deliberately does nothing: replacing a commit on
		// origin is not something to walk into with the key that means "next".
		if m.pushForce {
			return m, nil
		}
		// Pre-push sync check — if the current branch is behind origin, offer a
		// pull first (a plain push would be rejected, which is a confusing way
		// for a beginner to learn this). Strictly a suggestion, so it fires
		// once per visit: re-running it on every enter meant declining the pull
		// bounced the user back here forever, with no reachable push at all.
		if !m.pushCheckDone {
			m.pushCheckDone = true
			if m.populateSyncDialog() && m.syncPullCurrent {
				m.syncReturnStep = stepPush
				m.step = stepSync
				return m, nil
			}
		}
		m.pushing = true
		return m, tea.Batch(doPush(m.branch), m.spinner.Tick)
	case "f":
		if !m.pushForce {
			return m, nil
		}
		m.pushing = true
		return m, tea.Batch(doForcePush(m.branch, m.forceLeaseSHA()), m.spinner.Tick)
	case "n", "esc":
		// Leaving the step — the next visit gets a fresh check.
		m.pushCheckDone = false
		if m.pushReturnToMenu {
			// Nothing was committed on the way in, so there is no summary to
			// show; the dashboard is the honest place to land.
			return m, m.returnToMenu()
		}
		m.step = stepDone
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	return m, nil
}

func doPush(branch string) tea.Cmd {
	return func() tea.Msg {
		return pushResultMsg{err: git.Push(branch)}
	}
}

func doForcePush(branch, leaseSHA string) tea.Cmd {
	return func() tea.Msg {
		return pushResultMsg{err: git.PushForceWithLease(branch, leaseSHA), forced: true}
	}
}

// ── View ────────────────────────────────────────────────

func (m Model) viewPush() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	if m.pushReturnToMenu {
		// No commit was made on the way in, so the wizard breadcrumb would
		// check-mark four steps that never ran.
		b.WriteString(stepStyle.Render("  Push to origin"))
	} else {
		b.WriteString(m.renderProgress())
	}
	b.WriteString("\n")
	if !m.pushReturnToMenu {
		b.WriteString(stepStyle.Render("  Push to remote"))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Commit success. Only after a commit in this run — commitHash is what says
	// so. The stats come from the commit command (see doCommit); this used to
	// fork `git diff --stat` from inside the View, i.e. on every keypress, every
	// resize and every spinner tick.
	if m.commitHash != "" {
		msg := m.subjectLine(strings.TrimSpace(m.msgInput.Value()))
		// "Committed" would be a small lie on the amend path, which reaches this
		// screen through the Done screen's push key.
		verb := "Committed: "
		if m.amendMode {
			verb = "Amended: "
		}
		b.WriteString("  " + successStyle.Render(symDone) + " " + verb + msg + "\n")
		if m.commitStats != "" {
			b.WriteString("  " + dimStyle.Render(m.commitStats) + "\n")
		}
		b.WriteString("\n")
	}

	// What this push does — the whole point of the screen. It used to be a
	// branch picker, which said nothing about what was being sent and let the
	// user aim the current commits at a different branch's remote ref.
	switch {
	case m.pushForce:
		b.WriteString(m.renderForceExplainer())
	case !m.pushHasUpstream:
		b.WriteString("  " + highlightStyle.Render(symArrowUp+" Publish branch "+m.branch+" to origin") + "\n")
		b.WriteString("  " + dimStyle.Render("origin has no "+m.branch+" yet — this creates it and sets up tracking (-u),") + "\n")
		b.WriteString("  " + dimStyle.Render("so later pushes and pulls know where to go.") + "\n\n")
	case m.pushOutgoingTotal > 0:
		b.WriteString("  " + highlightStyle.Render(fmt.Sprintf("%s Push %s to origin/%s",
			symArrowUp, plural(m.pushOutgoingTotal, "commit", "commits"), m.branch)) + "\n\n")
	default:
		b.WriteString("  " + dimStyle.Render(symDone+" origin/"+m.branch+" already has everything on this branch") + "\n\n")
	}

	// The commits themselves, sampled.
	renderOutgoing(&b, m.pushOutgoing, m.pushOutgoingTotal)

	// Behind origin without a force on offer: the pull case. Say so here rather
	// than letting the rejection be the first the user hears of it — and never
	// dress it up as something `f` could solve.
	if !m.pushForce && m.pushHasUpstream && m.pushBehind > 0 {
		b.WriteString("  " + modifiedStyle.Render(fmt.Sprintf("%s origin/%s has %s you don't have yet",
			symWarn, m.branch, plural(m.pushBehind, "commit", "commits"))) + "\n")
		if m.historyRewritten && !m.originHoldsTheRewrittenCommit() {
			// The rewrite happened, but origin has moved past the commit it
			// replaced: those extra commits are somebody else's. Force is
			// withheld deliberately, and silence here reads as a bug.
			b.WriteString("  " + dimStyle.Render("Someone pushed on top of the commit you rewrote, so a force is not offered —") + "\n")
			b.WriteString("  " + dimStyle.Render("it would delete their work. Pull first, then push.") + "\n\n")
		} else {
			b.WriteString("  " + dimStyle.Render("Pushing will offer to pull them in first.") + "\n\n")
		}
	}

	// Loading
	if m.pushing {
		verb := "Pushing..."
		if m.pushForce {
			verb = "Force-pushing (with lease)..."
		}
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render(verb) + "\n")
	}

	// Error. A rejected push is the one error whose right answer depends on what
	// this session did to the local history — see formatErrorCtx.
	if m.err != nil {
		b.WriteString("\n  " + formatErrorCtx(m.err, m.historyRewritten) + "\n")
	}

	// Help bar. esc is a skip here, not a "back" — after a commit there is no
	// step behind this one to return to. Saying so is the difference between a
	// deliberate skip and a surprise.
	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))

	return m.styledBox(b.String())
}

// pushVerb names the non-force action for the help bar.
func (m Model) pushVerb() string {
	if !m.pushHasUpstream {
		return "publish branch"
	}
	return "push"
}

// renderForceExplainer writes the force-with-lease confirmation: what happened,
// why a normal push will bounce, and what the "lease" protects. Written for
// someone meeting the word "force" for the first time — the previous answer to
// this state was a Done screen printing a command to run somewhere else.
func (m Model) renderForceExplainer() string {
	var b strings.Builder
	b.WriteString("  " + modifiedStyle.Render(symWarn+" Force push required — your history was rewritten") + "\n\n")
	b.WriteString("  " + dimStyle.Render("You amended or undid a commit that origin already has, so the two") + "\n")
	b.WriteString("  " + dimStyle.Render("copies have diverged and a normal push will be rejected.") + "\n\n")
	b.WriteString("  " + dimStyle.Render("Force-with-lease replaces origin's copy ONLY if nobody else pushed") + "\n")
	b.WriteString("  " + dimStyle.Render("meanwhile — it is the safe force. If someone did, git refuses and") + "\n")
	b.WriteString("  " + dimStyle.Render("nothing is lost.") + "\n\n")
	b.WriteString(fmt.Sprintf("  %s  %s\n",
		modifiedStyle.Render(fmt.Sprintf("%s%d to send", symArrowUp, m.pushAhead)),
		dimStyle.Render(fmt.Sprintf("%s%d on origin will be replaced", symArrowDown, m.pushBehind))))
	b.WriteString("\n")
	return b.String()
}

// renderOutgoing lists the sampled subjects a push would send, disclosing how
// many it is not showing. Same shape as the sync dialog's incoming list.
func renderOutgoing(b *strings.Builder, subjects []string, total int) {
	if len(subjects) == 0 {
		return
	}
	for _, subj := range subjects {
		b.WriteString("    " + dimStyle.Render(symBullet) + " " + subj + "\n")
	}
	if total > len(subjects) {
		b.WriteString("    " + dimStyle.Render(fmt.Sprintf("... and %d more", total-len(subjects))) + "\n")
	}
	b.WriteString("\n")
}
