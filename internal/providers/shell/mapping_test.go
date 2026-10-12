package shell

import "testing"

// The executable-to-package mapping is many-to-one: both "python3" and
// "python" name the python3 package. Scanning the map directly returned a
// different executable on different runs, because Go randomises map iteration,
// which made Verify fail intermittently. The lookup has to be stable.
func TestExecutableForPackageIsDeterministic(t *testing.T) {
	mapping := map[string]string{
		"python3":       "python3",
		"python":        "python3",
		"git":           "git",
		"java":          "default-jdk",
		"javac":         "default-jdk",
		"ruby":          "ruby",
		"gem":           "rubygems",
		"nvim":          "neovim",
		"neovim":        "neovim",
		"cargo":         "rust",
		"rustc":         "rust",
		"shellcheckall": "not-a-real-package",
	}

	// Repeat enough times that a randomised scan would be caught.
	for i := 0; i < 200; i++ {
		for _, pkg := range []string{"python3", "default-jdk", "git", "neovim", "rust"} {
			first, ok := ExecutableForPackage(mapping, pkg)
			if !ok {
				t.Fatalf("no executable resolved for %q", pkg)
			}
			if mapping[first] != pkg {
				t.Fatalf("executable %q does not belong to package %q", first, pkg)
			}
			for j := 0; j < 5; j++ {
				again, _ := ExecutableForPackage(mapping, pkg)
				if again != first {
					t.Fatalf("unstable result for %q: %q then %q", pkg, first, again)
				}
			}
		}
	}
}

// An executable named exactly after its package wins the tie. Debian and Fedora
// ship /usr/bin/python3 for the python3 package, while /usr/bin/python may be a
// virtual provide with no executable behind it, so picking "python" here would
// verify against a file that does not exist.
func TestExecutableForPackagePrefersExactNameMatch(t *testing.T) {
	cases := []struct {
		pkg  string
		want string
	}{
		{"python3", "python3"}, // longer name, but exact
		{"neovim", "neovim"},
		{"git", "git"},
	}
	for _, c := range cases {
		mapping := map[string]string{
			"python3": "python3", "python": "python3",
			"neovim": "neovim", "nvim": "neovim",
			"git": "git",
		}
		got, ok := ExecutableForPackage(mapping, c.pkg)
		if !ok {
			t.Fatalf("expected a match for %q", c.pkg)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.pkg, got, c.want)
		}
	}
}

// With no exact-name match available, the shortest name wins so the result
// still never depends on map order.
func TestExecutableForPackageFallsBackToShortestName(t *testing.T) {
	mapping := map[string]string{"java": "default-jdk", "javac": "default-jdk"}
	got, ok := ExecutableForPackage(mapping, "default-jdk")
	if !ok {
		t.Fatal("expected a match")
	}
	if got != "java" {
		t.Errorf("got %q, want %q", got, "java")
	}
}

func TestExecutableForPackageNoMatch(t *testing.T) {
	mapping := map[string]string{"git": "git"}
	if got, ok := ExecutableForPackage(mapping, "nonesuch"); ok || got != "" {
		t.Errorf("got %q/%v, want no match", got, ok)
	}
	if got, ok := ExecutableForPackage(nil, "git"); ok || got != "" {
		t.Errorf("empty map: got %q/%v, want no match", got, ok)
	}
}
