package testsupport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
)

// FakeRunner is a scripted command runner. It records invocations so tests can
// assert on the exact commands a provider would run, and returns canned output
// so no test needs to touch the machine.
type FakeRunner struct {
	mu sync.Mutex
	// Responses maps "cmd arg1 arg2" to a canned result.
	Responses map[string]exec.Result
	// Path maps an executable name to the path LookPath should return.
	Path map[string]string
	// Calls records every invocation in order.
	Calls []string
	// Fallback returns a result for an unmapped command.
	Fallback func(name string, args ...string) (exec.Result, error)
}

var _ exec.Runner = (*FakeRunner)(nil)

// NewFakeRunner builds an empty runner.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{Responses: map[string]exec.Result{}, Path: map[string]string{}}
}

// Key builds the lookup key for a command.
func Key(name string, args ...string) string {
	return strings.TrimSpace(name + " " + strings.Join(args, " "))
}

// Set registers canned output for a command.
func (f *FakeRunner) Set(output string, name string, args ...string) *FakeRunner {
	f.Responses[Key(name, args...)] = exec.Result{Stdout: output}
	return f
}

// SetFail registers a nonzero exit for a command.
func (f *FakeRunner) SetFail(exit int, stderr string, name string, args ...string) *FakeRunner {
	f.Responses[Key(name, args...)] = exec.Result{Stderr: stderr, ExitCode: exit}
	return f
}

// WithBin marks an executable as present on PATH.
func (f *FakeRunner) WithBin(name string) *FakeRunner {
	f.Path[name] = "/usr/local/bin/" + name
	return f
}

// Run implements exec.Runner.
func (f *FakeRunner) Run(ctx context.Context, name string, args ...string) (exec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := Key(name, args...)
	f.Calls = append(f.Calls, key)
	if res, ok := f.Responses[key]; ok {
		return res, nil
	}
	if f.Fallback != nil {
		return f.Fallback(name, args...)
	}
	return exec.Result{ExitCode: 1, Stderr: "no canned response for: " + key}, nil
}

// LookPath implements exec.Runner.
func (f *FakeRunner) LookPath(name string) (string, error) {
	if p, ok := f.Path[name]; ok {
		return p, nil
	}
	return "", fmt.Errorf("%s not found", name)
}

// Ran reports whether a command was invoked.
func (f *FakeRunner) Ran(name string, args ...string) bool {
	want := Key(name, args...)
	for _, c := range f.Calls {
		if c == want {
			return true
		}
	}
	return false
}

// CallCount returns how many times a command was invoked.
func (f *FakeRunner) CallCount(name string, args ...string) int {
	want := Key(name, args...)
	n := 0
	for _, c := range f.Calls {
		if c == want {
			n++
		}
	}
	return n
}

// FakeProvider is an in-memory provider.Provider. Every operation is recorded and
// every failure is injectable, so the engine's behaviour under partial failure
// can be tested deterministically.
type FakeProvider struct {
	mu sync.Mutex

	// ProviderName is the identifier.
	ProviderName string
	// KindOf categorizes the provider.
	KindOf provider.Kind
	// Desc is the human label.
	Desc string
	// Caps is the advertised capability set.
	Caps provider.Capabilities
	// IsAvailable controls health.
	IsAvailable bool
	// UnavailableReason is reported when IsAvailable is false.
	UnavailableReason string
	// Items is the current inventory, keyed by name.
	Items map[string]model.Installation
	// InstallError, RemoveError and UpdateError inject failures.
	InstallError error
	RemoveError  error
	UpdateError  error
	// InstalledError makes Installed fail.
	InstalledError error
	// InstallPath is where Install puts the software. Tests set it so the paths a
	// fake reports line up with the PATH a fake locator resolves, which matters
	// whenever a test is exercising verification.
	InstallPath string

	// InstalledDelay makes Installed take this long, honouring context
	// cancellation. Tests use it to prove a slow provider cannot hang a command,
	// which is the one property concurrency can quietly lose.
	InstalledDelay time.Duration

	// InstalledHook runs before Installed answers, for tests that need to observe
	// or steer a concurrent lookup.
	InstalledHook func(ctx context.Context, pkg model.Package)

	// Recorded operations.
	InstallCalls []provider.Request
	RemoveCalls  []model.Package
	UpdateCalls  []model.Package
}

var _ provider.Provider = (*FakeProvider)(nil)

// NewFakeProvider builds a provider with sensible defaults.
func NewFakeProvider(name string, kind provider.Kind) *FakeProvider {
	return &FakeProvider{
		ProviderName: name,
		KindOf:       kind,
		Desc:         "Fake " + name,
		IsAvailable:  true,
		Items:        map[string]model.Installation{},
		Caps: provider.Capabilities{
			Discover:     true,
			Install:      true,
			Remove:       true,
			Update:       true,
			PinVersion:   true,
			DefaultScope: model.ScopeUser,
		},
	}
}

// With adds an installed item.
func (f *FakeProvider) With(inst model.Installation) *FakeProvider {
	if inst.Provider == "" {
		inst.Provider = f.ProviderName
	}
	if inst.Scope == "" {
		inst.Scope = f.Caps.DefaultScope
	}
	f.Items[inst.Name] = inst
	return f
}

func (f *FakeProvider) Name() string        { return f.ProviderName }
func (f *FakeProvider) Description() string { return f.Desc }
func (f *FakeProvider) Kind() provider.Kind { return f.KindOf }
func (f *FakeProvider) Capabilities() provider.Capabilities {
	return f.Caps
}

func (f *FakeProvider) Health(context.Context) provider.Health {
	return provider.Health{
		Name:         f.ProviderName,
		Description:  f.Desc,
		Kind:         f.KindOf,
		Available:    f.IsAvailable,
		Reason:       f.UnavailableReason,
		Capabilities: f.Caps,
	}
}

// Discover returns the inventory.
func (f *FakeProvider) Discover(context.Context) ([]model.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.Items))
	for n := range f.Items {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]model.Installation, 0, len(names))
	for _, n := range names {
		out = append(out, f.Items[n])
	}
	return out, nil
}

// Installed reports one item.
func (f *FakeProvider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	// A slow provider must be able to notice cancellation, and the wait must not
	// hold the lock: a serialised fake would make a concurrent caller look
	// synchronised when it is not.
	if f.InstalledDelay > 0 || f.InstalledHook != nil {
		if f.InstalledHook != nil {
			f.InstalledHook(ctx, pkg)
		}
		if f.InstalledDelay > 0 {
			timer := time.NewTimer(f.InstalledDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.InstalledError != nil {
		return nil, f.InstalledError
	}
	inst, ok := f.Items[pkg.Name]
	if !ok {
		return nil, nil
	}
	copied := inst
	return &copied, nil
}

// Install records the request and adds the item.
func (f *FakeProvider) Install(_ context.Context, req provider.Request) (*model.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.InstallCalls = append(f.InstallCalls, req)
	if f.InstallError != nil {
		return nil, f.InstallError
	}
	inst := model.Installation{
		Name:     req.Package.Name,
		Version:  req.Package.Version,
		Provider: f.ProviderName,
		Scope:    f.Caps.DefaultScope,
		Path:     f.installPath(req.Package.Name),
	}
	if inst.Version == "" {
		inst.Version = "1.0.0"
	}
	f.Items[req.Package.Name] = inst
	copied := inst
	return &copied, nil
}

// Remove deletes the item.
func (f *FakeProvider) Remove(_ context.Context, pkg model.Package) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemoveCalls = append(f.RemoveCalls, pkg)
	if f.RemoveError != nil {
		return f.RemoveError
	}
	delete(f.Items, pkg.Name)
	return nil
}

// Update changes the version.
func (f *FakeProvider) Update(_ context.Context, pkg model.Package) (*model.Installation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpdateCalls = append(f.UpdateCalls, pkg)
	if f.UpdateError != nil {
		return nil, f.UpdateError
	}
	inst, ok := f.Items[pkg.Name]
	if !ok {
		inst = model.Installation{Name: pkg.Name, Provider: f.ProviderName, Scope: f.Caps.DefaultScope, Version: "1.0.0"}
	}
	inst.Version = bump(inst.Version)
	f.Items[pkg.Name] = inst
	copied := inst
	return &copied, nil
}

// installPath returns where this fake installs a package.
func (f *FakeProvider) installPath(name string) string {
	if f.InstallPath != "" {
		if strings.HasSuffix(f.InstallPath, "/") {
			return f.InstallPath + name
		}
		return f.InstallPath + "/" + name
	}
	return "/fake/" + name
}

// bump increments a dotted version, defaulting to 2.0.0.
func bump(v string) string {
	if v == "" {
		return "2.0.0"
	}
	parts := strings.Split(v, ".")
	last := parts[len(parts)-1]
	n := 0
	for _, r := range last {
		if r < '0' || r > '9' {
			return v + ".1"
		}
		n = n*10 + int(r-'0')
	}
	parts[len(parts)-1] = fmt.Sprintf("%d", n+1)
	return strings.Join(parts, ".")
}

// TempHome creates a throwaway home directory so tests never read or write the
// developer's real configuration.
func TempHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// HomeConfigPath returns the config path inside a TempHome.
func HomeConfigPath(home string) string { return filepath.Join(home, ".starpoint.toml") }

// WriteConfig writes a TOML configuration file under a temp dir and returns its path.
func WriteConfig(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	WriteFile(t, path, body)
	return path
}

// WriteFile writes content to path, creating parent directories.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// ReadFile reads a file, failing loudly.
func ReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// MustDoc loads a document from a single file path.
func MustDoc(t *testing.T, path string) *config.Document {
	t.Helper()
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return doc
}
