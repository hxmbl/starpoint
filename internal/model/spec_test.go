package model

import "testing"

func TestParseSpec(t *testing.T) {
	cases := []struct {
		in      string
		name    string
		version string
		why     string
	}{
		{"fastfetch", "fastfetch", "", "a bare name has no version"},
		{"fastfetch@1.2.3", "fastfetch", "1.2.3", "an inline version is split off"},
		{"fastfetch@2.67.1_1", "fastfetch", "2.67.1_1", "a Homebrew revision suffix stays intact"},
		{"@types/node", "@types/node", "", "an npm scope starts with @ and is not a version"},
		{"@types/node@5.0.0", "@types/node", "5.0.0", "scope plus version splits at the last @"},
		{"golang.org/x/tools/gopls@v0.16.1", "golang.org/x/tools/gopls", "v0.16.1",
			"a module path with a v-prefixed version"},
		{"@scope/pkg@latest", "@scope/pkg", "latest", "a dist-tag is a version as far as parsing goes"},
		{"@", "@", "", "a lone @ is not a spec"},
		{"name@", "name@", "", "a trailing @ with no version is not a version"},
		{"@1.2.3", "@1.2.3", "", "a leading @ with no name is a scope-looking token, not a version"},
		{"", "", "", "an empty spec stays empty"},
	}
	for _, tc := range cases {
		got := ParseSpec(tc.in)
		if got.Name != tc.name || got.Version != tc.version {
			t.Errorf("ParseSpec(%q) = {%q, %q}, want {%q, %q} — %s",
				tc.in, got.Name, got.Version, tc.name, tc.version, tc.why)
		}
	}
}

func TestHasExplicitVersion(t *testing.T) {
	if HasExplicitVersion("fastfetch") {
		t.Error("a bare name carries no version")
	}
	if !HasExplicitVersion("fastfetch@1.2.3") {
		t.Error("an inline version must be detected")
	}
	if HasExplicitVersion("@types/node") {
		t.Error("an npm scope must not read as a version")
	}
}
