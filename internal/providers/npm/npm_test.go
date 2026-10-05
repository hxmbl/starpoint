package npm

import (
	"context"
	"testing"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/testsupport"
)

const lsJSON = `{
  "name": "lib",
  "dependencies": {
    "npm": { "version": "10.9.0", "overridden": false },
    "typescript": { "version": "5.6.3", "overridden": false }
  }
}
`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("npm")
	p := New(r)
	r.Set("10.9.0\n", "npm", "--version")
	r.Set(lsJSON, "npm", "ls", "-g", "--json", "--depth=0")
	return p, r
}

func TestHealthResolvesPrefix(t *testing.T) {
	p, r := newProvider(t)
	r.Set("/opt/homebrew\n", "npm", "config", "get", "prefix")
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("npm should be available: %s", h.Reason)
	}
	if h.Version != "10.9.0" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if p.prefix != "/opt/homebrew" {
		t.Errorf("prefix not resolved from npm: %q", p.prefix)
	}
}

func TestDiscoverParsesJSON(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(insts))
	}
	byName := map[string]model.Installation{}
	for _, i := range insts {
		byName[i.Name] = i
	}
	if byName["typescript"].Version != "5.6.3" {
		t.Errorf("unexpected typescript entry: %+v", byName["typescript"])
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
	}
}

func TestDiscoverToleratesNonZeroExitWithJSON(t *testing.T) {
	p, r := newProvider(t)
	broken := `{"dependencies":{"a":{"version":"1.0.0"}},"problems":["missing: b@2"]}`
	r.Responses["npm ls -g --json --depth=0"] = stdoutResult(1, broken)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("a tree with problems is still readable: %v", err)
	}
	if len(insts) != 1 {
		t.Errorf("expected 1 package, got %+v", insts)
	}
}

func TestDiscoverFailsOnNonJSONOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["npm ls -g --json --depth=0"] = stdoutResult(1, "npm ERR! code ENOENT")
	if _, err := p.Discover(context.Background()); err == nil {
		t.Fatal("unparseable output must be an error, not an empty inventory")
	}
}

func TestInstalledForSinglePackage(t *testing.T) {
	p, r := newProvider(t)
	single := `{"dependencies":{"typescript":{"version":"5.6.3"}}}`
	r.Responses["npm ls -g --json --depth=0 typescript"] = stdoutResult(0, single)
	inst, err := p.Installed(context.Background(), model.Package{Name: "typescript"})
	if err != nil || inst == nil {
		t.Fatalf("expected typescript to be found, got (%+v, %v)", inst, err)
	}
	if inst.Version != "5.6.3" {
		t.Errorf("version wrong: %+v", inst)
	}
	r.Responses["npm ls -g --json --depth=0 nope"] = stdoutResult(1, "")
	if absent, err := p.Installed(context.Background(), model.Package{Name: "nope"}); err != nil || absent != nil {
		t.Errorf("absence must be (nil, nil), got (%+v, %v)", absent, err)
	}
}

func TestInstallWithVersionAndChannel(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["npm install -g typescript@5.6.3"] = execResult(0, "")
	r.Responses["npm install -g typescript@next"] = execResult(0, "")
	r.Responses["npm install -g pnpm"] = execResult(0, "")

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "typescript", Version: "5.6.3"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("npm", "install", "-g", "typescript@5.6.3") {
		t.Errorf("calls: %v", r.Calls)
	}

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "typescript", Channel: "next"},
	}); err != nil {
		t.Fatalf("install with channel: %v", err)
	}
	if !r.Ran("npm", "install", "-g", "typescript@next") {
		t.Errorf("a channel becomes a dist-tag, calls: %v", r.Calls)
	}

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "pnpm"},
	}); err != nil {
		t.Fatalf("install plain: %v", err)
	}
	if !r.Ran("npm", "install", "-g", "pnpm") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestUpdateHonoursPinAndRemove(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["npm update -g typescript@5.6.3"] = execResult(0, "")
	r.Responses["npm update -g pnpm"] = execResult(0, "")
	r.Responses["npm uninstall -g pnpm"] = execResult(0, "")

	if _, err := p.Update(context.Background(), model.Package{Name: "typescript", Version: "5.6.3"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("npm", "update", "-g", "typescript@5.6.3") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "pnpm"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("npm", "update", "-g", "pnpm") {
		t.Errorf("calls: %v", r.Calls)
	}
	if err := p.Remove(context.Background(), model.Package{Name: "pnpm"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("npm", "uninstall", "-g", "pnpm") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "pnpm"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("npm", "install", "-g", "pnpm") {
		t.Error("a dry run must not invoke npm")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"npm error 404 Not Found - GET https://registry.npmjs.org/nope", provider.ErrNotFound},
		{"npm error code EEXIST", provider.ErrConflict},
		{"npm error code EACCES", provider.ErrPermission},
		{"npm error code ENETUNREACH", provider.ErrNetwork},
		{"npm error something else entirely", provider.ErrFailed},
	}
	for _, tc := range tests {
		err := classify("install", execResult(1, tc.stderr))
		pe, ok := provider.AsProviderError(err)
		if !ok || pe.Kind != tc.want {
			t.Errorf("%q classified as %v, want %q", tc.stderr, err, tc.want)
		}
	}
}

// execResult builds a command result carrying stderr.
func execResult(code int, stderr string) exec.Result {
	return exec.Result{Stderr: stderr, ExitCode: code}
}

// stdoutResult builds a command result carrying stdout, which is where npm puts
// its JSON.
func stdoutResult(code int, stdout string) exec.Result {
	return exec.Result{Stdout: stdout, ExitCode: code}
}
