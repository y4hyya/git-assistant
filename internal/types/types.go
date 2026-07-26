package types

// FileStatus represents the git status of a file.
type FileStatus int

const (
	StatusModified  FileStatus = iota
	StatusAdded
	StatusDeleted
	StatusRenamed
	StatusUntracked
)

// Symbol returns the short status indicator.
func (s FileStatus) Symbol() string {
	switch s {
	case StatusModified:
		return "M"
	case StatusAdded:
		return "A"
	case StatusDeleted:
		return "D"
	case StatusRenamed:
		return "R"
	case StatusUntracked:
		return "?"
	default:
		return "?"
	}
}

// FileEntry represents a changed file in the repository.
// OrigPath is set only for renamed/copied entries and holds the path the
// file had before the rename. Staging a rename requires both halves (the
// deletion of OrigPath and the addition of Path) or the commit records the
// new file while leaving the old one tracked.
type FileEntry struct {
	Path       string
	OrigPath   string
	Status     FileStatus
	Selected   bool
	Gitignored bool
}

// BranchEntry represents a git branch.
// CheckedOutElsewhere marks branches git reports with a "+ " prefix: they are
// checked out in another worktree, so they can't be switched to or deleted
// from here.
type BranchEntry struct {
	Name                string
	IsCurrent           bool
	IsRemote            bool
	CheckedOutElsewhere bool
}

// CommitType represents a conventional commit type.
type CommitType struct {
	Name        string
	Description string
}

// CommitTypes is the list of available conventional commit types.
var CommitTypes = []CommitType{
	{Name: "feat", Description: "A new feature"},
	{Name: "fix", Description: "A bug fix"},
	{Name: "docs", Description: "Documentation only"},
	{Name: "refactor", Description: "Code refactoring"},
	{Name: "style", Description: "Formatting, no code change"},
	{Name: "test", Description: "Adding tests"},
	{Name: "chore", Description: "Maintenance"},
	{Name: "ci", Description: "CI/CD changes"},
	{Name: "perf", Description: "Performance improvement"},
	{Name: "build", Description: "Build system changes"},
}
