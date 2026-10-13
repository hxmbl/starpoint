// Package rustup implements the rustup provider for Rust toolchains.
//
// rustup and Cargo share an ecosystem but not a job. `cargo install` adds
// third-party crates; `rustup toolchain install` provides the compiler
// toolchain. On a rustup machine rustc, cargo and rustfmt belong to rustup, not
// to Cargo, and saying otherwise is how a machine ends up with two compilers and
// no idea which one is in use. This provider exists to make that distinction
// visible, and it deliberately does not duplicate Cargo's package-install
// responsibility: an entry that names a crate is Cargo's business, and rustup
// reports nothing for it.
//
// A configuration entry names one of two things:
//
//   - a toolchain — "stable", "nightly", "beta", "1.99.0", "nightly-2024-01-01".
//     This is what rustup installs, updates and removes.
//   - a toolchain binary — "rustc", "cargo", "rustfmt" and friends. This is
//     resolved through the toolchain that provides it, which is always an
//     installed one.
//
// Two behaviours of the real tool matter more than they look:
//
// `rustup which --toolchain <missing> <bin>` will download that toolchain. A
// read-only lookup must never be able to install software, so paths are only ever
// resolved for a toolchain that `rustup toolchain list` already reported.
//
// `rustup toolchain uninstall <missing>` exits zero. It reports absence and
// treats it as success, so asking it to remove something that is not there proves
// nothing. Removal therefore checks the installed set itself.
package rustup

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "rustup"

// DefaultChannel is the toolchain installed when an entry names neither a
// channel nor a version. "stable" is what a Rust developer means by "I want Rust".
const DefaultChannel = "stable"

// channels are the rolling release tracks rustup knows by name.
var channels = map[string]bool{
	DefaultChannel: true,
	"beta":         true,
	"nightly":      true,
}

// toolchainBinaries are the executables a rustup toolchain provides. An entry
// naming one of these resolves through the toolchain that provides it, which is
// what makes `[[package]] name = "rustc"` meaningful.
//
// The list is explicit rather than discovered because knowing it is what lets
// Installed answer without shelling out to `rustup which` for every unknown name.
var toolchainBinaries = map[string]bool{
	"cargo":         true,
	"cargo-clippy":  true,
	"cargo-fmt":     true,
	"clippy-driver": true,
	"rust-analyzer": true,
	"rust-gdb":      true,
	"rust-gdbgui":   true,
	"rust-lldb":     true,
	"rustc":         true,
	"rustdoc":       true,
	"rustfmt":       true,
}

// Provider implements provider.Provider for rustup.
type Provider struct {
	runner exec.Runner
	// home is the user home directory, kept for diagnostics only.
	home string

	// toolchainCache holds the installed toolchains for the life of the
	// provider. Every lookup needs the installed set, and shelling out to
	// `rustup toolchain list` per lookup would make planning noticeably slow.
	toolchainCache []toolchain
	toolchainKnown bool
	// tripleCache is the host triple rustup appends to toolchain names.
	tripleCache string
	tripleKnown bool
}

var _ provider.Provider = (*Provider)(nil)

// New builds a rustup provider.
func New(runner exec.Runner, home string) *Provider { return &Provider{runner: runner, home: home} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "rustup (Rust toolchains)" }
func (p *Provider) Kind() provider.Kind { return provider.KindRust }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true, // `rustup toolchain install` again pulls a newer build.
		PinVersion:   true, // a concrete toolchain such as 1.99.0 is an exact pin
		Channels:     true, // stable, beta, nightly
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
	path, err := p.runner.LookPath("rustup")
	if err != nil {
		h.Reason = "rustup not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "rustup", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	// "rustup 1.29.1 (2026-08-13)" is followed by an informational paragraph about
	// which rustc is active; the first line is the tool's own version.
	h.Version = shell.VersionFromArgs(shell.Fields(shell.FirstLine(res.Stdout))...)
	return h
}

// present reports whether rustup itself is on PATH. A provider that is not
// installed owns nothing, and saying so without running anything is both cheaper
// and more accurate than letting a command fail.
func (p *Provider) present() bool {
	_, err := p.runner.LookPath("rustup")
	return err == nil
}

// toolchain is one installed toolchain.
type toolchain struct {
	// Full is the name rustup prints, including the host triple:
	// "stable-x86_64-apple-darwin".
	Full string
	// Name is the name a person writes in configuration: "stable", "1.99.0".
	Name string
	// Path is the toolchain directory.
	Path string
	// Active is true when this toolchain is the one in use.
	Active bool
	// Default is true when this toolchain is rustup's default.
	Default bool
}

// Toolchains returns the installed toolchains, cached for the life of the
// provider.
func (p *Provider) Toolchains(ctx context.Context) []toolchain {
	if p.toolchainKnown {
		return p.toolchainCache
	}
	p.toolchainKnown = true
	p.toolchainCache = nil
	if !p.present() {
		return nil
	}
	// The verbose form is used because it prints the toolchain directory, which
	// is the honest "where is this installed" answer for `starpoint list`.
	res, err := p.runner.Run(ctx, "rustup", "toolchain", "list", "-v")
	if err != nil || res.Failed() {
		return nil
	}
	p.toolchainCache = parseToolchainList(res.Stdout, p.hostTriple(ctx))
	return p.toolchainCache
}

// invalidate drops the cached toolchains so the next read sees the result of a
// mutation.
func (p *Provider) invalidate() {
	p.toolchainKnown = false
	p.toolchainCache = nil
}

// parseToolchainList reads `rustup toolchain list -v`.
//
// Lines look like one of:
//
//	stable-x86_64-apple-darwin (active, default) /path/to/toolchain
//	nightly-x86_64-unknown-linux-gnu
//
// Flags live in a parenthesised group and the verbose path, when present, is the
// final field. The host triple is stripped from the name so that a configuration
// entry can say "stable".
func parseToolchainList(out, triple string) []toolchain {
	var res []toolchain
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		flags := ""
		if open := strings.Index(line, "("); open >= 0 {
			if close := strings.Index(line[open:], ")"); close > 0 {
				flags = line[open+1 : open+close]
				line = strings.TrimSpace(line[:open] + line[open+close+1:])
			}
		}
		path := ""
		if fields := shell.Fields(line); len(fields) > 0 {
			if last := fields[len(fields)-1]; strings.HasPrefix(last, "/") {
				path = last
				line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), last))
			}
		}
		full := strings.TrimSpace(line)
		if full == "" {
			continue
		}
		res = append(res, toolchain{
			Full:    full,
			Name:    trimTriple(full, triple),
			Path:    path,
			Active:  strings.Contains(flags, "active"),
			Default: strings.Contains(flags, "default"),
		})
	}
	return res
}

// trimTriple removes the host triple rustup appends to toolchain names. When the
// triple is unknown the full name is kept: a longer name is harmless, a wrongly
// shortened one would name a toolchain that does not exist.
func trimTriple(full, triple string) string {
	if triple == "" {
		return full
	}
	return strings.TrimSuffix(full, "-"+triple)
}

// hostTriple returns the host triple rustup appends to toolchain names, for
// example "x86_64-apple-darwin".
func (p *Provider) hostTriple(ctx context.Context) string {
	if p.tripleKnown {
		return p.tripleCache
	}
	p.tripleKnown = true
	if !p.present() {
		return ""
	}
	res, err := p.runner.Run(ctx, "rustup", "show")
	if err != nil || res.Failed() {
		return ""
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Default host:"); ok {
			p.tripleCache = strings.TrimSpace(rest)
			return p.tripleCache
		}
	}
	return ""
}

// lookup finds an installed toolchain by the name a person would write, or by
// the full name rustup prints.
func (p *Provider) lookup(name string) (toolchain, bool) {
	if name == "" {
		return toolchain{}, false
	}
	for _, tc := range p.toolchainCache {
		if tc.Name == name || tc.Full == name {
			return tc, true
		}
	}
	return toolchain{}, false
}

// selected returns the toolchain that provides a toolchain binary: the default
// one, falling back to whichever is active.
func (p *Provider) selected() (toolchain, bool) {
	for _, tc := range p.toolchainCache {
		if tc.Default {
			return tc, true
		}
	}
	for _, tc := range p.toolchainCache {
		if tc.Active {
			return tc, true
		}
	}
	return toolchain{}, false
}

// Discover reports the installed toolchains. A concrete toolchain carries its own
// version in its name; a channel such as "stable" does not, and inventing one
// would be a lie about what is installed.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	if !p.present() {
		return nil, nil
	}
	tcs := p.Toolchains(ctx)
	out := make([]model.Installation, 0, len(tcs))
	for _, tc := range tcs {
		out = append(out, model.Installation{
			Name:     tc.Name,
			Version:  ConcreteVersion(tc.Name),
			Provider: p.Name(),
			Scope:    p.Capabilities().DefaultScope,
			Path:     tc.Path,
		})
	}
	return out, nil
}

// ConcreteVersion returns the version encoded in a toolchain name, and "" for a
// channel. "1.99.0" is a version; "stable" is a promise.
func ConcreteVersion(name string) string {
	if channels[name] {
		return ""
	}
	first := strings.SplitN(name, "-", 2)[0]
	if strings.Trim(first, "0123456789.") == "" && strings.ContainsAny(first, "0123456789") {
		return first
	}
	return ""
}

// IsToolchainBinary reports whether name is an executable a rustup toolchain
// provides. It is exported because the resolver needs the same vocabulary the
// provider uses.
func IsToolchainBinary(name string) bool { return toolchainBinaries[name] }

// IsToolchainName reports whether an entry names something rustup installs
// directly: a channel, a dated channel, or a concrete release.
func IsToolchainName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	if channels[name] || IsToolchainBinary(name) {
		return channels[name]
	}
	return ConcreteVersion(name) != ""
}

// Installed resolves one entry: an installed toolchain, a toolchain binary
// resolved through the toolchain that provides it, or rustup itself.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	if !p.present() {
		return nil, nil
	}
	p.Toolchains(ctx)

	if tc, ok := p.lookup(pkg.Name); ok {
		inst := model.Installation{
			Name:     tc.Name,
			Version:  p.toolchainRelease(ctx, tc),
			Provider: p.Name(),
			Scope:    p.Capabilities().DefaultScope,
			Path:     tc.Path,
		}
		return &inst, nil
	}

	// rustup's own executable: not part of a toolchain, but the one piece of
	// software here that no other provider can own.
	if pkg.Name == "rustup" {
		inst := &model.Installation{
			Name:     "rustup",
			Version:  p.rustupVersion(ctx),
			Provider: p.Name(),
			Scope:    p.Capabilities().DefaultScope,
		}
		if path, err := p.runner.LookPath("rustup"); err == nil {
			inst.Path = path
		}
		return inst, nil
	}

	if IsToolchainBinary(pkg.Name) {
		tc, ok := p.selected()
		if !ok {
			// rustup is installed but no toolchain is: it owns nothing yet.
			return nil, nil
		}
		return &model.Installation{
			Name:     pkg.Name,
			Version:  p.toolchainRelease(ctx, tc),
			Provider: p.Name(),
			Scope:    p.Capabilities().DefaultScope,
			Path:     p.binaryPath(ctx, tc, pkg.Name),
		}, nil
	}
	return nil, nil
}

// Install installs a toolchain. For an entry naming a toolchain binary this
// installs the toolchain that provides it.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	target := InstallTarget(req.Package)
	res, err := p.runner.Run(ctx, "rustup", "toolchain", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run rustup toolchain install", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	// Re-read the installed set: the toolchain that was just installed is the one
	// that answers for the entry.
	p.invalidate()
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a toolchain.
//
// rustup reports success when asked to uninstall something that is not installed,
// so the installed set is checked first: a removal that did nothing must not be
// reported as one that did.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	if !p.present() {
		return shell.Unsupported(p.Name(), "remove", "rustup is not installed")
	}
	p.Toolchains(ctx)

	if IsToolchainBinary(pkg.Name) {
		if tc, ok := p.selected(); ok {
			return shell.Unsupported(p.Name(), "remove", fmt.Sprintf(
				"%s is provided by the %s toolchain; remove that toolchain by name instead",
				pkg.Name, tc.Name))
		}
		return shell.Unsupported(p.Name(), "remove",
			pkg.Name+" is a toolchain binary and no toolchain is installed to provide it")
	}
	if !IsToolchainName(pkg.Name) {
		return shell.Unsupported(p.Name(), "remove", fmt.Sprintf(
			"%s is not a Rust toolchain; rustup installs and removes toolchains, not crates", pkg.Name))
	}

	tc, ok := p.lookup(pkg.Name)
	if !ok {
		return shell.NotFound(p.Name(), "remove", "no toolchain named "+pkg.Name+" is installed")
	}
	if tc.Default || tc.Active {
		return shell.Unsupported(p.Name(), "remove", fmt.Sprintf(
			"%s is the %s toolchain; removing it would leave no Rust compiler. Choose another default with 'rustup default <toolchain>' first.",
			tc.Name, roleWord(tc)))
	}
	res, err := p.runner.Run(ctx, "rustup", "toolchain", "uninstall", tc.Full)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run rustup toolchain uninstall", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	p.invalidate()
	return nil
}

// Update brings a toolchain up to date. rustup's install is idempotent: running it
// for an installed channel fetches a newer build of that channel.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	target := InstallTarget(pkg)
	res, err := p.runner.Run(ctx, "rustup", "toolchain", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run rustup toolchain install", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	p.invalidate()
	return p.Installed(ctx, pkg)
}

// InstallTarget returns the toolchain name an install or update should act on.
//
// An explicit version wins, because a concrete toolchain such as 1.99.0 is an
// exact pin and must not drift to a channel. A channel comes next. Then the entry
// name itself, which is the whole answer when the entry names a toolchain: an
// entry called "1.99.0" must install 1.99.0, not the default channel. Only for an
// entry naming a toolchain binary — `rustc`, `cargo` — is the default channel the
// right answer, because "install rustc" means "install Rust".
func InstallTarget(pkg model.Package) string {
	if pkg.Version != "" {
		return strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	}
	if pkg.Channel != "" {
		return pkg.Channel
	}
	if IsToolchainName(pkg.Name) {
		return pkg.Name
	}
	return DefaultChannel
}

// binaryPath resolves where a toolchain keeps one of its executables.
//
// The toolchain is always one that is already installed, because `rustup which`
// will download a toolchain it does not have and a lookup must not be able to
// install software.
func (p *Provider) binaryPath(ctx context.Context, tc toolchain, binary string) string {
	res, err := p.runner.Run(ctx, "rustup", "which", "--toolchain", tc.Full, binary)
	if err != nil || res.Failed() {
		return ""
	}
	path := strings.TrimSpace(res.Stdout)
	if !strings.HasPrefix(path, "/") {
		return ""
	}
	return path
}

// toolchainRelease reports the compiler release a toolchain provides. The compiler
// is what a Rust toolchain is pinned by, so its version is what a user means by
// "the version of stable".
func (p *Provider) toolchainRelease(ctx context.Context, tc toolchain) string {
	res, err := p.runner.Run(ctx, "rustup", "run", tc.Full, "rustc", "--version")
	if err != nil || res.Failed() {
		return ""
	}
	return shell.VersionFromArgs(shell.Fields(shell.FirstLine(res.Stdout))...)
}

// rustupVersion reports the version of the rustup tool itself.
func (p *Provider) rustupVersion(ctx context.Context) string {
	res, err := p.runner.Run(ctx, "rustup", "--version")
	if err != nil || res.Failed() {
		return ""
	}
	return shell.VersionFromArgs(shell.Fields(shell.FirstLine(res.Stdout))...)
}

// ToolchainHome describes where rustup keeps toolchains, for diagnostics.
func (p *Provider) ToolchainHome() string {
	if p.home == "" {
		return "~/.rustup"
	}
	return filepath.Join(p.home, ".rustup")
}

func roleWord(tc toolchain) string {
	if tc.Active && !tc.Default {
		return "active"
	}
	return "default"
}

// classify turns rustup's output into typed errors.
func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "unknown binary"),
		strings.Contains(text, "no toolchain installed for"),
		strings.Contains(text, "is not installed"),
		strings.Contains(text, "no such toolchain"):
		return shell.NotFound(Name, op, firstMessage(res, "rustup does not know this toolchain"))
	case strings.Contains(text, "invalid toolchain name"),
		strings.Contains(text, "unknown toolchain"),
		strings.Contains(text, "toolchain name must be"):
		return shell.Unsupported(Name, op, firstMessage(res, "rustup rejected this toolchain name"))
	case strings.Contains(text, "connection"), strings.Contains(text, "network"),
		strings.Contains(text, "could not resolve"), strings.Contains(text, "dns"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the rustup server", nil)
	case strings.Contains(text, "permission denied"), strings.Contains(text, "read-only file system"):
		return shell.Err(Name, op, provider.ErrPermission,
			"rustup needs write access to "+p.ToolchainHome(), nil)
	case strings.Contains(text, "already installed"), strings.Contains(text, "unchanged"),
		strings.Contains(text, "up to date"), strings.Contains(text, "not applicable"):
		return shell.Err(Name, op, provider.ErrConflict, firstMessage(res, "the toolchain is already installed"), nil)
	default:
		return shell.Failure(Name, op, firstMessage(res, "rustup exited nonzero"), nil)
	}
}

func firstMessage(res exec.Result, fallback string) string {
	if msg := shell.Diagnostic(res.Field()); msg != "" {
		return msg
	}
	return fallback
}

// Verify checks that an installed rustup toolchain or binary is functional.
// For rustup, this attempts to run the binary with --version.
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
