// Package dnf implements the DNF (Dandified YUM) provider for Fedora/RHEL systems.
//
// DNF is the default package manager on Fedora and RHEL-based systems. It shares
// many similarities with APT but uses different commands and output formats.
// This provider follows the same pattern as the apt provider for consistency.
package dnf

import (
	"context"
	"os"
	"runtime"
	"strings"
	"unicode"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "dnf"

// Provider implements provider.Provider for DNF.
type Provider struct {
	runner   exec.Runner
	platform func() bool
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ExecutableMappingProvider = (*Provider)(nil)
var _ provider.PackageExecutableProvider = (*Provider)(nil)

// New builds a DNF provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner, platform: dnfPlatform} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "DNF (Fedora/RHEL)" }
func (p *Provider) Kind() provider.Kind { return provider.KindDnf }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true,  // via dnf install pkg-version
		Channels:     false, // DNF uses repositories, not channels
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
		h.Reason = "DNF is only supported on Fedora/RHEL-family Linux"
		return h
	}
	path, err := p.runner.LookPath("dnf")
	if err != nil {
		h.Reason = "dnf not found on PATH"
		return h
	}
	h.Binary = path
	res, err := p.runner.Run(ctx, "dnf", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if res.Failed() {
		// dnf is on PATH but cannot run. Reporting it as available would send
		// the user to a provider that fails on every subsequent command.
		h.Reason = strings.TrimSpace(res.Stderr)
		if h.Reason == "" {
			h.Reason = "dnf exited with a non-zero status"
		}
		return h
	}
	h.Available = true
	// Discovery and version checks read the rpm database, so rpm is a hard
	// dependency. Without it every lookup fails even though dnf itself works.
	if _, err := p.runner.LookPath("rpm"); err != nil {
		h.Available = false
		h.Reason = "rpm not found on PATH; dnf relies on the rpm database"
		return h
	}
	// Output looks like "4.19.1\n" or "dnf version 4.19.1"
	if fields := shell.Fields(shell.FirstLine(res.Stdout)); len(fields) >= 1 {
		// Try to extract version from the output
		for _, f := range fields {
			if strings.Contains(f, ".") {
				h.Version = f
				break
			}
		}
	}
	return h
}

// rpmFormat selects machine-readable output from rpm.
const rpmFormat = `%{NAME}\t%{VERSION}-%{RELEASE}\n`

// Discover enumerates installed rpm packages.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "rpm", "-qa", "--queryformat="+rpmFormat)
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not enumerate rpm packages", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	var out []model.Installation
	for _, line := range strings.Split(res.Stdout, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "\t", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		out = append(out, model.Installation{
			Name:     parts[0],
			Version:  parts[1],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		})
	}
	return out, nil
}

// Installed reports the rpm state of a package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "rpm", "-q", "--queryformat="+rpmFormat, pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query rpm", err)
	}
	if res.Failed() {
		return nil, nil
	}
	fields := strings.SplitN(strings.TrimSpace(res.Stdout), "\t", 2)
	if len(fields) != 2 || fields[1] == "" {
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
		target = req.Package.Name + "-" + strings.TrimPrefix(req.Version(), "=")
	}
	args := []string{"install", "-y", target}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	res, err := p.runner.Run(ctx, "dnf", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run dnf install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "dnf", "remove", "-y", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run dnf remove", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update upgrades a package to the newest available candidate.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "dnf", "upgrade", "-y", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run dnf upgrade", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "no match"), strings.Contains(text, "nothing to do"),
		strings.Contains(text, "package not found"):
		return shell.NotFound(Name, op, "DNF does not have this package in its configured repositories")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "need to be root"),
		strings.Contains(text, "not permitted"), strings.Contains(text, "superuser privileges"):
		return shell.Err(Name, op, provider.ErrPermission, "dnf needs root; re-run with elevated privileges", nil)
	case strings.Contains(text, "could not resolve"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "network is unreachable"), strings.Contains(text, "failed to synchronize cache"),
		strings.Contains(text, "cannot download repomd.xml"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the DNF repositories", nil)
	case strings.Contains(text, "dependency"), strings.Contains(text, "conflict"),
		strings.Contains(text, "transaction check error"):
		return shell.Err(Name, op, provider.ErrConflict, shell.Diagnostic(res.Field()), nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "dnf exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// ExecutableToPackage returns a mapping from executable names to package names.
// For DNF, most packages install an executable with the same name, but there are
// exceptions. This provides a basic mapping for common packages.
func (p *Provider) ExecutableToPackage() map[string]string {
	return map[string]string{
		"git":            "git",
		"python3":        "python3",
		"python":         "python3",
		"node":           "nodejs",
		"npm":            "npm",
		"rustc":          "rust",
		"cargo":          "cargo",
		"go":             "golang",
		"ruby":           "ruby",
		"gem":            "rubygems",
		"java":           "java-latest-openjdk-devel",
		"javac":          "java-latest-openjdk-devel",
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
		"imagemagick":    "ImageMagick",
		"docker":         "docker",
		"docker-compose": "docker-compose",
	}
}

// Verify checks that an installed executable is functional by running it.
// For DNF, this attempts to run the package's executable with a --version flag.
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

func dnfPlatform() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return false
	}
	ids := osReleaseIDs(string(data))
	for _, id := range []string{"fedora", "rhel", "centos", "rocky", "almalinux", "ol", "amzn", "nobara"} {
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

var _ provider.Privileged = (*Provider)(nil)

// Privilege classifies the provider for an elevated run: it runs as root.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.RootPrivilege("dnf installs system packages, which needs root")
}

var _ provider.RequestedReporter = (*Provider)(nil)

// Requested reads the packages DNF records as installed by the user
// (`dnf repoquery --userinstalled`); every other installed package came in
// as a dependency. The format ends in a space rather than "\n" because DNF 4
// adds a newline after each entry and DNF 5 adds nothing, and splitting on
// whitespace reads both.
func (p *Provider) Requested(ctx context.Context, installed []model.Installation) (map[string]bool, error) {
	res, err := p.runner.Run(ctx, "dnf", "repoquery", "--userinstalled", "--queryformat", "%{name} ")
	if err != nil {
		return nil, shell.Failure(p.Name(), "requested", "could not run dnf repoquery --userinstalled", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "requested", shell.Diagnostic(res.Field()), nil)
	}
	return shell.RequestedFrom(strings.Fields(res.Stdout), installed), nil
}
