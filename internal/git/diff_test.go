package git

import (
	"errors"
	"os"
	"strings"
	"testing"

	"git-assist/internal/types"
)

// GetFileDiff is a ROUTER, not one command: the untracked, deleted and
// everything-else arms read completely different things, and each has a
// fallback the user only ever sees when the first read fails. The diff pane
// renders whatever comes back verbatim, so a wrong arm shows the wrong file's
// content under the right file's name.

// ── Untracked arm ──────────────────────────────────────

// A symlink must not be dereferenced: reading through it would show the
// target's contents under the link's name, and a broken link would error out
// on a file the picker is perfectly happy to list.
func TestGetFileDiffUntrackedSymlink(t *testing.T) {
	scratchRepo(t)
	write(t, "target.txt", "the target's content\n")
	if err := os.Symlink("target.txt", "link.txt"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	diff, err := GetFileDiff("link.txt", types.StatusUntracked)
	if err != nil {
		t.Fatalf("GetFileDiff on a symlink: %v", err)
	}
	if !strings.Contains(diff, "symlink") {
		t.Fatalf("diff = %q, want it named as a symlink", diff)
	}
	if !strings.Contains(diff, "target.txt") {
		t.Fatalf("diff = %q, want the link target", diff)
	}
	if strings.Contains(diff, "the target's content") {
		t.Fatalf("the symlink was dereferenced: %q", diff)
	}
}

// A dangling symlink still has to render — Readlink succeeds even when the
// target is gone, and the picker lists it either way.
func TestGetFileDiffBrokenSymlink(t *testing.T) {
	scratchRepo(t)
	if err := os.Symlink("gone.txt", "link.txt"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	diff, err := GetFileDiff("link.txt", types.StatusUntracked)
	if err != nil {
		t.Fatalf("GetFileDiff on a broken symlink: %v", err)
	}
	if !strings.Contains(diff, "symlink") {
		t.Fatalf("diff = %q, want it named as a symlink", diff)
	}
}

// Binary is refused with the sentinel, not rendered: the diff pane and the
// editor both branch on ErrBinaryFile, and round-tripping NUL bytes through a
// UTF-8 string would corrupt the file on save.
func TestGetFileDiffUntrackedBinaryIsRefused(t *testing.T) {
	scratchRepo(t)
	write(t, "logo.png", "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

	_, err := GetFileDiff("logo.png", types.StatusUntracked)
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("error = %v, want ErrBinaryFile", err)
	}
}

// A path the picker still lists but that has since vanished must report the
// read failure rather than render an empty "(new file)" block.
func TestGetFileDiffUntrackedMissingFile(t *testing.T) {
	scratchRepo(t)
	_, err := GetFileDiff("never-existed.txt", types.StatusUntracked)
	if err == nil {
		t.Fatal("GetFileDiff succeeded on a missing file")
	}
	if errors.Is(err, ErrBinaryFile) {
		t.Fatalf("error = %v, want a read failure, not the binary sentinel", err)
	}
}

// ── Deleted arm ────────────────────────────────────────

// The content comes out of HEAD (it is gone from disk) with every line marked
// as a removal.
func TestGetFileDiffDeletedReadsHEAD(t *testing.T) {
	scratchRepo(t)
	write(t, "gone.txt", "first\nsecond\n")
	commitAll(t, "seed")
	if err := os.Remove("gone.txt"); err != nil {
		t.Fatal(err)
	}

	diff, err := GetFileDiff("gone.txt", types.StatusDeleted)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "(deleted file)") {
		t.Fatalf("diff = %q, want the deleted-file header", diff)
	}
	for _, want := range []string{"- first", "- second"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff = %q, want a %q line", diff, want)
		}
	}
}

// Staged as an addition, then removed from disk: there is no HEAD copy to
// read, so the fallback shows the staged diff instead of an empty pane.
func TestGetFileDiffDeletedFallsBackToTheIndex(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "seed")

	write(t, "staged.txt", "brand new\n")
	runGit(t, "add", "staged.txt")
	if err := os.Remove("staged.txt"); err != nil {
		t.Fatal(err)
	}

	diff, err := GetFileDiff("staged.txt", types.StatusDeleted)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "brand new") {
		t.Fatalf("diff = %q, want the staged content from the index fallback", diff)
	}
}

// Neither HEAD nor the index knows the path — the placeholder, not an error
// banner, because a deleted file with nothing to show is a normal state.
func TestGetFileDiffDeletedWithNothingToShow(t *testing.T) {
	scratchRepo(t)
	write(t, "seed.txt", "seed\n")
	commitAll(t, "seed")

	diff, err := GetFileDiff("never-tracked.txt", types.StatusDeleted)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "(deleted file)") {
		t.Fatalf("diff = %q, want the deleted-file placeholder", diff)
	}
}

func TestGetFileDiffDeletedBinaryIsRefused(t *testing.T) {
	scratchRepo(t)
	write(t, "blob.bin", "\x00\x01\x02\x03binary payload\x00")
	commitAll(t, "seed")
	if err := os.Remove("blob.bin"); err != nil {
		t.Fatal(err)
	}

	_, err := GetFileDiff("blob.bin", types.StatusDeleted)
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("error = %v, want ErrBinaryFile", err)
	}
}

// ── Default arm (modified / added / renamed) ───────────

// git reports a binary change as "Binary files … differ" rather than failing,
// so the refusal has to be recognised in the output text.
func TestGetFileDiffModifiedBinaryIsRefused(t *testing.T) {
	scratchRepo(t)
	write(t, "blob.bin", "\x00\x01\x02\x03")
	commitAll(t, "seed")
	write(t, "blob.bin", "\x00\x09\x09\x09changed")

	_, err := GetFileDiff("blob.bin", types.StatusModified)
	if !errors.Is(err, ErrBinaryFile) {
		t.Fatalf("error = %v, want ErrBinaryFile", err)
	}
}

// A repo with no commits has no HEAD to diff against; `git diff HEAD` fails
// outright and the index fallback is the only thing that can show the very
// first commit's contents on the confirm screen.
func TestGetFileDiffAddedInARepoWithNoCommits(t *testing.T) {
	scratchRepo(t)
	write(t, "first.txt", "hello world\n")
	runGit(t, "add", "first.txt")

	diff, err := GetFileDiff("first.txt", types.StatusAdded)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "hello world") {
		t.Fatalf("diff = %q, want the staged content", diff)
	}
	if strings.Contains(diff, "no changes to display") {
		t.Fatalf("diff = %q — the index fallback did not run", diff)
	}
}

// A rename takes the default arm and renders as an ADDITION of the new path.
//
// This pins a known limitation rather than an intention: GetFileDiff is given
// one path, and `git diff HEAD -- new.txt` restricted to that single pathspec
// cannot pair it with the removal of the old one, so rename detection never
// fires. Everywhere else the app is careful to handle both halves of a rename
// (stagePaths stages both, DiscardFile restores both) — the diff pane is the
// one place that shows half of it. Fixing it means passing OrigPath in;
// the test is here so that change is visible when it happens.
func TestGetFileDiffRenamedShowsOnlyTheNewPath(t *testing.T) {
	scratchRepo(t)
	write(t, "old.txt", "content\n")
	commitAll(t, "seed")
	runGit(t, "mv", "old.txt", "new.txt")

	diff, err := GetFileDiff("new.txt", types.StatusRenamed)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "new.txt") {
		t.Fatalf("diff = %q, want the new path", diff)
	}
	if !strings.Contains(diff, "+content") {
		t.Fatalf("diff = %q, want the file's content", diff)
	}
	if strings.Contains(diff, "no changes to display") {
		t.Fatalf("diff = %q — a renamed file rendered as unchanged", diff)
	}
}

// A clean file reached through a stale file list: the pane says so instead of
// rendering an empty box the user has to interpret.
func TestGetFileDiffCleanFileSaysSo(t *testing.T) {
	scratchRepo(t)
	write(t, "clean.txt", "unchanged\n")
	commitAll(t, "seed")

	diff, err := GetFileDiff("clean.txt", types.StatusModified)
	if err != nil {
		t.Fatalf("GetFileDiff: %v", err)
	}
	if !strings.Contains(diff, "no changes to display") {
		t.Fatalf("diff = %q, want the no-changes placeholder", diff)
	}
}
