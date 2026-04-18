package git

import (
	"os"
	"path/filepath"
	"strings"
)

// GitignoreTemplate is a named .gitignore preset the user can pick.
type GitignoreTemplate struct {
	Name    string
	Content string
}

// GitignoreTemplates returns the full catalog of available templates. Order
// matters for the UI: the auto-detected template is promoted to the top by
// DetectGitignoreTemplate.
func GitignoreTemplates() []GitignoreTemplate {
	return []GitignoreTemplate{
		{Name: "Go", Content: gitignoreGo},
		{Name: "Node", Content: gitignoreNode},
		{Name: "Python", Content: gitignorePython},
		{Name: "Rust", Content: gitignoreRust},
		{Name: "Generic", Content: gitignoreGeneric},
		{Name: "None (skip)", Content: ""},
	}
}

// DetectGitignoreTemplate returns the name of the template that best matches
// the current directory based on marker files (go.mod, package.json, etc.).
// Returns "Generic" when no signal is found.
func DetectGitignoreTemplate() string {
	markers := map[string]string{
		"go.mod":           "Go",
		"package.json":     "Node",
		"pyproject.toml":   "Python",
		"requirements.txt": "Python",
		"setup.py":         "Python",
		"Cargo.toml":       "Rust",
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		return "Generic"
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name, ok := markers[e.Name()]; ok {
			return name
		}
	}
	return "Generic"
}

// WriteGitignoreTemplate writes the given template content to .gitignore.
// If .gitignore already exists, the template is appended (deduplicated per
// line) so we don't clobber user edits.
func WriteGitignoreTemplate(content string) error {
	if content == "" {
		return nil
	}
	path := ".gitignore"
	existing, _ := os.ReadFile(path)
	seen := map[string]bool{}
	for _, line := range strings.Split(string(existing), "\n") {
		seen[strings.TrimSpace(line)] = true
	}
	var appended []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			appended = append(appended, line)
			continue
		}
		if !seen[trimmed] {
			appended = append(appended, line)
			seen[trimmed] = true
		}
	}

	var out strings.Builder
	if len(existing) > 0 {
		out.Write(existing)
		if existing[len(existing)-1] != '\n' {
			out.WriteString("\n")
		}
		out.WriteString("\n")
	}
	out.WriteString(strings.Join(appended, "\n"))
	if !strings.HasSuffix(out.String(), "\n") {
		out.WriteString("\n")
	}
	return os.WriteFile(path, []byte(out.String()), 0644)
}

// CurrentDirName returns the base name of the current working directory,
// used as the default when asking for a repo name.
func CurrentDirName() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}

// GHAuthLoginCmd returns the command string used for the gh auth shortcut.
// Exposed so the TUI can shell out via tea.ExecProcess.
func GHAuthLoginCmd() (string, []string) {
	return "gh", []string{"auth", "login", "--web"}
}

const gitignoreGo = `# Binaries for programs and plugins
*.exe
*.exe~
*.dll
*.so
*.dylib

# Test binary, built with 'go test -c'
*.test

# Output of the go coverage tool
*.out

# Dependency directories
vendor/

# Go workspace file
go.work
go.work.sum

# Env
.env
`

const gitignoreNode = `# Dependencies
node_modules/
jspm_packages/

# Build output
dist/
build/
out/
.next/
.nuxt/

# Logs
logs
*.log
npm-debug.log*
yarn-debug.log*
yarn-error.log*
pnpm-debug.log*

# Runtime data
pids
*.pid
*.seed

# Env
.env
.env.local
.env.*.local

# Caches
.cache/
.parcel-cache/
.eslintcache
`

const gitignorePython = `# Byte-compiled / optimized / DLL files
__pycache__/
*.py[cod]
*$py.class

# C extensions
*.so

# Distribution / packaging
build/
dist/
*.egg-info/
*.egg

# Virtual environments
.venv/
venv/
env/
ENV/

# Caches
.pytest_cache/
.mypy_cache/
.ruff_cache/
.tox/
.coverage
htmlcov/

# Env
.env
`

const gitignoreRust = `# Build output
target/
**/*.rs.bk

# Cargo lockfile (keep for binaries, ignore for libraries — edit as needed)
# Cargo.lock

# Env
.env
`

const gitignoreGeneric = `# OS files
.DS_Store
Thumbs.db
desktop.ini

# Editors
.vscode/
.idea/
*.swp
*.swo
*~

# Env / secrets
.env
.env.local
*.pem
*.key

# Logs
*.log
`
