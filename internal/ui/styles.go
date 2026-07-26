package ui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Terminal symbol set with Unicode/ASCII fallbacks
var (
	symCursor     = "▸"
	symSelected   = "●"
	symUnselected = "○"
	symDone       = "✓"
	symBranch     = "⎇"
	symArrowUp    = "↑"
	symArrowDown  = "↓"
	symArrowRight = "→"
	symArrows     = "↑↓"
	symSkip       = "⊘"
	symDirty      = "●"
	symWarn       = "⚠"
	symEllipsis   = "…"
	symHRule      = "─"
	symMidDot     = "·"
	symBullet     = "•"

	// Commit-graph glyphs. git emits `*`, `|`, `/`, `\`, `_`, `-` and `.`;
	// these are the display forms. `-` and `.` only ever show up on octopus
	// merge rows (`*-----.`), where they draw the horizontal reach out to the
	// extra parents and the corner where the last one turns down.
	symGraphVert   = "│"
	symGraphDash   = "─"
	symGraphCorner = "╮"
)

func init() {
	if !isUnicodeSupported() {
		symCursor = ">"
		symSelected = "*"
		symUnselected = "o"
		symDone = "+"
		symBranch = "@"
		symArrowUp = "^"
		symArrowDown = "v"
		symArrowRight = "->"
		symArrows = "^v"
		symSkip = "-"
		symDirty = "*"
		symWarn = "!"
		symEllipsis = "..."
		symHRule = "-"
		symMidDot = "-"
		symBullet = "*"
		symGraphVert = "|"
		symGraphDash = "-"
		symGraphCorner = "."
	}
}

// isUnicodeSupported decides between the Unicode and ASCII symbol sets.
//
// The rule, in order:
//  1. TERM=dumb — no rendering guarantees at all, so ASCII.
//  2. An explicitly set locale wins over everything: the first of
//     LC_ALL / LC_CTYPE / LANG that is non-empty decides, and it has to say
//     UTF-8 or the terminal genuinely cannot render multi-byte glyphs
//     (LANG=C used to slip through the old TERM fallback and print mojibake).
//  3. Nothing set at all — the modern default. Terminals that ship without a
//     locale (macOS Terminal over ssh, most container shells) are UTF-8 in
//     practice, and downgrading them to ASCII punished the common case.
func isUnicodeSupported() bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	for _, env := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		val := os.Getenv(env)
		if val == "" {
			continue
		}
		lower := strings.ToLower(val)
		return strings.Contains(lower, "utf-8") || strings.Contains(lower, "utf8")
	}
	return true
}

var (
	// Colors. Every foreground that lands on the terminal's own background is
	// an AdaptiveColor: the Dark value is the original palette, the Light one
	// is the readable counterpart for white-background terminals, where the
	// old near-white text was invisible. The names still describe the dark
	// value (that is what a reader of `white` expects to see on a dark term).
	// NO_COLOR is unaffected — lipgloss drops all of this when it is set.
	purple    = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#A78BFA"}
	green     = lipgloss.AdaptiveColor{Light: "#047857", Dark: "#34D399"}
	yellow    = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	red       = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F87171"}
	blue      = lipgloss.AdaptiveColor{Light: "#1D4ED8", Dark: "#60A5FA"}
	gray      = lipgloss.AdaptiveColor{Light: "#4B5563", Dark: "#9CA3AF"}
	lightGray = lipgloss.AdaptiveColor{Light: "#374151", Dark: "#D1D5DB"}
	white     = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#E5E7EB"}
	dimColor  = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#4B5563"}
	cyan      = lipgloss.AdaptiveColor{Light: "#0E7490", Dark: "#22D3EE"}
	amber     = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	violet    = lipgloss.AdaptiveColor{Light: "#6D28D9", Dark: "#C4B5FD"}

	// Brand badge. A solid background carries its own contrast, so this pair
	// stays fixed in both themes — it is the one place the identity purple is
	// pinned rather than adapted.
	brandPurple = lipgloss.Color("#7C3AED")
	brandText   = lipgloss.Color("#FFFFFF")

	// Header
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(brandText).
			Background(brandPurple).
			Padding(0, 1)

	branchStyle = lipgloss.NewStyle().
			Foreground(cyan).
			Bold(true)

	stepStyle = lipgloss.NewStyle().
			Foreground(gray).
			Italic(true)

	// File status
	modifiedStyle  = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	addedStyle     = lipgloss.NewStyle().Foreground(green).Bold(true)
	deletedStyle   = lipgloss.NewStyle().Foreground(red).Bold(true)
	untrackedStyle = lipgloss.NewStyle().Foreground(blue).Bold(true)
	renamedStyle   = lipgloss.NewStyle().Foreground(violet).Bold(true)

	// List items
	cursorStyle     = lipgloss.NewStyle().Foreground(purple).Bold(true)
	selectedCheck   = lipgloss.NewStyle().Foreground(green).Bold(true)
	gitignoreCheck  = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	dimmedCheck     = lipgloss.NewStyle().Foreground(dimColor).Bold(true)
	unselectedCheck = lipgloss.NewStyle().Foreground(dimColor)
	filePathStyle   = lipgloss.NewStyle().Foreground(white)
	highlightStyle  = lipgloss.NewStyle().Foreground(white).Bold(true)

	// Radio list
	activeStyle   = lipgloss.NewStyle().Foreground(purple).Bold(true)
	inactiveStyle = lipgloss.NewStyle().Foreground(gray)

	// Help bar
	helpStyle    = lipgloss.NewStyle().Foreground(gray)
	helpKeyStyle = lipgloss.NewStyle().Foreground(lightGray).Bold(true)

	// Feedback
	successStyle = lipgloss.NewStyle().Foreground(green).Bold(true)
	errorStyle   = lipgloss.NewStyle().Foreground(red).Bold(true)

	// Misc
	previewStyle = lipgloss.NewStyle().Foreground(gray).Italic(true)
	dimStyle     = lipgloss.NewStyle().Foreground(dimColor)

	// Diff
	diffAddStyle    = lipgloss.NewStyle().Foreground(green)
	diffRemoveStyle = lipgloss.NewStyle().Foreground(red)
	diffHunkStyle   = lipgloss.NewStyle().Foreground(cyan)
	diffHeaderStyle = lipgloss.NewStyle().Foreground(dimColor)

	// Box
	boxBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(purple).
			Padding(1, 2)

	// Graph panels
	graphTitleStyle = lipgloss.NewStyle().
			Foreground(cyan).
			Bold(true)

	// Tag refs in the commit graph. Deliberately not branchStyle: a tag marks
	// a release, it is not somewhere you can check out and commit onto, and
	// amber is what every terminal tool uses to say "release" without words.
	tagStyle = lipgloss.NewStyle().
			Foreground(amber).
			Bold(true)
)
