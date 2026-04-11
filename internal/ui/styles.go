package ui

import "github.com/charmbracelet/lipgloss"

var (
	// Colors
	purple    = lipgloss.Color("#7C3AED")
	green     = lipgloss.Color("#10B981")
	yellow    = lipgloss.Color("#F59E0B")
	red       = lipgloss.Color("#EF4444")
	blue      = lipgloss.Color("#3B82F6")
	gray      = lipgloss.Color("#6B7280")
	lightGray = lipgloss.Color("#9CA3AF")
	white     = lipgloss.Color("#E5E7EB")
	dimColor  = lipgloss.Color("#4B5563")
	cyan      = lipgloss.Color("#06B6D4")

	// Header
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(purple).
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
	renamedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8B5CF6")).Bold(true)

	// List items
	cursorStyle     = lipgloss.NewStyle().Foreground(purple).Bold(true)
	selectedCheck   = lipgloss.NewStyle().Foreground(green).Bold(true)
	gitignoreCheck  = lipgloss.NewStyle().Foreground(yellow).Bold(true)
	unselectedCheck = lipgloss.NewStyle().Foreground(dimColor)
	filePathStyle   = lipgloss.NewStyle().Foreground(white)
	highlightStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)

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

	// Box
	boxBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(purple).
			Padding(1, 2)
)
