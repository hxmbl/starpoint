package context

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectType represents the type of project detected.
type ProjectType string

const (
	ProjectTypeUnknown ProjectType = "unknown"
	ProjectTypeGo      ProjectType = "go"
	ProjectTypeNode    ProjectType = "node"
	ProjectTypeRust    ProjectType = "rust"
	ProjectTypePython  ProjectType = "python"
)

// Context represents the execution environment where Starpoint is being run.
type Context struct {
	// WorkingDir is the current working directory.
	WorkingDir string
	// ProjectRoot is the detected root directory of the project.
	ProjectRoot string
	// ProjectType is the detected type of project.
	ProjectType ProjectType
	// IsGitRepo indicates whether the working directory is in a git repository.
	IsGitRepo bool
	// GitRoot is the root directory of the git repository, if IsGitRepo is true.
	GitRoot string
}

// DetectContext auto-detects the current context from the given working directory.
func DetectContext(cwd string) (*Context, error) {
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}

	ctx := &Context{
		WorkingDir: absCwd,
	}

	// Detect git repository
	gitRoot, isGit := detectGitRepo(absCwd)
	ctx.IsGitRepo = isGit
	ctx.GitRoot = gitRoot

	// Detect project root and type
	projectRoot, projectType := detectProject(absCwd)
	ctx.ProjectRoot = projectRoot
	ctx.ProjectType = projectType

	return ctx, nil
}

// detectGitRepo checks if the given directory is in a git repository.
// Returns the git root directory and a boolean indicating if it's a git repo.
func detectGitRepo(dir string) (string, bool) {
	for {
		gitDir := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			return dir, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the root of the filesystem
			return "", false
		}
		dir = parent
	}
}

// detectProject detects the project type and root directory.
// It searches upwards from the given directory for known project markers.
func detectProject(dir string) (string, ProjectType) {
	originalDir := dir
	for {
		// Check for project markers in current directory
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, ProjectTypeGo
		}
		if info, err := os.Stat(filepath.Join(dir, "package.json")); err == nil && !info.IsDir() {
			return dir, ProjectTypeNode
		}
		if info, err := os.Stat(filepath.Join(dir, "Cargo.toml")); err == nil && !info.IsDir() {
			return dir, ProjectTypeRust
		}
		if info, err := os.Stat(filepath.Join(dir, "pyproject.toml")); err == nil && !info.IsDir() {
			return dir, ProjectTypePython
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the root of the filesystem, no project found
			return originalDir, ProjectTypeUnknown
		}
		dir = parent
	}
}

// RelevantPackages returns which packages are relevant for the current context.
// This is a placeholder that will be integrated with Starpoint's config system.
// For now, it returns package names based on the detected project type.
func (c *Context) RelevantPackages() []string {
	switch c.ProjectType {
	case ProjectTypeGo:
		return []string{"golang", "go-tools"}
	case ProjectTypeNode:
		return []string{"node", "npm", "yarn", "pnpm"}
	case ProjectTypeRust:
		return []string{"rust", "cargo"}
	case ProjectTypePython:
		return []string{"python", "pip", "poetry"}
	default:
		return []string{}
	}
}

// InProjectRoot returns true if the current working directory is the project root.
func (c *Context) InProjectRoot() bool {
	return c.WorkingDir == c.ProjectRoot
}

// RelPath returns the relative path from the project root to the given path.
// If the path is outside the project root, returns the absolute path.
func (c *Context) RelPath(path string) string {
	if c.ProjectRoot == "" {
		return path
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return path
	}

	relPath, err := filepath.Rel(c.ProjectRoot, absPath)
	if err != nil {
		return path
	}

	// Check if the path is actually inside the project root
	if strings.HasPrefix(relPath, "..") {
		return absPath
	}

	return relPath
}
