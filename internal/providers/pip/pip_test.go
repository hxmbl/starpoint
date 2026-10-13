package pip

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// freeze is the shape `pip list --format=freeze` prints, captured from pip
// 26.2.1 on Python 3.14.
const freeze = `certifi==2026.7.22
lit==23.1.0
pillow==12.3.0
pip==26.2.1
wheel==0.47.0
`

const versionLine = "pip 26.2.1 from /usr/local/lib/python3.14/site-packages/pip (python 3.14)\n"

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("pip3")
	p := New(r)
	r.Set(versionLine, "pip3", "--version")
	r.Set(freeze, "pip3", "list", "--format=freeze")
	return p, r
}

func TestHealthNamesTheInterpreter(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("pip should be available: %s", h.Reason)
	}
	if h.Version != "26.2.1" {
		t.Errorf("version parsed as %q", h.Version)
	}
	// The interpreter matters: two pips on one machine can be different Pythons.
	if h.Reason == "" {
		t.Error("which interpreter pip belongs to should be reported")
	}
	if h.Capabilities.DefaultScope != model.ScopeUser {
		t.Errorf("pip installs with --user here, got %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthFallsBackToPlainPip(t *testing.T) {
	r := testsupport.NewFakeRunner().WithBin("pip")
	p := New(r)
	r.Set(versionLine, "pip", "--version")
	r.Set(freeze, "pip", "list", "--format=freeze")

	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("pip should be found: %s", h.Reason)
	}
	if p.binary != "pip" {
		t.Errorf("expected the unversioned executable, got %q", p.binary)
	}
}

func TestHealthWhenAbsent(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("neither pip3 nor pip is on the fake PATH")
	}
	if h.Reason == "" {
		t.Error("an unavailable provider must say why")
	}
}

func TestInterpreterFromVersionOutput(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	cases := map[string]string{
		versionLine:                            "3.14",
		"pip 24.0 from /x/pip (python 3.12)\n": "3.12",
		"pip 24.0 from /x/pip\n":               "",
		"":                                     "",
	}
	for in, want := range cases {
		if got := p.interpreterFrom(in); got != want {
			t.Errorf("interpreterFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscoverParsesFreeze(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 5 {
		t.Fatalf("expected 5 packages, got %d", len(insts))
	}
	byName := map[string]model.Installation{}
	for _, i := range insts {
		byName[i.Name] = i
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
	}
	if byName["pillow"].Version != "12.3.0" {
		t.Errorf("unexpected pillow entry: %+v", byName["pillow"])
	}
	if _, ok := byName["certifi"]; !ok {
		t.Error("certifi should be listed")
	}
}

func TestDiscoverIgnoresCommentsAndBlanks(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	insts := p.parseFreeze("# a comment\n\nnotaversion\nname==1.0\n==2.0\n")
	if len(insts) != 1 {
		t.Fatalf("expected only the well-formed line, got %+v", insts)
	}
	if insts[0].Name != "name" || insts[0].Version != "1.0" {
		t.Errorf("unexpected entry: %+v", insts[0])
	}
}

func TestInstallUsesUserSiteAndHonoursAPin(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["pip3 install --user pillow==12.3.0"] = exec.Result{}
	r.Responses["pip3 install --user pillow"] = exec.Result{}

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "pillow", Version: "12.3.0"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// --user is not optional here: a system site-packages needs root, and this
	// provider must never plan something that needs elevation.
	if !r.Ran("pip3", "install", "--user", "pillow==12.3.0") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "pillow"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("pip3", "install", "--user", "pillow") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["pip3 uninstall -y pillow"] = exec.Result{}
	r.Responses["pip3 install --user --upgrade pillow"] = exec.Result{}
	r.Responses["pip3 install --user --upgrade pillow==12.3.0"] = exec.Result{}

	if err := p.Remove(context.Background(), model.Package{Name: "pillow"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("pip3", "uninstall", "-y", "pillow") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "pillow"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("pip3", "install", "--user", "--upgrade", "pillow") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "pillow", Version: "12.3.0"}); err != nil {
		t.Fatalf("update with pin: %v", err)
	}
	if !r.Ran("pip3", "install", "--user", "--upgrade", "pillow==12.3.0") {
		t.Errorf("a pin must be passed through, calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "pillow"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("pip3", "install", "--user", "pillow") {
		t.Error("a dry run must not invoke pip")
	}
}

func TestAbsentPipOwnsNothing(t *testing.T) {
	r := testsupport.NewFakeRunner()
	p := New(r)
	insts, err := p.Discover(context.Background())
	if err != nil || len(insts) != 0 {
		t.Errorf("an absent provider owns nothing, got (%+v, %v)", insts, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("no command should run, got %v", r.Calls)
	}
}

func TestMutationsAreRefusedWithoutPip(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	ctx := context.Background()
	if _, err := p.Install(ctx, provider.Request{Package: model.Package{Name: "x"}}); err == nil {
		t.Error("install must be refused without pip")
	}
	if err := p.Remove(ctx, model.Package{Name: "x"}); err == nil {
		t.Error("remove must be refused without pip")
	}
	if _, err := p.Update(ctx, model.Package{Name: "x"}); err == nil {
		t.Error("update must be refused without pip")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"ERROR: Could not find a version that satisfies the requirement nope", provider.ErrNotFound},
		{"ERROR: externally-managed-environment", provider.ErrPermission},
		{"WARNING: Retrying ... connection to pypi.org failed", provider.ErrNetwork},
		{"ERROR: Cannot install pillow because these package versions have conflicting dependencies", provider.ErrConflict},
		{"ERROR: something entirely new", provider.ErrFailed},
	}
	p, _ := newProvider(t)
	for _, tc := range tests {
		err := p.classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 1})
		pe, ok := provider.AsProviderError(err)
		if !ok || pe.Kind != tc.want {
			t.Errorf("%q classified as %v, want %q", tc.stderr, err, tc.want)
		}
	}
}

func TestExecutableDirIsTheInterpretersUserBaseBin(t *testing.T) {
	t.Setenv("PYTHONUSERBASE", "")
	p, r := newProvider(t)
	r.WithBin("python3.14")
	r.Set("/home/u/.local\n", "python3.14", "-m", "site", "--user-base")
	if got := p.ExecutableDir(context.Background()); got != "/home/u/.local/bin" {
		t.Errorf("ExecutableDir = %q", got)
	}

	t.Setenv("PYTHONUSERBASE", "/opt/userbase")
	p2, _ := newProvider(t)
	if got := p2.ExecutableDir(context.Background()); got != "/opt/userbase/bin" {
		t.Errorf("PYTHONUSERBASE should be honoured, got %q", got)
	}
}

func TestExecutableDirIsUnknownWithoutAnInterpreter(t *testing.T) {
	t.Setenv("PYTHONUSERBASE", "")
	p, _ := newProvider(t)
	if got := p.ExecutableDir(context.Background()); got != "" {
		t.Errorf("with no interpreter to ask, the directory is unknown, got %q", got)
	}
}

// showCowsay is `pip show -f cowsay` from pip 25.0.1 with a --user install.
const showCowsay = `Name: cowsay
Version: 6.1
Summary: The famous cowsay for GNU/Linux is now available for python
Location: /home/u/.local/lib/python3.12/site-packages
Requires: 
Required-by: 
Files:
  ../../../bin/cowsay
  cowsay-6.1.dist-info/INSTALLER
  cowsay-6.1.dist-info/RECORD
  cowsay/__init__.py
  cowsay/main.py
`

const showCertifi = `Name: certifi
Version: 2026.7.22
Location: /usr/local/lib/python3.14/site-packages
Requires: 
Files:
  certifi-2026.7.22.dist-info/INSTALLER
  certifi/__init__.py
  certifi/cacert.pem
`

func TestInstalledReportsTheConsoleScriptPath(t *testing.T) {
	p, r := newProvider(t)
	r.Set("cowsay==6.1\n", "pip3", "list", "--format=freeze")
	r.Set(showCowsay, "pip3", "show", "-f", "cowsay")
	inst, err := p.Installed(context.Background(), model.Package{Name: "cowsay"})
	if err != nil || inst == nil {
		t.Fatalf("expected cowsay, got %v / %v", inst, err)
	}
	if inst.Path != "/home/u/.local/bin/cowsay" {
		t.Errorf("Path = %q, want the console script", inst.Path)
	}
}

func TestInstalledReportsTheTopLevelPackageForALibrary(t *testing.T) {
	p, r := newProvider(t)
	r.Set(showCertifi, "pip3", "show", "-f", "certifi")
	inst, err := p.Installed(context.Background(), model.Package{Name: "certifi"})
	if err != nil || inst == nil {
		t.Fatalf("expected certifi, got %v / %v", inst, err)
	}
	if inst.Path != "/usr/local/lib/python3.14/site-packages/certifi" {
		t.Errorf("Path = %q, want the package directory", inst.Path)
	}
}

func TestInstalledLeavesThePathUnknownWhenShowFails(t *testing.T) {
	p, _ := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "lit"})
	if err != nil || inst == nil {
		t.Fatalf("expected lit, got %v / %v", inst, err)
	}
	if inst.Path != "" {
		t.Errorf("an unanswered show must not invent a path, got %q", inst.Path)
	}
}

func TestInstalledMatchesNormalisedNames(t *testing.T) {
	p, _ := newProvider(t)
	inst, _ := p.Installed(context.Background(), model.Package{Name: "Pillow"})
	if inst == nil || inst.Name != "pillow" {
		t.Errorf("PEP 503 names should match regardless of case, got %+v", inst)
	}
}

func TestPathFromShowPrefersTheScriptNamedAfterThePackage(t *testing.T) {
	out := "Name: black\nLocation: /l/site-packages\nFiles:\n  ../../../bin/blackd\n  ../../../bin/black\n  black/__init__.py\n"
	if got := pathFromShow(out, "black"); got != "/bin/black" && got != filepath.Clean("/l/site-packages/../../../bin/black") {
		t.Errorf("got %q", got)
	}
	if got := pathFromShow("Name: x\n", "x"); got != "" {
		t.Errorf("no location means no path, got %q", got)
	}
}
