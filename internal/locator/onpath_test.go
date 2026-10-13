package locator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The audit found every file in a directory listed twice, each copy flagged as
// a duplicate of the other, because PATH named the directory twice (on Debian,
// /bin is a link to /usr/bin).
func TestOnPathReadsEachDirectoryOnce(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "usr", "bin")
	writeExe(t, filepath.Join(bin, "tool"))
	link := filepath.Join(root, "bin")
	if err := os.Symlink(bin, link); err != nil {
		t.Fatal(err)
	}
	l := &Locator{SearchPath: strings.Join([]string{bin, bin + "/", link, bin}, string(os.PathListSeparator))}
	found := l.OnPath()
	if len(found) != 1 {
		t.Fatalf("one file should be listed once, got %d: %+v", len(found), found)
	}
	if found[0].Duplicate {
		t.Error("a file is not a duplicate of itself")
	}
}

func TestOnPathFlagsOnlyRealShadowing(t *testing.T) {
	root := t.TempDir()
	first, second, third := filepath.Join(root, "a"), filepath.Join(root, "b"), filepath.Join(root, "c")
	writeExe(t, filepath.Join(first, "tool"))
	writeExe(t, filepath.Join(second, "tool"))
	// A link in a third directory to the first copy is the same program.
	if err := os.MkdirAll(third, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(first, "tool"), filepath.Join(third, "tool")); err != nil {
		t.Fatal(err)
	}
	l := &Locator{SearchPath: strings.Join([]string{first, second, third}, string(os.PathListSeparator))}
	found := l.OnPath()
	if len(found) != 3 {
		t.Fatalf("expected three entries, got %d", len(found))
	}
	dups := map[string]bool{}
	for _, f := range found {
		dups[filepath.Dir(f.Path)] = f.Duplicate
	}
	if dups[first] || !dups[second] || dups[third] {
		t.Errorf("only the different file later on PATH is shadowed: %v", dups)
	}
	sum := Summarize(found)
	if sum.Executables != 3 || sum.Directories != 3 || sum.Shadowed != 1 {
		t.Errorf("unexpected summary %+v", sum)
	}
}
