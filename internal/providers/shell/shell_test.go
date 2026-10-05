package shell

import (
	"testing"

	"github.com/starpoint/starpoint/internal/provider"
)

func TestVersionFromArgs(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"Homebrew", "4.3.5"}, "4.3.5"},
		{[]string{"cargo", "1.83.0", "(a8a5f3d5b", "2024-11-20)"}, "1.83.0"},
		{[]string{"git", "version", "2.43.0"}, "2.43.0"},
		{[]string{"uv", "0.4.20"}, "0.4.20"},
		{[]string{"no", "versions", "here"}, ""},
		{[]string{}, ""},
		{nil, ""},
	}
	for _, tc := range cases {
		if got := VersionFromArgs(tc.in...); got != tc.want {
			t.Errorf("VersionFromArgs(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionFromArgsSkipsNonVersionTokens(t *testing.T) {
	// A token with letters is not a version, however many digits it contains.
	if got := VersionFromArgs("go", "version", "go1.23.4"); got != "" {
		t.Errorf("a token containing letters must not be taken as a version, got %q", got)
	}
}

func TestFieldsFromNeverPanicsOnShortInput(t *testing.T) {
	if got := FieldsFrom("short", 2); got != nil {
		t.Errorf("expected nil for insufficient fields, got %v", got)
	}
	if got := FieldsFrom("a b c", 2); len(got) != 1 || got[0] != "c" {
		t.Errorf("FieldsFrom = %v", got)
	}
	if got := FieldsFrom("", 1); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n  hello  \nworld"); got != "hello" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := FirstLine("   \n\t\n"); got != "" {
		t.Errorf("FirstLine on blanks = %q", got)
	}
}

func TestFields(t *testing.T) {
	if got := Fields("  a  b\tc "); len(got) != 3 {
		t.Errorf("Fields = %v", got)
	}
	if got := Fields("   "); len(got) != 0 {
		t.Errorf("Fields on blanks = %v", got)
	}
}

func TestErrorBuildersCarryProviderAndKind(t *testing.T) {
	notFound := NotFound("brew", "install", "no such formula")
	pe, ok := provider.AsProviderError(notFound)
	if !ok {
		t.Fatal("NotFound must build a typed provider error")
	}
	if pe.Kind != provider.ErrNotFound {
		t.Errorf("kind = %q", pe.Kind)
	}
	if pe.Provider != "brew" || pe.Op != "install" {
		t.Errorf("context lost: %+v", pe)
	}

	unsupported := Unsupported("system", "remove", "read-only")
	if u, _ := provider.AsProviderError(unsupported); u.Kind != provider.ErrUnsupported {
		t.Errorf("kind = %q", u.Kind)
	}

	failure := Failure("npm", "install", "weird", nil)
	if f, _ := provider.AsProviderError(failure); f.Kind != provider.ErrFailed {
		t.Errorf("kind = %q", f.Kind)
	}
	network := Err("curl", "", provider.ErrNetwork, "unreachable", nil)
	if n, _ := provider.AsProviderError(network); n.Kind != provider.ErrNetwork {
		t.Errorf("kind = %q", n.Kind)
	}
}
