package rustup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// Fixtures captured from rustup 1.29.1 on x86_64-apple-darwin.
const (
	listOne = "stable-x86_64-apple-darwin (active, default) /Users/u/.rustup/toolchains/stable-x86_64-apple-darwin\n"

	listTwo = "stable-x86_64-apple-darwin (active, default) /Users/u/.rustup/toolchains/stable-x86_64-apple-darwin\n" +
		"nightly-x86_64-apple-darwin /Users/u/.rustup/toolchains/nightly-x86_64-apple-darwin\n"

	listPlain = "stable-x86_64-apple-darwin (active, default)\n1.99.0-x86_64-apple-darwin\n"

	showOut = `Default host: x86_64-apple-darwin
rustup home:  /Users/u/.rustup
`

	rustcVersionOut = "rustc 1.99.0 (b940084d7 2026-09-28)\n"
	rustupVersion   = "rustup 1.29.1 (2026-08-13)\ninfo: This is the version for the rustup toolchain manager, not the rustc compiler.\n"

	triple = "x86_64-apple-darwin"
)

const (
	tcStable  = "stable-x86_64-apple-darwin"
	tcNightly = "nightly-x86_64-apple-darwin"
)

// newProvider builds a provider over a scripted runner.
func newProvider(t *testing.T, listOutput string) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("rustup")
	p := New(r, "/Users/u")
	if listOutput == "" {
		listOutput = listOne
	}
	r.Set(listOutput, "rustup", "toolchain", "list", "-v")
	r.Set(listOutput, "rustup", "toolchain", "list")
	r.Set(showOut, "rustup", "show")
	r.Set(rustupVersion, "rustup", "--version")
	r.Set(rustcVersionOut, "rustup", "run", tcStable, "rustc", "--version")
	r.Set(rustcVersionOut, "rustup", "run", tcNightly, "rustc", "--version")
	r.Set("/Users/u/.rustup/toolchains/"+tcStable+"/bin/rustc\n",
		"rustup", "which", "--toolchain", tcStable, "rustc")
	r.Set("/Users/u/.rustup/toolchains/"+tcStable+"/bin/cargo\n",
		"rustup", "which", "--toolchain", tcStable, "cargo")
	r.Set("/Users/u/.rustup/toolchains/"+tcStable+"/bin/rustfmt\n",
		"rustup", "which", "--toolchain", tcStable, "rustfmt")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t, "")
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("rustup should be available: %s", h.Reason)
	}
	if h.Version != "1.29.1" {
		t.Errorf("version parsed as %q; the informational paragraph must not confuse it", h.Version)
	}
	if h.Binary == "" {
		t.Error("the resolved binary path should be reported")
	}
	if h.Kind != provider.KindRust {
		t.Errorf("kind = %q", h.Kind)
	}
	if !h.Capabilities.Channels {
		t.Error("stable/beta/nightly are channels and should be advertised")
	}
	if !h.Capabilities.PinVersion {
		t.Error("a concrete toolchain such as 1.99.0 is an exact pin")
	}
}

func TestHealthWhenAbsent(t *testing.T) {
	p := New(testsupport.NewFakeRunner(), "")
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("rustup is not on the fake PATH and must be reported unavailable")
	}
	if h.Reason == "" {
		t.Error("an unavailable provider must say why")
	}
}

func TestAbsentRustupOwnsNothingAndRunsNothing(t *testing.T) {
	r := testsupport.NewFakeRunner() // no rustup on PATH
	p := New(r, "/Users/u")

	if insts, err := p.Discover(context.Background()); err != nil || len(insts) != 0 {
		t.Errorf("an absent provider owns nothing, got (%+v, %v)", insts, err)
	}
	if inst, err := p.Installed(context.Background(), model.Package{Name: "rustc"}); err != nil || inst != nil {
		t.Errorf("Installed must be (nil, nil) when rustup is absent, got (%+v, %v)", inst, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("no command should run when rustup is absent, got %v", r.Calls)
	}
}

func TestParseToolchainList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []toolchain
	}{
		{
			name: "one default toolchain with a path",
			in:   listOne,
			want: []toolchain{{Full: tcStable, Name: "stable", Path: "/Users/u/.rustup/toolchains/" + tcStable, Active: true, Default: true}},
		},
		{
			name: "several toolchains, only one flagged",
			in:   listTwo,
			want: []toolchain{
				{Full: tcStable, Name: "stable", Path: "/Users/u/.rustup/toolchains/" + tcStable, Active: true, Default: true},
				{Full: tcNightly, Name: "nightly", Path: "/Users/u/.rustup/toolchains/" + tcNightly},
			},
		},
		{
			name: "plain output without paths",
			in:   listPlain,
			want: []toolchain{
				{Full: tcStable, Name: "stable", Active: true, Default: true},
				{Full: "1.99.0-x86_64-apple-darwin", Name: "1.99.0"},
			},
		},
		{
			name: "unknown triple keeps the full name",
			in:   listOne,
			want: []toolchain{{Full: tcStable, Name: tcStable, Path: "/Users/u/.rustup/toolchains/" + tcStable, Active: true, Default: true}},
		},
		{
			name: "empty output",
			in:   "\n\n",
			want: nil,
		},
		{
			name: "a line that is only flags is skipped",
			in:   "(default)\n" + listOne,
			want: []toolchain{{Full: tcStable, Name: "stable", Path: "/Users/u/.rustup/toolchains/" + tcStable, Active: true, Default: true}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tripleArg := triple
			if tc.name == "unknown triple keeps the full name" {
				tripleArg = ""
			}
			got := parseToolchainList(tc.in, tripleArg)
			if len(got) != len(tc.want) {
				t.Fatalf("parsed %d toolchains, want %d: %+v", len(got), len(tc.want), got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("toolchain %d = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestConcreteVersion(t *testing.T) {
	cases := map[string]string{
		"1.99.0":                     "1.99.0",
		"1.99.0-x86_64-apple-darwin": "1.99.0",
		"stable":                     "",
		"beta":                       "",
		"nightly":                    "",
		"nightly-2024-01-01":         "", // a dated nightly is a channel, not a release
		"rustc":                      "",
		"":                           "",
	}
	for in, want := range cases {
		if got := ConcreteVersion(in); got != want {
			t.Errorf("ConcreteVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsToolchainName(t *testing.T) {
	toolchains := []string{"stable", "beta", "nightly", "1.99.0", "1.99", "0"}
	notToolchains := []string{"rustc", "cargo", "rustup", "rustfmt", "ripgrep", "nightly-2024-01-01",
		"", "golang.org/x/tools/gopls"}
	for _, name := range toolchains {
		if !IsToolchainName(name) {
			t.Errorf("%q is a toolchain rustup can install", name)
		}
	}
	for _, name := range notToolchains {
		if IsToolchainName(name) {
			t.Errorf("%q is not a toolchain", name)
		}
	}
}

func TestIsToolchainBinary(t *testing.T) {
	for _, name := range []string{"rustc", "cargo", "rustfmt", "rust-analyzer", "cargo-clippy"} {
		if !IsToolchainBinary(name) {
			t.Errorf("%q is provided by a rustup toolchain", name)
		}
	}
	for _, name := range []string{"ripgrep", "rustup", "stable", ""} {
		if IsToolchainBinary(name) {
			t.Errorf("%q is not a toolchain binary", name)
		}
	}
}

func TestInstallTarget(t *testing.T) {
	cases := []struct {
		pkg  model.Package
		want string
		why  string
	}{
		{model.Package{Name: "rustc"}, DefaultChannel, "a toolchain binary means install Rust"},
		{model.Package{Name: "cargo"}, DefaultChannel, "a toolchain binary means install Rust"},
		{model.Package{Name: "rustc", Channel: "nightly"}, "nightly", "a channel is used when there is no version"},
		{model.Package{Name: "1.99.0"}, "1.99.0", "a concrete toolchain is itself the target"},
		{model.Package{Name: "nightly"}, "nightly", "a channel entry is its own target"},
		{model.Package{Name: "stable"}, "stable", "a channel entry is its own target"},
		{model.Package{Name: "nightly-2024-01-01"}, DefaultChannel,
			"a dated nightly is not something rustup installs by bare name, so the default channel is used"},
		{model.Package{Name: "stable", Version: "1.99.0"}, "1.99.0", "an exact pin must not drift to a channel"},
		{model.Package{Name: "stable", Version: "==1.99.0"}, "1.99.0", "a requirements-style prefix is tolerated"},
		{model.Package{Name: "stable", Version: "=1.99.0"}, "1.99.0", "a single equals is tolerated"},
	}
	for _, tc := range cases {
		if got := InstallTarget(tc.pkg); got != tc.want {
			t.Errorf("InstallTarget(%+v) = %q, want %q (%s)", tc.pkg, got, tc.want, tc.why)
		}
	}
}

func TestDiscoverReportsInstalledToolchains(t *testing.T) {
	p, _ := newProvider(t, listTwo)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 toolchains, got %+v", insts)
	}
	if insts[0].Name != "stable" || insts[0].Version != "" {
		t.Errorf("stable is a channel and must not be given a version: %+v", insts[0])
	}
	if insts[0].Path != "/Users/u/.rustup/toolchains/"+tcStable {
		t.Errorf("the toolchain directory should be reported: %+v", insts[0])
	}
	if insts[1].Name != "nightly" {
		t.Errorf("unexpected second toolchain: %+v", insts[1])
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
		if i.Scope != model.ScopeUser {
			t.Errorf("rustup is per-user, got %q", i.Scope)
		}
	}
}

func TestDiscoverReportsConcreteToolchainVersion(t *testing.T) {
	p, _ := newProvider(t, "1.99.0-x86_64-apple-darwin\n")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 1 || insts[0].Version != "1.99.0" {
		t.Errorf("a concrete toolchain carries its own version: %+v", insts)
	}
}

func TestInstalledResolvesTheDefaultToolchain(t *testing.T) {
	p, _ := newProvider(t, "")
	inst, err := p.Installed(context.Background(), model.Package{Name: "stable"})
	if err != nil {
		t.Fatalf("installed: %v", err)
	}
	if inst == nil {
		t.Fatal("stable should be found")
	}
	if inst.Name != "stable" || inst.Provider != Name {
		t.Errorf("unexpected entry: %+v", inst)
	}
	if inst.Version != "1.99.0" {
		t.Errorf("the toolchain's compiler release should be reported, got %q", inst.Version)
	}
	if inst.Path != "/Users/u/.rustup/toolchains/"+tcStable {
		t.Errorf("path wrong: %q", inst.Path)
	}
}

func TestInstalledMatchesFullToolchainName(t *testing.T) {
	p, _ := newProvider(t, "")
	// rustup prints the triple; a configuration entry may use either form.
	inst, err := p.Installed(context.Background(), model.Package{Name: tcStable})
	if err != nil || inst == nil {
		t.Fatalf("the full name should resolve too, got (%+v, %v)", inst, err)
	}
	if inst.Name != "stable" {
		t.Errorf("the canonical name should be reported, got %q", inst.Name)
	}
}

func TestInstalledResolvesToolchainBinary(t *testing.T) {
	p, r := newProvider(t, "")
	inst, err := p.Installed(context.Background(), model.Package{Name: "rustc"})
	if err != nil {
		t.Fatalf("installed: %v", err)
	}
	if inst == nil {
		t.Fatal("rustc should be resolved through the default toolchain")
	}
	if inst.Provider != Name {
		t.Errorf("rustc belongs to rustup, got %q", inst.Provider)
	}
	if inst.Version != "1.99.0" {
		t.Errorf("the compiler version should be reported, got %q", inst.Version)
	}
	if inst.Path != "/Users/u/.rustup/toolchains/"+tcStable+"/bin/rustc" {
		t.Errorf("rustup which should supply the path, got %q", inst.Path)
	}
	// The toolchain must be the one rustup said was installed.
	if !r.Ran("rustup", "which", "--toolchain", tcStable, "rustc") {
		t.Errorf("path should be resolved with the full toolchain name, calls: %v", r.Calls)
	}
}

func TestToolchainBinaryIsNotResolvedThroughANonDefaultToolchain(t *testing.T) {
	p, r := newProvider(t, "nightly-x86_64-apple-darwin /Users/u/.rustup/toolchains/"+tcNightly+"\n"+
		"1.99.0-x86_64-apple-darwin /Users/u/.rustup/toolchains/1.99.0\n")
	if _, err := p.Installed(context.Background(), model.Package{Name: "rustc"}); err != nil {
		t.Fatal(err)
	}
	if r.Ran("rustup", "which", "--toolchain", "1.99.0-x86_64-apple-darwin", "rustc") {
		t.Errorf("a binary must resolve through the default toolchain, calls: %v", r.Calls)
	}
}

func TestInstalledFallsBackToTheActiveToolchain(t *testing.T) {
	p, r := newProvider(t, "nightly-x86_64-apple-darwin (active) /Users/u/.rustup/toolchains/"+tcNightly+"\n")
	inst, err := p.Installed(context.Background(), model.Package{Name: "cargo"})
	if err != nil || inst == nil {
		t.Fatalf("the active toolchain should answer, got (%+v, %v)", inst, err)
	}
	if !r.Ran("rustup", "which", "--toolchain", tcNightly, "cargo") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestInstalledReturnsNilForMissingToolchain(t *testing.T) {
	p, _ := newProvider(t, listPlain)
	inst, err := p.Installed(context.Background(), model.Package{Name: "nightly"})
	if err != nil {
		t.Fatalf("absence must not be an error: %v", err)
	}
	if inst != nil {
		t.Fatalf("expected nil, got %+v", inst)
	}
}

func TestInstalledIgnoresCrates(t *testing.T) {
	// Cargo owns crates. rustup must claim nothing so that a crate never looks
	// like it came from a toolchain.
	p, _ := newProvider(t, listTwo)
	for _, name := range []string{"ripgrep", "cargo-edit", "fd-find", "some-crate"} {
		inst, err := p.Installed(context.Background(), model.Package{Name: name})
		if err != nil || inst != nil {
			t.Errorf("%q is a crate and must not be claimed by rustup, got (%+v, %v)", name, inst, err)
		}
	}
}

func TestInstalledResolvesRustupItself(t *testing.T) {
	p, _ := newProvider(t, "")
	inst, err := p.Installed(context.Background(), model.Package{Name: "rustup"})
	if err != nil {
		t.Fatalf("installed: %v", err)
	}
	if inst == nil {
		t.Fatal("rustup should own rustup")
	}
	if inst.Version != "1.29.1" {
		t.Errorf("version parsed as %q", inst.Version)
	}
	if inst.Path == "" {
		t.Error("the resolved rustup path should be reported")
	}
}

func TestInstalledWithNoToolchainPresent(t *testing.T) {
	// rustup exists but no toolchain is installed: it owns nothing yet.
	r := testsupport.NewFakeRunner().WithBin("rustup")
	r.SetFail(1, "", "rustup", "toolchain", "list", "-v")
	p := New(r, "/Users/u")
	inst, err := p.Installed(context.Background(), model.Package{Name: "rustc"})
	if err != nil || inst != nil {
		t.Errorf("expected (nil, nil) with no toolchain installed, got (%+v, %v)", inst, err)
	}
}

func TestInstallDefaultsToStable(t *testing.T) {
	p, r := newProvider(t, "")
	r.Responses["rustup toolchain install stable"] = exec.Result{Stdout: "info: unchanged\n"}
	if _, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "rustc"}}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("rustup", "toolchain", "install", "stable") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestInstallHonoursVersionPinOverChannel(t *testing.T) {
	p, r := newProvider(t, "")
	r.Responses["rustup toolchain install 1.99.0"] = exec.Result{}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "rustc", Channel: "nightly", Version: "1.99.0"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("rustup", "toolchain", "install", "1.99.0") {
		t.Errorf("an exact pin must not drift to a channel, calls: %v", r.Calls)
	}
}

func TestInstallRefreshesTheInstalledSet(t *testing.T) {
	p, r := newProvider(t, listPlain)
	r.Responses["rustup toolchain install stable"] = exec.Result{}
	// After installing stable the machine reports it.
	r.Responses["rustup toolchain list -v"] = exec.Result{Stdout: listOne}
	if _, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "rustc"}}); err != nil {
		t.Fatalf("install: %v", err)
	}
	inst, err := p.Installed(context.Background(), model.Package{Name: "rustc"})
	if err != nil || inst == nil {
		t.Fatalf("the freshly installed toolchain should be visible, got (%+v, %v)", inst, err)
	}
}

func TestInstallRejectsAnInvalidToolchainName(t *testing.T) {
	p, r := newProvider(t, "")
	r.SetFail(1, "error: invalid toolchain name: 'not a toolchain'", "rustup", "toolchain", "install", "not a toolchain")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "not a toolchain", Channel: "not a toolchain"}})
	pe, ok := provider.AsProviderError(err)
	if !ok {
		t.Fatalf("expected a provider error, got %v", err)
	}
	if pe.Kind != provider.ErrUnsupported {
		t.Errorf("an invalid toolchain name is unsupported, got %q", pe.Kind)
	}
}

func TestInstallNetworkFailureIsTyped(t *testing.T) {
	p, r := newProvider(t, "")
	r.SetFail(1, "error: failed to download: network failure", "rustup", "toolchain", "install", "nightly")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "nightly"}})
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrNetwork {
		t.Errorf("expected a network error, got %v", err)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t, "")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "rustc"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("rustup", "toolchain", "install", "stable") {
		t.Error("a dry run must not install anything")
	}
}

func TestRemoveUninstallsANonDefaultToolchain(t *testing.T) {
	p, r := newProvider(t, listTwo)
	r.Responses["rustup toolchain uninstall "+tcNightly] = exec.Result{Stdout: "info: uninstalling toolchain\n"}
	if err := p.Remove(context.Background(), model.Package{Name: "nightly"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("rustup", "toolchain", "uninstall", tcNightly) {
		t.Errorf("the full toolchain name should be used, calls: %v", r.Calls)
	}
}

func TestRemoveRefusesTheDefaultToolchain(t *testing.T) {
	p, r := newProvider(t, listOne)
	err := p.Remove(context.Background(), model.Package{Name: "stable"})
	if err == nil {
		t.Fatal("removing the default toolchain would leave no Rust compiler")
	}
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if !strings.Contains(pe.Message, "rustup default") {
		t.Errorf("the refusal should say what to do instead, got %q", pe.Message)
	}
	if len(r.Calls) == 0 {
		t.Error("expected at least the installed-set read")
	}
	if r.Ran("rustup", "toolchain", "uninstall", tcStable) {
		t.Error("no uninstall command should have been run")
	}
}

func TestRemoveRefusesAToolchainBinary(t *testing.T) {
	p, r := newProvider(t, "")
	err := p.Remove(context.Background(), model.Package{Name: "rustc"})
	if err == nil {
		t.Fatal("rustc is provided by a toolchain, not removed directly")
	}
	pe, _ := provider.AsProviderError(err)
	if pe == nil || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if !strings.Contains(pe.Message, "stable") {
		t.Errorf("the refusal should name the toolchain that provides it, got %q", pe.Message)
	}
	if r.Ran("rustup", "toolchain", "uninstall", tcStable) {
		t.Error("no uninstall command should have been run")
	}
}

func TestRemoveMissingToolchainDoesNotRunUninstall(t *testing.T) {
	// rustup exits zero when asked to remove something that is not installed, so
	// asking proves nothing. The installed set is checked first.
	p, r := newProvider(t, listOne)
	r.Responses["rustup toolchain uninstall 1.99.0-x86_64-apple-darwin"] = exec.Result{}
	err := p.Remove(context.Background(), model.Package{Name: "1.99.0"})
	if err == nil {
		t.Fatal("removing a toolchain that is not installed should be a not-found error")
	}
	pe, ok := provider.AsProviderError(err)
	if !ok || !provider.IsNotFound(err) {
		t.Fatalf("expected a not-found error, got %v", err)
	}
	if pe.Kind != provider.ErrNotFound {
		t.Errorf("kind = %q", pe.Kind)
	}
	if r.Ran("rustup", "toolchain", "uninstall", "1.99.0-x86_64-apple-darwin") {
		t.Error("rustup must not be asked to uninstall something absent")
	}
}

func TestRemoveRefusesCrates(t *testing.T) {
	p, r := newProvider(t, "")
	err := p.Remove(context.Background(), model.Package{Name: "ripgrep"})
	if err == nil {
		t.Fatal("rustup must not claim it can remove crates")
	}
	pe, _ := provider.AsProviderError(err)
	if pe == nil || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if !strings.Contains(pe.Message, "not crates") {
		t.Errorf("the refusal should say rustup is not a crate manager, got %q", pe.Message)
	}
	if len(r.Calls) == 0 {
		t.Error("expected at least the installed-set read")
	}
}

func TestUpdateReinstallsTheToolchain(t *testing.T) {
	p, r := newProvider(t, "")
	r.Responses["rustup toolchain install stable"] = exec.Result{}
	if _, err := p.Update(context.Background(), model.Package{Name: "stable"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("rustup", "toolchain", "install", "stable") {
		t.Errorf("rustup's install is how a channel is updated, calls: %v", r.Calls)
	}
}

func TestUpdateForABinaryEntryUpdatesItsToolchain(t *testing.T) {
	p, r := newProvider(t, "")
	r.Responses["rustup toolchain install stable"] = exec.Result{}
	if _, err := p.Update(context.Background(), model.Package{Name: "rustc"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("rustup", "toolchain", "install", "stable") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestPathResolutionNeverInstallsAToolchain(t *testing.T) {
	// `rustup which --toolchain <missing> <bin>` downloads that toolchain, so a
	// lookup must only ever name a toolchain that was already installed.
	p, r := newProvider(t, "stable-x86_64-apple-darwin\n")
	if _, err := p.Installed(context.Background(), model.Package{Name: "rustc"}); err != nil {
		t.Fatal(err)
	}
	for _, call := range r.Calls {
		if strings.Contains(call, "--toolchain nightly") || strings.Contains(call, "--toolchain beta") {
			t.Errorf("a lookup resolved a toolchain that was not installed: %s", call)
		}
	}
}

func TestLookupOnlyUsesTheToolchainList(t *testing.T) {
	p, r := newProvider(t, "")
	if _, err := p.Installed(context.Background(), model.Package{Name: "rustc"}); err != nil {
		t.Fatal(err)
	}
	if !r.Ran("rustup", "toolchain", "list", "-v") {
		t.Errorf("the installed set should be read first, calls: %v", r.Calls)
	}
}

func TestToolchainCacheIsUsedAcrossLookups(t *testing.T) {
	p, r := newProvider(t, listTwo)
	ctx := context.Background()
	for _, name := range []string{"rustc", "cargo", "rustfmt", "stable", "nightly"} {
		if _, err := p.Installed(ctx, model.Package{Name: name}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if n := r.CallCount("rustup", "toolchain", "list", "-v"); n != 1 {
		t.Errorf("the installed set should be read once per run, got %d", n)
	}
}

func TestLookupFailureIsTreatedAsNoToolchains(t *testing.T) {
	r := testsupport.NewFakeRunner().WithBin("rustup")
	r.SetFail(1, "error: could not read rustup home", "rustup", "toolchain", "list", "-v")
	p := New(r, "/Users/u")
	if got := p.Toolchains(context.Background()); len(got) != 0 {
		t.Errorf("a failed listing must not invent toolchains, got %+v", got)
	}
	inst, err := p.Installed(context.Background(), model.Package{Name: "rustc"})
	if err != nil || inst != nil {
		t.Errorf("nothing can be claimed when the set is unknown, got (%+v, %v)", inst, err)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"error: unknown binary 'rustup' in toolchain 'stable'", provider.ErrNotFound},
		{"error: toolchain 'nonexistent' is not installed", provider.ErrNotFound},
		{"info: no toolchain installed for '1.0.0'", provider.ErrNotFound},
		{"error: invalid toolchain name: 'nonsense'", provider.ErrUnsupported},
		{"error: network failure while downloading", provider.ErrNetwork},
		{"error: failed to create directory: Permission denied", provider.ErrPermission},
		{"info: unchanged stable", provider.ErrConflict},
		{"error: something entirely new", provider.ErrFailed},
	}
	p, _ := newProvider(t, "")
	for _, tc := range tests {
		err := p.classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 1})
		pe, ok := provider.AsProviderError(err)
		if !ok {
			t.Errorf("%q produced an untyped error %v", tc.stderr, err)
			continue
		}
		if pe.Kind != tc.want {
			t.Errorf("%q classified as %q, want %q", tc.stderr, pe.Kind, tc.want)
		}
	}
}

func TestRunnerErrorsAreWrapped(t *testing.T) {
	r := testsupport.NewFakeRunner().WithBin("rustup")
	r.Fallback = func(name string, args ...string) (exec.Result, error) {
		return exec.Result{}, errors.New("exec: permission denied")
	}
	p := New(r, "/Users/u")
	if _, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "stable"}}); err == nil {
		t.Fatal("a runner error must surface")
	}
}

func TestToolchainHome(t *testing.T) {
	if got := New(testsupport.NewFakeRunner(), "/Users/u").ToolchainHome(); got != "/Users/u/.rustup" {
		t.Errorf("ToolchainHome = %q", got)
	}
	if got := New(testsupport.NewFakeRunner(), "").ToolchainHome(); got != "~/.rustup" {
		t.Errorf("ToolchainHome without a home = %q", got)
	}
}
