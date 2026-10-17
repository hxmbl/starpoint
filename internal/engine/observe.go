package engine

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

// This file is how Starpoint knows what an action did to the machine.
//
// A provider operation is a subprocess: Starpoint cannot see which files
// `uv tool uninstall` or `brew install` touched. What it can do is look at the
// places those operations write to immediately before and immediately after,
// and report the difference. That is the point of the effect: never "remove
// probably deletes these files", always "these files were there, and now they
// are not". Where a location could not be watched beforehand, the effect says
// so through its evidence rather than claiming more certainty than it has.

// fsEntry is what one path looked like at one moment.
type fsEntry struct {
	exists bool
	kind   model.EffectKind
	target string
	size   int64
	mode   fs.FileMode
	mod    time.Time
	info   os.FileInfo
}

// observation is a before-snapshot waiting for its after.
type observation struct {
	e        *Engine
	provider string
	name     string
	dirs     []string
	files    []string
	profiles map[string]bool
	before   map[string]fsEntry
	// command is what the package's name resolved to on PATH before the step.
	command string
	// watchedDirs and watchedFiles let finish tell an observed path from one
	// it only heard about.
	watchedDirs  map[string]bool
	watchedFiles map[string]bool
}

// shellProfiles are the start-up files a provider's installer is known to
// edit to put its directory on PATH (rustup, nvm, Homebrew's shellenv advice).
var shellProfiles = []string{
	".profile", ".bash_profile", ".bashrc", ".bash_login",
	".zshrc", ".zprofile", ".zshenv",
	filepath.Join(".config", "fish", "config.fish"),
}

// profileHome is the home directory whose shell profiles are watched.
func (e *Engine) profileHome() string {
	if e.home != "" {
		return e.home
	}
	home, _ := os.UserHomeDir()
	return home
}

// observe snapshots the places an operation by p on pkg could write to.
//
// The set is: the provider's own executable directory (uv and pip's user bin,
// Cargo's CARGO_HOME/bin, Go's GOBIN or GOPATH/bin), the directory of the
// provider's binary (where Homebrew and npm put the commands they install), the directory and path of every installation already known for
// the package, the directory of whatever the name resolves to on PATH, and the
// user's shell start-up files.
func (e *Engine) observe(ctx context.Context, p provider.Provider, pkg model.Package, known ...*model.Installation) *observation {
	return e.observeAll(ctx, []provider.Provider{p}, pkg, known...)
}

// observeAll is observe for an operation that involves several providers, such
// as a transition. Effects it finds are not attributed to one provider.
func (e *Engine) observeAll(ctx context.Context, ps []provider.Provider, pkg model.Package, known ...*model.Installation) *observation {
	o := &observation{
		e:            e,
		name:         pkg.Name,
		profiles:     map[string]bool{},
		before:       map[string]fsEntry{},
		watchedDirs:  map[string]bool{},
		watchedFiles: map[string]bool{},
	}
	var named []string
	for _, p := range ps {
		if p == nil {
			continue
		}
		named = append(named, p.Name())
		if dp, ok := p.(provider.ExecutableDirProvider); ok {
			o.addDir(dp.ExecutableDir(ctx))
		}
		if h, ok := e.health(ctx)[p.Name()]; ok && h.Binary != "" {
			o.addDir(filepath.Dir(h.Binary))
		}
	}
	if len(named) == 1 {
		o.provider = named[0]
	}
	for _, inst := range known {
		o.addInstallation(inst)
	}
	if e.locator != nil && executableShaped(pkg.Name) {
		if found := e.locator.Locate(pkg.Name); found.Path != "" {
			o.command = found.Path
			o.addDir(filepath.Dir(found.Path))
		}
	}
	if home := e.profileHome(); home != "" {
		for _, rel := range shellProfiles {
			path := filepath.Join(home, rel)
			o.profiles[path] = true
			o.addFile(path)
		}
	}
	o.before = o.snapshot()
	return o
}

func (o *observation) addDir(dir string) {
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return
	}
	dir = filepath.Clean(dir)
	if o.watchedDirs[dir] {
		return
	}
	if o.e != nil && tooBroadToWatch(dir, o.e.profileHome()) {
		// A misconfigured GOBIN or CARGO_HOME could name the home directory
		// itself. Watching it would snapshot everything the user keeps there
		// on every action, and report their own edits as the provider's.
		return
	}
	o.watchedDirs[dir] = true
	o.dirs = append(o.dirs, dir)
}

func (o *observation) addFile(path string) {
	if path == "" {
		return
	}
	path = filepath.Clean(path)
	if o.watchedFiles[path] {
		return
	}
	o.watchedFiles[path] = true
	o.files = append(o.files, path)
}

// tooBroadToWatch reports whether dir is the home directory, one of its
// ancestors, or a filesystem root. The watched set is a bounded list of
// directories that providers write commands into; none of those is ever the
// home directory itself, so a directory that resolves to one is a
// misconfiguration and is skipped rather than snapshotted.
func tooBroadToWatch(dir, home string) bool {
	dir = filepath.Clean(dir)
	if filepath.Dir(dir) == dir || !filepath.IsAbs(dir) {
		return true
	}
	if home == "" {
		return false
	}
	home = filepath.Clean(home)
	if dir == home {
		return true
	}
	rel, err := filepath.Rel(dir, home)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// addInstallation watches an installation's path: the path itself and, when
// it is a file, the directory beside it.
func (o *observation) addInstallation(inst *model.Installation) {
	if inst == nil || inst.Path == "" || !filepath.IsAbs(inst.Path) {
		return
	}
	o.addFile(inst.Path)
	if info, err := os.Lstat(inst.Path); err == nil && info.IsDir() {
		return
	}
	o.addDir(filepath.Dir(inst.Path))
}

// snapshot reads every watched location.
func (o *observation) snapshot() map[string]fsEntry {
	out := map[string]fsEntry{}
	for _, dir := range o.dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			out[full] = readEntry(full)
		}
	}
	for _, f := range o.files {
		if _, seen := out[f]; !seen {
			out[f] = readEntry(f)
		}
	}
	return out
}

func readEntry(path string) fsEntry {
	info, err := os.Lstat(path)
	if err != nil {
		return fsEntry{}
	}
	e := fsEntry{exists: true, size: info.Size(), mode: info.Mode(), mod: info.ModTime(), info: info}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		e.kind = model.EffectSymlink
		e.target, _ = os.Readlink(path)
	case info.IsDir():
		e.kind = model.EffectDirectory
	case info.Mode()&0o111 != 0:
		e.kind = model.EffectExecutable
	default:
		e.kind = model.EffectFile
	}
	return e
}

// finish takes the after-snapshot and returns what changed.
//
// reported are installations the provider returned from the operation. A
// reported path in a location that was not watched beforehand is still listed,
// with evidence "reported", because a path the user cannot see is worse than a
// path whose prior state is unknown.
func (o *observation) finish(ctx context.Context, reportedOp model.EffectOp, reported ...*model.Installation) []model.Effect {
	_ = ctx
	after := o.snapshot()
	pathDirs := pathDirectories(o.e)
	var out []model.Effect

	keys := map[string]bool{}
	for k := range o.before {
		keys[k] = true
	}
	for k := range after {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, path := range sorted {
		b, a := o.before[path], after[path]
		eff, changed := diffEntry(path, b, a)
		if !changed {
			continue
		}
		eff.Provider = o.provider
		eff.Evidence = model.EvidenceObserved
		if o.profiles[path] {
			eff.Kind = model.EffectShellProfile
			eff.Environment = true
			eff.Destructive = eff.Op == model.EffectDelete || eff.Op == model.EffectReplace
			eff.Detail = "a shell start-up file changed; new shells may see a different PATH or environment"
		} else if pathDirs[filepath.Dir(path)] {
			eff.Environment = true
		}
		out = append(out, eff)
	}

	for _, inst := range reported {
		if inst == nil || inst.Path == "" || !filepath.IsAbs(inst.Path) {
			continue
		}
		path := filepath.Clean(inst.Path)
		if o.watchedFiles[path] || o.watchedDirs[filepath.Dir(path)] {
			continue
		}
		entry := readEntry(path)
		if !entry.exists {
			continue
		}
		out = append(out, model.Effect{
			Op:          reportedOp,
			Kind:        entry.kind,
			Path:        path,
			Provider:    o.provider,
			Environment: pathDirs[filepath.Dir(path)],
			Evidence:    model.EvidenceReported,
			Detail:      "reported by the provider; this location was not watched beforehand, so what was there before is unknown",
		})
	}

	if o.e.locator != nil && executableShaped(o.name) {
		now := o.e.locator.Locate(o.name).Path
		if eff, ok := commandEffect(o.name, o.command, now); ok {
			eff.Provider = o.provider
			out = append(out, eff)
		}
	}
	return out
}

// diffEntry compares one path across the step.
func diffEntry(path string, b, a fsEntry) (model.Effect, bool) {
	switch {
	case !b.exists && !a.exists:
		return model.Effect{}, false
	case !b.exists:
		return model.Effect{Op: model.EffectCreate, Kind: a.kind, Path: path, To: a.target}, true
	case !a.exists:
		return model.Effect{Op: model.EffectDelete, Kind: b.kind, Path: path, From: b.target, Destructive: true}, true
	case b.kind != a.kind || b.target != a.target:
		return model.Effect{Op: model.EffectReplace, Kind: a.kind, Path: path, From: b.target, To: a.target,
			Destructive: true, Detail: "what was at this path was replaced (" + string(b.kind) + " → " + string(a.kind) + ")"}, true
	case a.kind == model.EffectDirectory:
		// A directory's own timestamp changes whenever anything inside it does;
		// its contents are watched separately where it matters.
		return model.Effect{}, false
	case b.info != nil && a.info != nil && !os.SameFile(b.info, a.info):
		// Deleted and written again: a different file now sits at the path.
		// Filesystems that reuse the inode make this look like an in-place
		// rewrite instead, which is reported as a modify below; both are
		// destructive.
		return model.Effect{Op: model.EffectReplace, Kind: a.kind, Path: path, Destructive: true,
			Detail: "the file at this path was deleted and written again"}, true
	case b.size != a.size || b.mode != a.mode || !b.mod.Equal(a.mod):
		// Rewritten in place. Whatever it held before is gone, which is the
		// definition of destructive used throughout; shell profiles are the
		// exception and are reported through their own warning.
		return model.Effect{Op: model.EffectModify, Kind: a.kind, Path: path, Destructive: true,
			Detail: "the file's content changed in place"}, true
	}
	return model.Effect{}, false
}

// commandEffect describes a change in what a command name resolves to.
func commandEffect(name, before, after string) (model.Effect, bool) {
	if before == after {
		return model.Effect{}, false
	}
	eff := model.Effect{Kind: model.EffectCommand, Name: name, From: before, To: after,
		Environment: true, Evidence: model.EvidenceObserved}
	switch {
	case before == "":
		eff.Op = model.EffectCreate
		eff.Detail = name + " is now on PATH"
	case after == "":
		eff.Op = model.EffectDelete
		eff.Destructive = true
		eff.Detail = name + " no longer resolves on PATH"
	default:
		eff.Op = model.EffectModify
		eff.Detail = name + " now resolves to a different file"
	}
	return eff, true
}

// pathDirectories is the set of directories on PATH, cleaned.
func pathDirectories(e *Engine) map[string]bool {
	path := ""
	if e != nil && e.locator != nil {
		path = e.locator.SearchPath
	}
	if path == "" {
		path = os.Getenv("PATH")
	}
	out := map[string]bool{}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		out[filepath.Clean(dir)] = true
	}
	return out
}

// onPath reports whether dir is a PATH directory.
func (e *Engine) onPath(dir string) bool {
	return pathDirectories(e)[filepath.Clean(dir)]
}

// packageEffect is the provider-level record of an action on a package. It is
// listed whether or not any path was observed, because "uv uninstalled ruff"
// is a fact even when uv keeps its files somewhere Starpoint cannot see.
func packageEffect(op model.EffectOp, providerName string, pkgName string, inst *model.Installation, from, to string, evidence model.Evidence) model.Effect {
	eff := model.Effect{
		Op:          op,
		Kind:        model.EffectPackage,
		Name:        pkgName,
		Provider:    providerName,
		From:        from,
		To:          to,
		Destructive: op == model.EffectDelete,
		Evidence:    evidence,
	}
	if inst != nil {
		eff.Path = inst.Path
	}
	switch op {
	case model.EffectCreate:
		eff.Detail = providerName + " installs " + pkgName
	case model.EffectDelete:
		eff.Detail = providerName + " uninstalls " + pkgName
	case model.EffectModify:
		eff.Detail = providerName + " changes the installed version of " + pkgName
	}
	return eff
}

// plannedInstall predicts what installing pkg through p would touch.
func (e *Engine) plannedInstall(ctx context.Context, p provider.Provider, pkg model.Package) ([]model.Effect, []model.Warning) {
	if p == nil {
		return nil, nil
	}
	effects := []model.Effect{packageEffect(model.EffectCreate, p.Name(), pkg.Name, nil, "", pkg.Version, model.EvidencePlanned)}
	var warnings []model.Warning
	dp, ok := p.(provider.ExecutableDirProvider)
	if !ok || !executableShaped(pkg.Name) {
		return effects, warnings
	}
	dir := dp.ExecutableDir(ctx)
	if dir == "" {
		return effects, warnings
	}
	path := filepath.Join(dir, pkg.Name)
	env := e.onPath(dir)
	if entry := readEntry(path); entry.exists {
		effects = append(effects, model.Effect{
			Op: model.EffectReplace, Kind: entry.kind, Path: path, From: entry.target, Provider: p.Name(),
			Destructive: true, Environment: env, Evidence: model.EvidencePlanned,
			Detail: "something is already at the path " + p.Name() + " installs to; installing can overwrite it",
		})
	} else {
		effects = append(effects, model.Effect{
			Op: model.EffectCreate, Kind: model.EffectExecutable, Path: path, Provider: p.Name(),
			Environment: env, Evidence: model.EvidencePlanned,
			Detail: "where " + p.Name() + " puts the commands it installs",
		})
	}
	if !env {
		warnings = append(warnings, model.Warning{
			Severity: model.SeverityWarn, Code: model.WarnNotOnPath, Package: pkg.Name, Provider: p.Name(),
			Message: dir + " is not on PATH, so " + pkg.Name + " will not be runnable by name until it is",
		})
	}
	return effects, warnings
}

// plannedRemove predicts what removing pkg through p would delete: the
// package itself, then every file Starpoint can see that belongs to it.
func (e *Engine) plannedRemove(ctx context.Context, p provider.Provider, pkg model.Package, inst *model.Installation) []model.Effect {
	if p == nil {
		return nil
	}
	from := ""
	if inst != nil {
		from = inst.Version
	}
	effects := []model.Effect{packageEffect(model.EffectDelete, p.Name(), pkg.Name, inst, from, "", model.EvidencePlanned)}
	seen := map[string]bool{}
	add := func(path, why string) {
		if path == "" || !filepath.IsAbs(path) || seen[filepath.Clean(path)] {
			return
		}
		path = filepath.Clean(path)
		entry := readEntry(path)
		if !entry.exists {
			return
		}
		seen[path] = true
		effects = append(effects, model.Effect{
			Op: model.EffectDelete, Kind: entry.kind, Path: path, From: entry.target, Provider: p.Name(),
			Destructive: true, Environment: e.onPath(filepath.Dir(path)), Evidence: model.EvidencePlanned, Detail: why,
		})
	}
	if inst != nil {
		add(inst.Path, "the install location "+p.Name()+" reports")
	}
	if dp, ok := p.(provider.ExecutableDirProvider); ok && executableShaped(pkg.Name) {
		if dir := dp.ExecutableDir(ctx); dir != "" {
			candidate := filepath.Join(dir, pkg.Name)
			if e.ownedBy(candidate, p.Name(), inst) {
				add(candidate, "the command "+p.Name()+" put on PATH")
			}
		}
	}
	if e.locator != nil && executableShaped(pkg.Name) {
		found := e.locator.Locate(pkg.Name)
		if found.Path != "" && e.ownedBy(found.Path, p.Name(), inst) {
			add(found.Path, "what "+pkg.Name+" resolves to on PATH")
			effects = append(effects, model.Effect{
				Op: model.EffectDelete, Kind: model.EffectCommand, Name: pkg.Name, From: found.Path, Provider: p.Name(),
				Destructive: true, Environment: true, Evidence: model.EvidencePlanned,
				Detail: pkg.Name + " will stop resolving to " + found.Path + " (another copy further down PATH would take over, if there is one)",
			})
		}
	}
	return effects
}

// ownedBy reports whether a path belongs to a provider's copy of a package:
// it is the path the provider reported, or PATH attribution names the
// provider.
func (e *Engine) ownedBy(path, providerName string, inst *model.Installation) bool {
	if inst != nil && inst.Path != "" && (filepath.Clean(inst.Path) == filepath.Clean(path) || samePath(inst.Path, path)) {
		return true
	}
	if e.locator == nil {
		return false
	}
	_, owner, ok := e.locator.AttributeExecutable(path)
	return ok && owner == providerName
}

// effectWarnings derives warnings from what actually happened.
func effectWarnings(pkg string, effects []model.Effect) []model.Warning {
	var out []model.Warning
	for _, eff := range effects {
		switch {
		case eff.Kind == model.EffectShellProfile && eff.Evidence != model.EvidencePlanned:
			out = append(out, model.Warning{
				Severity: model.SeverityWarn, Code: model.WarnShellProfileChanged, Package: pkg, Provider: eff.Provider,
				Message: eff.Path + " changed while " + pkg + " was being changed; new shells may see a different PATH",
			})
		case eff.Kind == model.EffectCommand && eff.Op == model.EffectDelete && eff.Evidence == model.EvidenceObserved:
			out = append(out, model.Warning{
				Severity: model.SeverityWarn, Code: model.WarnCommandRemoved, Package: pkg, Provider: eff.Provider,
				Message: eff.Name + " no longer resolves on PATH (it was " + eff.From + ")",
			})
		}
	}
	return out
}

// Watch observes directories around a step Starpoint runs that is not a
// provider operation, such as a project's own dependency installer, so its
// effects are reported like any other.
type Watch struct{ o *observation }

// WatchDirs snapshots dirs (one level deep) and the user's shell profiles.
func (e *Engine) WatchDirs(dirs ...string) *Watch {
	o := &observation{
		e:            e,
		profiles:     map[string]bool{},
		watchedDirs:  map[string]bool{},
		watchedFiles: map[string]bool{},
	}
	for _, d := range dirs {
		o.addDir(d)
	}
	if home := e.profileHome(); home != "" {
		for _, rel := range shellProfiles {
			path := filepath.Join(home, rel)
			o.profiles[path] = true
			o.addFile(path)
		}
	}
	o.before = o.snapshot()
	return &Watch{o: o}
}

// Finish reports what changed in the watched directories since WatchDirs.
func (w *Watch) Finish(ctx context.Context) []model.Effect {
	effects := w.o.finish(ctx, model.EffectModify)
	model.SortEffects(effects)
	return effects
}
