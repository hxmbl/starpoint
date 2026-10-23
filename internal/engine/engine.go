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
	"errors"
	"fmt"
	"github.com/hxmbl/starpoint/internal/align"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/resolve"
	"github.com/hxmbl/starpoint/internal/tracking"
)

// Engine computes and applies convergence.
type Engine struct {
	reg      *provider.Registry
	locator  *locator.Locator
	resolver *resolve.Resolver
	tracker  *tracking.Tracker
	history  *history.History

	// healthCache makes provider probing happen once per Engine.
	//
	// Probing is the dominant cost of every command: it runs a subprocess per
	// provider, and a single `starpoint install` reaches for the answer several
	// times over. Memoising it turns three full sweeps into one and, more
	// importantly, makes every step of a command reason about the same machine
	// state rather than three slightly different samples of it.
	//
	// Only a probe that finished on its own is remembered. One whose context
	// was cancelled or ran out (Inventory bounds every probe) says nothing
	// about the provider, so it is asked again the next time; healthBusy
	// holds the providers whose abandoned probe is still running, so a
	// provider is never probed twice at once.
	healthMu    sync.Mutex
	healthCache map[string]provider.Health
	healthBusy  map[string]chan struct{}

	// historyErr records a failure to load the transition history database.
	//
	// A corrupt history file must not stop a command from running, but it must
	// not pass unnoticed either: history backs `transition --reverse`, and
	// silently recording nothing makes a transition that just succeeded look
	// like one that never happened. The engine keeps working and the caller
	// reports the problem.
	historyErr error

	// home is the directory whose shell start-up files are watched for changes
	// while an action runs. Empty means the user's home directory.
	home string

	// align, when set, fixes ownership and permissions of what each action
	// touched: an elevated run gives what it wrote under the target user's
	// home to that user, and widens what it created in shared locations.
	align *align.Policy

	// runAs reports the account a provider's commands run as in this run,
	// when the caller knows. Nil means Starpoint's own account throughout.
	runAs func(ctx context.Context, p provider.Provider) model.RunAs

	// discoverTimeout bounds each provider's answer to Inventory. Zero means
	// SurveyTimeout.
	discoverTimeout time.Duration
}

// HistoryError reports why transition history is unavailable, or nil if it
// loaded cleanly.
func (e *Engine) HistoryError() error { return e.historyErr }

// stores holds the record-keeping paths an Engine writes to.
type stores struct {
	historyPath string
	runsPath    string
	home        string
	align       *align.Policy
	runAs       func(ctx context.Context, p provider.Provider) model.RunAs
}

// Option configures an Engine's own stores.
type Option func(*stores)

// WithStateDir points the engine's transition history and run records at dir
// instead of the default under the user's home directory.
//
// This is what keeps a Starpoint run against a temporary configuration from
// writing into a real home directory. Without it, a test suite would age out and
// rewrite a developer's own records.
func WithStateDir(dir string) Option {
	return func(s *stores) {
		if dir == "" {
			return
		}
		s.historyPath = history.PathIn(dir)
		s.runsPath = tracking.PathIn(dir)
	}
}

// WithHome sets the home directory whose shell start-up files are watched
// while actions run. Tests use it so they never depend on a real home.
func WithHome(dir string) Option {
	return func(s *stores) { s.home = dir }
}

// WithAlignment makes every action fix the ownership and permissions of what
// it touched, as the policy says. The CLI sets it for elevated runs.
func WithAlignment(p *align.Policy) Option {
	return func(s *stores) { s.align = p }
}

// WithRunAs tells the engine which account each provider's commands run as,
// so that what it finds owned by root after a provider ran as the invoking
// user is reported as unexpected. The CLI sets it from its privilege
// decisions.
func WithRunAs(f func(ctx context.Context, p provider.Provider) model.RunAs) Option {
	return func(s *stores) { s.runAs = f }
}

// SetRunAs is WithRunAs for an engine that already exists.
func (e *Engine) SetRunAs(f func(ctx context.Context, p provider.Provider) model.RunAs) { e.runAs = f }

// RunAs reports the account p's commands run as, when the engine was told.
func (e *Engine) RunAs(ctx context.Context, p provider.Provider) (model.RunAs, bool) {
	if e.runAs == nil || p == nil {
		return model.RunAs{}, false
	}
	return e.runAs(ctx, p), true
}

// New builds an Engine.
func New(reg *provider.Registry, loc *locator.Locator, options ...Option) *Engine {
	cfg := stores{}
	for _, o := range options {
		o(&cfg)
	}
	tracker, _ := tracking.New(cfg.runsPath)
	hist, histErr := history.New(cfg.historyPath)
	if histErr != nil {
		// Keep going with history disabled, but remember why so the caller can
		// say so out loud instead of silently dropping every transition.
		hist = nil
	}
	if loc != nil {
		for _, p := range reg.All() {
			if mapped, ok := p.(provider.ExecutableMappingProvider); ok {
				loc.PackageMappings = append(loc.PackageMappings, mapped)
			}
		}
	}
	return &Engine{reg: reg, locator: loc, resolver: resolve.New(reg), tracker: tracker, history: hist, historyErr: histErr, home: cfg.home, align: cfg.align, runAs: cfg.runAs}
}

// SetTracker sets the execution tracker. This is primarily for testing.
func (e *Engine) SetTracker(t *tracking.Tracker) {
	e.tracker = t
}

// SetAlignment sets the ownership and permission policy applied after each
// action. Nil turns it off.
func (e *Engine) SetAlignment(p *align.Policy) { e.align = p }

// SetHistory sets the transition history. This is primarily for testing.
func (e *Engine) SetHistory(h *history.History) {
	e.history = h
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
	// ConfigPath identifies the source-of-truth file for audit records.
	ConfigPath string
	// Ecosystem is the ecosystem of the project at Dir, if any. It informs
	// provider resolution without overriding an explicit decision.
	Ecosystem provider.Kind
	// AdoptOnly records the decision without installing anything. It exists
	// because "not in this provider's list" and "not on the machine" are
	// different facts: a language toolchain is present without appearing in its
	// own package database, and mistaking one for the other turns a no-op into a
	// destructive install.
	AdoptOnly bool
	// UpdatePinned names pinned packages the user agreed to move to the newest
	// version during update. Their pin is rewritten in the configuration.
	UpdatePinned map[string]bool
	// SurveyTimeout bounds one round of concurrent provider lookups. Zero keeps
	// the default.
	SurveyTimeout time.Duration
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
	// PinConflicts are pinned packages with a newer version available.
	PinConflicts []model.PinConflict `json:"pin_conflicts,omitempty"`
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
		action = e.describePlanned(ctx, doc, action)
		action.Outcome = model.OutcomePlanned
		plan.Actions = append(plan.Actions, action)
	}
	return plan, warnings, nil
}

// health probes every registered provider, tolerating individual failures.
//
// A finished probe is memoised for the lifetime of the Engine. A caller that
// needs a fresh look — a long-running process, or a test that changes the
// machine mid-command — can build a new Engine. A probe cut short by ctx is
// not memoised: a cancelled deadline is not a verdict on the provider.
func (e *Engine) health(ctx context.Context) map[string]provider.Health {
	out := map[string]provider.Health{}
	for _, p := range e.reg.All() {
		if h, ok := e.cachedHealth(p); ok {
			out[p.Name()] = h
			continue
		}
		h := p.Health(ctx)
		if ctx.Err() == nil {
			e.rememberHealth(p.Name(), h)
		}
		out[p.Name()] = h
	}
	return out
}

// cachedHealth returns the memoised probe of p. A provider whose earlier,
// abandoned probe is still running is reported unavailable for now rather
// than probed a second time alongside it.
func (e *Engine) cachedHealth(p provider.Provider) (provider.Health, bool) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if h, ok := e.healthCache[p.Name()]; ok {
		return h, true
	}
	if _, busy := e.healthBusy[p.Name()]; busy {
		return provider.Health{
			Name: p.Name(), Description: p.Description(), Kind: p.Kind(), Capabilities: p.Capabilities(),
			Reason: "an earlier health check of " + p.Name() + " has not finished",
		}, true
	}
	return provider.Health{}, false
}

func (e *Engine) rememberHealth(name string, h provider.Health) {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if e.healthCache == nil {
		e.healthCache = map[string]provider.Health{}
	}
	e.healthCache[name] = h
}

// healthWithin probes the given providers concurrently, each under its own
// timeout, so one slow provider cannot spend another's time.
//
// It returns every provider's health and, for each probe that did not finish
// on its own (it timed out, ctx was cancelled, or it ignored cancellation and
// was still running after the grace), why. Those providers are reported
// unavailable here and are not memoised; the caller reports them as failed.
func (e *Engine) healthWithin(ctx context.Context, providers []provider.Provider, timeout time.Duration) (map[string]provider.Health, map[string]string) {
	out := map[string]provider.Health{}
	failed := map[string]string{}
	type result struct {
		name   string
		h      provider.Health
		cutOff error
	}
	results := make(chan result, len(providers))
	waiting := map[string]provider.Provider{}
	for _, p := range providers {
		if h, ok := e.cachedHealth(p); ok {
			out[p.Name()] = h
			continue
		}
		waiting[p.Name()] = p
		// Marked busy before the probe starts and cleared when it returns,
		// so a probe abandoned below is never started a second time while
		// it is still running.
		running := make(chan struct{})
		e.healthMu.Lock()
		if e.healthBusy == nil {
			e.healthBusy = map[string]chan struct{}{}
		}
		e.healthBusy[p.Name()] = running
		e.healthMu.Unlock()
		go func(p provider.Provider) {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			h := p.Health(pctx)
			cutOff := pctx.Err()
			// No longer busy before the answer is sent, so whoever reads it
			// can probe again straight away.
			e.healthMu.Lock()
			if e.healthBusy[p.Name()] == running {
				delete(e.healthBusy, p.Name())
			}
			e.healthMu.Unlock()
			close(running)
			results <- result{name: p.Name(), h: h, cutOff: cutOff}
		}(p)
	}
	timedOut := fmt.Sprintf("health check timed out after %s", timeout)
	cutOffReason := func(err error) string {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return timedOut
		}
		return "health check was cancelled: " + err.Error()
	}
	wait := time.NewTimer(timeout + min(timeout, discoverGrace))
	defer wait.Stop()
collect:
	for len(waiting) > 0 {
		select {
		case r := <-results:
			delete(waiting, r.name)
			if r.cutOff != nil {
				h := r.h
				h.Available = false
				h.Reason = cutOffReason(r.cutOff)
				out[r.name] = h
				failed[r.name] = h.Reason
				continue
			}
			e.rememberHealth(r.name, r.h)
			out[r.name] = r.h
		case <-wait.C:
			break collect
		}
	}
	// What is still waiting ignored its deadline and is abandoned; it stays
	// busy until it returns, so nothing probes it again alongside.
	for name, p := range waiting {
		reason := timedOut
		if ctx.Err() != nil {
			reason = cutOffReason(ctx.Err())
		}
		out[name] = provider.Health{
			Name: name, Description: p.Description(), Kind: p.Kind(), Capabilities: p.Capabilities(),
			Reason: reason,
		}
		failed[name] = reason
	}
	return out, failed
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
				Code:     model.WarnDuplicateDeclaration,
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
					Code:     model.WarnProviderUnknown,
					Package:  p.Name,
					Provider: p.Provider,
					Message:  "the configured provider is not known to this build of Starpoint",
				})
			case !h.Available:
				out = append(out, model.Warning{
					Severity: model.SeverityWarn,
					Code:     model.WarnProviderUnavailable,
					Package:  p.Name,
					Provider: p.Provider,
					Message:  "the configured provider is not available: " + h.Reason,
				})
			}
		}
		if p.Scope == model.ScopeProject && p.Origin.Path == "" {
			out = append(out, model.Warning{
				Severity: model.SeverityWarn,
				Code:     model.WarnProjectScopeUndecl,
				Package:  p.Name,
				Message:  "declared as project-scoped but no project configuration declares it",
			})
		}
	}
	for _, name := range sortedProviders(available) {
		if !available[name].Available {
			out = append(out, model.Warning{
				Severity: model.SeverityInfo,
				Code:     model.WarnProviderUnavailable,
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
		Code:     model.WarnOwnerUnavailable,
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

// Explain returns the resolver's full reasoning for a package, from exactly the
// input Decide, install and sync use. explain and info must never describe a
// different decision from the one install would make.
func (e *Engine) Explain(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) resolve.Explanation {
	available := e.health(ctx)
	return e.resolver.Explain(e.resolveInput(pkg, e.findExisting(ctx, pkg, available), available, opts))
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

// Prune removes orphaned software installed by Starpoint-managed providers.
//
// Orphaned software is software that exists on the machine but is not declared
// in the configuration. System and global-scoped providers are never pruned,
// as Starpoint does not own or manage those installations.
func (e *Engine) Prune(ctx context.Context, doc *config.Document, opts Options) (*Result, error) {
	available := e.health(ctx)
	res := &Result{DryRun: opts.DryRun}

	// Build a set of all managed packages
	managed := map[string]bool{}
	for _, pkg := range doc.Packages() {
		managed[pkg.Name] = true
	}

	// A configuration that declares nothing cannot tell managed software from
	// orphans: every user-scoped package would look orphaned and be removed.
	// That is how a missing or mistyped configuration becomes a wiped machine,
	// so prune refuses rather than guessing. Removing everything is still
	// possible one package at a time, where the intent is explicit.
	if len(managed) == 0 {
		return res, errors.New("refusing to prune: the configuration declares no packages, " +
			"so there is no source of truth to tell managed software from orphans")
	}

	// Two more boundaries, because a provider's idea of "user scope" is only
	// as good as what it can ask its tool:
	//
	//   - A package manager is never an orphan of itself or of another
	//     provider. npm lists npm and corepack among its globals, pip lists
	//     pip, and uv, pnpm or yarn may have been installed through pip or
	//     npm; pruning them would disable a provider Starpoint relies on.
	//   - Only software inside the home directory is the user's to prune. A
	//     global npm prefix of /usr, a Python image's site-packages, or a
	//     CARGO_HOME under /usr/local belong to the machine, whoever runs
	//     Starpoint. A plain root shell therefore only ever prunes root's own
	//     home.
	protected := map[string]bool{"corepack": true, "starpoint": true}
	for _, q := range e.reg.All() {
		protected[q.Name()] = true
	}
	home := e.profileHome()

	var failures []string
	for _, p := range e.reg.All() {
		provName := p.Name()
		h := available[provName]

		// Skip unavailable providers
		if !h.Available {
			continue
		}

		// The system provider and every provider with a machine-wide default scope
		// are outside Starpoint's safe cleanup boundary.
		if provName == system.Name || p.Capabilities().DefaultScope == model.ScopeGlobal {
			continue
		}
		if !p.Capabilities().Remove || !p.Capabilities().Discover {
			continue
		}
		items, err := p.Discover(ctx)
		if err != nil {
			failures = append(failures, provName+": "+err.Error())
			res.Warnings = append(res.Warnings, model.Warning{
				Severity: model.SeverityError, Code: model.WarnDiscoverFailed, Provider: provName,
				Message: "could not list what " + provName + " has installed, so nothing it owns was considered: " + err.Error(),
			})
			continue
		}
		for _, inst := range items {
			if inst.Scope == model.ScopeGlobal || inst.Scope == model.ScopeProject || managed[inst.Name] {
				continue
			}
			if protected[inst.Name] {
				res.Warnings = append(res.Warnings, pruneSkipped(inst.Name, provName,
					"it is a package manager, and prune never removes one"))
				continue
			}
			inst := inst
			located := e.withPath(&inst)
			if where := e.pruneLocation(ctx, p, located); where != "" && home != "" && !insideDir(where, home) {
				res.Warnings = append(res.Warnings, pruneSkipped(inst.Name, provName,
					"it is outside your home directory ("+where+"), and prune only removes software inside "+home))
				continue
			}
			pkg := model.Package{Name: inst.Name, Provider: provName, Scope: model.ScopeUser}
			action := model.PlannedAction{
				Action:  model.ActionRemove,
				State:   model.State{Package: pkg, Current: located, Res: model.Resolution{Provider: provName}},
				Note:    "not declared in any configuration",
				Outcome: model.OutcomePlanned,
				Effects: e.plannedRemove(ctx, p, pkg, located),
			}
			model.SortEffects(action.Effects)
			if opts.DryRun {
				res.Applied = append(res.Applied, action)
				continue
			}
			obs := e.observe(ctx, p, pkg, located)
			err := p.Remove(ctx, pkg)
			observed := obs.finish(ctx, model.EffectDelete)
			if err != nil {
				failures = append(failures, provName+"/"+inst.Name+": "+err.Error())
				action = finishEffects(failed(action, err), observed)
				res.Applied = append(res.Applied, action)
				res.Warnings = append(res.Warnings, action.Warnings...)
				continue
			}
			observed = append(observed, packageEffect(model.EffectDelete, provName, pkg.Name, located,
				inst.Version, "", model.EvidenceReported))
			action = finishEffects(action, observed)
			action.Outcome = model.OutcomeApplied
			res.Applied = append(res.Applied, action)
			res.Warnings = append(res.Warnings, action.Warnings...)
		}
	}
	if len(failures) > 0 {
		return res, fmt.Errorf("prune incomplete:\n  %s", strings.Join(failures, "\n  "))
	}

	return res, nil
}

// pruneSkipped is the warning for an orphan prune left alone, so a skipped
// candidate is reported rather than silently dropped.
func pruneSkipped(name, provName, why string) model.Warning {
	return model.Warning{
		Severity: model.SeverityInfo, Code: model.WarnPruneSkipped, Package: name, Provider: provName,
		Message: "not removing " + name + " (" + provName + "), which no configuration declares: " + why,
	}
}

// pruneLocation is where a prune candidate lives on disk: its executable or
// package path when known, otherwise the first directory the provider says
// the package occupies. Empty means the location is unknown.
func (e *Engine) pruneLocation(ctx context.Context, p provider.Provider, inst *model.Installation) string {
	if inst != nil && inst.Path != "" {
		return inst.Path
	}
	if inst == nil {
		return ""
	}
	if d, ok := p.(provider.InstallDirProvider); ok {
		for _, dir := range d.InstallDirs(ctx, model.Package{Name: inst.Name}) {
			if dir != "" {
				return dir
			}
		}
	}
	return ""
}

// insideDir reports whether path is dir or below it, comparing the paths as
// written and with symlinks resolved, so a home reached through a symlink
// (/home -> /var/home) still counts as the same place.
func insideDir(path, dir string) bool {
	within := func(p, d string) bool {
		rel, err := filepath.Rel(d, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if within(path, dir) {
		return true
	}
	rp, err1 := filepath.EvalSymlinks(path)
	rd, err2 := filepath.EvalSymlinks(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	return within(rp, rd)
}
