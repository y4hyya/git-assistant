package git

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ── Parsing ────────────────────────────────────────────

// The whole point of asking git for explicit fields is that the free text
// inside them cannot break the parse. These are subjects git genuinely allows.
func TestParseStashListSurvivesHostileSubjects(t *testing.T) {
	rec := func(ref, sha, age, gs string) string {
		return strings.Join([]string{ref, sha, age, gs}, stashFieldSep) + "\x00"
	}
	data := rec("stash@{0}", "aaa1111", "3 hours ago", `On feat: "quoted" {braces} and: colons`) +
		rec("stash@{1}", "bbb2222", "2 days ago", "WIP on main: 1a2b3c4 chore: seed") +
		rec("stash@{2}", "ccc3333", "5 weeks ago", "On ünïcode-brañch: em — dash ünïcode") +
		// A subject that itself contains the field separator. The free-text
		// field is last precisely so this cannot shift anything.
		rec("stash@{3}", "ddd4444", "1 year ago", "On main: sep"+stashFieldSep+"inside") +
		// No recognisable prefix at all — branch unknown, subject verbatim.
		rec("stash@{4}", "eee5555", "10 minutes ago", "something else entirely")

	got := parseStashList(data)
	if len(got) != 5 {
		t.Fatalf("parsed %d entries, want 5: %#v", len(got), got)
	}

	want := []StashEntry{
		{Ref: "stash@{0}", SHA: "aaa1111", Age: "3h ago", Branch: "feat", Subject: `"quoted" {braces} and: colons`},
		{Ref: "stash@{1}", SHA: "bbb2222", Age: "2d ago", Branch: "main", Subject: "1a2b3c4 chore: seed"},
		{Ref: "stash@{2}", SHA: "ccc3333", Age: "5w ago", Branch: "ünïcode-brañch", Subject: "em — dash ünïcode"},
		{Ref: "stash@{3}", SHA: "ddd4444", Age: "1y ago", Branch: "main", Subject: "sep" + stashFieldSep + "inside"},
		{Ref: "stash@{4}", SHA: "eee5555", Age: "10m ago", Branch: "", Subject: "something else entirely"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry %d:\n got %#v\nwant %#v", i, got[i], w)
		}
	}
}

func TestParseStashListEmptyAndMalformed(t *testing.T) {
	if got := parseStashList(""); len(got) != 0 {
		t.Errorf("empty output parsed as %d entries", len(got))
	}
	if got := parseStashList("\x00\x00"); len(got) != 0 {
		t.Errorf("empty records parsed as %d entries", len(got))
	}
	// Too few fields: dropped rather than guessed at. A half-parsed ref is a
	// wrong ref, and wrong refs get dropped and applied.
	if got := parseStashList("stash@{0}" + stashFieldSep + "abc1234\x00"); len(got) != 0 {
		t.Errorf("short record parsed as %d entries: %#v", len(got), got)
	}
}

func TestCompactAgePassesThroughWhatItCannotParse(t *testing.T) {
	cases := map[string]string{
		"0 seconds ago":  "just now",
		"41 seconds ago": "just now",
		"1 minute ago":   "1m ago",
		"2 hours ago":    "2h ago",
		"3 days ago":     "3d ago",
		"4 weeks ago":    "4w ago",
		"5 months ago":   "5mo ago",
		"6 years ago":    "6y ago",
		// git's compound and non-numeric forms: a wrong age on a recovery
		// screen is worse than a long one, so they come through untouched.
		"2 years, 3 months ago": "2 years, 3 months ago",
		"in the future":         "in the future",
		"":                      "",
	}
	for in, want := range cases {
		if got := compactAge(in); got != want {
			t.Errorf("compactAge(%q) = %q, want %q", in, got, want)
		}
	}
}

// ── Against a real repository ──────────────────────────

// stashSomething modifies path and stashes it, returning nothing — the point is
// the side effect on the stack.
func stashSomething(t *testing.T, path, content string, message ...string) {
	t.Helper()
	write(t, path, content)
	args := []string{"stash", "push", "-q", "--include-untracked"}
	if len(message) > 0 {
		args = append(args, "-m", message[0])
	}
	runGit(t, args...)
}

func TestStashListReadsTheRealStack(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	commitAll(t, "seed")

	if entries, err := StashList(); err != nil || len(entries) != 0 {
		t.Fatalf("empty stack: got %d entries, err %v", len(entries), err)
	}
	if n := StashCount(); n != 0 {
		t.Fatalf("StashCount on an empty stack = %d, want 0", n)
	}

	stashSomething(t, "a.txt", "two\n")
	stashSomething(t, "a.txt", "three\n", "named entry")

	entries, err := StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %#v", len(entries), entries)
	}
	// Newest first, and the refs are git's own numbering.
	if entries[0].Ref != "stash@{0}" || entries[1].Ref != "stash@{1}" {
		t.Errorf("refs are %q/%q, want stash@{0}/stash@{1}", entries[0].Ref, entries[1].Ref)
	}
	if entries[0].Subject != "named entry" {
		t.Errorf("named entry's subject is %q", entries[0].Subject)
	}
	if entries[0].Branch != "main" || entries[1].Branch != "main" {
		t.Errorf("branches are %q/%q, want main/main", entries[0].Branch, entries[1].Branch)
	}
	for _, e := range entries {
		if len(e.SHA) < 4 {
			t.Errorf("entry %s has no usable SHA (%q)", e.Ref, e.SHA)
		}
		if e.Age == "" {
			t.Errorf("entry %s has no age", e.Ref)
		}
	}
	if n := StashCount(); n != 2 {
		t.Errorf("StashCount = %d, want 2", n)
	}
}

func TestStashShowIncludesUntrackedFiles(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	commitAll(t, "seed")

	write(t, "a.txt", "two\n")
	write(t, "brand-new.txt", "hello\n")
	runGit(t, "stash", "push", "-q", "--include-untracked")

	patch, err := StashShow("stash@{0}")
	if err != nil {
		t.Fatalf("StashShow: %v", err)
	}
	// The untracked half is exactly what a beginner is most worried about
	// losing, so leaving it out of the preview would hide the scary part.
	for _, want := range []string{"a.txt", "brand-new.txt", "+hello"} {
		if !strings.Contains(patch, want) {
			t.Errorf("the patch never mentions %q:\n%s", want, patch)
		}
	}
	if _, err := StashShow("../etc/passwd"); err == nil {
		t.Error("StashShow accepted something that is not a stash ref")
	}
}

// ── Apply / pop / drop ─────────────────────────────────

func TestStashApplyKeepsTheEntryAndPopRemovesIt(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	commitAll(t, "seed")
	stashSomething(t, "a.txt", "stashed\n")

	if err := StashApplyRef("stash@{0}"); err != nil {
		t.Fatalf("StashApplyRef: %v", err)
	}
	if got := readFile(t, "a.txt"); got != "stashed\n" {
		t.Errorf("apply did not restore the file, a.txt = %q", got)
	}
	if n := StashCount(); n != 1 {
		t.Errorf("apply left %d entries, want the entry kept (1)", n)
	}

	// Put the tree back so the pop has somewhere clean to land.
	runGit(t, "checkout", "--", "a.txt")
	if err := StashPopRef("stash@{0}"); err != nil {
		t.Fatalf("StashPopRef: %v", err)
	}
	if got := readFile(t, "a.txt"); got != "stashed\n" {
		t.Errorf("pop did not restore the file, a.txt = %q", got)
	}
	if n := StashCount(); n != 0 {
		t.Errorf("pop left %d entries, want 0", n)
	}
}

func TestStashDropRemovesOnlyTheNamedEntry(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	commitAll(t, "seed")
	stashSomething(t, "a.txt", "first\n", "first")
	stashSomething(t, "a.txt", "second\n", "second")

	if err := StashDropRef("stash@{0}"); err != nil {
		t.Fatalf("StashDropRef: %v", err)
	}
	entries, err := StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(entries) != 1 || entries[0].Subject != "first" {
		t.Fatalf("after dropping the top entry the stack is %#v", entries)
	}
	if err := StashDropRef("not-a-ref"); err == nil {
		t.Error("StashDropRef accepted something that is not a stash ref")
	}
}

// The reason StashRefForSHA exists. Drop the top entry and everything below it
// renumbers: the SHA that was stash@{1} is stash@{0} now, and operating on the
// cached ref would hit a different entry entirely.
func TestRefsShiftAfterADropAndSHAsDoNot(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	commitAll(t, "seed")
	stashSomething(t, "a.txt", "oldest\n", "oldest")
	stashSomething(t, "a.txt", "middle\n", "middle")
	stashSomething(t, "a.txt", "newest\n", "newest")

	before, err := StashList()
	if err != nil {
		t.Fatalf("StashList: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(before))
	}
	middle := before[1] // stash@{1} right now
	if middle.Subject != "middle" {
		t.Fatalf("fixture order is wrong: %#v", before)
	}

	// Drop the newest. "middle" is now stash@{0}.
	if err := StashDropRef(before[0].Ref); err != nil {
		t.Fatalf("StashDropRef: %v", err)
	}

	ref, err := StashRefForSHA(middle.SHA)
	if err != nil {
		t.Fatalf("StashRefForSHA: %v", err)
	}
	if ref != "stash@{0}" {
		t.Fatalf("after the drop, %s resolves to %s — want stash@{0}", middle.SHA, ref)
	}
	if ref == middle.Ref {
		t.Fatal("the ref did not shift, so this test proves nothing")
	}

	// Applying through the resolved ref restores the middle entry's content —
	// applying through the cached one would have restored "oldest".
	if err := StashApplyRef(ref); err != nil {
		t.Fatalf("StashApplyRef: %v", err)
	}
	if got := readFile(t, "a.txt"); got != "middle\n" {
		t.Errorf("the wrong entry was applied: a.txt = %q, want %q", got, "middle\n")
	}

	// And a SHA that has left the stack is reported as such, not silently
	// resolved to whatever is on top now.
	if _, err := StashRefForSHA(before[0].SHA); !errors.Is(err, ErrNoSuchStash) {
		t.Errorf("a dropped SHA resolved to %v, want ErrNoSuchStash", err)
	}
}

// ── The two failure modes ──────────────────────────────

// Traced against real git: with a clean tree whose HEAD has moved, apply merges
// and writes conflict markers, the index goes unmerged, and the stash is KEPT.
func TestApplyConflictLeavesMarkersAndKeepsTheStash(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "l1\nl2\nl3\n")
	commitAll(t, "seed")

	write(t, "a.txt", "l1\nSTASHED\nl3\n")
	runGit(t, "stash", "push", "-q")
	write(t, "a.txt", "l1\nCOMMITTED\nl3\n")
	commitAll(t, "conflicting change")

	err := StashApplyRef("stash@{0}")
	if !errors.Is(err, ErrStashConflict) {
		t.Fatalf("conflicting apply returned %v, want ErrStashConflict", err)
	}
	// Every clause of the message the UI builds from this has to be true.
	if got := readFile(t, "a.txt"); !strings.Contains(got, "<<<<<<<") {
		t.Errorf("no conflict markers in the file:\n%s", got)
	}
	if len(GetConflictFiles()) == 0 {
		t.Error("the index has no unmerged entries, so 'resolve the markers' would be wrong")
	}
	if n := StashCount(); n != 1 {
		t.Errorf("the stash was not kept (%d entries) — 'nothing is lost' would be a lie", n)
	}
}

// A conflicted POP keeps the entry too ("The stash entry is kept in case you
// need it again"), which is what makes pop safe to offer to a beginner.
func TestPopConflictAlsoKeepsTheStash(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "l1\nl2\nl3\n")
	commitAll(t, "seed")

	write(t, "a.txt", "l1\nSTASHED\nl3\n")
	runGit(t, "stash", "push", "-q")
	write(t, "a.txt", "l1\nCOMMITTED\nl3\n")
	commitAll(t, "conflicting change")

	if err := StashPopRef("stash@{0}"); !errors.Is(err, ErrStashConflict) {
		t.Fatalf("conflicting pop returned %v, want ErrStashConflict", err)
	}
	if n := StashCount(); n != 1 {
		t.Errorf("a conflicted pop dropped the entry (%d left) — the work would be gone", n)
	}
}

// The other failure: uncommitted changes cover the same files. git refuses
// before touching anything, so the message must NOT talk about markers.
func TestApplyOntoADirtyTreeIsRefusedWithoutTouchingAnything(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "l1\nl2\nl3\n")
	commitAll(t, "seed")

	write(t, "a.txt", "l1\nSTASHED\nl3\n")
	runGit(t, "stash", "push", "-q")
	write(t, "a.txt", "l1\nWORKTREE\nl3\n")

	err := StashApplyRef("stash@{0}")
	if !errors.Is(err, ErrStashDirtyTree) {
		t.Fatalf("apply onto a dirty tree returned %v, want ErrStashDirtyTree", err)
	}
	if got := readFile(t, "a.txt"); got != "l1\nWORKTREE\nl3\n" {
		t.Errorf("the working tree was modified after a refusal: %q", got)
	}
	if len(GetConflictFiles()) != 0 {
		t.Error("the index has unmerged entries after a refusal")
	}
	if n := StashCount(); n != 1 {
		t.Errorf("the stash count changed after a refusal (%d)", n)
	}
}

// The third failure, and the one that used to be reported as the first: git
// refuses to restore a stash while the index holds unmerged entries, and it
// refuses BEFORE touching anything. Classifying that after the fact read the
// merge's own unmerged files back out of the index and blamed them on the
// stash — "conflict markers are now in s.txt, and the stash itself was kept" —
// with an advised recovery (`git reset HEAD` then `git checkout -- .`) that
// would have destroyed the half-resolved merge.
//
// Two keypresses from the conflict resolver: esc leaves the merge open by
// design, and the resolver has just told the user their work is parked in a
// stash.
func TestRestoreDuringAMergeBlamesTheMergeNotTheStash(t *testing.T) {
	for _, tc := range []struct {
		verb    string
		restore func(string) error
	}{
		{"apply", StashApplyRef},
		{"pop", StashPopRef},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			scratchRepo(t)
			write(t, "s.txt", "base\n")
			commitAll(t, "seed")
			runGit(t, "checkout", "-q", "-b", "feat")
			write(t, "s.txt", "theirs\n")
			commitAll(t, "feat edit")
			runGit(t, "checkout", "-q", "main")
			write(t, "s.txt", "ours\n")
			commitAll(t, "main edit")

			// An unrelated stash, so nothing about the failure is about the
			// stash's own content.
			write(t, "other.txt", "work in progress\n")
			runGit(t, "stash", "push", "-q", "--include-untracked")

			// The merge conflicts and is LEFT open — the app's own behaviour.
			if err := exec.Command("git", "merge", "--no-ff", "feat").Run(); err == nil {
				t.Fatal("the fixture merge did not conflict")
			}
			if !MergeInProgress() {
				t.Fatal("no merge in progress — the fixture proves nothing")
			}

			err := tc.restore("stash@{0}")
			if !errors.Is(err, ErrStashDuringMerge) {
				t.Fatalf("%s during a merge returned %v, want ErrStashDuringMerge", tc.verb, err)
			}
			if errors.Is(err, ErrStashConflict) {
				t.Error("reported as a stash conflict — the stash never ran")
			}
			if !strings.Contains(err.Error(), "abort the merge") {
				t.Errorf("error = %q, want it to name the merge as the blocker", err)
			}
			// Nothing was touched: the entry is still there and the merge's own
			// state is intact.
			if n := StashCount(); n != 1 {
				t.Errorf("stash count = %d, want the entry untouched", n)
			}
			if !MergeInProgress() {
				t.Error("the merge state was disturbed by a refused stash restore")
			}
		})
	}
}

// The same refusal without MERGE_HEAD: a conflicted stash pop leaves unmerged
// entries and no merge at all, and "finish or abort the merge" would then name
// something that does not exist.
func TestRestoreOntoAnUnmergedIndexWithoutAMerge(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "l1\nl2\nl3\n")
	commitAll(t, "seed")
	write(t, "a.txt", "l1\nSTASHED\nl3\n")
	runGit(t, "stash", "push", "-q")
	write(t, "a.txt", "l1\nCOMMITTED\nl3\n")
	commitAll(t, "conflicting change")

	// First apply conflicts for real — markers written, index unmerged.
	if err := StashApplyRef("stash@{0}"); !errors.Is(err, ErrStashConflict) {
		t.Fatalf("first apply returned %v, want ErrStashConflict", err)
	}
	if MergeInProgress() {
		t.Fatal("a conflicted stash apply set MERGE_HEAD — the fixture is wrong")
	}

	// The second one is refused because of what the first left behind.
	err := StashApplyRef("stash@{0}")
	if !errors.Is(err, ErrStashDuringMerge) {
		t.Fatalf("apply onto an unmerged index returned %v, want ErrStashDuringMerge", err)
	}
	if strings.Contains(err.Error(), "merge is in progress") {
		t.Errorf("error = %q, but there is no merge to abort", err)
	}
	if !strings.Contains(err.Error(), "a.txt") {
		t.Errorf("error = %q, want the unmerged path named", err)
	}
}

func TestStashFileCountCountsTheStashNotTheTree(t *testing.T) {
	scratchRepo(t)
	write(t, "a.txt", "one\n")
	write(t, "b.txt", "two\n")
	commitAll(t, "seed")

	write(t, "a.txt", "changed\n")
	write(t, "untracked.txt", "new\n")
	runGit(t, "stash", "push", "-q", "--include-untracked")

	// An unrelated edit in the tree. Counting `git status` afterwards would
	// report it as part of what the stash restored.
	write(t, "b.txt", "unrelated edit\n")

	if n := StashFileCount("stash@{0}"); n != 2 {
		t.Errorf("StashFileCount = %d, want 2 (a.txt + untracked.txt)", n)
	}
	if n := StashFileCount("nonsense"); n != 0 {
		t.Errorf("StashFileCount on a non-ref = %d, want 0", n)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
