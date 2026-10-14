// Package brew implements the Homebrew provider.
//
// Homebrew owns two distinct kinds of software: formulae (compiled or scripted
// packages) and casks (macOS applications). Starpoint treats the channel as the
// switch between them, because that is genuinely how Homebrew models it, rather
// than pretending casks are just another name.
package brew

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "brew"

// ChannelCask selects a cask instead of a formula.
const ChannelCask = "cask"

// Provider implements provider.Provider for Homebrew.
type Provider struct {
	runner exec.Runner
	// prefix is the Homebrew installation prefix, used to locate executables.
	prefix string
	// prefixKnown records whether prefix has been resolved yet.
	prefixKnown bool
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ExecutableMappingProvider = (*Provider)(nil)

// New builds a Homebrew provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Homebrew" }
func (p *Provider) Kind() provider.Kind { return provider.KindBrew }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   false, // Homebrew installs the current revision only.
		Channels:     true,  // formula vs cask
		Search:       true,
		DefaultScope: model.ScopeUser,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, err := p.runner.LookPath("brew")
	if err != nil {
		h.Reason = "brew not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "brew", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	// Output looks like "Homebrew 4.3.5\nHomebrew/homebrew-core (git revision abc)".
	if fields := shell.Fields(strings.SplitN(res.Stdout, "\n", 2)[0]); len(fields) >= 2 {
		h.Version = fields[1]
	}
	return h
}

// Discover lists installed formulae and casks.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	var out []model.Installation
	for _, args := range [][]string{
		{"list", "--versions", "--formula"},
		{"list", "--versions", "--cask"},
	} {
		res, err := p.runner.Run(ctx, "brew", args...)
		if err != nil {
			return nil, shell.Failure(p.Name(), "list", "could not enumerate Homebrew packages", err)
		}
		if res.Failed() {
			// A missing subcommand on an unusual install should not fail the
			// whole enumeration.
			continue
		}
		channel := ""
		if strings.Contains(strings.Join(args, " "), "--cask") {
			channel = ChannelCask
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			fields := shell.Fields(line)
			if len(fields) < 2 {
				continue
			}
			out = append(out, model.Installation{
				Name:     fields[0],
				Version:  fields[len(fields)-1],
				Provider: p.Name(),
				Scope:    model.ScopeUser,
			})
			_ = channel
		}
	}
	return out, nil
}

// Installed reports whether Homebrew has the package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	args := []string{"list", "--versions"}
	if pkg.Channel == ChannelCask {
		args = append(args, "--cask")
	}
	args = append(args, pkg.Name)
	res, err := p.runner.Run(ctx, "brew", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query Homebrew", err)
	}
	if res.Failed() {
		return nil, nil
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := shell.Fields(line)
		if len(fields) < 2 || fields[0] != pkg.Name {
			continue
		}
		return &model.Installation{
			Name:     pkg.Name,
			Version:  fields[len(fields)-1],
			Provider: p.Name(),
			Scope:    model.ScopeUser,
			Path:     p.binaryPath(pkg.Name),
		}, nil
	}
	return nil, nil
}

// binaryPath guesses where a formula's executable lands.
//
// Homebrew's own data model is a package list, so there is no path to read. The
// prefix is queried once and the conventional bin/<name> location is checked on
// disk; when that misses — because the formula installs a differently named
// binary — an empty path is returned rather than a guess that would be wrong.
func (p *Provider) binaryPath(formula string) string {
	prefix := p.installPrefix()
	if prefix == "" || formula == "" {
		return ""
	}
	candidate := filepath.Join(prefix, "bin", formula)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// installPrefix memoizes `brew --prefix` for the life of the provider.
func (p *Provider) installPrefix() string {
	if p.prefixKnown {
		return p.prefix
	}
	p.prefixKnown = true
	res, err := p.runner.Run(context.Background(), "brew", "--prefix")
	if err != nil || res.Failed() {
		return ""
	}
	p.prefix = strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	return p.prefix
}

// Install installs a formula or cask.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if req.Version() != "" {
		return nil, shell.Unsupported(p.Name(), "install",
			"Homebrew cannot install a specific version; remove the version pin from the configuration or let the provider choose another")
	}
	args := []string{"install"}
	if req.Package.Channel == ChannelCask {
		args = append(args, "--cask")
	}
	args = append(args, req.Package.Name)
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "brew", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run brew install", err)
	}
	if res.Failed() {
		return nil, classify(p.Name(), "install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a formula or cask.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	args := []string{"uninstall"}
	if pkg.Channel == ChannelCask {
		args = append(args, "--cask")
	}
	args = append(args, pkg.Name)
	res, err := p.runner.Run(ctx, "brew", args...)
	if err != nil {
		return shell.Failure(p.Name(), "uninstall", "could not run brew uninstall", err)
	}
	if res.Failed() {
		return classify(p.Name(), "uninstall", res)
	}
	return nil
}

// Update upgrades a formula or cask.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	args := []string{"upgrade"}
	if pkg.Channel == ChannelCask {
		args = append(args, "--cask")
	}
	args = append(args, pkg.Name)
	res, err := p.runner.Run(ctx, "brew", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run brew upgrade", err)
	}
	if res.Failed() {
		return nil, classify(p.Name(), "upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// Search finds formulae matching a query.
func (p *Provider) Search(ctx context.Context, query string) ([]string, error) {
	res, err := p.runner.Run(ctx, "brew", "search", query)
	if err != nil {
		return nil, shell.Failure(p.Name(), "search", "could not run brew search", err)
	}
	var out []string
	for _, line := range strings.Split(res.Stdout, "\n") {
		if f := shell.Fields(line); len(f) > 0 {
			out = append(out, f[0])
		}
	}
	return out, nil
}

// classify turns Homebrew's exit status into a typed error. Homebrew reports
// package-level problems on stderr with recognizable wording; mapping the common
// cases keeps doctor output useful.
func classify(name, op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "no available formula"), strings.Contains(text, "no available cask"),
		strings.Contains(text, "no formulae or casks found"):
		return shell.NotFound(name, op, "Homebrew does not know this package")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "not writable"),
		strings.Contains(text, "sudo"):
		return shell.Err(name, op, provider.ErrPermission, "Homebrew needs elevated permissions", nil)
	case strings.Contains(text, "curl"), strings.Contains(text, "network"), strings.Contains(text, "connection"):
		return shell.Err(name, op, provider.ErrNetwork, "could not reach the Homebrew index", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "brew exited nonzero"
		}
		return shell.Failure(name, op, msg, nil)
	}
}

// ExecutableToPackage returns a mapping from executable names to package names.
// For Homebrew, most formulae install an executable with the same name, but there
// are exceptions. This provides a basic mapping for common packages.
func (p *Provider) ExecutableToPackage() map[string]string {
	// This is a minimal mapping for common packages. In production, this could be
	// expanded by querying Homebrew's metadata or maintaining a larger database.
	return map[string]string{
		"git":         "git",
		"python3":     "python",
		"python":      "python",
		"node":        "node",
		"npm":         "node",
		"rustc":       "rust",
		"cargo":       "rust",
		"go":          "go",
		"ruby":        "ruby",
		"gem":         "ruby",
		"java":        "openjdk",
		"javac":       "openjdk",
		"ripgrep":     "ripgrep",
		"fd":          "fd",
		"bat":         "bat",
		"exa":         "exa",
		"eza":         "eza",
		"jq":          "jq",
		"yq":          "yq",
		"fzf":         "fzf",
		"tmux":        "tmux",
		"vim":         "vim",
		"neovim":      "neovim",
		"nvim":        "neovim",
		"htop":        "htop",
		"btop":        "btop",
		"wget":        "wget",
		"curl":        "curl",
		"ffmpeg":      "ffmpeg",
		"imagemagick": "imagemagick",
	}
}

// Verify checks that an installed executable is functional by running it.
// For Homebrew, this attempts to run the package's executable with a --version flag.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// Try to run the executable with --version to verify it's functional.
	res, err := p.runner.Run(ctx, pkg.Name, "--version")
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not run executable for verification", err)
	}
	if res.Failed() {
		return shell.Failure(p.Name(), "verify", "executable failed verification", nil)
	}
	return nil
}

var _ provider.LatestVersionProvider = (*Provider)(nil)

// LatestVersion reads the stable version Homebrew would install from
// `brew info --json=v2`.
func (p *Provider) LatestVersion(ctx context.Context, pkg model.Package) (string, error) {
	res, err := p.runner.Run(ctx, "brew", "info", "--json=v2", pkg.Name)
	if err != nil {
		return "", shell.Failure(p.Name(), "latest", "could not run brew info", err)
	}
	if res.Failed() {
		return "", shell.Failure(p.Name(), "latest", shell.Diagnostic(res.Field()), nil)
	}
	var info struct {
		Formulae []struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
		Casks []struct {
			Version string `json:"version"`
		} `json:"casks"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &info); err != nil {
		return "", shell.Failure(p.Name(), "latest", "could not parse brew info output", err)
	}
	if len(info.Formulae) > 0 {
		return info.Formulae[0].Versions.Stable, nil
	}
	if len(info.Casks) > 0 {
		return info.Casks[0].Version, nil
	}
	return "", nil
}
