// Package nix implements the Nix provider for profile-installed packages.
//
// Nix has two package managers in one binary: the legacy nix-env and the
// profile-based `nix profile` that ships with the flakes CLI. Starpoint uses
// `nix profile`, whose JSON inventory names what it installed and where each
// package came from. A machine still on nix-env is reported as unsupported
// rather than half-managed: the two keep separate profiles, and guessing which
// one owns a command is how a user ends up with two copies of it.
//
// Packages are installed into the invoking user's profile (~/.nix-profile),
// which needs no privileges at all. System generations on NixOS are declared in
// the system configuration and are not Starpoint's to change.
package nix

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "nix"

// experimental enables the unified CLI and flakes for Starpoint's own
// invocations.
//
// `nix profile` is still an experimental feature in every released Nix, and a
// machine that has not put `experimental-features = nix-command flakes` in
// nix.conf would otherwise reject a perfectly good Nix install. The flag applies
// to the command Starpoint runs and changes nothing on the machine.
var experimental = []string{"--extra-experimental-features", "nix-command flakes"}

// Provider implements provider.Provider for `nix profile`.
type Provider struct {
	runner exec.Runner
	home   string
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
	_ provider.Privileged            = (*Provider)(nil)
)

// New builds a Nix provider. home is the user whose profile is managed; an
// empty home makes the provider report unavailable.
func New(runner exec.Runner, home string) *Provider { return &Provider{runner: runner, home: home} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Nix (profiles)" }
func (p *Provider) Kind() provider.Kind { return provider.KindNix }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
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
	if p.home == "" {
		h.Reason = "no home directory is known, so no Nix profile can be managed"
		return h
	}
	path, err := p.runner.LookPath("nix")
	if err != nil {
		h.Reason = "nix not found on PATH"
		return h
	}
	h.Binary = path
	res, err := p.runner.Run(ctx, "nix", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if res.Failed() {
		h.Reason = shell.Diagnostic(res.Field())
		if h.Reason == "" {
			h.Reason = "nix exited with a non-zero status"
		}
		return h
	}
	h.Version = shell.VersionFromArgs(strings.Fields(res.Stdout)...)
	// `nix profile` is the only interface this provider can keep honest, and it
	// only exists from Nix 2.4. Probing for it here turns "every command fails
	// with an experimental-feature error" into one clear reason.
	if probe, err := p.run(ctx, "profile", "--help"); err != nil || probe.Failed() {
		h.Available = false
		h.Reason = "this Nix has no `nix profile` (Nix 2.4 or newer is required)"
		return h
	}
	h.Available = true
	return h
}

// run invokes nix with the experimental features Starpoint's profile commands
// need. They are global flags, so they come before the subcommand.
func (p *Provider) run(ctx context.Context, args ...string) (exec.Result, error) {
	return p.runner.Run(ctx, "nix", append(append([]string{}, experimental...), args...)...)
}

// profileOutput is the subset of `nix profile list --json` Starpoint reads.
type profileOutput struct {
	Elements json.RawMessage `json:"elements"`
}

type profileElement struct {
	Active     bool     `json:"active"`
	AttrPath   string   `json:"attrPath"`
	StorePaths []string `json:"storePaths"`
	URL        string   `json:"url"`
}

type namedProfileElement struct {
	Name     string
	Selector string
	Element  profileElement
}

// profileElements handles both manifest formats emitted by nix profile:
// versions 2.4–2.19 use an array, while later versions use a name-keyed object.
func profileElements(data []byte) ([]namedProfileElement, error) {
	var out profileOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	var object map[string]profileElement
	if err := json.Unmarshal(out.Elements, &object); err == nil && object != nil {
		items := make([]namedProfileElement, 0, len(object))
		for name, element := range object {
			items = append(items, namedProfileElement{Name: name, Selector: name, Element: element})
		}
		return items, nil
	}
	var array []profileElement
	if err := json.Unmarshal(out.Elements, &array); err != nil {
		return nil, err
	}
	items := make([]namedProfileElement, 0, len(array))
	for i, element := range array {
		name := packageName(element.AttrPath)
		// The older CLI selects profile elements by their zero-based position.
		items = append(items, namedProfileElement{Name: name, Selector: strconv.Itoa(i), Element: element})
	}
	return items, nil
}

// Discover lists the packages in the user's profile.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.run(ctx, "profile", "list", "--json")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list the Nix profile", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	items, err := profileElements([]byte(res.Stdout))
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not parse `nix profile list --json`: "+err.Error(), nil)
	}
	insts := make([]model.Installation, 0, len(items))
	for _, item := range items {
		if !item.Element.Active {
			continue
		}
		inst := model.Installation{
			Name:     item.Name,
			Provider: p.Name(),
			Scope:    model.ScopeUser,
		}
		if len(item.Element.StorePaths) > 0 {
			inst.Version = versionFromStorePath(item.Element.StorePaths[0], packageName(item.Name))
		}
		insts = append(insts, inst)
	}
	return insts, nil
}

// Installed reports whether the profile has this package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	insts, err := p.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range insts {
		if inst.Name == pkg.Name {
			found := inst
			return &found, nil
		}
	}
	return nil, nil
}

// Install installs from nixpkgs into the user's profile.
//
// `nixpkgs#name` is the flake reference the Nix registry already knows on a
// default install, so it works without knowing where the user keeps nixpkgs. A
// machine with no registry entry for nixpkgs is told so by the error Nix prints.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	target := "nixpkgs#" + req.Package.Name
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.run(ctx, "profile", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run nix profile install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove deletes a package from the profile.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	selector, err := p.selector(ctx, pkg.Name)
	if err != nil {
		return err
	}
	res, err := p.run(ctx, "profile", "remove", selector)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run nix profile remove", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update upgrades a profile element from the flake it was installed from.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	selector, err := p.selector(ctx, pkg.Name)
	if err != nil {
		return nil, err
	}
	res, err := p.run(ctx, "profile", "upgrade", selector)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run nix profile upgrade", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// selector returns the command-line selector for a package in this Nix version.
// Old array manifests and old profile commands identify elements by index.
func (p *Provider) selector(ctx context.Context, name string) (string, error) {
	res, err := p.run(ctx, "profile", "list", "--json")
	if err != nil {
		return "", shell.Failure(p.Name(), "list", "could not list the Nix profile", err)
	}
	if res.Failed() {
		return "", shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	items, err := profileElements([]byte(res.Stdout))
	if err != nil {
		return "", shell.Failure(p.Name(), "list", "could not parse `nix profile list --json`: "+err.Error(), nil)
	}
	for _, item := range items {
		if item.Name == name && item.Element.Active {
			return item.Selector, nil
		}
	}
	return name, nil
}

// ExecutableDir reports where the user's profile exposes commands.
func (p *Provider) ExecutableDir(context.Context) string {
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, ".nix-profile", "bin")
}

// packageName is the package part of a profile element name. Elements are named
// after the attribute installed ("ripgrep"), and some Nix versions keep the
// flake prefix in the element name ("nixpkgs#ripgrep").
func packageName(element string) string {
	if i := strings.LastIndex(element, "#"); i >= 0 {
		element = element[i+1:]
	}
	if i := strings.LastIndex(element, "."); i >= 0 {
		return element[i+1:]
	}
	return element
}

// versionFromStorePath reads the version out of a store path such as
// /nix/store/3n9...-ripgrep-14.1.0.
//
// Nix does not otherwise report a package's version, and this is the same name
// `nix profile list` shows a person. A path that does not carry the package's
// name leaves the version unknown rather than guessing at the last dash-separated
// segment, which is wrong for versions like 1.0.0-rc1.
func versionFromStorePath(path, pkg string) string {
	base := filepath.Base(path)
	if i := strings.IndexByte(base, '-'); i >= 0 {
		base = base[i+1:]
	}
	if pkg == "" || !strings.HasPrefix(base, pkg+"-") {
		return ""
	}
	return strings.TrimPrefix(base, pkg+"-")
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "does not provide attribute"), strings.Contains(text, "attribute '"),
		strings.Contains(text, "not found"), strings.Contains(text, "no matches for"):
		return shell.NotFound(Name, op, "nixpkgs does not have this package")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "not owned by current user"),
		strings.Contains(text, "are you root"):
		return shell.Err(Name, op, provider.ErrPermission, "the Nix store rejected the operation; check its ownership", nil)
	case strings.Contains(text, "unable to download"), strings.Contains(text, "could not resolve host"),
		strings.Contains(text, "connection refused"), strings.Contains(text, "network is unreachable"),
		strings.Contains(text, "tls"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the flake's source", nil)
	case strings.Contains(text, "is already installed"), strings.Contains(text, "already exists"),
		strings.Contains(text, "conflicts with"):
		return shell.Err(Name, op, provider.ErrConflict, "the profile already has an element with this name", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "nix exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Privilege classifies the provider for an elevated run: it manages the
// invoking user's profile, so root would only add confusion.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.UserPrivilege("nix installs into the invoking user's profile (~/.nix-profile)")
}
