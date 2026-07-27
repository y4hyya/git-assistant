package git

import (
	"os"
	"strings"
	"testing"
)

// ── Fixtures ───────────────────────────────────────────

// conflictedRepo builds a merge that stops on four conflicts at once, one of
// each shape the resolver has to route differently:
//
//	both.txt        UU — both sides changed the same lines
//	bothadd.txt     AA — both sides created a file of that name
//	theydelete.txt  UD — we changed it, feat deleted it
//	wedelete.txt    DU — we deleted it, feat changed it
//
// Left mid-merge, exactly as the app finds it.
func conflictedRepo(t *testing.T) {
	t.Helper()
	scratchRepo(t)
	write(t, "both.txt", "base\n")
	write(t, "theydelete.txt", "base\n")
	write(t, "wedelete.txt", "base\n")
	write(t, "untouched.txt", "base\n")
	commitAll(t, "base")

	runGit(t, "checkout", "-q", "-b", "feat")
	write(t, "both.txt", "from feat\n")
	runGit(t, "rm", "-q", "theydelete.txt")
	write(t, "wedelete.txt", "feat changed it\n")
	write(t, "bothadd.txt", "feat's version\n")
	commitAll(t, "feat")

	runGit(t, "checkout", "-q", "main")
	write(t, "both.txt", "from main\n")
	write(t, "theydelete.txt", "main changed it\n")
	runGit(t, "rm", "-q", "wedelete.txt")
	write(t, "bothadd.txt", "main's version\n")
	commitAll(t, "main")

	// Expected to fail — that is the fixture.
	if _, err := MergeBranch("feat"); err == nil {
		t.Fatal("the merge fixture did not conflict")
	}
}

func conflictsByPath(t *testing.T) map[string]ConflictFile {
	t.Helper()
	files, err := ConflictFiles()
	if err != nil {
		t.Fatalf("ConflictFiles: %v", err)
	}
	byPath := make(map[string]ConflictFile, len(files))
	for _, f := range files {
		byPath[f.Path] = f
	}
	return byPath
}

// ── Parsing ────────────────────────────────────────────

// The seven unmerged codes, the records that are not unmerged, and the one
// alignment trap: a rename record carries its ORIGINAL path in the following
// field, and skipping it as if it were a record makes the next path parse as a
// status code.
func TestParseUnmergedZ(t *testing.T) {
	rec := func(parts ...string) string { return strings.Join(parts, "\x00") + "\x00" }
	data := rec(
		"UU both.txt",
		"AA both added.txt", // a space in the path, unquoted in -z mode
		"UD they deleted.txt",
		"DU we deleted.txt",
		"DD both gone.txt",
		"AU we added.txt",
		"UA they added.txt",
		"R  renamed.txt", "old-name.txt", // two fields, not unmerged
		" M plain.txt",
		"?? untracked.txt",
		"A  staged.txt",
	)

	got := parseUnmergedZ(data)
	want := []ConflictFile{
		{Path: "both.txt", Code: "UU", Kind: ConflictBothModified},
		{Path: "both added.txt", Code: "AA", Kind: ConflictBothAdded},
		{Path: "they deleted.txt", Code: "UD", Kind: ConflictTheyDeleted},
		{Path: "we deleted.txt", Code: "DU", Kind: ConflictWeDeleted},
		{Path: "both gone.txt", Code: "DD", Kind: ConflictBothDeleted},
		{Path: "we added.txt", Code: "AU", Kind: ConflictWeAdded},
		{Path: "they added.txt", Code: "UA", Kind: ConflictTheyAdded},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// The codes have to come from a real merge, not only from a table: git is the
// authority on which shape produces which letters.
func TestConflictFilesClassifiesARealMerge(t *testing.T) {
	conflictedRepo(t)
	byPath := conflictsByPath(t)

	want := map[string]ConflictKind{
		"both.txt":       ConflictBothModified,
		"bothadd.txt":    ConflictBothAdded,
		"theydelete.txt": ConflictTheyDeleted,
		"wedelete.txt":   ConflictWeDeleted,
	}
	if len(byPath) != len(want) {
		t.Fatalf("ConflictFiles reported %d paths, want %d: %+v", len(byPath), len(want), byPath)
	}
	for path, kind := range want {
		got, ok := byPath[path]
		if !ok {
			t.Errorf("%s is missing from the conflict list", path)
			continue
		}
		if got.Kind != kind {
			t.Errorf("%s classified as %v (code %s), want %v", path, got.Kind, got.Code, kind)
		}
	}
	// A file neither side touched must never appear.
	if _, ok := byPath["untouched.txt"]; ok {
		t.Error("a file with no conflict is in the list")
	}
}

// ── Routing ────────────────────────────────────────────

// The heart of the feature. `git checkout --ours` FAILS on a path our side
// deleted and `--theirs` fails on one their side deleted, so for the delete
// variants "keep this side" has to become `git rm`. A wrong route here leaves
// the path unmerged with no error on screen.
func TestResolveConflictRoutesEverySideOfEveryKind(t *testing.T) {
	cases := []struct {
		path    string
		side    ConflictSide
		content string // "" means the file must be gone
	}{
		{"both.txt", SideOurs, "from main\n"},
		{"both.txt", SideTheirs, "from feat\n"},
		{"bothadd.txt", SideOurs, "main's version\n"},
		{"bothadd.txt", SideTheirs, "feat's version\n"},
		// We changed it, feat deleted it: ours keeps the file, theirs deletes it.
		{"theydelete.txt", SideOurs, "main changed it\n"},
		{"theydelete.txt", SideTheirs, ""},
		// We deleted it, feat changed it: ours keeps it deleted.
		{"wedelete.txt", SideOurs, ""},
		{"wedelete.txt", SideTheirs, "feat changed it\n"},
	}

	for _, c := range cases {
		name := c.path + "/ours"
		if c.side == SideTheirs {
			name = c.path + "/theirs"
		}
		t.Run(name, func(t *testing.T) {
			conflictedRepo(t)
			f, ok := conflictsByPath(t)[c.path]
			if !ok {
				t.Fatalf("%s is not conflicted in the fixture", c.path)
			}
			if got := f.KeepsFile(c.side); got != (c.content != "") {
				t.Errorf("KeepsFile = %v, but the side %s a file", got,
					map[bool]string{true: "keeps", false: "removes"}[c.content != ""])
			}
			if err := ResolveConflict(f, c.side); err != nil {
				t.Fatalf("ResolveConflict: %v", err)
			}
			// Whatever it did, the path must no longer be unmerged — otherwise
			// the commit at the end is refused with nothing on screen to explain
			// why.
			if _, still := conflictsByPath(t)[c.path]; still {
				t.Errorf("%s is still unmerged after resolving it", c.path)
			}
			data, err := os.ReadFile(c.path)
			switch {
			case c.content == "":
				if err == nil {
					t.Errorf("%s is still on disk (%q); keeping this side means deleting it", c.path, data)
				}
			case err != nil:
				t.Fatalf("%s should hold %q, but reading it failed: %v", c.path, c.content, err)
			case string(data) != c.content:
				t.Errorf("%s = %q, want %q", c.path, data, c.content)
			}
		})
	}
}

// The by-hand path: markers edited out, then staged.
func TestStageResolvedClearsTheConflict(t *testing.T) {
	conflictedRepo(t)
	write(t, "both.txt", "a merged version by hand\n")
	if err := StageResolved("both.txt"); err != nil {
		t.Fatalf("StageResolved: %v", err)
	}
	if _, still := conflictsByPath(t)["both.txt"]; still {
		t.Error("both.txt is still unmerged after git add")
	}
	if got := readWorktree(t, "both.txt"); got != "a merged version by hand\n" {
		t.Errorf("staging rewrote the file: %q", got)
	}
}

// ── Finishing ──────────────────────────────────────────

// `git merge --continue` opens the user's editor on top of the TUI. This is the
// assertion that --no-edit does not: GIT_EDITOR is pointed at a command that
// fails, so a commit that tried to launch one could not succeed.
func TestMergeContinueCommitsWithoutAnEditor(t *testing.T) {
	conflictedRepo(t)
	t.Setenv("GIT_EDITOR", "false")
	t.Setenv("EDITOR", "false")

	for path, side := range map[string]ConflictSide{
		"both.txt": SideOurs, "bothadd.txt": SideOurs,
		"theydelete.txt": SideOurs, "wedelete.txt": SideTheirs,
	} {
		f, ok := conflictsByPath(t)[path]
		if !ok {
			t.Fatalf("%s is not conflicted", path)
		}
		if err := ResolveConflict(f, side); err != nil {
			t.Fatalf("resolving %s: %v", path, err)
		}
	}

	if !MergeInProgress() {
		t.Fatal("MergeInProgress is false while MERGE_HEAD exists")
	}
	if err := MergeContinue(); err != nil {
		t.Fatalf("MergeContinue: %v", err)
	}
	if MergeInProgress() {
		t.Error("the merge is still in progress after committing it")
	}
	// A merge commit, not an ordinary one: two parents is the whole point of
	// the operation.
	parents := strings.Fields(runGit(t, "rev-list", "--parents", "-n", "1", "HEAD"))
	if len(parents) != 3 {
		t.Errorf("HEAD has %d parent(s), want 2 — this is not a merge commit", len(parents)-1)
	}
	if subject := strings.TrimSpace(runGit(t, "log", "-1", "--format=%s")); !strings.Contains(subject, "feat") {
		t.Errorf("merge commit subject = %q, want git's prepared message", subject)
	}
}

func TestMergeInProgressIsFalseOnACleanRepo(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "x\n")
	commitAll(t, "seed")
	if MergeInProgress() {
		t.Error("MergeInProgress is true with no merge running")
	}
}

// ── Labels ─────────────────────────────────────────────

// A merge started outside git-assist has no in-process record of what it was
// merging; MERGE_MSG is the only thing that knows.
func TestMergeLabelsReadsTheMergeGitPrepared(t *testing.T) {
	conflictedRepo(t)
	source, target := MergeLabels()
	if source != "feat" {
		t.Errorf("source = %q, want %q", source, "feat")
	}
	// git omits "into <branch>" when merging into the default branch, which is
	// exactly why the caller supplies its own target when this is empty.
	if target != "" && target != "main" {
		t.Errorf("target = %q, want %q or empty", target, "main")
	}
}

func TestParseMergeMsgLine(t *testing.T) {
	cases := []struct {
		line, source, target string
	}{
		{"Merge branch 'feat'", "feat", ""},
		{"Merge branch 'feat' into side", "feat", "side"},
		{"Merge remote-tracking branch 'origin/main'", "origin/main", ""},
		{"Merge remote-tracking branch 'origin/main' into feature/x", "origin/main", "feature/x"},
		{"Merge commit '0a58639812be0b155be397f1dfcd18c264b5d85d'", "0a58639812be0b155be397f1dfcd18c264b5d85d", ""},
		// Nothing quoted: no name to give, and inventing one would be worse.
		{"Merge", "", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		source, target := parseMergeMsgLine(c.line)
		if source != c.source || target != c.target {
			t.Errorf("parse(%q) = (%q, %q), want (%q, %q)", c.line, source, target, c.source, c.target)
		}
	}
}

// ── Markers ────────────────────────────────────────────

// Both outer fences are required. `=======` alone underlines a heading in
// Markdown and in reStructuredText, and a warning that fires on every README
// is a warning the user learns to click through.
func TestHasConflictMarkers(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"a real conflict", "a\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> feat\nb\n", true},
		{"resolved by hand", "a\nours and theirs\nb\n", false},
		{"markdown heading rule", "Title\n=======\n\nbody\n", false},
		{"opened but not closed", "<<<<<<< HEAD\nours\n=======\n", false},
		{"closed before opened", ">>>>>>> feat\n<<<<<<< HEAD\n", false},
		{"mid-line, not a marker", "printf('<<<<<<< %s >>>>>>> ', x)\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := HasConflictMarkers(c.content); got != c.want {
			t.Errorf("%s: HasConflictMarkers = %v, want %v", c.name, got, c.want)
		}
	}
}

// readWorktree is os.ReadFile with the test's error handling.
func readWorktree(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
