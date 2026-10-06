package locator

import "testing"

func TestHasPathSegments(t *testing.T) {
	cases := []struct {
		path     string
		segments string
		want     bool
		partial  bool
	}{
		{"/home/u/.cargo/bin/rg", ".cargo/bin", true, false},
		{"/home/u/.cargo/bin/rg", "cargo/bin", false, false}, // ".cargo" is its own component
		{"/home/u/go/bin/gopls", "go/bin", true, false},
		{"/home/u/.local/share/uv/tools/ruff/bin/ruff", ".local/share/uv/tools", true, false},
		{"/home/u/.cargo/bin/rg", ".car", false, false},
		{"/home/u/cargo/bin/rg", ".cargo/bin", false, false},
		{"/rg", ".cargo/bin", false, false},
		{"/home/u/Go/bin/gopls", "go/bin", false, false},
		{"", ".cargo/bin", false, false},
		{"/home/u/.local/lib/python3.12/site-packages", ".local/lib/python", true, true},
		{"/home/u/.local/lib/python3.12/site-packages", ".local/lib/python", false, false},
	}
	for _, tc := range cases {
		if got := hasPathSegments(tc.path, tc.segments, tc.partial); got != tc.want {
			t.Errorf("hasPathSegments(%q, %q) = %v, want %v", tc.path, tc.segments, got, tc.want)
		}
	}
}
