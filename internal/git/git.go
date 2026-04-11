package git

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git-assist/internal/types"
)

// IsGitRepo checks if the current directory is inside a git repository.
func IsGitRepo() bool {
	return exec.Command("git", "rev-parse", "--is-inside-work-tree").Run() == nil
}

// GetCurrentBranch returns the name of the current branch.
func GetCurrentBranch() (string, error) {
	out, err := exec.Command("git", "branch", "--show-current").Output()
	if err != nil {
		return "", err
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "HEAD (detached)", nil
	}
	return branch, nil
}

// GetStatus returns the list of changed files.
func GetStatus() ([]types.FileEntry, error) {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}

	output := strings.TrimSpace(string(out))
	if output == "" {
		return nil, nil
	}

	lines := strings.Split(output, "\n")
	var files []types.FileEntry

	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		xy := line[:2]
		path := line[3:]

		// Handle renamed files: "old -> new"
		if idx := strings.Index(path, " -> "); idx != -1 {
			path = path[idx+4:]
		}

		var status types.FileStatus
		switch {
		case xy == "??":
			status = types.StatusUntracked
		case xy[0] == 'A' || xy[1] == 'A':
			status = types.StatusAdded
		case xy[0] == 'D' || xy[1] == 'D':
			status = types.StatusDeleted
		case xy[0] == 'R' || xy[1] == 'R':
			status = types.StatusRenamed
		default:
			status = types.StatusModified
		}

		files = append(files, types.FileEntry{
			Path:   path,
			Status: status,
		})
	}

	return files, nil
}

// GetBranches returns available branches for pushing.
// The current branch is always listed first.
func GetBranches(currentBranch string) []string {
	branches := []string{currentBranch}

	out, err := exec.Command("git", "branch", "-r").Output()
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			branch := strings.TrimSpace(line)
			if branch == "" || strings.Contains(branch, "->") {
				continue
			}
			branch = strings.TrimPrefix(branch, "origin/")
			if branch != currentBranch {
				branches = append(branches, branch)
			}
		}
	}

	return branches
}

// HasRemote checks if any remote is configured.
func HasRemote() bool {
	out, err := exec.Command("git", "remote").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) != ""
}

// Commit stages the selected files and creates a commit.
// cachedPaths are files that were gitignored and need git rm --cached
// re-applied after the staging reset.
func Commit(filePaths []string, cachedPaths []string, message string) error {
	// Reset staging area so only selected files are committed.
	// Ignore error — fails on repos with no commits yet.
	exec.Command("git", "reset").Run()

	// Re-apply rm --cached for gitignored tracked files
	if err := RemoveCached(cachedPaths); err != nil {
		return err
	}

	// Stage selected files
	args := append([]string{"add", "--"}, filePaths...)
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("staging failed: %s", strings.TrimSpace(string(out)))
	}

	// Commit
	if out, err := exec.Command("git", "commit", "-m", message).CombinedOutput(); err != nil {
		return fmt.Errorf("commit failed: %s", strings.TrimSpace(string(out)))
	}

	return nil
}

// Push pushes to the specified remote branch.
func Push(currentBranch, targetBranch string) error {
	var args []string
	if currentBranch == targetBranch {
		args = []string{"push", "-u", "origin", targetBranch}
	} else {
		args = []string{"push", "origin", "HEAD:" + targetBranch}
	}

	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// AddToGitignore appends the given paths to .gitignore, skipping duplicates.
func AddToGitignore(paths []string) error {
	if len(paths) == 0 {
		return nil
	}

	existing := make(map[string]bool)
	content, err := os.ReadFile(".gitignore")
	if err == nil {
		for _, line := range strings.Split(string(content), "\n") {
			existing[strings.TrimSpace(line)] = true
		}
	}

	var toAdd []string
	for _, p := range paths {
		if !existing[p] {
			toAdd = append(toAdd, p)
		}
	}

	if len(toAdd) == 0 {
		return nil
	}

	f, err := os.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open .gitignore: %w", err)
	}
	defer f.Close()

	// Ensure we start on a new line if file doesn't end with one
	if len(content) > 0 && content[len(content)-1] != '\n' {
		if _, err := f.WriteString("\n"); err != nil {
			return err
		}
	}

	for _, p := range toAdd {
		if _, err := f.WriteString(p + "\n"); err != nil {
			return err
		}
	}

	return nil
}

// RemoveCached removes files from git tracking without deleting them from disk.
func RemoveCached(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"rm", "--cached", "-r", "--"}, paths...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("rm --cached failed: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// GetCommitStats returns a short summary of the last commit.
func GetCommitStats() string {
	out, err := exec.Command("git", "diff", "--stat", "HEAD~1..HEAD").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[len(lines)-1])
	}
	return ""
}
