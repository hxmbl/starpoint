package context

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectContext(t *testing.T) {
	// Test with current directory (should detect this as a Go project)
	ctx, err := DetectContext(".")
	if err != nil {
		t.Fatalf("DetectContext failed: %v", err)
	}

	if ctx.WorkingDir == "" {
		t.Error("WorkingDir should not be empty")
	}

	// The current directory should be detected as a Go project (has go.mod)
	if ctx.ProjectType != ProjectTypeGo {
		t.Logf("Note: Current directory detected as %s (expected Go)", ctx.ProjectType)
	}
}

func TestDetectGitRepo(t *testing.T) {
	// Test with current directory (may or may not be a git repo)
	gitRoot, isGit := detectGitRepo(".")
	if isGit {
		if gitRoot == "" {
			t.Error("Git root should not be empty when in a git repo")
		}
	}

	// Test with a temporary directory that's not a git repo
	tmpDir := t.TempDir()
	gitRoot, isGit = detectGitRepo(tmpDir)
	if isGit {
		t.Error("Temporary directory should not be in a git repository")
	}
	if gitRoot != "" {
		t.Error("Git root should be empty when not in a git repo")
	}
}

func TestDetectProject(t *testing.T) {
	tests := []struct {
		name         string
		setupFunc    func(t *testing.T) string
		expectedType ProjectType
	}{
		{
			name: "Go project",
			setupFunc: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			expectedType: ProjectTypeGo,
		},
		{
			name: "Node project",
			setupFunc: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"test"}`), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			expectedType: ProjectTypeNode,
		},
		{
			name: "Rust project",
			setupFunc: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\nname = \"test\"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			expectedType: ProjectTypeRust,
		},
		{
			name: "Python project",
			setupFunc: func(t *testing.T) string {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\nname = \"test\"\n"), 0644); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			expectedType: ProjectTypePython,
		},
		{
			name: "Unknown project",
			setupFunc: func(t *testing.T) string {
				return t.TempDir()
			},
			expectedType: ProjectTypeUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := tt.setupFunc(t)
			root, ptype := detectProject(dir)
			if ptype != tt.expectedType {
				t.Errorf("detectProject() returned type %s, expected %s", ptype, tt.expectedType)
			}
			if root != dir {
				t.Errorf("detectProject() returned root %s, expected %s", root, dir)
			}
		})
	}
}

func TestDetectProjectNested(t *testing.T) {
	// Test that project detection searches upwards
	tmpDir := t.TempDir()

	// Create a nested directory structure
	nestedDir := filepath.Join(tmpDir, "subdir", "deep")

	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create go.mod in the root
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Detect from nested directory - should find the root
	root, ptype := detectProject(nestedDir)
	if ptype != ProjectTypeGo {
		t.Errorf("detectProject() from nested dir returned type %s, expected Go", ptype)
	}
	if root != tmpDir {
		t.Errorf("detectProject() from nested dir returned root %s, expected %s", root, tmpDir)
	}
}

func TestDetectProjectPriority(t *testing.T) {
	// Test that the first matching file is used (files are checked in order)
	tmpDir := t.TempDir()

	// Create multiple project files
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Should detect as Go (go.mod is checked first)
	root, ptype := detectProject(tmpDir)
	if ptype != ProjectTypeGo {
		t.Errorf("detectProject() with multiple files returned type %s, expected Go", ptype)
	}
	if root != tmpDir {
		t.Errorf("detectProject() with multiple files returned root %s, expected %s", root, tmpDir)
	}
}

func TestContextRelevantPackages(t *testing.T) {
	tests := []struct {
		name          string
		projectType   ProjectType
		expectedPkg   string
		shouldContain bool
	}{
		{"Go project contains golang", ProjectTypeGo, "golang", true},
		{"Go project contains go-tools", ProjectTypeGo, "go-tools", true},
		{"Node project contains node", ProjectTypeNode, "node", true},
		{"Node project contains npm", ProjectTypeNode, "npm", true},
		{"Rust project contains rust", ProjectTypeRust, "rust", true},
		{"Rust project contains cargo", ProjectTypeRust, "cargo", true},
		{"Python project contains python", ProjectTypePython, "python", true},
		{"Python project contains pip", ProjectTypePython, "pip", true},
		{"Unknown project empty", ProjectTypeUnknown, "anything", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{ProjectType: tt.projectType}
			pkgs := ctx.RelevantPackages()

			found := false
			for _, pkg := range pkgs {
				if pkg == tt.expectedPkg {
					found = true
					break
				}
			}

			if found != tt.shouldContain {
				if tt.shouldContain {
					t.Errorf("RelevantPackages() for %s should contain %s, got %v", tt.projectType, tt.expectedPkg, pkgs)
				} else {
					t.Errorf("RelevantPackages() for %s should not contain %s, got %v", tt.projectType, tt.expectedPkg, pkgs)
				}
			}
		})
	}
}

func TestContextInProjectRoot(t *testing.T) {
	tests := []struct {
		name           string
		workingDir     string
		projectRoot    string
		expectedInRoot bool
	}{
		{"In project root", "/project", "/project", true},
		{"Not in project root", "/project/subdir", "/project", false},
		{"Empty project root", "/project", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{
				WorkingDir:  tt.workingDir,
				ProjectRoot: tt.projectRoot,
			}
			if ctx.InProjectRoot() != tt.expectedInRoot {
				t.Errorf("InProjectRoot() returned %v, expected %v", ctx.InProjectRoot(), tt.expectedInRoot)
			}
		})
	}
}

func TestContextRelPath(t *testing.T) {
	// Use absolute paths for testing
	tmpDir, err := os.MkdirTemp("", "context-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name        string
		projectRoot string
		targetPath  string
		expected    string
	}{
		{
			name:        "Relative path inside project",
			projectRoot: tmpDir,
			targetPath:  filepath.Join(tmpDir, "subdir", "file.txt"),
			expected:    filepath.Join("subdir", "file.txt"),
		},
		{
			name:        "File in project root",
			projectRoot: tmpDir,
			targetPath:  filepath.Join(tmpDir, "file.txt"),
			expected:    "file.txt",
		},
		{
			name:        "Path outside project returns absolute",
			projectRoot: tmpDir,
			targetPath:  filepath.Join(tmpDir, "..", "other", "file.txt"),
			expected:    filepath.Join(tmpDir, "..", "other", "file.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &Context{ProjectRoot: tt.projectRoot}
			result := ctx.RelPath(tt.targetPath)
			if result != tt.expected {
				t.Errorf("RelPath() returned %s, expected %s", result, tt.expected)
			}
		})
	}
}

func TestDetectContextWithGit(t *testing.T) {
	// Test that DetectContext properly detects git repository
	ctx, err := DetectContext(".")
	if err != nil {
		t.Fatalf("DetectContext failed: %v", err)
	}

	// The test is running in a git repository
	if !ctx.IsGitRepo {
		t.Log("Note: Not in a git repository (this is expected in some test environments)")
	} else {
		if ctx.GitRoot == "" {
			t.Error("GitRoot should not be empty when IsGitRepo is true")
		}
	}
}
