// Package yarn implements the yarn provider for globally installed packages,
// and — more importantly — for being honest about the versions that have no
// global package manager at all.
//
// yarn classic (1.x) has `yarn global add`, `yarn global remove` and a global
// directory full of packages. Yarn Berry (2.x and later, including 3, 4 and 5)
// deliberately has no global commands: projects are installed with Corepack and
// Plug'n'Play, and "global" is not part of its model. A provider that assumed the
// classic commands would plan operations that cannot work.
//
// So the capability set is decided by the detected major version, and a Berry
// installation reports that it cannot manage globals rather than pretending.
package yarn

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
const Name = "yarn"

// BerryMajor is the first yarn major version without global commands.
const BerryMajor = 2

// Provider implements provider.Provider for yarn globals.
type Provider struct {
	runner exec.Runner

	// version and major are the detected yarn version, or zero when undetected.
	version string
	major   int
	// detected records whether the version has been probed.
	detected bool
	// globalDir is where yarn classic installs globals.
	globalDir string
}

var _ provider.Provider = (*Provider)(nil)

// New builds a yarn provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "yarn (global packages, classic only)" }
func (p *Provider) Kind() provider.Kind { return provider.KindNode }

// Capabilities reports what this yarn can actually do. Berry advertises nothing,
// which is what keeps the resolver from choosing it for an install.
func (p *Provider) Capabilities() provider.Capabilities {
	if p.major == 0 && !p.detected {
		// Before the version is known, assume classic so the capability set is not
		// wrong for a yarn that turns out to be 1.x. Berry is corrected the moment
		// the version is read.
		return classicCapabilities()
	}
	if p.major >= BerryMajor {
		return provider.Capabilities{DefaultScope: model.ScopeUser}
	}
	return classicCapabilities()
}

// classicCapabilities is what yarn 1.x can do.
func classicCapabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true, // name@version, as npm
		DefaultScope: model.ScopeUser,
	}
}

// detect resolves the major version once, so the provider's behaviour does not
// depend on Health having been called first. Whether yarn can manage globals at
// all is decided by that number, and getting it wrong means planning commands
// that cannot exist.
func (p *Provider) detect(ctx context.Context) {
	if p.detected {
		return
	}
	p.detected = true
	if _, err := p.runner.LookPath("yarn"); err != nil {
		return
	}
	res, err := p.runner.Run(ctx, "yarn", "--version")
	if err != nil || res.Failed() {
		return
	}
	p.version = strings.TrimSpace(shell.FirstLine(res.Stdout))
	p.major = MajorVersion(p.version)
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, err := p.runner.LookPath("yarn")
	if err != nil {
		h.Reason = "yarn not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	p.detect(ctx)
	h.Version = p.version
	if p.version == "" {
		h.Reason = "could not read yarn's version"
		return h
	}
	if p.major >= BerryMajor {
		h.Capabilities = p.Capabilities()
		h.Reason = "yarn " + h.Version + " has no global package manager; " +
			"it installs into projects, so Starpoint cannot manage software with it"
		return h
	}
	h.Capabilities = p.Capabilities()
	return h
}

// MajorVersion extracts the major version number from a yarn version string.
func MajorVersion(v string) int {
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, ".-+"); i > 0 {
		v = v[:i]
	}
	n := 0
	for _, r := range v {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// Classic reports whether this yarn has global commands.
func (p *Provider) Classic() bool { return p.major > 0 && p.major < BerryMajor }

// Discover reads the global directory yarn classic maintains.
//
// The directory is read directly rather than through `yarn global list`, whose
// table output changes shape between classic releases. node_modules and each
// package's own package.json are a stable contract.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	p.detect(ctx)
	if !p.Classic() {
		return nil, nil
	}
	dir, err := p.resolveGlobalDir(ctx)
	if err != nil || dir == "" {
		return nil, err
	}
	modules := filepath.Join(dir, "node_modules")
	entries, err := os.ReadDir(modules)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, shell.Failure(p.Name(), "list", "could not read "+modules, err)
	}
	var out []model.Installation
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name, version, ok := readPackageJSON(filepath.Join(modules, e.Name(), "package.json"))
		if !ok {
			continue
		}
		out = append(out, model.Installation{
			Name:     name,
			Version:  version,
			Provider: p.Name(),
			Scope:    model.ScopeUser,
		})
	}
	return out, nil
}

// readPackageJSON reads the name and version from a package manifest.
func readPackageJSON(path string) (string, string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	var manifest struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", "", false
	}
	if manifest.Name == "" {
		return "", "", false
	}
	return manifest.Name, manifest.Version, true
}

// Installed resolves one global package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	all, err := p.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range all {
		if inst.Name == pkg.Name {
			found := inst
			return &found, nil
		}
	}
	return nil, nil
}

// Install installs a global package. Only classic yarn can.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	p.detect(ctx)
	if !p.Classic() {
		return nil, shell.Unsupported(p.Name(), "install",
			"this yarn version has no global package manager; yarn "+p.versionText()+
				" installs into projects, not into a user-wide set of packages")
	}
	target := req.Package.Name
	if req.Version() != "" {
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	} else if req.Package.Channel != "" {
		target += "@" + req.Package.Channel
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "yarn", "global", "add", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run yarn global add", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a global package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	p.detect(ctx)
	if !p.Classic() {
		return shell.Unsupported(p.Name(), "remove", "this yarn version cannot remove global packages")
	}
	res, err := p.runner.Run(ctx, "yarn", "global", "remove", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run yarn global remove", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	return nil
}

// Update upgrades a global package. Classic yarn has no global update command;
// reinstalling at the latest matching version is the upgrade.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	p.detect(ctx)
	if !p.Classic() {
		return nil, shell.Unsupported(p.Name(), "upgrade", "this yarn version cannot update global packages")
	}
	target := pkg.Name
	if pkg.Version != "" {
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	}
	res, err := p.runner.Run(ctx, "yarn", "global", "add", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run yarn global add", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// resolveGlobalDir asks classic yarn where its globals live.
func (p *Provider) resolveGlobalDir(ctx context.Context) (string, error) {
	if p.globalDir != "" {
		return p.globalDir, nil
	}
	res, err := p.runner.Run(ctx, "yarn", "global", "dir")
	if err != nil {
		return "", shell.Failure(p.Name(), "list", "could not locate yarn's global directory", err)
	}
	if res.Failed() {
		return "", shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	p.globalDir = strings.TrimSpace(shell.FirstLine(res.Stdout))
	return p.globalDir, nil
}

func (p *Provider) versionText() string {
	if p.major == 0 {
		return "(unversioned)"
	}
	return "v" + itoa(p.major)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "couldn't find"), strings.Contains(text, "not found"),
		strings.Contains(text, "no such package"):
		return shell.NotFound(Name, op, "yarn does not have that global package")
	case strings.Contains(text, "already exists"), strings.Contains(text, "already installed"):
		return shell.Err(Name, op, provider.ErrConflict, "the package is already installed globally", nil)
	case strings.Contains(text, "permission denied"), strings.Contains(text, "eacces"):
		return shell.Err(Name, op, provider.ErrPermission, "yarn's global directory is not writable", nil)
	case strings.Contains(text, "network"), strings.Contains(text, "enotfound"),
		strings.Contains(text, "registry.yarnpkg.com"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the registry", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "yarn exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Verify checks that an installed yarn package's executable is functional.
// For yarn, this attempts to run the package's executable with --version.
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
