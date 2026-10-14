package golang

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner, string) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("go")
	p := New(r, "/home/u")
	bin := t.TempDir()
	r.Set("go version go1.23.4 darwin/amd64\n", "go", "version")
	r.Set("", "go", "env", "GOBIN")
	r.Set(filepath.Join("/home/u", "go")+"\n", "go", "env", "GOPATH")
	return p, r, bin
}

func TestHealth(t *testing.T) {
	p, _, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("go should be available: %s", h.Reason)
	}
	if h.Version != "1.23.4" {
		t.Errorf("version parsed as %q", h.Version)
	}
}

func TestBinDirPrefersGobin(t *testing.T) {
	p, r, _ := newProvider(t)
	custom := t.TempDir()
	r.Set(custom+"\n", "go", "env", "GOBIN")
	if got := p.binDir(context.Background()); got != custom {
		t.Errorf("GOBIN must win, got %q", got)
	}
}

func TestBinDirFallsBackToGopath(t *testing.T) {
	p, r, _ := newProvider(t)
	r.SetFail(1, "", "go", "env", "GOBIN")
	r.Set("/custom/gopath\n", "go", "env", "GOPATH")
	if got := p.binDir(context.Background()); got != "/custom/gopath/bin" {
		t.Errorf("expected GOPATH/bin, got %q", got)
	}
}

func TestDiscoverListsExecutablesOnly(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	writeExec(t, filepath.Join(bin, "gopls"))
	writeExec(t, filepath.Join(bin, "staticcheck"))
	writeFile(t, filepath.Join(bin, "README.md"), "not an executable")

	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 binaries, got %+v", insts)
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
		if i.Version != "" {
			t.Errorf("Go records no version metadata beside the binary; guessing is wrong: %+v", i)
		}
		if i.Path == "" {
			t.Errorf("path should be recorded: %+v", i)
		}
	}
}

func TestDiscoverOnMissingDir(t *testing.T) {
	p, r, _ := newProvider(t)
	r.Set("/definitely/not/here\n", "go", "env", "GOBIN")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("a missing bin directory is normal: %v", err)
	}
	if len(insts) != 0 {
		t.Errorf("expected nothing, got %+v", insts)
	}
}

func TestInstallRequiresModulePath(t *testing.T) {
	p, r, _ := newProvider(t)
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "gopls"}})
	if err == nil {
		t.Fatal("a bare binary name cannot be installed with go install")
	}
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if r.Ran("go", "install", "gopls@latest") {
		t.Error("no command should have been run")
	}
}

func TestInstallUsesModulePathAndVersion(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	r.Responses["go install golang.org/x/tools/gopls@v0.16.1"] = exec.Result{}
	_, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "golang.org/x/tools/gopls", Version: "0.16.1"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("go", "install", "golang.org/x/tools/gopls@v0.16.1") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestInstallDefaultsToLatest(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	r.Responses["go install golang.org/x/tools/gopls@latest"] = exec.Result{}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "golang.org/x/tools/gopls"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("go", "install", "golang.org/x/tools/gopls@latest") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveDeletesTheBinary(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	target := filepath.Join(bin, "gopls")
	writeExec(t, target)

	if err := p.Remove(context.Background(), model.Package{Name: "gopls"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("the binary should be gone")
	}
}

func TestRemoveOnMissingBinaryIsNotAnError(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	if err := p.Remove(context.Background(), model.Package{Name: "absent"}); err != nil {
		t.Fatalf("removing something already gone should succeed: %v", err)
	}
}

func TestUpdateRefusesBareBinaryName(t *testing.T) {
	p, _, _ := newProvider(t)
	_, err := p.Update(context.Background(), model.Package{Name: "gopls"})
	if err == nil {
		t.Fatal("reinstalling from a bare name must be refused")
	}
	pe, _ := provider.AsProviderError(err)
	if pe == nil || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected unsupported, got %v", err)
	}
}

func TestIsModulePath(t *testing.T) {
	cases := map[string]bool{
		"golang.org/x/tools/gopls": true,
		"example.com/tool":         true,
		"gopls":                    false,
		"ripgrep":                  false,
		"":                         false,
	}
	for in, want := range cases {
		if got := IsModulePath(in); got != want {
			t.Errorf("IsModulePath(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestBinaryName(t *testing.T) {
	cases := map[string]string{
		"golang.org/x/tools/gopls@v0.16.1": "gopls",
		"example.com/tool/cmd":             "cmd",
		"gopls":                            "gopls",
	}
	for in, want := range cases {
		if got := binaryName(in); got != want {
			t.Errorf("binaryName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r, bin := newProvider(t)
	r.Set(bin+"\n", "go", "env", "GOBIN")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "golang.org/x/tools/gopls"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("go", "install", "golang.org/x/tools/gopls@latest") {
		t.Error("a dry run must not invoke go install")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"malformed module path \"gopls\"", provider.ErrUnsupported},
		{"go: golang.org/x/tools/gopls@v0.0.0: invalid version", provider.ErrUnsupported},
		{"go: module golang.org/x/tools@nonexistent found, but does not contain", provider.ErrNotFound},
		{"dial tcp: lookup proxy.golang.org: no such host", provider.ErrNetwork},
		{"open /usr/local/bin/gopls: permission denied", provider.ErrPermission},
	}
	for _, tc := range tests {
		err := classify("install", execResult(tc.stderr))
		pe, ok := provider.AsProviderError(err)
		if !ok || pe.Kind != tc.want {
			t.Errorf("%q classified as %v, want %q", tc.stderr, err, tc.want)
		}
	}
}

func execResult(stderr string) exec.Result {
	return exec.Result{Stderr: stderr, ExitCode: 1}
}

func writeExec(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func TestGoVersionDoesNotDoubleThePrefix(t *testing.T) {
	cases := map[string]string{
		"0.16.1":  "0.16.1",
		"v0.16.1": "0.16.1",
		"==v0.16": "0.16",
		"vendor":  "vendor",
		"":        "",
	}
	for in, want := range cases {
		if got := goVersion(in); got != want {
			t.Errorf("goVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
