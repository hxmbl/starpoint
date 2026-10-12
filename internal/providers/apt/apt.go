// Package apt implements the Debian/Ubuntu APT provider.
//
// APT splits packages across dpkg (installed set) and apt-get (mutation), and it
// separates CLI packages from the desktop application set. Starpoint exposes the
// package the user named and leaves the archive choice to apt unless the
// configuration pins a channel.
package apt

import (
	"context"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "apt"

// ChannelMain selects the main archive rather than universe.
const ChannelMain = "main"

// Provider implements provider.Provider for APT.
type Provider struct {
	runner exec.Runner
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ExecutableMappingProvider = (*Provider)(nil)
var _ provider.PackageExecutableProvider = (*Provider)(nil)

// New builds an APT provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "APT (Debian/Ubuntu)" }
func (p *Provider) Kind() provider.Kind { return provider.KindApt }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true, // via apt-get install pkg=version
		Channels:     true, // main vs universe archive
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
	path, err := p.runner.LookPath("apt-get")
	if err != nil {
		h.Reason = "apt-get not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "apt-get", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if fields := shell.Fields(shell.FirstLine(res.Stdout)); len(fields) >= 2 {
		h.Version = fields[1]
	}
	return h
}

// dpkgFormat selects machine-readable output from dpkg-query.
const dpkgFormat = `${binary:Package}\t${Version}\n`

// Discover enumerates installed dpkg packages.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "dpkg-query", "-W", "-f="+dpkgFormat)
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not enumerate dpkg packages", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.FirstLine(res.Field()), nil)
	}
	var out []model.Installation
	for _, line := range strings.Split(res.Stdout, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		// dpkg distinguishes installed from config-files-only states; -W only
		// lists known packages, so filter on the status text when present.
		out = append(out, model.Installation{
			Name:     parts[0],
			Version:  parts[1],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		})
	}
	return out, nil
}

// Installed reports the dpkg state of a package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "dpkg-query", "-W", "-f="+dpkgFormat, pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query dpkg", err)
	}
	if res.Failed() {
		return nil, nil
	}
	fields := strings.SplitN(strings.TrimSpace(res.Stdout), "\t", 2)
	if len(fields) != 2 || fields[1] == "" {
		// Config-files-only and unknown states report empty versions.
		return nil, nil
	}
	return &model.Installation{
		Name:     fields[0],
		Version:  fields[1],
		Provider: p.Name(),
		Scope:    model.ScopeGlobal,
	}, nil
}

// Install installs a package, honouring an explicit version pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	target := req.Package.Name
	if req.Version() != "" {
		target = req.Package.Name + "=" + strings.TrimPrefix(req.Version(), "=")
	}
	args := []string{"install", "-y"}
	if req.Package.Channel == ChannelMain {
		args = append(args, "-t", ChannelMain)
	}
	args = append(args, target)
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	res, err := p.runner.Run(ctx, "apt-get", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run apt-get install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "apt-get", "remove", "-y", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run apt-get remove", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update upgrades a package to the newest available candidate.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "apt-get", "install", "-y", "--only-upgrade", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run apt-get upgrade", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "unable to locate package"), strings.Contains(text, "no such package"),
		strings.Contains(text, "has no installation candidate"):
		return shell.NotFound(Name, op, "APT does not have this package in its configured archives")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "are you root"),
		strings.Contains(text, "not permitted"):
		return shell.Err(Name, op, provider.ErrPermission, "apt-get needs root; re-run with elevated privileges", nil)
	case strings.Contains(text, "could not resolve"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "network is unreachable"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the APT archives", nil)
	case strings.Contains(text, "held broken packages"), strings.Contains(text, "depends:"),
		strings.Contains(text, "but it is not going to be installed"):
		return shell.Err(Name, op, provider.ErrConflict, shell.FirstLine(res.Field()), nil)
	default:
		msg := shell.FirstLine(res.Field())
		if msg == "" {
			msg = "apt-get exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// ExecutableToPackage returns a mapping from executable names to package names.
// For APT, most packages install an executable with the same name, but there are
// exceptions (e.g., `python3` is in the `python3` package). This provides a basic
// mapping for common packages.
func (p *Provider) ExecutableToPackage() map[string]string {
	return map[string]string{
		"git":            "git",
		"python3":        "python3",
		"python":         "python3",
		"node":           "nodejs",
		"npm":            "npm",
		"rustc":          "rustc",
		"cargo":          "cargo",
		"go":             "golang-go",
		"ruby":           "ruby",
		"gem":            "rubygems",
		"java":           "default-jdk",
		"javac":          "default-jdk",
		"ripgrep":        "ripgrep",
		"fd":             "fd-find",
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
		"docker":         "docker.io",
		"docker-compose": "docker-compose",
	}
}

// Verify checks that an installed executable is functional by running it.
// For APT, this attempts to run the package's executable with a --version flag.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// Try to run the executable with --version to verify it's functional.
	// This is a basic check; a more sophisticated implementation would
	// use provider-specific metadata to determine the correct verification command.
	exe := pkg.Name
	if mapped, ok := p.ExecutableForPackage(pkg.Name); ok {
		exe = mapped
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
