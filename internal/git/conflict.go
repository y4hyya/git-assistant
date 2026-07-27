package git

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ── Merge conflicts ─────────────────────────────────────
//
// Everything the conflict-resolution screen needs, and nothing about how it is
// drawn. Three facts drive the whole design, and all three were traced against
// real git before this file was written (see internal/ui/step_conflicts.go for
// the ordering decision they force):
//
//  1. `git checkout --ours` FAILS on a path our side deleted ("does not have
//     our version"), and `--theirs` fails on a path their side deleted. For
//     those, keeping a side means deleting the file — `git rm` — not checking
//     one out. Routing that by conflict kind is what ResolveConflict does.
//  2. `git stash pop` REFUSES while a merge is in progress ("needs merge"), so
//     no auto-stash can be restored until the merge is committed or aborted.
//  3. `git commit --no-edit` finishes a merge without ever opening an editor —
//     git has already prepared MERGE_MSG. `git merge --continue` is the same
//     commit with an editor attached, which a TUI cannot survive.

// ConflictKind classifies one unmerged index entry. The seven porcelain codes
// collapse to these: what the user has to decide is always "which side", but
// what "keep that side" MEANS differs, and a beginner has to be told which.
type ConflictKind int

const (
	// ConflictBothModified is UU: the same lines changed on both sides.
	ConflictBothModified ConflictKind = iota
	// ConflictBothAdded is AA: a file of this name was created on both sides.
	ConflictBothAdded
	// ConflictTheyDeleted is UD: we changed it, the incoming branch deleted it.
	ConflictTheyDeleted
	// ConflictWeDeleted is DU: we deleted it, the incoming branch changed it.
	ConflictWeDeleted
	// ConflictBothDeleted is DD.
	ConflictBothDeleted
	// ConflictWeAdded is AU: only our side added it.
	ConflictWeAdded
	// ConflictTheyAdded is UA: only the incoming branch added it.
	ConflictTheyAdded
)

// ConflictSide names the version to keep.
type ConflictSide int

const (
	// SideOurs is the version already on the branch being merged INTO.
	SideOurs ConflictSide = iota
	// SideTheirs is the version coming in from the branch being merged.
	SideTheirs
)

// ConflictFile is one unmerged path.
type ConflictFile struct {
	Path string
	// Code is the raw two-letter porcelain status ("UU", "DU", …), kept so a
	// kind nothing has a name for can still be reported honestly.
	Code string
	Kind ConflictKind
}

// KeepsFile reports whether keeping side leaves a file behind. False means the
// resolution IS the deletion — the screen has to say so before running it,
// which is the whole reason this is a method and not an implementation detail
// of ResolveConflict.
func (f ConflictFile) KeepsFile(side ConflictSide) bool {
	switch f.Kind {
	case ConflictTheyDeleted:
		return side == SideOurs
	case ConflictWeDeleted, ConflictTheyAdded:
		return side == SideTheirs
	case ConflictWeAdded:
		return side == SideOurs
	case ConflictBothDeleted:
		return false
	}
	// UU and AA: both sides have content.
	return true
}

// conflictKindFor maps a porcelain unmerged code to a kind. Only the seven
// codes git documents as unmerged reach it.
func conflictKindFor(code string) (ConflictKind, bool) {
	switch code {
	case "UU":
		return ConflictBothModified, true
	case "AA":
		return ConflictBothAdded, true
	case "UD":
		return ConflictTheyDeleted, true
	case "DU":
		return ConflictWeDeleted, true
	case "DD":
		return ConflictBothDeleted, true
	case "AU":
		return ConflictWeAdded, true
	case "UA":
		return ConflictTheyAdded, true
	}
	return 0, false
}

// ConflictFiles lists the unmerged paths with their kinds.
//
// Read from `git status --porcelain=v1 -z`, not from `diff --diff-filter=U`
// (which GetConflictFiles uses and which reports paths without saying what
// happened to them) and never from the plain porcelain format: in -z mode git
// does not quote or C-escape paths, so a filename with a space or a non-ASCII
// character survives as a usable pathspec.
func ConflictFiles() ([]ConflictFile, error) {
	out, err := exec.Command("git", "status", "--porcelain=v1", "-z").Output()
	if err != nil {
		return nil, fmt.Errorf("git status: %w", err)
	}
	return parseUnmergedZ(string(out)), nil
}

// parseUnmergedZ picks the unmerged records out of NUL-separated porcelain
// output. Records are `XY <path>`; a rename/copy record carries its original
// path in the FOLLOWING field, which has to be consumed even though such a
// record is never unmerged — leaving it in the stream would make the next path
// parse as a status code.
func parseUnmergedZ(data string) []ConflictFile {
	records := strings.Split(data, "\x00")
	var files []ConflictFile

	for i := 0; i < len(records); i++ {
		rec := records[i]
		if len(rec) < 4 {
			continue
		}
		code := rec[:2]
		path := rec[3:]

		if code != "??" && (code[0] == 'R' || code[1] == 'R' || code[0] == 'C' || code[1] == 'C') {
			if i+1 < len(records) {
				i++
			}
		}
		kind, ok := conflictKindFor(code)
		if !ok {
			continue
		}
		files = append(files, ConflictFile{Path: path, Code: code, Kind: kind})
	}
	return files
}

// ResolveConflict keeps one side of one conflicted path and stages the result,
// so that after it returns the path is no longer unmerged.
//
// Routing, not one command: `git checkout --ours` errors out on a path our side
// deleted, so for those "keep ours" is `git rm`. Getting this wrong does not
// fail loudly — it leaves the path unmerged and the commit refused later, with
// nothing on screen explaining why.
func ResolveConflict(f ConflictFile, side ConflictSide) error {
	if f.Path == "" {
		return fmt.Errorf("no file to resolve")
	}
	if !f.KeepsFile(side) {
		return removePath(f.Path)
	}
	flag := "--ours"
	if side == SideTheirs {
		flag = "--theirs"
	}
	if out, err := exec.Command("git", "checkout", flag, "--", f.Path).CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return StageResolved(f.Path)
}

// removePath resolves a conflict whose kept side is the deletion.
func removePath(path string) error {
	out, err := exec.Command("git", "rm", "-q", "--", path).CombinedOutput()
	if err == nil {
		return nil
	}
	// Neither side has a file on disk (both deleted it): `git rm` reports the
	// pathspec matched nothing, and dropping the index entry is the whole
	// resolution. Only ever tried after the plain form has already failed.
	if _, cachedErr := exec.Command("git", "rm", "-q", "--cached", "--", path).CombinedOutput(); cachedErr == nil {
		return nil
	}
	return fmt.Errorf("%s", strings.TrimSpace(string(out)))
}

// StageResolved marks one path resolved — `git add`, which is what tells git a
// conflicted file has been dealt with. Used after the user edits the markers
// out by hand.
func StageResolved(path string) error {
	if path == "" {
		return fmt.Errorf("no file to mark resolved")
	}
	if out, err := exec.Command("git", "add", "--", path).CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// MergeInProgress reports whether the repository is sitting in a merge that has
// not been committed or aborted — including one started outside this app, and
// one this app left behind when the user quit mid-resolution.
func MergeInProgress() bool {
	return exec.Command("git", "rev-parse", "-q", "--verify", "MERGE_HEAD").Run() == nil
}

// MergeContinue commits the merge git has already prepared a message for.
//
// `git commit --no-edit`, deliberately not `git merge --continue`: the latter
// opens the user's editor (core.editor, $EDITOR, vi) on top of the TUI, which
// leaves a beginner in a full-screen program they did not ask for and cannot
// leave. --no-edit takes MERGE_MSG as it stands, which is the message every
// merge commit gets anyway. GIT_EDITOR is pinned as well: --no-edit already
// prevents the spawn, and this makes a hung TUI impossible rather than
// unlikely.
func MergeContinue() error {
	cmd := exec.Command("git", "commit", "--no-edit")
	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// MergeLabels names the two sides of the merge in progress, read from the
// message git prepared for it (MERGE_MSG). Both are empty when there is nothing
// to read — a merge started outside this app is exactly the case this exists
// for, and the caller supplies its own wording for what it cannot learn.
//
// The three shapes git writes:
//
//	Merge branch 'feat' into side
//	Merge remote-tracking branch 'origin/main'
//	Merge commit '0a58639812be…'
func MergeLabels() (source, target string) {
	out, err := exec.Command("git", "rev-parse", "--git-path", "MERGE_MSG").Output()
	if err != nil {
		return "", ""
	}
	data, err := os.ReadFile(strings.TrimSpace(string(out)))
	if err != nil {
		return "", ""
	}
	line := data
	if i := strings.IndexByte(string(data), '\n'); i >= 0 {
		line = data[:i]
	}
	return parseMergeMsgLine(string(line))
}

// parseMergeMsgLine pulls the source (the quoted ref) and the optional target
// out of MERGE_MSG's subject. Split out so the shapes can be exercised without
// a repository mid-merge.
func parseMergeMsgLine(line string) (source, target string) {
	open := strings.IndexByte(line, '\'')
	if open < 0 {
		return "", ""
	}
	rest := line[open+1:]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return "", ""
	}
	source = rest[:end]
	if into := strings.TrimSpace(rest[end+1:]); strings.HasPrefix(into, "into ") {
		target = strings.TrimSpace(strings.TrimPrefix(into, "into "))
	}
	return source, target
}

// The outer conflict-marker fences, as git writes them. The middle one
// (`=======`) is deliberately not part of the test — see HasConflictMarkers.
const (
	markerOurs   = "<<<<<<<"
	markerTheirs = ">>>>>>>"
)

// HasConflictMarkers reports whether content still holds an unresolved conflict
// block. BOTH outer fences are required, at the start of a line and in order:
// `=======` on its own is a Markdown heading rule and a reStructuredText
// underline, and warning "markers still present" on a README the user has just
// finished editing teaches them to ignore the warning.
func HasConflictMarkers(content string) bool {
	opened := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, markerOurs):
			opened = true
		case opened && strings.HasPrefix(line, markerTheirs):
			return true
		}
	}
	return false
}
