package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git-assist/internal/git"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── The conflict resolver ───────────────────────────────
//
// Until this screen existed, every conflicting merge, pull and sync was
// auto-aborted: git-assist could START a merge it was structurally unable to
// FINISH, and told the user to "resolve in your terminal and retry". The abort
// is still here — it is `a`, one keypress — but it is a choice now, not the
// only outcome.
//
// ── The stash-ordering decision ─────────────────────────
//
// A merge on a dirty tree auto-stashes first (see doMergeBranch/doPullCurrent),
// so a conflict arrives with the user's uncommitted work parked in the stash.
// The old handlers restored it immediately, in this order:
//
//	git merge --abort   →   git stash pop
//
// which worked only BECAUSE of the abort: the abort put the tree back on the
// commit the stash was taken from, which is exactly where it applies cleanly.
//
// Not aborting removes that window, and git does not leave a second one.
// Traced in a scratch repo: `git stash pop` during a merge fails outright —
//
//	error: could not write index
//	s.txt: needs merge
//	The stash entry is kept in case you need it again.
//
// — because the index holds unmerged entries. There is no ordering in which the
// stash can come back while conflicts are open, and a pop attempted anyway
// would leave a failed apply layered on top of a half-merged tree.
//
// So: the pending auto-stash is left UNTOUCHED for the whole resolution, and is
// restored at exactly two points, both of which return the tree to a state a
// stash can apply onto:
//
//	continue:   git commit --no-edit   →   pop the parked entry
//	abort:      git merge --abort      →   pop the parked entry
//
// Either way the pop is last, and a failed pop takes the same recoveryError /
// stash-manager path every other auto-stash failure in this app takes — the
// entry stays in the stack and the banner says which SHA it is.
//
// ── Addressing the parked entry ─────────────────────────
//
// BY SHA, never `git stash pop` (which means stash@{0}, whatever is on top
// right now). The resolver deliberately lets the user leave — esc goes back to
// the dashboard, and the stash manager is one keypress from there — so the
// stack can be mutated in the middle of a resolution. Traced in a scratch repo,
// both halves of that were real damage:
//
//   - the parked entry dropped from the manager, and the positional pop then
//     restored an unrelated week-old stash INTO the merged tree, reporting it
//     as "your uncommitted changes were restored";
//   - the parked entry popped from the manager (which succeeds once every
//     conflict is staged), and the positional pop then failed with "No stash
//     entries found" — whereupon CleanupFailedStashPop's `git checkout -- .`
//     destroyed the changes that had just been restored, under a banner saying
//     nothing was lost.
//
// So: StashRefForSHA immediately before the pop (the same rule the stash
// manager follows), ErrNoSuchStash means SKIP — the entry was already applied
// or deleted, and there is nothing left to restore — and CleanupFailedStashPop
// runs ONLY for a genuinely conflicted apply of the right entry, which is the
// one case where git wrote markers and kept the stash. The stash manager's
// mutating keys are additionally locked while a merge is in progress (see
// updateStash), because the least confusing version of this is the one where
// the stack cannot move under the resolver in the first place.
//
// The consequence the screen has to own: while conflicts are open, the user's
// uncommitted work is NOT in the working tree. The header says so, because
// otherwise a beginner sees their edits missing and assumes the merge ate them.
//
// ── Surviving a quit ────────────────────────────────────
//
// Quitting mid-resolution leaves the merge on disk (and the stash in the
// stack). That is why NewModel checks git.MergeInProgress() and opens here —
// which also picks up a merge started outside git-assist entirely, where the
// labels come from MERGE_MSG instead of from the operation that started it.
//
// The parked stash cannot survive in memory, though, and a recovered merge that
// finishes without restoring it breaks the previous session's on-screen promise
// ("your changes come back when this finishes") without a word. So the
// association is written to a small file next to git's own merge state —
// `.git/git-assist-conflict-stash`, holding the stash SHA and the two branch
// labels. `.git` is the right home for exactly the reasons MERGE_HEAD lives
// there: it is repo-scoped, it survives a relaunch, it is never committed, and
// it disappears with the repository. It is written when the merge parks a
// stash, honoured by the startup recovery path, and deleted when the merge is
// finished or aborted. Every access is best-effort: a repository that will not
// give up its git directory still resolves conflicts, it just cannot remember
// the stash across a restart.

// conflictOrigin records what stopped, so the header can name it. The wording
// differs enough to matter: "merging feat into main" and "pulling origin/main"
// are the same git operation and completely different sentences to a beginner.
type conflictOrigin int

const (
	conflictFromMerge conflictOrigin = iota
	conflictFromPull
	conflictFromSync
	// conflictFromRecovered is a merge this screen did not start: found at
	// startup, either because the user quit mid-resolution or because they ran
	// `git merge` in a terminal.
	conflictFromRecovered
)

// conflictRow is one conflicted path as the screen tracks it. Resolved entries
// stay in the list — moved to their own section, with what was done to them —
// because "3 of 4 resolved" is only meaningful next to the four.
type conflictRow struct {
	file     git.ConflictFile
	resolved bool
	// how is what the user did, in the words the row shows: "kept your
	// version", "took feat's version", "edited and marked resolved".
	how string
}

// ── Async results ───────────────────────────────────────

// conflictResolveResultMsg carries one per-file resolution (o, t, or m).
// files is the unmerged list as it stands AFTER the operation — never a cached
// one: the whole point of re-reading is that git, not the screen, is the
// authority on what is still conflicted.
type conflictResolveResultMsg struct {
	err   error
	path  string
	how   string
	files []git.ConflictFile
	// listed says the re-read itself succeeded. Without it an empty slice is
	// ambiguous — "nothing is conflicted any more" and "git status failed" look
	// identical, and taking the second for the first marks every file resolved
	// and offers to commit a merge that is still unmerged.
	listed bool
}

// conflictSaveResultMsg is the editor's save. Deliberately its own message
// rather than saveResultMsg: that handler belongs to the file selector, and it
// replaces m.files and the wizard's cursor with a fresh git status.
type conflictSaveResultMsg struct {
	err  error
	path string
	// markers reports whether what was just written still holds conflict
	// fences, read on the command goroutine off the bytes that were saved.
	markers bool
	files   []git.ConflictFile
	listed  bool // see conflictResolveResultMsg
}

// conflictFinishResultMsg is `c`: the merge commit, then the stash.
type conflictFinishResultMsg struct {
	err error
	// resolved is how many files the user decided, counted before the commit
	// so the note can say what the merge cost.
	resolved      int
	source        string
	target        string
	stashRestored bool
	stashOrphaned bool
	// stashMissing: the parked entry was not in the stack any more, so there was
	// nothing to pop. Reported rather than inferred — silence here reads as "the
	// merge restored my work" on a tree where nothing came back.
	stashMissing bool
	stashRef     string
}

// conflictAbortResultMsg is `a`: the abort, then the stash.
type conflictAbortResultMsg struct {
	err           error
	stashRestored bool
	stashOrphaned bool
	stashMissing  bool
	stashRef      string
}

// ── Async commands ──────────────────────────────────────

// doConflictResolve keeps one side of one file and re-reads the unmerged list.
func doConflictResolve(f git.ConflictFile, side git.ConflictSide, how string) tea.Cmd {
	return func() tea.Msg {
		err := git.ResolveConflict(f, side)
		files, listErr := git.ConflictFiles()
		if err == nil {
			err = listErr
		}
		return conflictResolveResultMsg{
			err: err, path: f.Path, how: how, files: files, listed: listErr == nil,
		}
	}
}

// doConflictMark stages a file the user resolved by hand.
func doConflictMark(path string) tea.Cmd {
	return func() tea.Msg {
		err := git.StageResolved(path)
		files, listErr := git.ConflictFiles()
		if err == nil {
			err = listErr
		}
		return conflictResolveResultMsg{
			err: err, path: path, how: "edited and marked resolved",
			files: files, listed: listErr == nil,
		}
	}
}

// doConflictSave writes the editor's buffer back. It does NOT stage: saving an
// intermediate state is a normal thing to do halfway through untangling a
// conflict, and marking it resolved is a separate, deliberate keypress.
func doConflictSave(path, content string) tea.Cmd {
	return func() tea.Msg {
		if err := git.WriteFileContent(path, content); err != nil {
			return conflictSaveResultMsg{err: err, path: path}
		}
		files, err := git.ConflictFiles()
		return conflictSaveResultMsg{
			err:     err,
			path:    path,
			markers: git.HasConflictMarkers(content),
			files:   files,
			listed:  err == nil,
		}
	}
}

// parkedStashOutcome is what restoring the merge's parked auto-stash did. Four
// outcomes, and they are genuinely different states of the repository — see
// restoreParkedStash.
type parkedStashOutcome struct {
	restored bool
	missing  bool
	orphaned bool
	err      error
}

// restoreParkedStash pops the entry this merge parked, addressed by its SHA.
//
// done names what has already happened ("the merge is committed"), because
// every message below has to start by saying that the merge itself is over —
// the stash is the part that may not have worked.
//
// The three failure paths are deliberately NOT the same:
//
//   - gone: the entry is not in the stack, so nothing is restored and nothing
//     is cleaned up. This is a normal outcome — the user can have applied it
//     from the stash manager — and it is reported, not treated as an error.
//   - conflicted: git wrote markers and KEPT the entry, so the working tree is
//     reset clean and the banner points at the stash manager. The only path
//     that may run CleanupFailedStashPop.
//   - refused: git stopped before touching anything. The tree and the stash are
//     exactly as they were, so cleaning up here would destroy work git did not.
func restoreParkedStash(sha, done string) parkedStashOutcome {
	ref, err := git.StashRefForSHA(sha)
	if err != nil {
		if errors.Is(err, git.ErrNoSuchStash) {
			return parkedStashOutcome{missing: true}
		}
		// The stack could not be read at all. Nothing was touched, and guessing
		// at stash@{0} is exactly the guess this function exists to remove.
		return parkedStashOutcome{orphaned: true, err: recoveryError{fmt.Errorf(
			"%s, but the stash list could not be read, so your uncommitted changes were left parked. %s",
			done, stashRecoveryHint(sha))}}
	}
	popErr := git.StashPopRef(ref)
	switch {
	case popErr == nil:
		return parkedStashOutcome{restored: true}
	case errors.Is(popErr, git.ErrStashConflict):
		git.CleanupFailedStashPop()
		return parkedStashOutcome{orphaned: true, err: recoveryError{fmt.Errorf(
			"%s, but restoring your uncommitted changes conflicted — the working tree was reset clean and nothing was lost. %s",
			done, stashRecoveryHint(sha))}}
	default:
		return parkedStashOutcome{orphaned: true, err: recoveryError{fmt.Errorf(
			"%s, but your uncommitted changes could not be restored: %v — nothing in your working tree was changed. %s",
			done, popErr, stashRecoveryHint(sha))}}
	}
}

// doConflictFinish commits the merge and only then restores the auto-stash.
// See the ordering note at the top of this file: the pop is impossible before
// the commit, and the commit is what makes the tree stash-appliable again.
func doConflictFinish(resolved int, source, target string, stashed bool, stashRef string) tea.Cmd {
	return func() tea.Msg {
		if err := git.MergeContinue(); err != nil {
			return conflictFinishResultMsg{err: err, source: source, target: target}
		}
		msg := conflictFinishResultMsg{
			resolved: resolved, source: source, target: target, stashRef: stashRef,
		}
		if stashed {
			out := restoreParkedStash(stashRef, "the merge is committed")
			msg.stashRestored = out.restored
			msg.stashMissing = out.missing
			msg.stashOrphaned = out.orphaned
			msg.err = out.err
		}
		return msg
	}
}

// doConflictAbort throws the merge away and puts the auto-stash back. Same
// order, same reason.
func doConflictAbort(stashed bool, stashRef string) tea.Cmd {
	return func() tea.Msg {
		if err := git.MergeAbort(); err != nil {
			return conflictAbortResultMsg{err: err, stashRef: stashRef}
		}
		msg := conflictAbortResultMsg{stashRef: stashRef}
		if stashed {
			out := restoreParkedStash(stashRef, "the merge was aborted")
			msg.stashRestored = out.restored
			msg.stashMissing = out.missing
			msg.stashOrphaned = out.orphaned
			msg.err = out.err
		}
		return msg
	}
}

// ── The parked-stash association on disk ────────────────

// conflictStateName is the file, inside the git directory, that remembers which
// stash a merge in progress parked. See "Surviving a quit" at the top of this
// file for why it lives there and not in a config directory.
const conflictStateName = "git-assist-conflict-stash"

// conflictStateSep separates the record's three fields. A tab cannot appear in
// a branch name (git refuses the ref) or in a short SHA.
const conflictStateSep = "\t"

// conflictStatePath resolves the file's location. `.git` is normally a
// directory in the working tree root — the app chdirs there at startup — but a
// linked worktree and a submodule both have a `.git` FILE holding
// "gitdir: <path>", and the merge state of those lives at the far end of it.
func conflictStatePath() (string, error) {
	root, err := git.RepoToplevel()
	if err != nil {
		return "", err
	}
	dot := filepath.Join(root, ".git")
	info, err := os.Stat(dot)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return filepath.Join(dot, conflictStateName), nil
	}
	data, err := os.ReadFile(dot)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", fmt.Errorf("%s is neither a git directory nor a gitdir pointer", dot)
	}
	dir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return filepath.Join(dir, conflictStateName), nil
}

// writeConflictState records the stash a merge parked, plus the labels the
// header needs, so a relaunch can pick all three up. Best-effort: failing to
// write it costs the association across a restart, nothing else.
func writeConflictState(stashRef, source, target string) {
	if stashRef == "" {
		return
	}
	path, err := conflictStatePath()
	if err != nil {
		return
	}
	record := strings.Join([]string{stashRef, source, target}, conflictStateSep) + "\n"
	_ = os.WriteFile(path, []byte(record), 0o644)
}

// readConflictState returns what a previous process parked, or empty strings.
func readConflictState() (stashRef, source, target string) {
	path, err := conflictStatePath()
	if err != nil {
		return "", "", ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", ""
	}
	fields := strings.Split(strings.TrimSpace(string(data)), conflictStateSep)
	if len(fields) < 3 || fields[0] == "" {
		return "", "", ""
	}
	return fields[0], fields[1], fields[2]
}

// clearConflictState drops the association. Called when the merge is over,
// however it ended — an entry left behind would have the NEXT recovered merge
// claim a stash that belongs to nothing.
func clearConflictState() {
	path, err := conflictStatePath()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// ── Entry ───────────────────────────────────────────────

// beginConflicts is the interception path: a merge, pull or sync has just
// stopped on conflicts and is STILL IN PROGRESS on disk. stashed/stashRef carry
// the auto-stash that must not be touched until the merge is finished or
// aborted (see the ordering note at the top of this file).
func (m *Model) beginConflicts(origin conflictOrigin, source, target string, stashed bool, stashRef string) {
	m.conflictOrigin = origin
	m.conflictSource = source
	m.conflictTarget = target
	m.conflictStashed = stashed
	m.conflictStashRef = stashRef
	if stashed {
		// The promise this screen makes ("your changes come back when this
		// finishes") has to outlive the process that made it.
		writeConflictState(stashRef, source, target)
	}
	m.loadConflicts()
}

// openConflicts is the re-entry path: startup detection, and the dashboard's
// "Resolve conflicts" entry. Returns false when there is no merge to resolve —
// the flag can outlive the merge by a moment (a dashboard snapshot taken before
// the commit landing after it), and the menu must not open an empty screen.
func (m *Model) openConflicts() bool {
	if !git.MergeInProgress() {
		m.mergeInProgress = false
		// A merge that is over cannot have a parked stash waiting on it, and a
		// record left behind would be claimed by the next one.
		clearConflictState()
		return false
	}
	// A merge this process did not start knows nothing about the stash it
	// parked, and finishing without restoring it — silently — is how the
	// previous session's promise gets broken. Recover the association first,
	// since it also carries better labels than MERGE_MSG does.
	if !m.conflictStashed && m.conflictStashRef == "" {
		m.recoverParkedStash()
	}
	// Labels are only re-derived when we have none: a merge this session
	// started already knows both sides, and MERGE_MSG's "into <target>" is
	// omitted by git whenever the target is the default branch.
	if m.conflictSource == "" {
		source, target := git.MergeLabels()
		m.conflictOrigin = conflictFromRecovered
		m.conflictSource = source
		m.conflictTarget = target
	}
	m.loadConflicts()
	return true
}

// recoverParkedStash re-associates the stash a previous process parked for the
// merge that is still on disk.
//
// The entry is verified before it is adopted: a stash the user has since popped
// from the manager is gone, and carrying "your uncommitted changes are parked"
// on the header over an empty stack is the same lie in the other direction.
func (m *Model) recoverParkedStash() {
	sha, source, target := readConflictState()
	if sha == "" {
		return
	}
	if _, err := git.StashRefForSHA(sha); err != nil {
		// Already applied or deleted. The record is stale either way.
		clearConflictState()
		return
	}
	m.conflictStashed = true
	m.conflictStashRef = sha
	m.conflictOrigin = conflictFromRecovered
	if m.conflictSource == "" {
		m.conflictSource = source
	}
	if m.conflictTarget == "" {
		m.conflictTarget = target
	}
}

// loadConflicts reads the unmerged list and puts the screen up. The list is
// re-read here and after every mutation — never carried across one.
func (m *Model) loadConflicts() {
	if m.conflictTarget == "" {
		m.conflictTarget = m.branch
	}
	m.mergeInProgress = true
	m.conflictRows = nil
	m.conflictCursor = 0
	m.conflictScroll = 0
	m.conflictMarkWarn = false
	m.conflictMarkPath = ""
	m.editMode = false
	m.editDirty = false
	m.confirmExit = false
	m.conflictEditPath = ""
	files, err := git.ConflictFiles()
	if err != nil {
		// Nothing to fold in, and nothing to claim: an unreadable status is not
		// "no conflicts". The screen opens empty with the error on it, and `c`
		// would come back from git with the same truth.
		m.err = err
	} else {
		m.applyConflictList(files)
	}
	m.step = stepConflicts
}

// applyConflictList folds a fresh unmerged listing into the rows on screen: a
// path git no longer reports is resolved, a path it reports that we have never
// seen is appended, and the unresolved half is kept at the top so the next
// decision is always under the cursor.
func (m *Model) applyConflictList(files []git.ConflictFile) {
	unmerged := make(map[string]git.ConflictFile, len(files))
	for _, f := range files {
		unmerged[f.Path] = f
	}
	seen := make(map[string]bool, len(m.conflictRows))
	rows := make([]conflictRow, 0, len(m.conflictRows)+len(files))
	for _, row := range m.conflictRows {
		seen[row.file.Path] = true
		if f, still := unmerged[row.file.Path]; still {
			// Kind can change under a resolution that git re-classified; take
			// git's word for it rather than the reading we opened with.
			row.file = f
			row.resolved = false
		} else if !row.resolved {
			row.resolved = true
			if row.how == "" {
				row.how = "resolved"
			}
		}
		rows = append(rows, row)
	}
	for _, f := range files {
		if !seen[f.Path] {
			rows = append(rows, conflictRow{file: f})
		}
	}
	// Stable partition: unresolved first, in the order git listed them.
	ordered := make([]conflictRow, 0, len(rows))
	for _, r := range rows {
		if !r.resolved {
			ordered = append(ordered, r)
		}
	}
	for _, r := range rows {
		if r.resolved {
			ordered = append(ordered, r)
		}
	}
	m.conflictRows = ordered
	m.clampConflictCursor()
}

func (m *Model) clampConflictCursor() {
	if m.conflictCursor >= len(m.conflictRows) {
		m.conflictCursor = max(0, len(m.conflictRows)-1)
	}
	if m.conflictCursor < 0 {
		m.conflictCursor = 0
	}
	if m.conflictScroll > m.conflictCursor {
		m.conflictScroll = m.conflictCursor
	}
	if m.conflictScroll < 0 {
		m.conflictScroll = 0
	}
}

// ── Queries ─────────────────────────────────────────────

// conflictBusy reports whether one of this screen's operations is running.
func (m Model) conflictBusy() bool {
	return m.conflictResolving || m.conflictFinishing || m.conflictAborting || m.saving
}

// conflictSelected returns the row under the cursor.
func (m Model) conflictSelected() (conflictRow, bool) {
	if m.conflictCursor < 0 || m.conflictCursor >= len(m.conflictRows) {
		return conflictRow{}, false
	}
	return m.conflictRows[m.conflictCursor], true
}

// conflictActionable reports whether the row under the cursor still has a
// decision to make. A resolved file is out of o/t/e/m entirely: once `git add`
// has recorded a version, the index no longer holds the two sides, and
// `git checkout --ours` there fails with "does not have our version".
func (m Model) conflictActionable() bool {
	row, ok := m.conflictSelected()
	return ok && !row.resolved
}

func (m Model) conflictUnresolved() int {
	n := 0
	for _, r := range m.conflictRows {
		if !r.resolved {
			n++
		}
	}
	return n
}

func (m Model) conflictResolvedCount() int { return len(m.conflictRows) - m.conflictUnresolved() }

// conflictDone reports whether every file has been decided — the gate on `c`.
//
// An EMPTY list counts as done, and that is not an edge case: a merge picked up
// at startup may have been resolved in a terminal already, and refusing to
// commit it here would leave the user with a repository this app can neither
// finish nor explain.
func (m Model) conflictDone() bool { return m.conflictUnresolved() == 0 }

// conflictSourceLabel names the incoming side. A merge picked up at startup may
// have no name to give (MERGE_MSG gone, or a merge of a raw SHA), and inventing
// a branch name there would be worse than the honest generic.
func (m Model) conflictSourceLabel() string {
	if m.conflictSource == "" {
		return "the incoming branch"
	}
	return m.conflictSource
}

func (m Model) conflictTargetLabel() string {
	if m.conflictTarget == "" {
		return m.branch
	}
	return m.conflictTarget
}

// conflictListRows is how many rows the list may draw — listRows minus the
// footer rows past the first and the two section headings, read off the footer
// itself so adding a key cannot silently delete the footer instead of a row.
func (m Model) conflictListRows() int {
	rows := m.listRows() - (m.footerHeight() - 1) - 4
	if rows < 4 {
		rows = 4
	}
	return rows
}

// ── Plain words for what git did ────────────────────────

// conflictDesc describes one conflict kind in the words a beginner needs, with
// the incoming branch named rather than called "theirs".
func conflictDesc(kind git.ConflictKind, source string) string {
	switch kind {
	case git.ConflictBothModified:
		return "both changed it"
	case git.ConflictBothAdded:
		return "you and " + source + " each added a file with this name"
	case git.ConflictTheyDeleted:
		return "you changed it, " + source + " deleted it"
	case git.ConflictWeDeleted:
		return "you deleted it, " + source + " changed it"
	case git.ConflictBothDeleted:
		return "both deleted it"
	case git.ConflictWeAdded:
		return "only you have this file"
	case git.ConflictTheyAdded:
		return "only " + source + " has this file"
	}
	return "conflicting changes"
}

// conflictSideLabel spells out what keeping one side DOES to this file. The
// delete variants are the reason it exists: on a file the other side removed,
// "take their version" means the file goes away, and a key labelled anything
// else is lying about a deletion.
func conflictSideLabel(f git.ConflictFile, side git.ConflictSide, source string) string {
	switch f.Kind {
	case git.ConflictTheyDeleted:
		if side == git.SideOurs {
			return "keep your version of the file"
		}
		return "delete the file — " + source + " deleted it"
	case git.ConflictWeDeleted:
		if side == git.SideOurs {
			return "keep the file deleted"
		}
		return "restore " + source + "'s version"
	case git.ConflictBothDeleted:
		return "delete the file — both sides did"
	case git.ConflictWeAdded:
		if side == git.SideOurs {
			return "keep your file"
		}
		return "delete the file — " + source + " does not have it"
	case git.ConflictTheyAdded:
		if side == git.SideOurs {
			return "delete the file — you do not have it"
		}
		return "take " + source + "'s file"
	}
	if side == git.SideOurs {
		return "keep YOUR version"
	}
	return "take " + source + "'s version"
}

// conflictKeyLabel is the footer's short form of the same thing. Short, and
// branch-free, so the bar does not change width as the cursor moves; the long
// form with the branch name is rendered above it, on the file it applies to.
func conflictKeyLabel(f git.ConflictFile, side git.ConflictSide) string {
	if !f.KeepsFile(side) {
		return "delete it"
	}
	if side == git.SideOurs {
		return "keep yours"
	}
	return "take theirs"
}

// conflictNoteFor is the past tense of conflictSideLabel — what the resolved
// row says happened.
func conflictNoteFor(f git.ConflictFile, side git.ConflictSide, source string) string {
	if !f.KeepsFile(side) {
		return "deleted the file"
	}
	if side == git.SideOurs {
		return "kept your version"
	}
	return "took " + source + "'s version"
}

// ── Update ──────────────────────────────────────────────

func (m Model) updateConflicts(msg tea.Msg) (tea.Model, tea.Cmd) {
	// One operation at a time. Every key here mutates the index, and a second
	// press dispatched against a list that is about to be replaced resolves the
	// wrong file — or commits a merge the first press is still resolving.
	if m.conflictBusy() {
		if _, ok := msg.(tea.KeyMsg); ok {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	// ── The editor ─────────────────────────────────────
	if m.editMode {
		if m.confirmExit {
			switch keyMsg.String() {
			case "y":
				m.confirmExit = false
				m.editMode = false
				m.editDirty = false
				m.conflictEditPath = ""
				return m, nil
			default:
				m.confirmExit = false
				return m, nil
			}
		}
		switch keyMsg.String() {
		case "ctrl+s":
			m.saving = true
			return m, doConflictSave(m.conflictEditPath, m.editArea.Value())
		case "esc":
			if m.editDirty {
				m.confirmExit = true
				return m, nil
			}
			m.editMode = false
			m.conflictEditPath = ""
			return m, nil
		default:
			var cmd tea.Cmd
			prev := m.editArea.Value()
			m.editArea, cmd = m.editArea.Update(msg)
			if m.editArea.Value() != prev {
				m.editDirty = true
			}
			return m, cmd
		}
	}

	// ── "throw the whole merge away" confirmation ──────
	//
	// `a` is select-all in the file selector and apply in the stash manager —
	// two screens that look exactly like this one — and it used to abort here on
	// the first press, discarding every resolution with no undo. Every other
	// destructive key in the app asks first; this one asks too, and states what
	// it costs.
	if m.conflictConfirmAbort {
		m.conflictConfirmAbort = false
		if keyMsg.String() == "y" {
			m.conflictAborting = true
			return m, tea.Batch(doConflictAbort(m.conflictStashed, m.conflictStashRef), m.spinner.Tick)
		}
		return m, nil
	}

	// ── "markers are still in there" confirmation ──────
	if m.conflictMarkWarn {
		path := m.conflictMarkPath
		m.conflictMarkWarn = false
		m.conflictMarkPath = ""
		if keyMsg.String() == "y" {
			m.conflictResolving = true
			return m, tea.Batch(doConflictMark(path), m.spinner.Tick)
		}
		return m, nil
	}

	// ── The list ───────────────────────────────────────
	switch keyMsg.String() {
	case "up", "k":
		if m.conflictCursor > 0 {
			m.conflictCursor--
		}
	case "down", "j":
		if m.conflictCursor < len(m.conflictRows)-1 {
			m.conflictCursor++
		}
	case "o", "t":
		row, ok := m.conflictSelected()
		if !ok || row.resolved {
			return m, nil
		}
		side := git.SideOurs
		if keyMsg.String() == "t" {
			side = git.SideTheirs
		}
		m.conflictResolving = true
		return m, tea.Batch(
			doConflictResolve(row.file, side, conflictNoteFor(row.file, side, m.conflictSourceLabel())),
			m.spinner.Tick)
	case "e":
		return m.openConflictEditor()
	case "m":
		row, ok := m.conflictSelected()
		if !ok || row.resolved {
			return m, nil
		}
		// Read the file as it stands on disk, not as the editor last had it:
		// `m` is reachable without ever opening the editor, and the user may
		// have edited the file in another window.
		content, err := git.ReadFileContent(row.file.Path)
		if err == nil && git.HasConflictMarkers(content) {
			m.conflictMarkWarn = true
			m.conflictMarkPath = row.file.Path
			return m, nil
		}
		m.conflictResolving = true
		return m, tea.Batch(doConflictMark(row.file.Path), m.spinner.Tick)
	case "c":
		if !m.conflictDone() {
			m.err = fmt.Errorf("%s %s still to decide — pick a version with o or t first",
				symWarn, plural(m.conflictUnresolved(), "file", "files"))
			return m, nil
		}
		m.conflictFinishing = true
		return m, tea.Batch(
			doConflictFinish(len(m.conflictRows), m.conflictSourceLabel(), m.conflictTargetLabel(),
				m.conflictStashed, m.conflictStashRef),
			m.spinner.Tick)
	case "a":
		m.conflictConfirmAbort = true
		return m, nil
	case "esc":
		// The merge stays in progress, deliberately: the dashboard carries a
		// permanent "Resolve conflicts" entry while it does, and the user may
		// legitimately want to look at the history of what they are merging.
		return m, m.returnToMenu()
	case "q":
		m.quitting = true
		return m, tea.Quit
	}

	// Keep the cursor inside the window the view draws.
	rows := m.conflictListRows()
	if m.conflictCursor < m.conflictScroll {
		m.conflictScroll = m.conflictCursor
	}
	if m.conflictCursor >= m.conflictScroll+rows {
		m.conflictScroll = m.conflictCursor - rows + 1
	}
	if m.conflictScroll > len(m.conflictRows)-rows {
		m.conflictScroll = len(m.conflictRows) - rows
	}
	if m.conflictScroll < 0 {
		m.conflictScroll = 0
	}
	return m, nil
}

// openConflictEditor loads the conflicted file — markers and all — into the
// same textarea the file selector edits with.
func (m Model) openConflictEditor() (tea.Model, tea.Cmd) {
	row, ok := m.conflictSelected()
	if !ok || row.resolved {
		return m, nil
	}
	content, err := git.ReadFileContent(row.file.Path)
	if err != nil {
		// A file neither side kept has nothing to edit; say which key does
		// apply rather than reporting a bare "no such file".
		m.err = fmt.Errorf("%s is not in your working tree — resolve it with o or t instead", row.file.Path)
		return m, nil
	}
	// Same guard the file selector applies: a NUL byte round-tripped through a
	// UTF-8 string comes back as U+FFFD, and ctrl+s would write that over the
	// file. Conflict markers are plain text and never trip this.
	if git.IsBinaryContent(content) {
		m.err = fmt.Errorf("%s is a binary file — pick a whole version with o or t", row.file.Path)
		return m, nil
	}
	m.editArea.SetValue(content)
	m.editArea.SetWidth(editAreaWidth(m.width))
	m.editArea.SetHeight(editAreaHeight(m.height))
	m.editArea.Focus()
	m.editMode = true
	m.editDirty = false
	m.conflictEditPath = row.file.Path
	return m, nil
}

// ── Result handling ─────────────────────────────────────

func (m Model) handleConflictResolve(msg conflictResolveResultMsg) (tea.Model, tea.Cmd) {
	m.conflictResolving = false
	m.startResult()
	if msg.err != nil {
		m.err = msg.err
		// Even a failure re-lists: git may have got half way (checked the file
		// out, failed to stage it), and the screen must show what IS — but only
		// if the listing itself is trustworthy.
		if msg.listed {
			m.applyConflictList(msg.files)
		}
		return m, nil
	}
	for i := range m.conflictRows {
		if m.conflictRows[i].file.Path == msg.path {
			m.conflictRows[i].how = msg.how
		}
	}
	m.applyConflictList(msg.files)
	m.setStatusNote("%s — %s", msg.path, msg.how)
	return m, nil
}

func (m Model) handleConflictSave(msg conflictSaveResultMsg) (tea.Model, tea.Cmd) {
	m.saving = false
	m.startResult()
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.editDirty = false
	m.editMode = false
	m.conflictEditPath = ""
	if msg.listed {
		m.applyConflictList(msg.files)
	}
	// Saving is not resolving: the file may well be a half-finished attempt,
	// and a save that silently staged it would commit that. `m` is the
	// deliberate second step, and the note says so.
	if msg.markers {
		m.setStatusNote("Saved %s — conflict markers are still in it; press m to mark it resolved anyway", msg.path)
	} else {
		m.setStatusNote("Saved %s — press m to mark it resolved", msg.path)
	}
	return m, nil
}

func (m Model) handleConflictFinish(msg conflictFinishResultMsg) (tea.Model, tea.Cmd) {
	m.conflictFinishing = false
	m.startResult()
	if msg.stashOrphaned {
		m.noteOrphanedStash()
	}
	if msg.err != nil && !msg.stashOrphaned {
		// The commit itself failed: the merge is still in progress and every
		// resolution the user made is still staged, so stay put.
		m.err = msg.err
		// Same rule as the per-file results: a listing that failed is not
		// evidence that nothing is conflicted.
		if files, listErr := git.ConflictFiles(); listErr == nil {
			m.applyConflictList(files)
		}
		return m, nil
	}
	// The merge commit landed. Whatever happened to the stash afterwards, the
	// merge is over.
	m.clearConflicts()
	if msg.err != nil {
		m.err = msg.err
	} else {
		if msg.resolved > 0 {
			m.setStatusNote("Merged %s into %s — %s resolved",
				msg.source, msg.target, plural(msg.resolved, "conflict", "conflicts"))
		} else {
			// A merge that arrived here already resolved (finished in a
			// terminal, then picked up at startup) has no count to report, and
			// "0 conflicts resolved" reads as a failure.
			m.setStatusNote("Merged %s into %s", msg.source, msg.target)
		}
		switch {
		case msg.stashRestored:
			m.appendNoteClause("your uncommitted changes were stashed and restored")
		case msg.stashMissing:
			// Reported, never inferred: the entry was gone before the pop (the
			// user applied or deleted it from the stash manager), so nothing
			// came back HERE and the tree is not missing anything either.
			m.appendNoteClause("your parked stash was already applied or deleted — nothing extra was restored")
		}
	}
	return m, m.returnToMenu()
}

func (m Model) handleConflictAbort(msg conflictAbortResultMsg) (tea.Model, tea.Cmd) {
	m.conflictAborting = false
	m.startResult()
	if msg.stashOrphaned {
		m.noteOrphanedStash()
	}
	if msg.err != nil && !msg.stashOrphaned {
		// The abort failed, so the merge is still there and this screen is
		// still the way out of it.
		m.err = msg.err
		// Same rule as the per-file results: a listing that failed is not
		// evidence that nothing is conflicted.
		if files, listErr := git.ConflictFiles(); listErr == nil {
			m.applyConflictList(files)
		}
		return m, nil
	}
	m.clearConflicts()
	if msg.err != nil {
		m.err = msg.err
	} else {
		// "nothing changed" is only true of the repository's committed state.
		// The abort discards every resolution the user made, which is exactly
		// what the confirmation warned about, so the note says what happened
		// rather than reassuring past it.
		m.setStatusNote("Merge aborted — the repository is back to before the merge")
		switch {
		case msg.stashRestored:
			m.appendNoteClause("your uncommitted changes were restored")
		case msg.stashMissing:
			m.appendNoteClause("your parked stash was already applied or deleted — nothing extra was restored")
		}
	}
	return m, m.returnToMenu()
}

// clearConflicts drops everything about a merge that is over. The stash fields
// go with it: leaving conflictStashed set would make the NEXT conflict try to
// pop an entry this one already restored — and so does the record on disk, for
// the same reason one process later.
func (m *Model) clearConflicts() {
	m.mergeInProgress = false
	m.conflictRows = nil
	m.conflictCursor = 0
	m.conflictScroll = 0
	m.conflictSource = ""
	m.conflictTarget = ""
	m.conflictStashed = false
	m.conflictStashRef = ""
	m.conflictMarkWarn = false
	m.conflictMarkPath = ""
	m.conflictConfirmAbort = false
	m.conflictEditPath = ""
	m.editMode = false
	m.editDirty = false
	m.confirmExit = false
	clearConflictState()
}

// ── View ────────────────────────────────────────────────

func (m Model) viewConflicts() string {
	if m.editMode {
		return m.viewConflictEdit()
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	b.WriteString(stepStyle.Render("  Resolve Conflicts"))
	b.WriteString("\n\n")

	// ── What happened, in one paragraph ────────────────
	source := m.conflictSourceLabel()
	if m.conflictOrigin == conflictFromRecovered {
		b.WriteString("  " + modifiedStyle.Render(symWarn+" You have an unfinished merge from earlier") + "\n")
		b.WriteString("  " + dimStyle.Render("It is still in progress in this repository — finish it or undo it here.") + "\n\n")
	}
	b.WriteString("  " + highlightStyle.Render(m.conflictHeadline()) + "\n")
	b.WriteString("  " + dimStyle.Render("git needs you to pick which version to keep in each.") + "\n")
	if m.conflictStashed {
		// The auto-stash cannot come back until the merge is over (see the
		// ordering note at the top of this file). Unexplained, this reads as
		// "the merge ate my work".
		b.WriteString("  " + dimStyle.Render("Your uncommitted changes are parked in a stash and come back when this finishes.") + "\n")
	}
	b.WriteString("\n")

	// ── The abort confirmation ─────────────────────────
	// Costed in the two currencies the user has spent: the decisions they made,
	// and the uncommitted work the merge parked.
	if m.conflictConfirmAbort {
		b.WriteString("  " + modifiedStyle.Render("Undo the whole merge?") + "\n\n")
		if n := m.conflictResolvedCount(); n > 0 {
			b.WriteString("  " + dimStyle.Render(fmt.Sprintf(
				"%s you have already decided go back to conflicted — git merge --abort throws",
				plural(n, "file", "files"))) + "\n")
			b.WriteString("  " + dimStyle.Render("every resolution away, and there is no undo for it.") + "\n")
		} else {
			b.WriteString("  " + dimStyle.Render("The merge is abandoned and the repository goes back to how it was") + "\n")
			b.WriteString("  " + dimStyle.Render("before it started.") + "\n")
		}
		if m.conflictStashed {
			b.WriteString("  " + dimStyle.Render("Your parked uncommitted changes are restored afterwards.") + "\n")
		}
		b.WriteString("\n")
		b.WriteString(renderHelpRows(m.helpRows()))
		return m.styledBox(b.String())
	}

	// ── The marker confirmation ────────────────────────
	if m.conflictMarkWarn {
		b.WriteString("  " + modifiedStyle.Render(symWarn+" "+m.conflictMarkPath+" still has conflict markers in it") + "\n\n")
		b.WriteString("  " + dimStyle.Render("The <<<<<<< / ======= / >>>>>>> lines are still there, so the file would be") + "\n")
		b.WriteString("  " + dimStyle.Render("committed with them. Mark it resolved anyway?") + " " + dimStyle.Render("(y/N)") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelpRows(m.helpRows()))
		return m.styledBox(b.String())
	}

	b.WriteString("  " + dimStyle.Render(fmt.Sprintf("%d of %d resolved",
		m.conflictResolvedCount(), len(m.conflictRows))) + "\n\n")

	// ── The list ───────────────────────────────────────
	if len(m.conflictRows) == 0 {
		b.WriteString("  " + dimStyle.Render("No conflicting files are left.") + "\n")
		b.WriteString("  " + dimStyle.Render("Press c to finish the merge, or a to undo it.") + "\n")
	} else {
		rows := m.conflictListRows()
		start, end := listWindow(len(m.conflictRows), m.conflictScroll, rows)
		if start > 0 {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowUp, start)) + "\n")
		}
		headed := map[bool]bool{}
		for i := start; i < end; i++ {
			row := m.conflictRows[i]
			if !headed[row.resolved] {
				headed[row.resolved] = true
				heading := "Needs a decision"
				if row.resolved {
					heading = "Resolved"
				}
				b.WriteString("  " + stepStyle.Render(heading) + "\n")
			}
			cursor := "  "
			if i == m.conflictCursor {
				cursor = cursorStyle.Render(symCursor + " ")
			}
			b.WriteString(cursor + m.renderConflictRow(row, i == m.conflictCursor) + "\n")
		}
		if end < len(m.conflictRows) {
			b.WriteString(dimStyle.Render(fmt.Sprintf("  %s %d more", symArrowDown, len(m.conflictRows)-end)) + "\n")
		}
	}

	// ── What o and t would do to THIS file ─────────────
	if row, ok := m.conflictSelected(); ok && !row.resolved {
		b.WriteString("\n  " + dimStyle.Render("On "+row.file.Path+" — ") +
			helpKeyStyle.Render("o") + " " + helpStyle.Render(conflictSideLabel(row.file, git.SideOurs, source)) +
			dimStyle.Render("  "+symMidDot+"  ") +
			helpKeyStyle.Render("t") + " " + helpStyle.Render(conflictSideLabel(row.file, git.SideTheirs, source)) + "\n")
	}

	// ── Spinners ───────────────────────────────────────
	switch {
	case m.conflictResolving:
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Resolving...") + "\n")
	case m.conflictFinishing:
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Finishing the merge...") + "\n")
	case m.conflictAborting:
		b.WriteString("\n  " + m.spinner.View() + " " + dimStyle.Render("Undoing the merge...") + "\n")
	}

	if note := m.renderStatusNote(); note != "" {
		b.WriteString("\n" + note + "\n")
	}
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}

	b.WriteString("\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}

// conflictHeadline is the one sentence that says what stopped and how big it
// is, named after the operation the user actually asked for.
func (m Model) conflictHeadline() string {
	source, target := m.conflictSourceLabel(), m.conflictTargetLabel()
	// Nothing conflicted (or nothing conflicts any more): the merge is not a
	// problem to solve, it is a commit waiting to be made.
	if len(m.conflictRows) == 0 {
		return fmt.Sprintf("Merging %s into %s is ready to finish — no conflicting files are left.", source, target)
	}
	files := plural(len(m.conflictRows), "file has", "files have")
	switch m.conflictOrigin {
	case conflictFromPull:
		return fmt.Sprintf("Pulling %s into %s stopped — %s conflicting changes.", source, target, files)
	case conflictFromSync:
		return fmt.Sprintf("Syncing %s with %s stopped — %s conflicting changes.", target, source, files)
	}
	return fmt.Sprintf("Merging %s into %s stopped — %s conflicting changes.", source, target, files)
}

// renderConflictRow is one file: a status glyph, the path, and what git says
// happened to it (or what the user decided).
func (m Model) renderConflictRow(row conflictRow, active bool) string {
	glyph := modifiedStyle.Render(symDirty)
	desc := conflictDesc(row.file.Kind, m.conflictSourceLabel())
	if row.resolved {
		glyph = successStyle.Render(symDone)
		desc = row.how
		if desc == "" {
			desc = "resolved"
		}
	}
	path := filePathStyle.Render(row.file.Path)
	if active {
		path = highlightStyle.Render(row.file.Path)
	}
	// The box's inner text width, less the cursor column and the glyph.
	width := m.width - 12
	if width < 20 {
		width = 20
	}
	tail := truncateWidth(desc, max(10, width-lipgloss.Width(row.file.Path)-4))
	return glyph + " " + path + "  " + dimStyle.Render(tail)
}

// viewConflictEdit is the editor, with the one thing the file selector's editor
// never has to explain: what the markers in the file are.
func (m Model) viewConflictEdit() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(" git-assist "))
	b.WriteString("  ")
	b.WriteString(branchStyle.Render(symBranch + " " + m.branch))
	b.WriteString("\n")
	title := "  Resolving: " + m.conflictEditPath
	if m.editDirty {
		title += "  " + modifiedStyle.Render(symSelected)
	}
	b.WriteString(stepStyle.Render(title))
	b.WriteString("\n")
	// The legend. A beginner has never seen these lines before and git writes
	// them into the file without a word of explanation.
	b.WriteString("  " + dimStyle.Render("<<<<<<< yours "+symEllipsis+" ======= "+symEllipsis+" >>>>>>> theirs "+
		symEmDash+" delete the markers, keep what you want, save") + "\n\n")

	if m.confirmExit {
		b.WriteString(m.editArea.View())
		b.WriteString("\n\n")
		b.WriteString("  " + modifiedStyle.Render("You have unsaved changes. Discard?") + "\n")
		b.WriteString("\n")
		b.WriteString(renderHelpRows(m.helpRows()))
		return m.styledBox(b.String())
	}

	if m.saving {
		b.WriteString(m.editArea.View())
		b.WriteString("\n\n")
		b.WriteString("  " + dimStyle.Render("Saving...") + "\n")
		if m.err != nil {
			b.WriteString("\n  " + formatError(m.err) + "\n")
		}
		return m.styledBox(b.String())
	}

	b.WriteString(m.editArea.View())
	if m.err != nil {
		b.WriteString("\n  " + formatError(m.err) + "\n")
	}
	b.WriteString("\n\n")
	b.WriteString(renderHelpRows(m.helpRows()))
	return m.styledBox(b.String())
}
