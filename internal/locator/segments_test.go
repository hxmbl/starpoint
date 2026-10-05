package locator

import "testing"

func TestHasPathSegments(t *testing.T) {
	cases := []struct {
		path     string
		segments string
		want     bool
	}{
		{"/home/u/.cargo/bin/rg", ".cargo/bin", true},
		{"/home/u/.cargo/bin/rg", "cargo/bin", false}, // ".cargo" is its own component
		{"/home/u/go/bin/gopls", "go/bin", true},
		{"/home/u/.local/share/uv/tools/ruff/bin/ruff", ".local/share/uv/tools", true},
		{"/home/u/.cargo/bin/rg", ".car", false},
		{"/home/u/cargo/bin/rg", ".cargo/bin", false},
		{"/rg", ".cargo/bin", false},
		{"/home/u/Go/bin/gopls", "go/bin", false},
		{"", ".cargo/bin", false},
	}
	for _, tc := range cases {
		if got := hasPathSegments(tc.path, tc.segments); got != tc.want {
			t.Errorf("hasPathSegments(%q, %q) = %v, want %v", tc.path, tc.segments, got, tc.want)
		}
	}
}
