// Package pip implements the pip provider for Python packages installed with
// pip itself.
//
// pip installs into whichever interpreter owns it, which is why this provider is
// careful about two things. First, which interpreter: `pip` and `pip3` on the
// same machine can belong to different Pythons, so both are probed and the
// interpreter is named in diagnostics. Second, where: pip needs elevation to
// write a system site-packages, so every install is `--user` by default and the
// placement reported is the user base, which is the only one pip can write
// without root.
//
// uv and pip are deliberately separate providers. uv replaces the *workflow*;
// pip remains the tool that owns a site-packages tree, and a machine can have
// software installed by each.
package pip

import (
	"context"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "pip"

// Provider implements provider.Provider for pip.
type Provider struct {
	runner exec.Runner

	// binary is the pip executable to invoke: "pip3" when present, else "pip".
	binary string
	// python is the interpreter pip belongs to, for diagnostics.
	python string
	// binDir caches the user-base bin directory once it is known.
	binDir string
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
)

// New builds a pip provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "pip (Python packages)" }
func (p *Provider) Kind() provider.Kind { return provider.KindPython }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true,            // name==version
		DefaultScope: model.ScopeUser, // installs with --user
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, binary, err := p.locate()
	if err != nil {
		h.Reason = "pip not found on PATH"
		return h
	}
	p.binary = binary
	h.Available = true
	h.Binary = path
	res, runErr := p.runner.Run(ctx, p.binary, "--version")
	if runErr != nil {
		h.Reason = runErr.Error()
		return h
	}
	// "pip 26.2.1 from /usr/local/lib/python3.14/site-packages/pip (python 3.14)"
	h.Version = shell.VersionFromArgs(shell.Fields(shell.FirstLine(res.Stdout))...)
	if interpreter := strings.TrimSpace(p.interpreterFrom(res.Stdout)); interpreter != "" {
		h.Reason = "installs into " + interpreter + "'s user site-packages"
	}
	return h
}

// locate finds a usable pip executable, preferring the version-specific name.
func (p *Provider) locate() (string, string, error) {
	if p.binary != "" {
		return "", p.binary, nil
	}
	for _, candidate := range []string{"pip3", "pip"} {
		if path, err := p.runner.LookPath(candidate); err == nil {
			// Remember it: every later command must use the same interpreter's
			// pip, not whichever name happens to appear in a config entry.
			p.binary = candidate
			p.python = candidate
			return path, candidate, nil
		}
	}
	return "", "", shell.NotFound(Name, "locate", "neither pip3 nor pip is on PATH")
}

// interpreterFrom extracts the interpreter from `pip --version`.
func (p *Provider) interpreterFrom(out string) string {
	open := strings.LastIndex(out, "(python ")
	if open < 0 {
		return ""
	}
	rest := out[open+len("(python "):]
	if end := strings.Index(rest, ")"); end > 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// Discover parses `pip list --format=freeze`, whose lines are `name==version`.
//
// That list is every distribution the interpreter can import: the user site
// and the interpreter's own site-packages alike. Only what `pip list --user`
// also names is in the user site; everything else belongs to the interpreter
// (a system or image-wide Python, a package manager's python3-* packages, pip
// itself) and is reported as global scope, so prune never treats it as the
// user's orphan. When the user listing cannot be read, nothing is claimed for
// the user: the conservative answer is "global".
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, nil
	}
	res, err := p.runner.Run(ctx, p.binary, "list", "--format=freeze")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list installed Python packages", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	all := p.parseFreeze(res.Stdout)
	user := map[string]bool{}
	if ures, uerr := p.runner.Run(ctx, p.binary, "list", "--user", "--format=freeze"); uerr == nil && !ures.Failed() {
		for _, inst := range p.parseFreeze(ures.Stdout) {
			user[normalize(inst.Name)] = true
		}
	}
	for i := range all {
		if !user[normalize(all[i].Name)] {
			all[i].Scope = model.ScopeGlobal
		}
	}
	return all, nil
}

// parseFreeze reads `name==version` lines.
func (p *Provider) parseFreeze(out string) []model.Installation {
	res := []model.Installation{}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, version, found := strings.Cut(trimmed, "==")
		if !found || name == "" {
			continue
		}
		res = append(res, model.Installation{
			Name:     name,
			Version:  version,
			Provider: p.Name(),
			Scope:    model.ScopeUser,
		})
	}
	return res
}

// Installed resolves one package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	all, err := p.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range all {
		if strings.EqualFold(normalize(inst.Name), normalize(pkg.Name)) {
			found := inst
			found.Path = p.installPath(ctx, inst.Name)
			return &found, nil
		}
	}
	return nil, nil
}

// installPath reports where an installed distribution lives, from
// `pip show -f`: its console script when it has one (that is what a user runs
// and what verification needs to find), otherwise its top-level package.
//
// An empty result means pip could not say; that is reported as unknown rather
// than guessed.
func (p *Provider) installPath(ctx context.Context, name string) string {
	res, err := p.runner.Run(ctx, p.binary, "show", "-f", name)
	if err != nil || res.Failed() {
		return ""
	}
	return pathFromShow(res.Stdout, name)
}

// pathFromShow picks the most useful location out of `pip show -f` output.
// Files are listed relative to Location, so a console script appears as
// ../../../bin/<name>.
func pathFromShow(out, name string) string {
	var (
		location string
		files    []string
		inFiles  bool
	)
	for _, line := range strings.Split(out, "\n") {
		if inFiles {
			if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
				if f := strings.TrimSpace(line); f != "" {
					files = append(files, f)
				}
				continue
			}
			inFiles = false
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Location":
			location = strings.TrimSpace(value)
		case "Files":
			inFiles = true
		}
	}
	if location == "" {
		return ""
	}
	resolve := func(rel string) string {
		return filepath.Clean(filepath.Join(location, filepath.FromSlash(rel)))
	}

	var scripts []string
	for _, f := range files {
		slashed := filepath.ToSlash(f)
		dir := pathpkg.Base(pathpkg.Dir(slashed))
		if dir == "bin" || dir == "Scripts" {
			scripts = append(scripts, resolve(f))
		}
	}
	want := normalize(name)
	for _, s := range scripts {
		base := strings.TrimSuffix(filepath.Base(s), ".exe")
		if normalize(base) == want {
			return s
		}
	}
	if len(scripts) > 0 {
		return scripts[0]
	}
	for _, f := range files {
		slashed := filepath.ToSlash(f)
		if strings.HasPrefix(slashed, "..") {
			continue
		}
		top := strings.SplitN(slashed, "/", 2)[0]
		if strings.HasSuffix(top, ".dist-info") || strings.HasSuffix(top, ".egg-info") || top == "__pycache__" {
			continue
		}
		return resolve(top)
	}
	return location
}

// normalize applies PEP 503 name normalisation, so "Pillow", "pillow" and
// "python_dateutil"/"python-dateutil" compare equal.
func normalize(name string) string {
	name = strings.ToLower(name)
	return strings.NewReplacer("_", "-", ".", "-").Replace(name)
}

// Install installs into the user site, which is the only location pip can write
// without elevation. An explicit version becomes a PEP 440 pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, shell.Unsupported(p.Name(), "install", "pip is not installed")
	}
	target := req.Package.Name
	if req.Version() != "" {
		target += "==" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, p.binary, "install", "--user", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run pip install", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	if _, _, err := p.locate(); err != nil {
		return shell.Unsupported(p.Name(), "remove", "pip is not installed")
	}
	res, err := p.runner.Run(ctx, p.binary, "uninstall", "-y", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run pip uninstall", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	return nil
}

// Update upgrades a package within the user site.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, shell.Unsupported(p.Name(), "upgrade", "pip is not installed")
	}
	args := []string{"install", "--user", "--upgrade"}
	if pkg.Version != "" {
		args = append(args, pkg.Name+"=="+strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "="))
	} else {
		args = append(args, pkg.Name)
	}
	res, err := p.runner.Run(ctx, p.binary, args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run pip install --upgrade", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// classify turns pip's output into typed errors.
func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "no matching distribution found"),
		strings.Contains(text, "could not find a version"),
		strings.Contains(text, "no such file or directory: /"):
		return shell.NotFound(Name, op, "pip could not find that package")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "externally-managed"),
		strings.Contains(text, "defaulting to user installation"):
		return shell.Err(Name, op, provider.ErrPermission,
			"pip cannot write this interpreter's site-packages", nil)
	case strings.Contains(text, "connection"), strings.Contains(text, "network"),
		strings.Contains(text, "pypi.org"), strings.Contains(text, "temporary failure"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach PyPI", nil)
	case strings.Contains(text, "conflict"), strings.Contains(text, "dependency"),
		strings.Contains(text, "resolutionimpossible"):
		return shell.Err(Name, op, provider.ErrConflict, shell.Diagnostic(res.Field()), nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "pip exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Verify checks that an installed Python package is functional.
// For pip, this attempts to run the package's executable with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	if _, _, err := p.locate(); err != nil {
		return shell.Unsupported(p.Name(), "verify", "pip is not installed")
	}
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

// ExecutableDir reports where `pip install --user` puts console scripts: the
// bin directory of the interpreter's user base. On Linux that is ~/.local/bin,
// the same directory uv links its tools into, which is why a move between the
// two has to know about it.
func (p *Provider) ExecutableDir(ctx context.Context) string {
	if p.binDir != "" {
		return p.binDir
	}
	if runtime.GOOS == "windows" {
		// The Windows user scheme nests scripts under a versioned directory;
		// rather than guess, report that the location is unknown.
		return ""
	}
	base := exec.Getenv(p.runner, "PYTHONUSERBASE")
	if base == "" {
		base = p.userBase(ctx)
	}
	if base == "" {
		return ""
	}
	p.binDir = filepath.Join(base, "bin")
	return p.binDir
}

// userBase asks the interpreter pip belongs to for its user base.
func (p *Provider) userBase(ctx context.Context) string { return p.site(ctx, "--user-base") }

var _ provider.StateDirProvider = (*Provider)(nil)

// StateDirs reports the user site-packages directory (`python -m site
// --user-site`), where `pip install --user` puts the packages themselves.
// It is only ever checked for root-owned entries under the invoking user's
// home, after pip ran as that user.
func (p *Provider) StateDirs(ctx context.Context) []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	if dir := p.site(ctx, "--user-site"); filepath.IsAbs(dir) {
		return []string{dir}
	}
	return nil
}

// site asks the interpreter pip belongs to for one of its user-scheme
// directories.
func (p *Provider) site(ctx context.Context, flag string) string {
	if _, _, err := p.locate(); err != nil {
		return ""
	}
	var candidates []string
	if res, err := p.runner.Run(ctx, p.binary, "--version"); err == nil && !res.Failed() {
		if version := strings.TrimSpace(p.interpreterFrom(res.Stdout)); version != "" {
			candidates = append(candidates, "python"+version)
		}
	}
	candidates = append(candidates, "python3")
	for _, python := range candidates {
		if _, err := p.runner.LookPath(python); err != nil {
			continue
		}
		res, err := p.runner.Run(ctx, python, "-m", "site", flag)
		if err != nil || res.Failed() {
			continue
		}
		if base := strings.TrimSpace(shell.FirstLine(res.Stdout)); base != "" {
			return base
		}
	}
	return ""
}

var _ provider.LatestVersionProvider = (*Provider)(nil)

// LatestVersion asks the index which version pip would install, from
// `pip index versions`, whose first line is "name (x.y.z)".
func (p *Provider) LatestVersion(ctx context.Context, pkg model.Package) (string, error) {
	if _, _, err := p.locate(); err != nil {
		return "", shell.Unsupported(p.Name(), "latest", "pip is not installed")
	}
	res, err := p.runner.Run(ctx, p.binary, "index", "versions", pkg.Name)
	if err != nil {
		return "", shell.Failure(p.Name(), "latest", "could not run pip index versions", err)
	}
	if res.Failed() {
		return "", p.classify("latest", res)
	}
	return parenthesisedVersion(shell.FirstLine(res.Stdout)), nil
}

// parenthesisedVersion extracts "x.y.z" from "name (x.y.z)".
func parenthesisedVersion(line string) string {
	open := strings.Index(line, "(")
	end := strings.Index(line, ")")
	if open < 0 || end <= open {
		return ""
	}
	return strings.TrimSpace(strings.Fields(line[open+1:end] + " ")[0])
}

var _ provider.Privileged = (*Provider)(nil)

// Privilege classifies the provider for an elevated run: it runs as the
// invoking user.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.UserPrivilege("pip installs with --user, into the user's site-packages and ~/.local/bin")
}
