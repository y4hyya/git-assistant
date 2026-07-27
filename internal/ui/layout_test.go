package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"unicode/utf8"

	"git-assist/internal/git"
	"github.com/charmbracelet/lipgloss"
)

// sep is the byte GetUnifiedGraph puts between subject and decorations.
const sep = string(graphFieldSep)

// ── Subject / decoration split ─────────────────────────

// The whole point of the %x1f separator: a subject is whatever git says it
// is, including its own parentheses, and a decoration only exists when git
// emitted one.
func TestParseLineSplitsOnUnitSeparator(t *testing.T) {
	cases := []struct {
		name string
		line string
		msg  string
		refs []string
	}{
		{
			name: "subject ending in a parenthetical is not a decoration",
			line: "* fix: handle (edge case)" + sep,
			msg:  "fix: handle (edge case)",
		},
		{
			name: "subject with parenthetical AND a real decoration",
			line: "* feat: amend last commit (v1.2.0)" + sep + " (HEAD -> main, origin/main)",
			msg:  "feat: amend last commit (v1.2.0)",
			refs: []string{"main", "origin/main"},
		},
		{
			name: "issue number in the subject stays in the subject",
			line: "* Fix login (#123)" + sep,
			msg:  "Fix login (#123)",
		},
		{
			name: "nested parens survive verbatim",
			line: "* refactor: drop (old (legacy)) path" + sep + " (dev)",
			msg:  "refactor: drop (old (legacy)) path",
			refs: []string{"dev"},
		},
		{
			name: "empty subject with a decoration",
			line: "* " + sep + " (main)",
			msg:  "",
			refs: []string{"main"},
		},
		{
			name: "connector row has neither",
			line: "|\\ \\ \\ \\  ",
			msg:  "",
		},
		{
			name: "no separator at all: everything is subject",
			line: "* legacy output (not a branch)",
			msg:  "legacy output (not a branch)",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gl := parseLine(c.line)
			if gl.message != c.msg {
				t.Errorf("message = %q, want %q", gl.message, c.msg)
			}
			var got []string
			for _, r := range gl.refs {
				got = append(got, r.name)
			}
			if strings.Join(got, ",") != strings.Join(c.refs, ",") {
				t.Errorf("refs = %v, want %v", got, c.refs)
			}
		})
	}
}

// ── cleanDecoration ────────────────────────────────────

func TestCleanDecoration(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		names []string
		kinds []decoKind
	}{
		{
			name:  "origin/HEAD is dropped, tag kept and ordered last",
			raw:   " (tag: v1.2.0, origin/main, origin/HEAD)",
			names: []string{"origin/main", "v1.2.0"},
			kinds: []decoKind{decoBranch, decoTag},
		},
		{
			name:  "checked-out branch comes first",
			raw:   " (origin/main, HEAD -> main, tag: v1.0.0, dev)",
			names: []string{"main", "origin/main", "dev", "v1.0.0"},
			kinds: []decoKind{decoHead, decoBranch, decoBranch, decoTag},
		},
		{
			name:  "a tag-only commit is still decorated",
			raw:   " (tag: v1.1.0)",
			names: []string{"v1.1.0"},
			kinds: []decoKind{decoTag},
		},
		{
			name:  "bare HEAD alone leaves nothing",
			raw:   " (HEAD)",
			names: nil,
		},
		{
			name:  "upstream/HEAD is dropped too",
			raw:   " (upstream/HEAD, upstream/main)",
			names: []string{"upstream/main"},
			kinds: []decoKind{decoBranch},
		},
		{name: "empty field", raw: "", names: nil},
		{name: "whitespace only", raw: "   ", names: nil},
		{name: "unbalanced", raw: " (main", names: nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			refs := cleanDecoration(c.raw)
			if len(refs) != len(c.names) {
				t.Fatalf("got %d refs %v, want %d %v", len(refs), refs, len(c.names), c.names)
			}
			for i, r := range refs {
				if r.name != c.names[i] {
					t.Errorf("ref[%d] = %q, want %q", i, r.name, c.names[i])
				}
				if c.kinds != nil && r.kind != c.kinds[i] {
					t.Errorf("ref[%d] %q kind = %v, want %v", i, r.name, r.kind, c.kinds[i])
				}
			}
		})
	}
}

// Tags must not be painted as branches — a release is not somewhere you can
// commit onto, and that is the only signal the graph gives about it.
func TestTagStyleIsDistinctFromBranchStyle(t *testing.T) {
	tagFg := refStyle(decoRef{kind: decoTag}).GetForeground()
	branchFg := refStyle(decoRef{kind: decoBranch}).GetForeground()
	headFg := refStyle(decoRef{kind: decoHead}).GetForeground()
	if tagFg == branchFg {
		t.Errorf("tag and branch refs share the foreground %v", tagFg)
	}
	if headFg == branchFg {
		t.Errorf("checked-out branch is indistinguishable from other branches (%v)", headFg)
	}
}

// ── Width-aware truncation ─────────────────────────────

func TestTruncateWidthIsRuneSafe(t *testing.T) {
	const turkish = "döküman çevirisi tamamlandı"

	for width := 0; width <= lipgloss.Width(turkish)+2; width++ {
		got := truncateWidth(turkish, width)
		if !utf8.ValidString(got) {
			t.Fatalf("width %d produced invalid UTF-8: %q", width, got)
		}
		if w := lipgloss.Width(got); w > width {
			t.Fatalf("width %d produced %d cells: %q", width, w, got)
		}
	}

	// Byte-slicing would have cut this at 20 bytes — 17 runes in — and left a
	// broken rune behind. Cell counting keeps every character whole.
	got := truncateWidth(turkish, 20)
	if !strings.HasSuffix(got, symEllipsis) {
		t.Errorf("truncated text %q does not end in the ellipsis symbol", got)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Errorf("truncated text contains a replacement rune: %q", got)
	}

	if got := truncateWidth(turkish, 100); got != turkish {
		t.Errorf("a fitting string was altered: %q", got)
	}
	// Double-width runes count as two cells.
	if w := lipgloss.Width(truncateWidth("東京都のコミット", 7)); w > 7 {
		t.Errorf("wide runes overflowed the budget: %d cells", w)
	}
}

// ── Graph row rendering stays inside the box ───────────

func TestRenderGraphLineNeverExceedsWidth(t *testing.T) {
	longRefs := " (HEAD -> feature/user-authentication, origin/feature/user-authentication, main, origin/main)"
	lines := []string{
		"* Fix login" + sep + longRefs,
		"* döküman çevirisi tamamlandı ve gözden geçirildi" + sep + " (feature/really-long-branch-name-for-truncation)",
		"* " + strings.Repeat("very long subject ", 12) + sep + " (main)",
		"| | | | * feat(four): work on four" + sep + " (four)",
		"*-----.   Merge branches 'one', 'two', 'three' and 'four'" + sep + " (HEAD -> main, tag: v1.0.0)",
		"* chore: seed" + sep,
	}

	for _, maxWidth := range []int{28, 40, 66, 80, 120} {
		for _, raw := range lines {
			out := renderGraphLine(parseLine(raw), maxWidth)
			if w := lipgloss.Width(out); w > maxWidth {
				t.Errorf("maxWidth %d: rendered %d cells\n  in:  %q\n  out: %q", maxWidth, w, raw, out)
			}
			if !utf8.ValidString(out) {
				t.Errorf("maxWidth %d: invalid UTF-8 out of %q", maxWidth, raw)
			}
		}
	}
}

// A row whose refs alone are wider than the row must still show them — the
// refs are what says where you are — but bounded, and never at the cost of
// wrapping the row and breaking the graph columns underneath.
func TestLongDecorationIsTruncatedNotWrapped(t *testing.T) {
	raw := "* Fix login" + sep + " (HEAD -> feature/user-authentication, origin/feature/user-authentication, main)"
	out := renderGraphLine(parseLine(raw), 66)

	if strings.Contains(out, "\n") {
		t.Fatalf("row wrapped:\n%s", out)
	}
	if !strings.Contains(out, "feature/user-auth") {
		t.Errorf("checked-out branch dropped entirely: %q", out)
	}
	if !strings.Contains(out, symEllipsis) {
		t.Errorf("truncation is not marked: %q", out)
	}
}

// ── Graph character translation ────────────────────────

func TestGraphCharTranslation(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		chars string
		msg   string
	}{
		{
			name:  "octopus merge row",
			line:  "*-----.   Merge branches 'one' and 'two'" + sep + " (HEAD -> main)",
			chars: "*-----.   ",
			msg:   "Merge branches 'one' and 'two'",
		},
		{
			name:  "three-parent shorthand",
			line:  "*-.   Merge branches" + sep,
			chars: "*-.   ",
			msg:   "Merge branches",
		},
		{
			name:  "a subject starting with a dash is not graph",
			line:  "* -fix the thing" + sep,
			chars: "* ",
			msg:   "-fix the thing",
		},
		{
			name:  "a subject starting with a dot is not graph",
			line:  "* .gitignore now covers builds" + sep,
			chars: "* ",
			msg:   ".gitignore now covers builds",
		},
		{
			name:  "plain merge row",
			line:  "|\\ \\ \\ \\  ",
			chars: "|\\ \\ \\ \\  ",
			msg:   "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gl := parseLine(c.line)
			if string(gl.chars) != c.chars {
				t.Errorf("graph chars = %q, want %q", string(gl.chars), c.chars)
			}
			if gl.message != c.msg {
				t.Errorf("message = %q, want %q", gl.message, c.msg)
			}
		})
	}

	// The octopus connectors render as graph glyphs, not as message text.
	out := renderGraphLine(parseLine("*-----.   Merge branches"+sep), 80)
	if !strings.Contains(out, symGraphDash) || !strings.Contains(out, symGraphCorner) {
		t.Errorf("octopus connectors were not drawn: %q", out)
	}
	if strings.Contains(out, "-----. ") && symGraphDash != "-" {
		t.Errorf("raw git connectors leaked into the row: %q", out)
	}
}

// ── isUnicodeSupported ─────────────────────────────────

func TestIsUnicodeSupportedRuleTable(t *testing.T) {
	cases := []struct {
		name                      string
		term, lcAll, lcType, lang string
		want                      bool
	}{
		{name: "dumb terminal wins over a UTF-8 locale", term: "dumb", lang: "en_US.UTF-8", want: false},
		{name: "nothing set is Unicode", term: "xterm-256color", want: true},
		{name: "nothing set at all is Unicode", want: true},
		{name: "explicit C locale beats the TERM heuristic", term: "xterm-256color", lcAll: "C", want: false},
		{name: "POSIX locale", term: "xterm-256color", lang: "POSIX", want: false},
		{name: "LC_ALL wins over LC_CTYPE", lcAll: "C", lcType: "en_US.UTF-8", want: false},
		{name: "LC_CTYPE wins over LANG", lcType: "UTF-8", lang: "C", want: true},
		{name: "utf8 without the hyphen", lang: "en_US.utf8", want: true},
		{name: "case insensitive", lang: "tr_TR.utf-8", want: true},
		{name: "latin-1 locale", lang: "en_US.ISO-8859-1", want: false},
		{name: "empty TERM does not force ASCII", term: "", lang: "en_US.UTF-8", want: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("TERM", c.term)
			t.Setenv("LC_ALL", c.lcAll)
			t.Setenv("LC_CTYPE", c.lcType)
			t.Setenv("LANG", c.lang)
			if got := isUnicodeSupported(); got != c.want {
				t.Errorf("isUnicodeSupported() = %v, want %v", got, c.want)
			}
		})
	}
}

// ── Height budget ──────────────────────────────────────

// The box must keep its floor at every terminal height. MaxHeight applied to
// the rendered box used to delete the bottom padding and border instead of
// the overflowing content — and it fired exactly when an error was on screen.
func TestMenuKeepsItsBottomBorderAtEveryHeight(t *testing.T) {
	graph := strings.Repeat("* fix: a commit that is on the graph"+sep+" (main)\n", 15)

	for _, h := range []int{10, 12, 15, 20, 24, 30, 40} {
		for _, withErr := range []bool{false, true} {
			m := wizardModel(t, stepMenu)
			m.width = 80
			m.height = h
			m.localGraph = strings.TrimRight(graph, "\n")
			m.aheadBehind = "2 ahead " + symMidDot + " 1 behind"
			m.hasAnyCommit = true
			m.initSuccessMsg = "Connected to github.com/y4hyya/Git-Assistant"
			if withErr {
				m.err = fmt.Errorf("nothing to commit — working tree clean")
			}

			view := m.viewMenu()
			lines := strings.Split(view, "\n")
			last := lines[len(lines)-1]

			if !strings.Contains(last, "╰") || !strings.Contains(last, "╯") {
				t.Errorf("height %d (err=%v): bottom border missing, last line = %q",
					h, withErr, last)
			}
			if got := len(lines); got > h {
				t.Errorf("height %d (err=%v): box is %d rows tall", h, withErr, got)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > m.width {
					t.Errorf("height %d (err=%v): line %d is %d cells wide (terminal is %d): %q",
						h, withErr, i, w, m.width, line)
				}
			}
		}
	}
}

// The graph is the section that gives way first, and it gives way whole.
func TestGraphSectionYieldsBeforeTheRestOfTheMenu(t *testing.T) {
	tall := wizardModel(t, stepMenu)
	tall.width = 80
	tall.height = 40
	tall.localGraph = strings.Repeat("* fix: something"+sep+"\n", 15)

	short := tall
	short.height = 14

	if !strings.Contains(tall.viewMenu(), "Commit Graph") {
		t.Error("a 40-row terminal should have room for the graph")
	}
	if strings.Contains(short.viewMenu(), "Commit Graph") {
		t.Error("a 14-row terminal showed the graph instead of dropping it")
	}
	// The menu itself never gives way while the graph is still there to drop.
	if !strings.Contains(short.viewMenu(), "Branch") {
		t.Error("menu items were sacrificed before the graph section")
	}
}

func TestClampLinesCountsWrappedRows(t *testing.T) {
	content := "short\n" + strings.Repeat("x", 25) + "\ntail"
	// Width 10: the middle line costs 3 rows, so only 4 of the 5 rows fit and
	// the tail has to go.
	got := clampLines(content, 4, 10)
	if strings.Contains(got, "tail") {
		t.Errorf("wrapped rows were not counted: %q", got)
	}
	if !strings.Contains(got, "short") {
		t.Errorf("clamp dropped rows that fit: %q", got)
	}
	if got := clampLines("a\nb\nc", 10, 40); got != "a\nb\nc" {
		t.Errorf("content that fits was altered: %q", got)
	}
}

// ── Menu cursor clamp ──────────────────────────────────

func TestMenuCursorClampedWhenTheListShrinks(t *testing.T) {
	m := wizardModel(t, stepMenu)
	m.hasAnyCommit = true
	items := m.menuItems()

	// Simulates "Connect to GitHub" disappearing under the cursor after the
	// repo gains a remote: the cursor now points past the end.
	m.menuCursor = len(items) + 3

	view := m.viewMenu()
	if !strings.Contains(view, symCursor) {
		t.Error("no row is highlighted with an out-of-range cursor")
	}

	m, _ = key(t, m, "x") // any keypress must repair the model itself
	if m.menuCursor != len(items)-1 {
		t.Fatalf("menuCursor = %d, want %d", m.menuCursor, len(items)-1)
	}
	// And navigation works again from there.
	m, _ = key(t, m, "up")
	if m.menuCursor != len(items)-2 {
		t.Fatalf("after up: menuCursor = %d, want %d", m.menuCursor, len(items)-2)
	}
}

// ── parseConventionalSubject ───────────────────────────

// Prose with a colon in it is not a conventional commit. Treating it as one
// dropped everything before the colon out of the wizard.
func TestParseConventionalSubjectRejectsProsePrefixes(t *testing.T) {
	raw := []string{
		"Update the README: add badges",
		"WIP feat: still working",
		"see http://example.com: link rot",
		"feat (auth): spaced scope",
		"feat(): empty scope",
		"fix(a(b)): nested parens",
	}
	for _, s := range raw {
		cType, scope, breaking, rest := parseConventionalSubject(s)
		if cType != "" || scope != "" || breaking || rest != s {
			t.Errorf("parse(%q) = (%q,%q,%v,%q), want the raw path with the subject intact",
				s, cType, scope, breaking, rest)
		}
	}

	conventional := []struct {
		in, cType, scope, rest string
		breaking               bool
	}{
		{in: "feat: add login", cType: "feat", rest: "add login"},
		{in: "feat(auth): add login", cType: "feat", scope: "auth", rest: "add login"},
		{in: "wip(core)!: halfway", cType: "wip", scope: "core", rest: "halfway", breaking: true},
		{in: "chore-deps: bump", cType: "chore-deps", rest: "bump"},
		{in: "fix: handle (edge case)", cType: "fix", rest: "handle (edge case)"},
		// One word is one word, even when it reads like prose. This still
		// round-trips: the type picker lands on "custom" with the word
		// prefilled, so enter-through rebuilds "Note: this was a hotfix".
		{in: "Note: this was a hotfix", cType: "Note", rest: "this was a hotfix"},
	}
	for _, c := range conventional {
		cType, scope, breaking, rest := parseConventionalSubject(c.in)
		if cType != c.cType || scope != c.scope || breaking != c.breaking || rest != c.rest {
			t.Errorf("parse(%q) = (%q,%q,%v,%q), want (%q,%q,%v,%q)",
				c.in, cType, scope, breaking, rest, c.cType, c.scope, c.breaking, c.rest)
		}
	}
}

// ── End to end against real git output ─────────────────

// octopusRepo builds a throwaway repo whose history has an octopus merge, a
// tag, and a subject that ends in a parenthetical — the three shapes the
// graph renderer used to mangle — and chdirs into it.
func octopusRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", isolatedGitConfig(t))
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t.invalid")
	t.Chdir(t.TempDir())

	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(name, subject string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", name)
		run("commit", "-q", "-m", subject)
	}

	run("init", "-q", "-b", "main")
	commit("seed", "chore: seed")
	for _, b := range []string{"one", "two", "three"} {
		run("checkout", "-q", "-b", b, "main")
		commit(b, "feat("+b+"): work on "+b)
	}
	run("checkout", "-q", "main")
	commit("edge", "fix: handle (edge case)")
	run("merge", "-q", "--no-edit", "one", "two", "three",
		"-m", "Merge branches 'one', 'two' and 'three'")
	run("tag", "v1.0.0")
	run("checkout", "-q", "-b", "feature/really-long-branch-name-for-truncation", "main")
	commit("tr", "döküman çevirisi tamamlandı ve gözden geçirildi")
	run("checkout", "-q", "main")
}

func TestUnifiedGraphRendersRealHistory(t *testing.T) {
	octopusRepo(t)

	raw := git.GetUnifiedGraph(20)
	if raw == "" {
		t.Fatal("GetUnifiedGraph returned nothing")
	}
	if !strings.Contains(raw, sep) {
		t.Fatal("graph output carries no unit separator — the format string regressed")
	}

	var octopus, edge, tagged, turkish bool
	for _, line := range strings.Split(raw, "\n") {
		gl := parseLine(line)
		switch {
		case strings.HasPrefix(gl.message, "Merge branches"):
			octopus = true
			if strings.ContainsAny(string(gl.chars), "*") && !strings.Contains(string(gl.chars), "-") {
				t.Errorf("octopus row lost its connectors: %q", string(gl.chars))
			}
			if gl.message != "Merge branches 'one', 'two' and 'three'" {
				t.Errorf("octopus subject = %q — graph characters leaked in", gl.message)
			}
			for _, r := range gl.refs {
				if r.kind == decoTag && r.name == "v1.0.0" {
					tagged = true
				}
				if r.name == "v1.0.0" && r.kind != decoTag {
					t.Errorf("v1.0.0 rendered as kind %v, want a tag", r.kind)
				}
			}
		case gl.message == "fix: handle (edge case)":
			edge = true
			if len(gl.refs) != 0 {
				t.Errorf("subject parenthetical became refs: %v", gl.refs)
			}
		case strings.HasPrefix(gl.message, "döküman"):
			turkish = true
		}
		if strings.ContainsRune(gl.message, graphFieldSep) {
			t.Errorf("separator leaked into the message: %q", gl.message)
		}
	}
	if !octopus || !edge || !tagged || !turkish {
		t.Fatalf("history not fully parsed: octopus=%v edge=%v tagged=%v turkish=%v",
			octopus, edge, tagged, turkish)
	}

	// And the whole panel stays inside its width at any terminal size.
	for _, width := range []int{40, 60, 80, 120} {
		for _, line := range strings.Split(transformGraph(raw, width, 12), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: graph row is %d cells: %q", width, w, line)
			}
		}
	}
}

// The file selector's footer is two rows now (the honest key set does not fit
// one 80-column line), and the list budget has to leave room for both — at
// every height, in both modes.
func TestFileSelectorKeepsItsFooterAndBorder(t *testing.T) {
	for _, h := range []int{12, 15, 20, 24, 30, 40} {
		for _, gitignore := range []bool{false, true} {
			m := wizardModel(t, stepFiles, manyFiles(40)...)
			m.width = 80
			m.height = h
			if gitignore {
				m.gitignoreMode = true
				m.removeIgnored = map[string]bool{}
				for i := 0; i < 20; i++ {
					m.existingIgnored = append(m.existingIgnored, fmt.Sprintf("build/out%02d", i))
				}
			}

			view := m.viewFiles()
			lines := strings.Split(view, "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, "╰") || !strings.Contains(last, "╯") {
				t.Errorf("height %d (gitignore=%v): bottom border missing, last line = %q", h, gitignore, last)
			}
			if got := len(lines); got > h {
				t.Errorf("height %d (gitignore=%v): box is %d rows tall", h, gitignore, got)
			}
			// The last footer row survives from the height where the screen is
			// usable at all.
			if h >= 20 && !strings.Contains(view, "quit") {
				t.Errorf("height %d (gitignore=%v): the footer was clipped:\n%s", h, gitignore, view)
			}
			for i, line := range lines {
				if w := lipgloss.Width(line); w > m.width {
					t.Errorf("height %d (gitignore=%v): line %d is %d cells wide: %q", h, gitignore, i, w, line)
				}
			}
		}
	}
}
