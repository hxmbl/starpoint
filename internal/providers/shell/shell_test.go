package shell

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/provider"
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

func TestDiagnosticPrefersErrorLinesOverProgress(t *testing.T) {
	out := "Resolved 1 package in 1ms\nerror: Executable already exists: cowsay (use `--force` to overwrite)\nhint: a tool other than uv owns it\n"
	got := Diagnostic(out)
	if got == "Resolved 1 package in 1ms" || !strings.Contains(got, "Executable already exists: cowsay") {
		t.Fatalf("the error line must be reported, got %q", got)
	}
	if !strings.Contains(got, "hint:") {
		t.Errorf("the hint that explains the error should be kept, got %q", got)
	}
}

func TestDiagnosticFallsBackToTheLastLines(t *testing.T) {
	out := "Collecting cowsay\n  Downloading cowsay-6.1.tar.gz\nsomething went sideways\n"
	got := Diagnostic(out)
	if !strings.Contains(got, "something went sideways") {
		t.Errorf("unlabelled output should end with its last line, got %q", got)
	}
	if Diagnostic("  \n") != "" {
		t.Error("empty output has nothing to report")
	}
}

func TestDiagnosticIsBounded(t *testing.T) {
	long := "error: " + strings.Repeat("x", 2000)
	if got := Diagnostic(long); len(got) > maxDiagnosticLen {
		t.Errorf("diagnostic should be bounded, got %d bytes", len(got))
	}
}
