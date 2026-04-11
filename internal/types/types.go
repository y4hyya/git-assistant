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
type FileEntry struct {
	Path     string
	Status   FileStatus
	Selected bool
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
