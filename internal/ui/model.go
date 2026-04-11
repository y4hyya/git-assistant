package ui

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"git-assist/internal/git"
	"git-assist/internal/types"
)

// step represents which screen we're on.
type step int

const (
	stepFiles   step = iota // file selection
	stepType                // commit type picker
	stepCustom              // custom type input
	stepMessage             // commit message input
	stepPush                // branch picker + push
	stepDone                // success screen
)

// Async result messages
type commitResultMsg struct{ err error }
type pushResultMsg struct{ err error }

// Model is the main Bubble Tea model.
type Model struct {
	// Current wizard step
	step step

	// Step 1 — file selection
	files  []types.FileEntry
	cursor int
	branch string

	// Step 2 — type picker
	typeIdx    int
	commitType string

	// Step 2b — custom type input
	customInput textinput.Model

	// Step 3 — commit message
	msgInput textinput.Model

	// Step 4 — push
	branches   []string
	branchIdx  int
	hasRemote  bool
	pushBranch string

	// State flags
	committing bool
	pushing    bool
	pushed     bool

	// Error (shown on current step, cleared on next keypress)
	err error

	// Terminal dimensions
	width    int
	height   int
	quitting bool
}

// NewModel creates the initial model.
func NewModel(files []types.FileEntry, branch string) Model {
	mi := textinput.New()
	mi.Placeholder = "Describe your changes..."
	mi.CharLimit = 200
	mi.Width = 50

	ci := textinput.New()
	ci.Placeholder = "Enter custom type..."
	ci.CharLimit = 20
	ci.Width = 30

	return Model{
		step:        stepFiles,
		files:       files,
		branch:      branch,
		msgInput:    mi,
		customInput: ci,
		hasRemote:   git.HasRemote(),
	}
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case commitResultMsg:
		m.committing = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		if m.hasRemote {
			m.branches = git.GetBranches(m.branch)
			m.step = stepPush
		} else {
			m.step = stepDone
		}
		return m, nil

	case pushResultMsg:
		m.pushing = false
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.pushed = true
		m.step = stepDone
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
		// Clear error on any keypress
		m.err = nil
	}

	// Route to the active step handler
	switch m.step {
	case stepFiles:
		return m.updateFiles(msg)
	case stepType:
		return m.updateType(msg)
	case stepCustom:
		return m.updateCustom(msg)
	case stepMessage:
		return m.updateMessage(msg)
	case stepPush:
		return m.updatePush(msg)
	case stepDone:
		return m.updateDone(msg)
	}

	return m, nil
}

func (m Model) View() string {
	if m.quitting {
		return ""
	}

	switch m.step {
	case stepFiles:
		return m.viewFiles()
	case stepType:
		return m.viewType()
	case stepCustom:
		return m.viewCustom()
	case stepMessage:
		return m.viewMessage()
	case stepPush:
		return m.viewPush()
	case stepDone:
		return m.viewDone()
	}

	return ""
}
