package git

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ── The stash list ──────────────────────────────────────
//
// git-assist creates stashes the user never asked for: every branch switch,
// merge and pull auto-stashes a dirty tree, and when the pop afterwards fails
// the entry stays in the stack. This file is what lets the app show, apply and
// drop them, instead of printing a `git stash apply` command and sending the
// user to another terminal.

// StashEntry is one entry of `git stash list`.
//
// Ref is POSITIONAL. stash@{0} is whatever is on top right now, and every drop
// or pop renumbers everything below it — so a Ref read a moment ago can name a
// different entry by the time a key is pressed. SHA does not move: resolve a
// SHA back to its current Ref (StashRefForSHA) immediately before every
// mutation, and never reuse a cached one.
type StashEntry struct {
	Ref     string // stash@{N} as git spells it — positional, see above
	SHA     string // short SHA of the stash commit — stable across drops
	Branch  string // the branch the work was stashed from ("" if unparseable)
	Subject string // the message, minus git's "WIP on <branch>: " prefix
	Age     string // "2h ago", compacted from %cr
}

// stashFieldSep separates the fields of one record. Deliberately a control
// character no commit message or branch name can contain, and the free-text
// field is LAST so that even a subject carrying one cannot shift the parse.
const stashFieldSep = "\x1f"

// stashListFormat is what `git stash list` is asked to print. The default
// human format ("stash@{0}: WIP on main: 1a2b3c4 subject") is a display string
// with no escaping rules at all — parsing it would break on the first branch
// or subject containing ": ". These are explicit fields, NUL-terminated by -z.
const stashListFormat = "%gd" + stashFieldSep + "%h" + stashFieldSep + "%cr" + stashFieldSep + "%gs"

// ErrStashConflict marks an apply/pop that git could not merge: the working
// tree now holds conflict markers and the index has unmerged entries. The
// stash entry itself survives (git keeps it, even on pop), so nothing is lost.
var ErrStashConflict = errors.New("stash conflicts with the working tree")

// StashConflictError is ErrStashConflict with the paths attached, so the caller
// can name the files the user has to go and fix instead of pasting git's whole
// status report into an error banner.
type StashConflictError struct{ Files []string }

func (e *StashConflictError) Error() string {
	if len(e.Files) == 0 {
		return ErrStashConflict.Error()
	}
	return ErrStashConflict.Error() + ": " + strings.Join(e.Files, ", ")
}

// Is makes errors.Is(err, ErrStashConflict) work on the typed form, so callers
// that only care which failure it was do not have to know about this type.
func (e *StashConflictError) Is(target error) bool { return target == ErrStashConflict }

// ErrStashDirtyTree marks an apply/pop git refused outright, because
// uncommitted changes in the working tree cover files the stash touches.
// Nothing was applied and nothing was modified.
var ErrStashDirtyTree = errors.New("uncommitted changes would be overwritten")

// ErrStashDuringMerge marks an apply/pop git refused because the INDEX already
// held unmerged entries when it was asked ("could not write index / <path>:
// needs merge"). Nothing was applied; the blocker is the unresolved merge, not
// the stash.
//
// It is its own failure because the honest sentence differs at every clause
// from a stash conflict's. The stash manager is two keypresses from the
// conflict resolver — esc leaves the merge open by design, and the resolver has
// just told the user their uncommitted work is parked in a stash — so this is a
// path a beginner walks, not a corner case.
var ErrStashDuringMerge = errors.New("cannot restore a stash while files are unmerged")

// StashDuringMergeError is ErrStashDuringMerge with the paths that were already
// unmerged, plus the one fact that decides the advice: whether git is sitting in
// a merge (MERGE_HEAD) or the unmerged entries were left behind by something
// else — an earlier conflicted stash pop leaves exactly that, markers and all,
// with no merge in progress.
type StashDuringMergeError struct {
	Files []string
	Merge bool
}

func (e *StashDuringMergeError) Error() string {
	msg := ErrStashDuringMerge.Error()
	if e.Merge {
		msg = "cannot restore a stash while a merge is in progress — finish or abort the merge first"
	} else {
		msg += " — resolve them first"
	}
	if len(e.Files) > 0 {
		msg += " (unmerged: " + strings.Join(e.Files, ", ") + ")"
	}
	return msg
}

// Is makes errors.Is(err, ErrStashDuringMerge) work on the typed form.
func (e *StashDuringMergeError) Is(target error) bool { return target == ErrStashDuringMerge }

// ErrNoSuchStash marks a SHA that is no longer in the stack — normally because
// another operation dropped or popped it since the list was read.
var ErrNoSuchStash = errors.New("stash entry is gone")

// StashList returns the stash stack, newest first.
func StashList() ([]StashEntry, error) {
	out, err := exec.Command("git", "stash", "list", "-z",
		"--format="+stashListFormat).Output()
	if err != nil {
		return nil, fmt.Errorf("git stash list: %w", err)
	}
	return parseStashList(string(out)), nil
}

// StashCount is how many entries the stack holds. Read with the rest of the
// dashboard so the menu can offer (or hide) the Stash entry without forking git
// on every keypress.
func StashCount() int {
	entries, err := StashList()
	if err != nil {
		return 0
	}
	return len(entries)
}

// parseStashList turns the NUL-terminated records of stashListFormat into
// entries. Split out from StashList so the parse can be exercised against the
// subjects git actually allows — quotes, braces, unit separators, non-ASCII.
func parseStashList(data string) []StashEntry {
	var entries []StashEntry
	for _, rec := range strings.Split(data, "\x00") {
		if rec == "" {
			continue
		}
		// SplitN with the free-text field last: anything the subject contains,
		// including another separator, stays inside the subject.
		fields := strings.SplitN(rec, stashFieldSep, 4)
		if len(fields) < 4 {
			continue
		}
		branch, subject := splitStashSubject(fields[3])
		entries = append(entries, StashEntry{
			Ref:     strings.TrimSpace(fields[0]),
			SHA:     strings.TrimSpace(fields[1]),
			Age:     compactAge(strings.TrimSpace(fields[2])),
			Branch:  branch,
			Subject: subject,
		})
	}
	return entries
}

// splitStashSubject pulls the branch out of a stash's reflog subject. git
// writes one of two shapes:
//
//	WIP on main: 1a2b3c4 the base commit's subject   (no message given)
//	On main: whatever the user typed                 (git stash push -m ...)
//
// The remainder is returned verbatim. For the auto-stashes this app creates
// that is "<base sha> <base subject>" — the commit the work was sitting on,
// which is the most identifying thing git records about an entry nobody named.
// Branch names cannot contain ':' (git refuses the ref), so the first ": " is
// always the right place to cut.
func splitStashSubject(gs string) (branch, subject string) {
	rest := gs
	switch {
	case strings.HasPrefix(rest, "WIP on "):
		rest = strings.TrimPrefix(rest, "WIP on ")
	case strings.HasPrefix(rest, "On "):
		rest = strings.TrimPrefix(rest, "On ")
	default:
		return "", gs
	}
	idx := strings.Index(rest, ": ")
	if idx < 0 {
		return "", gs
	}
	return rest[:idx], rest[idx+2:]
}

// compactAge shortens git's relative date ("2 hours ago") to something a list
// row can afford ("2h ago"). Anything it does not recognise is passed through
// untouched rather than guessed at — a wrong age on a recovery screen is worse
// than a long one.
func compactAge(cr string) string {
	fields := strings.Fields(cr)
	if len(fields) < 2 {
		return cr
	}
	n, err := strconv.Atoi(fields[0])
	if err != nil {
		return cr
	}
	unit := strings.TrimSuffix(fields[1], "s")
	short := ""
	switch unit {
	case "second":
		// Under a minute, the number is noise.
		return "just now"
	case "minute":
		short = "m"
	case "hour":
		short = "h"
	case "day":
		short = "d"
	case "week":
		short = "w"
	case "month":
		short = "mo"
	case "year":
		short = "y"
	default:
		return cr
	}
	return fmt.Sprintf("%d%s ago", n, short)
}

// ── Addressing one entry ────────────────────────────────

// isStashRef guards everything that hands a ref to git. Refs are built from
// our own parse of %gd, but this is the boundary where a display string could
// otherwise become a command argument.
func isStashRef(ref string) bool {
	return strings.HasPrefix(ref, "stash@{") && strings.HasSuffix(ref, "}")
}

// StashRefForSHA re-reads the stack and returns the stash@{N} that names the
// given SHA right now. Every mutation goes through this: indices shift under
// drops and pops, so applying "the stash@{1} the screen was showing" after a
// drop applies somebody else's entry.
func StashRefForSHA(sha string) (string, error) {
	if sha == "" {
		return "", ErrNoSuchStash
	}
	entries, err := StashList()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.SHA == sha {
			return e.Ref, nil
		}
	}
	return "", ErrNoSuchStash
}

// ── Reading one entry ───────────────────────────────────

// StashShow returns the patch for one entry, untracked files included (they
// were stashed with --include-untracked, so leaving them out of the preview
// would hide exactly the files a beginner is most worried about).
func StashShow(ref string) (string, error) {
	if !isStashRef(ref) {
		return "", fmt.Errorf("not a stash ref: %q", ref)
	}
	out, err := exec.Command("git", "stash", "show", "-p", "--include-untracked", ref).CombinedOutput()
	if err != nil {
		// --include-untracked reached `stash show` in git 2.32; on anything
		// older the flag itself is the failure, not the stash.
		alt, altErr := exec.Command("git", "stash", "show", "-p", ref).CombinedOutput()
		if altErr != nil {
			return "", fmt.Errorf("%s", strings.TrimSpace(string(out)))
		}
		out = alt
	}
	// Same treatment the file diff gets: \r returns the terminal cursor to
	// column 0 and the box border overwrites the line.
	patch := stripCR(string(out))
	if len(patch) > maxPreviewBytes {
		patch = patch[:maxPreviewBytes] + "\n" + symTruncated
	}
	if strings.TrimSpace(patch) == "" {
		return "(this stash records no changes)\n", nil
	}
	return strings.TrimRight(patch, "\n"), nil
}

// symTruncated is the marker appended to a patch that hit maxPreviewBytes.
// Plain ASCII: internal/git has no access to the UI's symbol set.
const symTruncated = "... (truncated — this stash is too large to preview in full)"

// ── Mutating the stack ──────────────────────────────────

// StashApplyRef restores one entry into the working tree and LEAVES it in the
// stack. Failures are classified, because the two of them leave the repository
// in completely different states — see ErrStashConflict / ErrStashDirtyTree.
func StashApplyRef(ref string) error {
	return runStashRestore("apply", ref)
}

// StashPopRef restores one entry and drops it. git only drops on success: a
// conflicted pop keeps the entry ("The stash entry is kept in case you need it
// again"), so a failure here never loses the work.
func StashPopRef(ref string) error {
	return runStashRestore("pop", ref)
}

// runStashRestore is apply and pop, which fail identically and have to be
// reported differently from each other only in what they promised to do.
func runStashRestore(verb, ref string) error {
	if !isStashRef(ref) {
		return fmt.Errorf("not a stash ref: %q", ref)
	}
	// Snapshot the unmerged set BEFORE running the verb. The classification
	// below reads the index, and an unresolved merge has already filled it: read
	// after the fact, the MERGE's files came back as the stash's own casualties.
	unmergedBefore := GetConflictFiles()

	out, err := exec.Command("git", "stash", verb, ref).CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))

	// Already unmerged going in: git refuses ("could not write index / <path>:
	// needs merge") before touching the tree or the stash. Calling that a stash
	// conflict was wrong in every clause — the apply never ran, the markers in
	// those files belong to the merge, and the recovery it advises
	// (`git reset HEAD` then `git checkout -- .`) would destroy a half-resolved
	// merge. The blocker is the merge, so the error names the merge.
	if len(unmergedBefore) > 0 {
		return &StashDuringMergeError{Files: unmergedBefore, Merge: MergeInProgress()}
	}
	// Classify by what the repository looks like now, not by matching English:
	// an unmerged index is the difference between "your files hold conflict
	// markers" and "nothing happened at all", and only the first needs the user
	// to go and resolve something.
	//
	// The paths come from the index rather than from git's stdout, which on this
	// path is a full `git status` report — ten lines of advice, most of it about
	// committing, pasted into an error banner three terminal rows tall.
	if files := GetConflictFiles(); len(files) > 0 {
		return &StashConflictError{Files: files}
	}
	// git's own refusal, minus its closing advice: it happens before anything is
	// touched, so the tree and the stash are both exactly as they were, and the
	// caller's hint says what to do about it better than "Aborting" does.
	if strings.Contains(msg, "would be overwritten") || strings.Contains(msg, "already exists, no checkout") {
		return fmt.Errorf("%w: %s", ErrStashDirtyTree, trimGitAdvice(msg))
	}
	return fmt.Errorf("git stash %s: %s", verb, msg)
}

// trimGitAdvice drops the lines of a git refusal that repeat what the UI's own
// hint already says, leaving the part only git knows: which files are in the way.
func trimGitAdvice(msg string) string {
	var kept []string
	for _, line := range strings.Split(msg, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "", trimmed == "Aborting",
			strings.HasPrefix(trimmed, "Please commit your changes"),
			strings.HasPrefix(trimmed, "hint:"):
			continue
		}
		kept = append(kept, trimmed)
	}
	return strings.Join(kept, " ")
}

// StashDropRef deletes one entry. There is no undo inside git for this beyond
// the reflog, which is why the caller confirms first.
func StashDropRef(ref string) error {
	if !isStashRef(ref) {
		return fmt.Errorf("not a stash ref: %q", ref)
	}
	out, err := exec.Command("git", "stash", "drop", ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git stash drop: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// StashFileCount is how many files one entry holds, so a result note can say
// "3 files restored to the working tree" instead of a bare "Applied".
//
// Counted from the STASH, not from `git status` afterwards: status reports
// everything the tree contains, so on a tree that already had two edits of its
// own it would report five files as restored by a stash holding three.
func StashFileCount(ref string) int {
	if !isStashRef(ref) {
		return 0
	}
	out, err := exec.Command("git", "stash", "show", "--name-only", "-z",
		"--include-untracked", ref).Output()
	if err != nil {
		out, err = exec.Command("git", "stash", "show", "--name-only", "-z", ref).Output()
		if err != nil {
			return 0
		}
	}
	n := 0
	for _, p := range strings.Split(string(out), "\x00") {
		if strings.TrimSpace(p) != "" {
			n++
		}
	}
	return n
}
