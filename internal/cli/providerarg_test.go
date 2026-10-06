package cli

import (
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/uv"
)

func TestProviderMayBeGivenAtTheEnd(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	out := h.mustRun("install", "ruff", uv.Name)
	if !strings.Contains(out, uv.Name) {
		t.Errorf("the provider should be used:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("expected one install into uv, got %d", len(h.uv.InstallCalls))
	}
	if len(h.brew.InstallCalls) != 0 {
		t.Error("the wrong provider must not be asked")
	}
}

func TestProviderMayBeGivenAtTheStart(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", uv.Name, "ruff")
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("expected one install into uv, got %d", len(h.uv.InstallCalls))
	}
}

// The leading form is the one that used to be wrong: a bare token became the
// package name, so `install npm fastfetch` tried to install a package called npm.
func TestLeadingProviderIsNotTakenAsThePackageName(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", uv.Name, "ruff")
	for _, call := range h.uv.InstallCalls {
		if call.Package.Name == uv.Name {
			t.Errorf("the provider name was treated as a package: %+v", call)
		}
	}
}

// A single token is a package name even when it happens to be spelled like a
// provider. `starpoint install brew` must install the software called brew.
func TestASingleProviderShapedTokenIsAPackageName(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", brew.Name)
	if len(h.brew.InstallCalls) != 1 {
		t.Fatalf("expected the package called brew to be installed, got %+v", h.brew.InstallCalls)
	}
	if got := h.brew.InstallCalls[0].Package.Name; got != brew.Name {
		t.Errorf("the token should have been the package name, got %q", got)
	}
}

// A version in the name must survive, or npm would be asked to install the
// wrong thing.
func TestInlineVersionIsKeptForTheProvider(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", "ruff@0.6.0", uv.Name)
	if len(h.uv.InstallCalls) != 1 {
		t.Fatalf("expected one install, got %d", len(h.uv.InstallCalls))
	}
	got := h.uv.InstallCalls[0]
	if got.Package.Name != "ruff" {
		t.Errorf("the name should be split from the version, got %q", got.Package.Name)
	}
	if got.Package.Version != "0.6.0" {
		t.Errorf("the version should be carried through, got %q", got.Package.Version)
	}
}

// An explicit --version still wins over the inline form, because it is the more
// specific instruction.
func TestExplicitVersionBeatsTheInlineOne(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", "ruff@0.6.0", uv.Name, "--version", "0.7.0")
	got := h.uv.InstallCalls[0]
	if got.Package.Version != "0.7.0" {
		t.Errorf("the explicit flag should win, got %q", got.Package.Version)
	}
}

func TestAnInlineVersionIsNeverReadAsAProvider(t *testing.T) {
	e := &Env{Registry: newHarness(t, "schema = 1\n").env.Registry}
	if e.isProviderName("ruff@1.2.3") {
		t.Error("a spec carrying a version is a package, not a provider")
	}
	if e.isProviderName(uv.Name + "@10") {
		t.Error("a provider name with a version is still not a bare provider name")
	}
	if !e.isProviderName(uv.Name) {
		t.Error("a bare provider name must be recognised")
	}
}

func TestTwoProvidersInOneCommandIsAnError(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	code, _, errOut := h.run("install", uv.Name, "ruff", brew.Name)
	if code == 0 {
		t.Fatal("naming two providers must not be guessed at")
	}
	if !strings.Contains(errOut, "both providers") {
		t.Errorf("the conflict should be explained: %s", errOut)
	}
	if len(h.uv.InstallCalls) != 0 || len(h.brew.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
}

func TestNamingTheProviderTwiceIsAnError(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	code, _, errOut := h.run("--provider", uv.Name, "install", uv.Name, "ruff")
	if code == 0 {
		t.Fatal("the same provider twice in different spellings must not be accepted")
	}
	if !strings.Contains(errOut, "already given") {
		t.Errorf("the conflict should be explained: %s", errOut)
	}
}

func TestTheProviderFlagStillWorks(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("--provider", uv.Name, "install", "ruff")
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("the flag form must keep working, got %d installs", len(h.uv.InstallCalls))
	}
}

func TestOptionsAreNotMistakenForPackageNames(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	// `--version brew` is a version value, not a provider name.
	h.mustRun("install", "ruff", "--version", "1.0.0")
	if len(h.uv.InstallCalls) != 1 || len(h.brew.InstallCalls) != 0 {
		t.Errorf("only uv should have been asked: uv=%d brew=%d",
			len(h.uv.InstallCalls), len(h.brew.InstallCalls))
	}
}

func TestBareTokens(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"ruff"}, []string{"ruff"}},
		{[]string{"--dry-run", "ruff"}, []string{"ruff"}},
		{[]string{"--version", "1.2.3", "ruff"}, []string{"ruff"}},
		{[]string{"-p", "uv", "ruff", "brew"}, []string{"ruff", "brew"}},
		{[]string{"ruff", "--yes"}, []string{"ruff"}},
		{nil, nil},
	}
	for _, tc := range cases {
		got := bareTokens(tc.args)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("bareTokens(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestRemoveBareTokenKeepsEverythingElseInPlace(t *testing.T) {
	// bareTokens sees ["ruff", brew.Name]: the value of --version is not a bare
	// token, so brew.Name is the second one.
	args := []string{"--dry-run", "ruff", "--version", "1.0", brew.Name}
	if bare := bareTokens(args); len(bare) != 2 || bare[1] != brew.Name {
		t.Fatalf("bareTokens sees %v, which the test does not assume", bare)
	}
	got := removeBareToken(args, brew.Name, 1)
	want := []string{"--dry-run", "ruff", "--version", "1.0"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("removeBareToken = %v, want %v", got, want)
	}
	// The original slice must not have been modified.
	if len(args) != 5 {
		t.Error("the caller's arguments were modified")
	}
}
