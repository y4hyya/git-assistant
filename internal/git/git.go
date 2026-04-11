package git

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git-assist/internal/types"
)

// ErrBinaryFile is returned when a file is detected as binary.
var ErrBinaryFile = errors.New("binary file")

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

	// Stage selected files individually — skip any that fail
	staged := 0
	var lastErr string
	for _, p := range filePaths {
		if out, err := exec.Command("git", "add", "--", p).CombinedOutput(); err != nil {
			lastErr = strings.TrimSpace(string(out))
			continue
		}
		staged++
	}
	if staged == 0 && len(filePaths) > 0 {
		return fmt.Errorf("staging failed: %s", lastErr)
	}

	// Commit
	if out, err := exec.Command("git", "commit", "-m", message).CombinedOutput(); err != nil {
		return fmt.Errorf("commit failed: %s", strings.TrimSpace(string(out)))
	}

	return nil
}

// GetLastCommitMessage returns the subject line of the last commit.
func GetLastCommitMessage() string {
	out, err := exec.Command("git", "log", "-1", "--format=%s").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// UndoLastCommit performs a soft reset, keeping changes staged.
func UndoLastCommit() error {
	out, err := exec.Command("git", "reset", "--soft", "HEAD~1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("undo failed: %s", strings.TrimSpace(string(out)))
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

// GetGitignoreEntries reads .gitignore and returns non-empty, non-comment lines.
func GetGitignoreEntries() []string {
	content, err := os.ReadFile(".gitignore")
	if err != nil {
		return nil
	}
	var entries []string
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			entries = append(entries, line)
		}
	}
	return entries
}

// RemoveFromGitignore removes the given entries from .gitignore by rewriting the file.
func RemoveFromGitignore(entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	remove := make(map[string]bool)
	for _, e := range entries {
		remove[e] = true
	}

	content, err := os.ReadFile(".gitignore")
	if err != nil {
		return nil // no .gitignore, nothing to remove
	}

	var kept []string
	for _, line := range strings.Split(string(content), "\n") {
		if !remove[strings.TrimSpace(line)] {
			kept = append(kept, line)
		}
	}

	// Trim trailing empty lines
	for len(kept) > 0 && strings.TrimSpace(kept[len(kept)-1]) == "" {
		kept = kept[:len(kept)-1]
	}

	result := strings.Join(kept, "\n")
	if result != "" {
		result += "\n"
	}
	return os.WriteFile(".gitignore", []byte(result), 0644)
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

// GetFileDiff returns the diff output for a single file.
// Routes by FileStatus to avoid guessing from empty diff output.
func GetFileDiff(path string, status types.FileStatus) (string, error) {
	switch status {
	case types.StatusUntracked:
		content, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read file: %w", err)
		}
		if isBinary(content) {
			return "", ErrBinaryFile
		}
		var b strings.Builder
		b.WriteString("(new file)\n")
		for _, line := range strings.Split(strings.TrimRight(string(content), "\n"), "\n") {
			b.WriteString("+ " + line + "\n")
		}
		return b.String(), nil

	case types.StatusDeleted:
		out, err := exec.Command("git", "show", "HEAD:"+path).CombinedOutput()
		if err != nil {
			// File may not be in HEAD — try index
			out, err = exec.Command("git", "diff", "--cached", "--", path).CombinedOutput()
			if err != nil {
				return "(deleted file)\n", nil
			}
			result := strings.TrimSpace(string(out))
			if result != "" {
				return result, nil
			}
			return "(deleted file)\n", nil
		}
		if isBinary(out) {
			return "", ErrBinaryFile
		}
		var b strings.Builder
		b.WriteString("(deleted file)\n")
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			b.WriteString("- " + line + "\n")
		}
		return b.String(), nil

	default:
		// Modified, Added, Renamed — try diff against HEAD, then cached
		out, err := exec.Command("git", "diff", "HEAD", "--", path).CombinedOutput()
		if err == nil {
			result := strings.TrimSpace(string(out))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		// Fallback: staged changes not yet committed
		out, err = exec.Command("git", "diff", "--cached", "--", path).CombinedOutput()
		if err == nil {
			result := strings.TrimSpace(string(out))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		// Fallback: unstaged changes only
		out, err = exec.Command("git", "diff", "--", path).CombinedOutput()
		if err == nil {
			result := strings.TrimSpace(string(out))
			if result != "" {
				if strings.Contains(result, "Binary files") {
					return "", ErrBinaryFile
				}
				return result, nil
			}
		}
		return "(no changes to display)\n", nil
	}
}

// ReadFileContent reads the raw content of a file in the working tree.
func ReadFileContent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	return string(content), nil
}

// WriteFileContent writes content to a file in the working tree.
func WriteFileContent(path string, content string) error {
	info, err := os.Stat(path)
	perm := os.FileMode(0644)
	if err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// isBinary checks if content contains null bytes (indicating binary data).
func isBinary(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}
