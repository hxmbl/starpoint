// Package engine turns configuration plus observed machine state into a plan,
// and applies that plan.
//
// The engine is where "converge rather than blindly reinstall" is implemented.
// Every decision is derived from a comparison of desired state with current
// state; the engine never reinstalls something that already matches, and it
// never rewrites the configuration to make a mismatch disappear.
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/locator"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/resolve"
)

// Engine computes and applies convergence.
type Engine struct {
	reg      *provider.Registry
	locator  *locator.Locator
	resolver *resolve.Resolver

	// healthOnce and healthCache make provider probing happen once per Engine.
	//
	// Probing is the dominant cost of every command: it runs a subprocess per
	// provider, and a single `starpoint install` reaches for the answer several
	// times over. Memoising it turns three full sweeps into one and, more
	// importantly, makes every step of a command reason about the same machine
	// state rather than three slightly different samples of it.
	healthOnce  sync.Once
	healthCache map[string]provider.Health
}

// New builds an Engine.
func New(reg *provider.Registry, loc *locator.Locator) *Engine {
	return &Engine{reg: reg, locator: loc, resolver: resolve.New(reg)}
}

// Options configures a single operation.
type Options struct {
	// DryRun reports what would happen without changing anything.
	DryRun bool
	// NoConfirm applies changes without asking.
	NoConfirm bool
	// Allowed restricts provider candidates to these names.
	Allowed []string
	// Dir is the working directory, used for project context.
	Dir string
	// Ecosystem is the ecosystem of the project at Dir, if any. It informs
	// provider resolution without overriding an explicit decision.
	Ecosystem provider.Kind
	// AdoptOnly records the decision without installing anything. It exists
	// because "not in this provider's list" and "not on the machine" are
	// different facts: a language toolchain is present without appearing in its
	// own package database, and mistaking one for the other turns a no-op into a
	// destructive install.
	AdoptOnly bool
	// SurveyTimeout bounds one round of concurrent provider lookups. Zero keeps
	// the default.
	SurveyTimeout time.Duration
	// Quiet suppresses the extra reporting a command normally makes about how it
	// looked. It exists for JSON consumers and for scripts, where a paragraph of
	// provenance between the command and its output is noise.
	Quiet bool
}

// Result is the outcome of an operation.
type Result struct {
	// States is the per-package comparison.
	States []model.State `json:"states"`
	// Applied lists actions that were carried out.
	Applied []model.PlannedAction `json:"applied,omitempty"`
	// Transitions are the provider moves that were carried out, with their final
	// phase and the evidence behind it.
	Transitions []model.Transition `json:"transitions,omitempty"`
	// ConfigChanged reports whether the source of truth was written.
	ConfigChanged bool `json:"config_changed"`
	// ConfigPath is the file that was written, when one was.
	ConfigPath string `json:"config_path,omitempty"`
	// Warnings are non-fatal problems.
	Warnings []model.Warning `json:"warnings,omitempty"`
	// DryRun echoes the mode.
	DryRun bool `json:"dry_run"`
}

// Plan computes the convergence plan for a document without changing anything.
func (e *Engine) Plan(ctx context.Context, doc *config.Document, opts Options) (model.Plan, []model.Warning, error) {
	available := e.health(ctx)
	packages := doc.Packages()
	warnings := e.validate(packages, available)

	var states []model.State
	for _, pkg := range packages {
		st := e.evaluate(ctx, pkg, available, opts)
		states = append(states, st)
		warnings = append(warnings, unavailableOwnerWarnings(st, available)...)
	}
	model.SortStates(states)

	plan := model.Plan{States: states, Warnings: warnings, DryRun: opts.DryRun}
	for _, st := range states {
		action, ok := e.actionFor(st, available)
		if !ok {
			continue
		}
		if action.Action == model.ActionRelocate {
			// A provider move is a transition, and a transition has phases and
			// evidence. Attaching the record here keeps `sync --dry-run` showing
			// the same object the transition command and JSON consumers see.
			t := e.TransitionFor(ctx, st, opts)
			action.Transition = &t
			plan.Transitions = append(plan.Transitions, t)
		}
		plan.Actions = append(plan.Actions, action)
	}
	return plan, warnings, nil
}

// health probes every registered provider, tolerating individual failures.
//
// The result is memoised for the lifetime of the Engine. A caller that needs a
// fresh look — a long-running process, or a test that changes the machine
// mid-command — can build a new Engine.
func (e *Engine) health(ctx context.Context) map[string]provider.Health {
	e.healthOnce.Do(func() {
		out := map[string]provider.Health{}
		for _, p := range e.reg.All() {
			out[p.Name()] = p.Health(ctx)
		}
		e.healthCache = out
	})
	return e.healthCache
}

// evaluate builds the State for one desired package.
func (e *Engine) evaluate(ctx context.Context, pkg model.Package, available map[string]provider.Health, opts Options) model.State {
	st := model.State{Package: pkg}

	existing := e.findExisting(ctx, pkg, available)
	st.Res = e.resolver.Resolve(e.resolveInput(pkg, existing, available, opts))

	desiredProv, ok := e.reg.Get(st.Res.Provider)
	if !ok {
		st.Status = model.StatusMissing
		st.Notes = append(st.Notes, fmt.Sprintf("provider %q is not registered in this build", st.Res.Provider))
		return st
	}

	if st.Res.Forced && !available[st.Res.Provider].Available {
		st.Status = model.StatusMissing
		st.Notes = append(st.Notes, fmt.Sprintf("configured provider %q is not available on this machine", st.Res.Provider))
		return st
	}

	inst, err := e.providerInstallation(ctx, desiredProv, pkg)
	if err != nil {
		st.Status = model.StatusMissing
		st.Notes = append(st.Notes, "could not query provider: "+err.Error())
		return st
	}
	if inst == nil && st.Res.Provider != system.Name && existing != nil && existing.Provider != st.Res.Provider {
		// The provider does not have it, but something else does. This is a
		// transition, not a fresh install.
		st.Elsewhere = []model.Installation{*existing}
		st.Current = existing
		st.Status = model.StatusProviderDiffers
		st.Notes = append(st.Notes, fmt.Sprintf("currently provided by %s at %s", existing.Provider, existing.Path))
		return st
	}

	if inst != nil {
		st.Desired = e.withPath(inst)
		st.Status = e.statusFor(pkg, st.Desired, st.Res)
		return st
	}

	if existing != nil {
		st.Current = existing
		st.Elsewhere = []model.Installation{*existing}
		st.Status = model.StatusProviderDiffers
		return st
	}

	st.Status = model.StatusMissing
	return st
}

// statusFor decides the verdict for an installation the desired provider owns.
func (e *Engine) statusFor(pkg model.Package, inst *model.Installation, res model.Resolution) model.Status {
	if pkg.Version == "" {
		return model.StatusCorrect
	}
	if model.VersionMatches(pkg.Version, inst.Version) {
		return model.StatusCorrect
	}
	// The version does not match an explicit pin. Whether Starpoint can fix it
	// depends on the provider, and that distinction is reported as a different
	// status so the user is not told to do something impossible.
	if p, ok := e.reg.Get(res.Provider); ok && p.Capabilities().PinVersion {
		return model.StatusVersionDiffers
	}
	return model.StatusUnpinnedConflict
}

// withPath fills in a missing installation path from PATH.
//
// Most providers report a name and version but not a location, because their own
// data model is a package list rather than a filesystem. Starpoint needs the path
// to answer "where is it?", so it is resolved here rather than making every
// provider reimplement path lookup — and rather than guessing inside a provider
// that would then be wrong about its own data.
func (e *Engine) withPath(inst *model.Installation) *model.Installation {
	if inst == nil || inst.Path != "" || e.locator == nil {
		return inst
	}
	found := e.locator.Locate(inst.Name)
	if !found.Attributed {
		return inst
	}
	// Only adopt the located path when it belongs to the provider that reported
	// the installation; a mismatch means PATH is pointing somewhere else.
	if found.Installation.Provider != inst.Provider {
		return inst
	}
	enriched := *inst
	enriched.Path = found.Installation.Path
	if enriched.Scope == "" {
		enriched.Scope = found.Installation.Scope
	}
	return &enriched
}

// findExisting locates the software on the machine.
//
// Precedence matters more than speed here. A provider's own inventory is
// authoritative — it knows what it installed — while a PATH lookup is inference
// from a directory name, which is exactly the kind of guess that mislabels
// software (an installer that happens to write to the same prefix as Homebrew).
// So: the configured provider first, then any provider that actually reports the
// software, and only then path attribution for software no provider tracks.
func (e *Engine) findExisting(ctx context.Context, pkg model.Package, available map[string]provider.Health) *model.Installation {
	// 1. The provider the configuration names.
	if pkg.Provider != "" {
		if p, ok := e.reg.Get(pkg.Provider); ok && available[pkg.Provider].Available {
			if inst, err := e.providerInstallation(ctx, p, pkg); err == nil && inst != nil {
				return e.withPath(inst)
			}
		}
	}
	// 2. Any provider whose inventory includes it. Managing providers are asked
	// first: the read-only system provider matches almost every command on a
	// Unix machine, so answering with it first would make Starpoint claim that
	// the OS owns whatever it happens to be asked about.
	for _, pass := range []bool{true, false} {
		for _, p := range e.reg.All() {
			caps := p.Capabilities()
			if caps.Install != pass {
				continue
			}
			if p.Name() == pkg.Provider || !available[p.Name()].Available || !caps.Discover {
				continue
			}
			if inst, err := e.providerInstallation(ctx, p, pkg); err == nil && inst != nil {
				return e.withPath(inst)
			}
		}
	}
	// 3. Path attribution, for software no provider claims.
	if e.locator != nil {
		if found := e.locator.Locate(pkg.Name); found.Installation != nil {
			return found.Installation
		}
	}
	return nil
}

// providerInstallation queries one provider, converting provider errors into
// notes rather than failing the whole evaluation.
func (e *Engine) providerInstallation(ctx context.Context, p provider.Provider, pkg model.Package) (*model.Installation, error) {
	inst, err := p.Installed(ctx, pkg)
	if err != nil {
		if pe, ok := provider.AsProviderError(err); ok {
			switch pe.Kind {
			case provider.ErrUnsupported:
				return nil, nil
			case provider.ErrNotFound:
				return nil, nil
			}
		}
		return nil, err
	}
	return inst, nil
}

// actionFor maps a State to the action needed to converge it.
func (e *Engine) actionFor(st model.State, available map[string]provider.Health) (model.PlannedAction, bool) {
	prov, ok := e.reg.Get(st.Res.Provider)
	if !ok {
		return model.PlannedAction{}, false
	}
	if !available[st.Res.Provider].Available {
		return model.PlannedAction{}, false
	}

	switch st.Status {
	case model.StatusMissing:
		if !prov.Capabilities().Install {
			return model.PlannedAction{}, false
		}
		// The provider decision has not been recorded yet. Recording it is a
		// configuration change, and it is the durable part of the operation.
		edit := &model.ConfigEdit{File: st.Package.Origin.Path}
		pkg := st.Package
		pkg.Provider = st.Res.Provider
		if pkg.Reason == "" {
			pkg.Reason = st.Res.Reason
		}
		edit.Upsert = &pkg
		note := ""
		if st.Res.Ambiguous {
			note = "provider choice was ambiguous; the configuration will record " + st.Res.Provider
		}
		return model.PlannedAction{
			Action:       model.ActionInstall,
			State:        st,
			ConfigChange: edit,
			Note:         note,
		}, true

	case model.StatusVersionDiffers:
		edit := &model.ConfigEdit{File: st.Package.Origin.Path}
		pkg := st.Package
		pkg.Provider = st.Res.Provider
		edit.Upsert = &pkg
		return model.PlannedAction{
			Action:       model.ActionUpdate,
			State:        st,
			ConfigChange: edit,
			Note: fmt.Sprintf("version %s does not match the configured pin of %s",
				shortVersion(st.Desired), pkg.Version),
		}, true

	case model.StatusUnpinnedConflict:
		// Do not touch the configuration and do not guess. The user pinned a
		// version the provider cannot deliver, and silently changing either
		// would destroy information.
		return model.PlannedAction{
			Action: model.ActionRecord,
			State:  st,
			Note: fmt.Sprintf("provider %s cannot install the configured version %s; the installed version is %s",
				st.Res.Provider, st.Package.Version, shortVersion(st.Desired)),
		}, true

	case model.StatusProviderDiffers:
		// The software exists but the configuration wants a different provider.
		// This is a deliberate move, and the reason has to come from the user
		// or the configuration, never from Starpoint's own preference.
		return model.PlannedAction{
			Action: model.ActionRelocate,
			State:  st,
			Note: fmt.Sprintf("configuration wants %s but %s provides it at %s",
				st.Res.Provider, st.Current.Provider, st.Current.Path),
		}, true
	}
	return model.PlannedAction{}, false
}

func shortVersion(i *model.Installation) string {
	if i == nil || i.Version == "" {
		return "unknown"
	}
	return i.Version
}

// validate produces warnings about configuration and availability problems that
// are not themselves convergence actions.
func (e *Engine) validate(pkgs []model.Package, available map[string]provider.Health) []model.Warning {
	var out []model.Warning
	seen := map[string]bool{}
	for _, p := range pkgs {
		if seen[p.Name] {
			out = append(out, model.Warning{
				Severity: model.SeverityWarn,
				Package:  p.Name,
				Message:  "declared more than once in the merged configuration",
			})
		}
		seen[p.Name] = true

		if p.Provider != "" {
			h, ok := available[p.Provider]
			switch {
			case !ok:
				out = append(out, model.Warning{
					Severity: model.SeverityError,
					Package:  p.Name,
					Provider: p.Provider,
					Message:  "the configured provider is not known to this build of Starpoint",
				})
			case !h.Available:
				out = append(out, model.Warning{
					Severity: model.SeverityWarn,
					Package:  p.Name,
					Provider: p.Provider,
					Message:  "the configured provider is not available: " + h.Reason,
				})
			}
		}
		if p.Scope == model.ScopeProject && p.Origin.Path == "" {
			out = append(out, model.Warning{
				Severity: model.SeverityWarn,
				Package:  p.Name,
				Message:  "declared as project-scoped but no project configuration declares it",
			})
		}
	}
	for _, name := range sortedProviders(available) {
		if !available[name].Available {
			out = append(out, model.Warning{
				Severity: model.SeverityInfo,
				Provider: name,
				Message:  "not available on this machine",
			})
		}
	}
	return out
}

// unavailableOwnerWarnings reports packages whose owner is not on this machine.
//
// A package whose provider cannot be reached produces no action at all, so
// without this warning it would silently do nothing — the most confusing outcome
// a tool like this can have.
func unavailableOwnerWarnings(st model.State, available map[string]provider.Health) []model.Warning {
	h, ok := available[st.Res.Provider]
	if !ok || h.Available {
		return nil
	}
	if st.Desired != nil {
		// The provider is missing yet reports the software: trust the inventory
		// and do not claim a contradiction.
		return nil
	}
	reason := h.Reason
	if reason == "" {
		reason = "it is not available on this machine"
	}
	return []model.Warning{{
		Severity: model.SeverityWarn,
		Package:  st.Package.Name,
		Provider: st.Res.Provider,
		Message: fmt.Sprintf("nothing can be done for it: provider %s is unavailable (%s)",
			st.Res.Provider, reason),
	}}
}

func sortedProviders(m map[string]provider.Health) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Decide resolves a provider for a package without changing anything. The CLI
// uses it to decide whether a question is worth asking.
func (e *Engine) Decide(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) model.Resolution {
	available := e.health(ctx)
	return e.resolver.Resolve(e.resolveInput(pkg, e.findExisting(ctx, pkg, available), available, opts))
}

// Duplicate records a second copy of software that Starpoint already tracks.
// This is not a convergence problem: it is a machine that has drifted into
// ambiguity, and only the user can decide which copy to keep.
type Duplicate struct {
	Package string
	// Owner is the provider the configuration wants.
	Owner string
	// Extra is the unwanted copy.
	Extra model.Installation
}

// Duplicates scans every available discoverable provider for extra copies of
// software the configuration already accounts for.
//
// The state comparison cannot do this on its own: findExisting stops at the
// provider that owns a package, so a second copy elsewhere is invisible until
// every provider is asked. That scan is expensive, which is why it lives in
// doctor rather than in every sync.
func (e *Engine) Duplicates(ctx context.Context, doc *config.Document, opts Options) []Duplicate {
	available := e.health(ctx)
	var out []Duplicate

	for _, pkg := range doc.Packages() {
		owner := pkg.Provider
		if owner == "" {
			owner = e.Decide(ctx, doc, pkg, opts).Provider
		}
		for _, p := range e.reg.All() {
			if p.Name() == owner || !available[p.Name()].Available || !p.Capabilities().Discover {
				continue
			}
			inst, err := e.providerInstallation(ctx, p, pkg)
			if err != nil || inst == nil {
				continue
			}
			out = append(out, Duplicate{Package: pkg.Name, Owner: owner, Extra: *inst})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Extra.Provider < out[j].Extra.Provider
	})
	return out
}

// Discovery reports where a piece of software currently lives, whether or not it
// is in the configuration. The CLI uses it to answer questions about software
// Starpoint does not manage yet.
func (e *Engine) Discovery(ctx context.Context, name string, opts Options) *model.Installation {
	pkg := model.Package{Name: name}
	return e.findExisting(ctx, pkg, e.health(ctx))
}

// Shadowing describes a package whose owner is known but whose executable is not
// the one a shell would run.
type Shadowing struct {
	Package string
	// Managed is the installation the configuration points at.
	Managed model.Installation
	// OnPath is the executable the name currently resolves to.
	OnPath string
}

// Shadowed reports managed packages whose name on PATH resolves somewhere other
// than the installation Starpoint is managing.
//
// Provider inventories answer "who installed this", which is the wrong question
// when a second copy sits earlier on PATH. A Homebrew `rust` formula and a rustup
// toolchain both provide `rustc`; only one of them is what `rustc` runs. Saying so
// is the difference between Starpoint describing ownership and Starpoint being
// right about it.
func (e *Engine) Shadowed(ctx context.Context, doc *config.Document, opts Options) []Shadowing {
	if e.locator == nil {
		return nil
	}
	available := e.health(ctx)
	var out []Shadowing
	for _, pkg := range doc.Packages() {
		owner := pkg.Provider
		if owner == "" {
			owner = e.Decide(ctx, doc, pkg, opts).Provider
		}
		p, ok := e.reg.Get(owner)
		if !ok || !available[owner].Available || !p.Capabilities().Discover {
			continue
		}
		inst, err := e.providerInstallation(ctx, p, pkg)
		if err != nil || inst == nil || inst.Path == "" {
			continue
		}
		found := e.locator.Locate(pkg.Name)
		if found.Path == "" || samePath(found.Path, inst.Path) {
			continue
		}
		out = append(out, Shadowing{Package: pkg.Name, Managed: *inst, OnPath: found.Path})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

// samePath compares two paths after resolving symlinks, so a shim and its target
// are not reported as a conflict with themselves.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		return false
	}
	return ra == rb
}

// Registry exposes the provider registry, which the CLI needs for doctor output.
func (e *Engine) Registry() *provider.Registry { return e.reg }

// Resolver exposes the resolver.
func (e *Engine) Resolver() *resolve.Resolver { return e.resolver }

// resolveInput bundles the fields every resolution needs.
func (e *Engine) resolveInput(pkg model.Package, existing *model.Installation, available map[string]provider.Health, opts Options) resolve.Input {
	return resolve.Input{
		Package:   pkg,
		Existing:  existing,
		Available: available,
		Allowed:   opts.Allowed,
		Ecosystem: opts.Ecosystem,
		Locator:   e.locator,
	}
}

// scopeOf reports the observed or desired placement for a state.
func scopeOf(st model.State) string {
	if st.Desired != nil && st.Desired.Scope != "" {
		return string(st.Desired.Scope)
	}
	if st.Current != nil && st.Current.Scope != "" {
		return string(st.Current.Scope)
	}
	if st.Package.Scope != "" {
		return string(st.Package.Scope)
	}
	return "-"
}

// ScopeOf is the exported accessor used by the reporting layer.
func ScopeOf(st model.State) string { return scopeOf(st) }
