// Package pacman implements the Pacman provider for Arch Linux systems.
//
// Pacman is the package manager for Arch Linux and its derivatives. It uses
// a different command set and output format compared to APT and DNF. This
// provider follows the same pattern as the apt provider for consistency.
package pacman

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"unicode"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "pacman"

// Provider implements provider.Provider for Pacman.
type Provider struct {
	runner   exec.Runner
	platform func() bool
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ExecutableMappingProvider = (*Provider)(nil)
var _ provider.PackageExecutableProvider = (*Provider)(nil)

// New builds a Pacman provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner, platform: pacmanPlatform} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Pacman (Arch Linux)" }
func (p *Provider) Kind() provider.Kind { return provider.KindPacman }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   false, // Arch repositories only serve current versions.
		Channels:     false, // Pacman uses repositories, not channels
		Verify:       true,
		DefaultScope: model.ScopeGlobal,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	if p.platform != nil && !p.platform() {
		h.Reason = "Pacman is only supported on Arch Linux-family systems"
		return h
	}
	path, err := p.runner.LookPath("pacman")
	if err != nil {
		h.Reason = "pacman not found on PATH"
		return h
	}
	h.Binary = path
	res, err := p.runner.Run(ctx, "pacman", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if res.Failed() {
		// pacman is on PATH but cannot run. Reporting it as available would send
		// the user to a provider that fails on every subsequent command.
		h.Reason = strings.TrimSpace(res.Stderr)
		if h.Reason == "" {
			h.Reason = "pacman exited with a non-zero status"
		}
		return h
	}
	h.Available = true
	// Output looks like " .--                  Pacman v6.0.2 - libalpm v14.0.2"
	// We extract the version after "Pacman v"
	fields := strings.Fields(res.Stdout)
	for i, f := range fields {
		if strings.HasPrefix(f, "Pacman") && i+1 < len(fields) {
			h.Version = strings.TrimPrefix(fields[i+1], "v")
			break
		}
	}
	return h
}

// Discover enumerates installed packages using pacman -Q.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "pacman", "-Q")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not enumerate pacman packages", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.FirstLine(res.Field()), nil)
	}
	var out []model.Installation
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		out = append(out, model.Installation{
			Name:     fields[0],
			Version:  fields[1],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		})
	}
	return out, nil
}

// Installed reports the pacman state of a package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "pacman", "-Q", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query pacman", err)
	}
	if res.Failed() {
		return nil, nil
	}
	fields := strings.Fields(res.Stdout)
	if len(fields) < 2 {
		return nil, nil
	}
	return &model.Installation{
		Name:     fields[0],
		Version:  fields[1],
		Provider: p.Name(),
		Scope:    model.ScopeGlobal,
	}, nil
}

// Install installs a package.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	// A specific version is deliberately not accepted. `pacman -S` resolves to
	// whatever the repository currently serves, and `pacman -U` installs from a
	// file or URL, so neither can honour `pkg=version` the way apt can. Declaring
	// no pinning support lets the engine report an unsatisfiable pin for a
	// package that is already installed at the wrong version. For a package that
	// is not installed yet, this refusal is the error the user sees, which is the
	// honest outcome: the version they asked for cannot be obtained.
	if v := req.Version(); v != "" {
		return nil, shell.Unsupported(p.Name(), "install",
			fmt.Sprintf("pacman cannot install %s at version %s; Arch repositories serve current versions only",
				req.Package.Name, v))
	}
	target := req.Package.Name
	args := []string{"-S", "--noconfirm", target}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	res, err := p.runner.Run(ctx, "pacman", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run pacman -S", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "pacman", "-R", "--noconfirm", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run pacman -R", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update upgrades a package to the newest available candidate.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "pacman", "-S", "--noconfirm", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run pacman -S", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "target not found"), strings.Contains(text, "package not found"),
		strings.Contains(text, "no such package"):
		return shell.NotFound(Name, op, "Pacman does not have this package in its repositories")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "you cannot perform this operation"),
		strings.Contains(text, "need root privileges"):
		return shell.Err(Name, op, provider.ErrPermission, "pacman needs root; re-run with elevated privileges", nil)
	case strings.Contains(text, "could not resolve"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "network is unreachable"), strings.Contains(text, "failed to synchronize"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the Pacman repositories", nil)
	case strings.Contains(text, "dependency"), strings.Contains(text, "conflict"),
		strings.Contains(text, "unresolvable package conflicts"):
		return shell.Err(Name, op, provider.ErrConflict, shell.FirstLine(res.Field()), nil)
	default:
		msg := shell.FirstLine(res.Field())
		if msg == "" {
			msg = "pacman exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// ExecutableToPackage returns a mapping from executable names to package names.
// For Pacman, most packages install an executable with the same name, but there are
// exceptions. This provides a basic mapping for common packages.
func (p *Provider) ExecutableToPackage() map[string]string {
	return map[string]string{
		"git":            "git",
		"python3":        "python3",
		"node":           "nodejs",
		"npm":            "npm",
		"rustc":          "rust",
		"cargo":          "rust",
		"go":             "go",
		"ruby":           "ruby",
		"gem":            "ruby",
		"java":           "jdk-openjdk",
		"javac":          "jdk-openjdk",
		"ripgrep":        "ripgrep",
		"fd":             "fd",
		"bat":            "bat",
		"exa":            "exa",
		"eza":            "eza",
		"jq":             "jq",
		"yq":             "yq",
		"fzf":            "fzf",
		"tmux":           "tmux",
		"vim":            "vim",
		"neovim":         "neovim",
		"nvim":           "neovim",
		"htop":           "htop",
		"btop":           "btop",
		"wget":           "wget",
		"curl":           "curl",
		"ffmpeg":         "ffmpeg",
		"imagemagick":    "imagemagick",
		"docker":         "docker",
		"docker-compose": "docker-compose",
	}
}

// Verify checks that an installed executable is functional by running it.
// For Pacman, this attempts to run the package's executable with a --version flag.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// Try to run the executable with --version to verify it's functional.
	exe, ok := p.ExecutableForPackage(pkg.Name)
	if !ok {
		return shell.UnsupportedVerify(p.Name())
	}
	if exe == "" || strings.ContainsAny(exe, "/\\") {
		return shell.UnsupportedVerify(p.Name())
	}
	res, err := p.runner.Run(ctx, exe, "--version")
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not run executable for verification", err)
	}
	if res.Failed() {
		return shell.Failure(p.Name(), "verify", "executable failed verification", nil)
	}
	return nil
}

func (p *Provider) ExecutableForPackage(packageName string) (string, bool) {
	return shell.ExecutableForPackage(p.ExecutableToPackage(), packageName)
}

func pacmanPlatform() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	ids := osReleaseIDs(string(data))
	for _, id := range []string{"arch", "manjaro", "endeavouros", "cachyos", "garuda", "artix", "arcolinux"} {
		if ids[id] {
			return true
		}
	}
	return false
}

func osReleaseIDs(contents string) map[string]bool {
	ids := map[string]bool{}
	for _, line := range strings.Split(contents, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || (key != "ID" && key != "ID_LIKE") {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		for _, id := range strings.FieldsFunc(strings.ToLower(value), unicode.IsSpace) {
			ids[id] = true
		}
	}
	return ids
}
