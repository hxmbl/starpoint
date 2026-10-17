// Package align keeps what an elevated run creates usable by the people it
// was created for.
//
// Two things go wrong when a package manager runs as root. Files written into
// a user's home (because the run acts for the user who typed sudo) end up
// owned by root, so that user's next ordinary `cargo install` or `uv tool
// install` fails on a directory it cannot write. And files written into shared
// locations (/usr/local, /opt) inherit root's umask, often 077 or 027, so
// nobody but root can run what was installed.
//
// Policy fixes both, for the paths a provider actually touched during the run
// and nothing else:
//
//   - Under the target home, entries owned by root and changed since the run
//     started are given to the target user (chown). Their ancestors up to,
//     but never including, the home directory get the same treatment, since a
//     provider that created ~/.local/bin also created ~/.local. Modes are left
//     alone: the user's own umask-shaped modes are theirs to keep.
//   - Outside the target home, what the provider created (or replaced) is
//     made readable by everyone, and directories and executables traversable
//     and executable by everyone (0755 at least; 0644 at least for other
//     files). Bits are only ever added, never removed. Anything that existed
//     before the run, and anything that looks like a secret or credential, is
//     left alone.
//
// Every fix is reported as an effect with op chown or chmod and evidence
// "observed" (the path is read back after the change), and a fix that fails
// is reported with its error so the caller can warn.
package align

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/model"
)

// Owner is a uid and gid.
type Owner struct {
	UID int
	GID int
}

// Policy decides which ownership and permission fixes an elevated run makes.
type Policy struct {
	// Home is the home directory the run acts on: the invoking user's under
	// sudo or doas, root's own otherwise. Paths under it are the user's; paths
	// outside it are shared.
	Home string
	// Owner is who should own what is created under Home. Nil means nobody
	// is reassigned: a plain root run acting on root's own home.
	Owner *Owner
	// User names the owner for messages.
	User string
	// FromUID is the uid whose entries are reassigned: 0, the elevated
	// account. Tests set it to their own uid.
	FromUID int
	// Since is when the run started. Only entries whose status changed since
	// then are reassigned, so files that were root-owned before the run (left
	// by an earlier sudo, say) are not silently taken over.
	Since time.Time
	// MaxEntries bounds how many entries one fix may walk. Zero means
	// DefaultMaxEntries.
	MaxEntries int

	// Chown and Chmod replace os.Lchown and os.Chmod when set, for tests
	// that cannot change owners. A replaced Chown is not read back.
	Chown func(path string, uid, gid int) error
	Chmod func(path string, mode fs.FileMode) error
}

// DefaultMaxEntries bounds a single tree walk. A provider that writes more
// than this in one action is reported as only partly aligned rather than
// walked indefinitely.
const DefaultMaxEntries = 200000

// Touched is a path a provider touched during an action.
type Touched struct {
	Path string
	// Created is true when the path did not exist before the action, or was
	// replaced by something new. Only created paths have their permissions
	// widened.
	Created bool
}

// stat is what align needs to know about one entry.
type stat struct {
	uid, gid int
	ctime    time.Time
	known    bool
}

// Enabled reports whether the policy would do anything on this platform.
func (p *Policy) Enabled() bool { return p != nil && supported }

// Align fixes ownership and permissions for the touched paths and returns one
// effect per fixed path. A directory's fixes inside it are summarised on the
// directory's effect rather than listed one by one.
func (p *Policy) Align(touched []Touched) []model.Effect {
	if !p.Enabled() {
		return nil
	}
	var out []model.Effect
	seen := map[string]bool{}
	for _, t := range touched {
		if t.Path == "" || !filepath.IsAbs(t.Path) {
			continue
		}
		path := filepath.Clean(t.Path)
		if seen[path] {
			continue
		}
		seen[path] = true
		if p.underHome(path) {
			if p.Owner == nil {
				continue
			}
			for _, anc := range p.ancestors(path) {
				if seen["ancestor\x00"+anc] {
					continue
				}
				seen["ancestor\x00"+anc] = true
				if eff, ok := p.chownOne(anc); ok {
					out = append(out, eff)
				}
			}
			if eff, ok := p.chownTree(path); ok {
				out = append(out, eff)
			}
			continue
		}
		if t.Created {
			if eff, ok := p.chmodTree(path); ok {
				out = append(out, eff)
			}
		}
	}
	return out
}

// Tree reassigns everything under root that the run changed. It is for
// Starpoint's own records and for a provider's state directories (Cargo's
// registry, Go's module cache) under the target home, where the provider
// writes far from the paths it reports.
func (p *Policy) Tree(root string) []model.Effect {
	if !p.Enabled() || p.Owner == nil || root == "" || !p.underHome(filepath.Clean(root)) {
		return nil
	}
	if _, err := os.Lstat(root); err != nil {
		return nil
	}
	var out []model.Effect
	for _, anc := range p.ancestors(filepath.Clean(root)) {
		if eff, ok := p.chownOne(anc); ok {
			out = append(out, eff)
		}
	}
	if eff, ok := p.chownTree(filepath.Clean(root)); ok {
		out = append(out, eff)
	}
	return out
}

// underHome reports whether path lies strictly inside the target home.
func (p *Policy) underHome(path string) bool {
	if p.Home == "" {
		return false
	}
	home := filepath.Clean(p.Home)
	rel, err := filepath.Rel(home, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ancestors lists the directories between the home and path, outermost
// first, excluding both.
func (p *Policy) ancestors(path string) []string {
	home := filepath.Clean(p.Home)
	var out []string
	for dir := filepath.Dir(path); dir != home && p.underHome(dir); dir = filepath.Dir(dir) {
		out = append([]string{dir}, out...)
	}
	return out
}

// needsChown reports whether an entry belongs to the elevated account and was
// changed during the run.
func (p *Policy) needsChown(path string) (stat, bool) {
	st, err := lstat(path)
	if err != nil || !st.known {
		return st, false
	}
	if st.uid != p.FromUID || (st.uid == p.Owner.UID && st.gid == p.Owner.GID) {
		return st, false
	}
	return st, !st.ctime.Before(p.Since)
}

func (p *Policy) doChown(path string) error {
	if p.Chown != nil {
		return p.Chown(path, p.Owner.UID, p.Owner.GID)
	}
	return os.Lchown(path, p.Owner.UID, p.Owner.GID)
}

func (p *Policy) doChmod(path string, mode fs.FileMode) error {
	if p.Chmod != nil {
		return p.Chmod(path, mode)
	}
	return os.Chmod(path, mode)
}

// chownOne reassigns a single entry.
func (p *Policy) chownOne(path string) (model.Effect, bool) {
	st, ok := p.needsChown(path)
	if !ok {
		return model.Effect{}, false
	}
	eff := p.chownEffect(path, st)
	if err := p.doChown(path); err != nil {
		eff.Error = err.Error()
	}
	return eff, true
}

// chownTree reassigns path and, when it is a directory, everything below it
// that the run changed.
func (p *Policy) chownTree(path string) (model.Effect, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return model.Effect{}, false
	}
	top, topOK := p.needsChown(path)
	var eff model.Effect
	fixed, failures, firstErr := 0, 0, ""
	if topOK {
		eff = p.chownEffect(path, top)
		if err := p.doChown(path); err != nil {
			failures++
			firstErr = err.Error()
		}
	}
	below := 0
	truncated := false
	if info.IsDir() {
		limit := p.limit()
		walked := 0
		_ = filepath.WalkDir(path, func(sub string, d fs.DirEntry, err error) error {
			if err != nil || sub == path {
				return nil
			}
			walked++
			if walked > limit {
				truncated = true
				return filepath.SkipAll
			}
			if _, ok := p.needsChown(sub); !ok {
				return nil
			}
			if err := p.doChown(sub); err != nil {
				failures++
				if firstErr == "" {
					firstErr = sub + ": " + err.Error()
				}
				return nil
			}
			below++
			return nil
		})
	}
	fixed = below
	if !topOK && fixed == 0 && failures == 0 && !truncated {
		return model.Effect{}, false
	}
	if !topOK {
		st, _ := lstat(path)
		eff = p.chownEffect(path, st)
		eff.From = ""
		eff.Detail = fmt.Sprintf("entries below given to %s", p.ownerName())
	}
	if below > 0 {
		eff.Detail += fmt.Sprintf("; %d entr%s below", below, plural(below, "y", "ies"))
	}
	if truncated {
		eff.Detail += fmt.Sprintf("; stopped after %d entries, so some may still belong to root", p.limit())
		if firstErr == "" {
			firstErr = "too many entries to align in one action"
		}
	}
	if failures > 0 || truncated {
		eff.Error = firstErr
	}
	if eff.Error == "" {
		p.verify(&eff)
	}
	return eff, true
}

func (p *Policy) chownEffect(path string, st stat) model.Effect {
	return model.Effect{
		Op:       model.EffectChown,
		Kind:     kindOf(path),
		Path:     path,
		From:     fmt.Sprintf("%d:%d", st.uid, st.gid),
		To:       fmt.Sprintf("%d:%d", p.Owner.UID, p.Owner.GID),
		Evidence: model.EvidenceObserved,
		Detail:   fmt.Sprintf("given to %s, for whom the run acted, so it is not left owned by root", p.ownerName()),
	}
}

// verify reads the owner back, so the effect reports what is on disk.
func (p *Policy) verify(eff *model.Effect) {
	if p.Chown != nil {
		return // a test double changed nothing on disk
	}
	st, err := lstat(eff.Path)
	if err != nil || !st.known {
		return
	}
	if eff.Op == model.EffectChown && eff.From != "" && (st.uid != p.Owner.UID || st.gid != p.Owner.GID) {
		eff.Error = fmt.Sprintf("still owned by %d:%d after the change", st.uid, st.gid)
	}
}

// wantMode is the mode a created entry in a shared location should have at
// least: everyone may read it, and traverse or execute it when it is a
// directory or an executable.
func wantMode(mode fs.FileMode) fs.FileMode {
	perm := mode.Perm()
	switch {
	case mode.IsDir(), perm&0o111 != 0:
		perm |= 0o755
	case mode.IsRegular():
		perm |= 0o644
	}
	return perm
}

// chmodTree widens a created path and everything below it.
func (p *Policy) chmodTree(path string) (model.Effect, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&fs.ModeSymlink != 0 {
		return model.Effect{}, false
	}
	eff := model.Effect{
		Op:       model.EffectChmod,
		Kind:     kindOf(path),
		Path:     path,
		Evidence: model.EvidenceObserved,
		Detail:   "made readable (and, for directories and executables, executable) by everyone, so the elevated account's umask does not leave it usable only by root",
	}
	topChanged := false
	failures, firstErr := 0, ""
	if !secretLooking(path) {
		if want := wantMode(info.Mode()); want != info.Mode().Perm() {
			eff.From = fmt.Sprintf("%04o", info.Mode().Perm())
			eff.To = fmt.Sprintf("%04o", want)
			topChanged = true
			if err := p.doChmod(path, want|(info.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))); err != nil {
				failures++
				firstErr = err.Error()
			}
		}
	}
	below, truncated := 0, false
	if info.IsDir() {
		limit := p.limit()
		walked := 0
		_ = filepath.WalkDir(path, func(sub string, d fs.DirEntry, err error) error {
			if err != nil || sub == path {
				return nil
			}
			walked++
			if walked > limit {
				truncated = true
				return filepath.SkipAll
			}
			if d.Type()&fs.ModeSymlink != 0 || secretLooking(sub) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			want := wantMode(fi.Mode())
			if want == fi.Mode().Perm() {
				return nil
			}
			if err := p.doChmod(sub, want|(fi.Mode()&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky))); err != nil {
				failures++
				if firstErr == "" {
					firstErr = sub + ": " + err.Error()
				}
				return nil
			}
			below++
			return nil
		})
	}
	if !topChanged && below == 0 && failures == 0 && !truncated {
		return model.Effect{}, false
	}
	if below > 0 {
		eff.Detail += fmt.Sprintf("; %d entr%s below", below, plural(below, "y", "ies"))
	}
	if truncated {
		eff.Detail += fmt.Sprintf("; stopped after %d entries", p.limit())
		if firstErr == "" {
			firstErr = "too many entries to align in one action"
		}
	}
	if failures > 0 || truncated {
		eff.Error = firstErr
	} else if topChanged && p.Chmod == nil {
		if fi, err := os.Lstat(path); err == nil && fmt.Sprintf("%04o", fi.Mode().Perm()) != eff.To {
			eff.Error = fmt.Sprintf("mode is %04o after the change", fi.Mode().Perm())
		}
	}
	return eff, true
}

func (p *Policy) limit() int {
	if p.MaxEntries > 0 {
		return p.MaxEntries
	}
	return DefaultMaxEntries
}

func (p *Policy) ownerName() string {
	if p.User != "" {
		return p.User
	}
	return fmt.Sprintf("uid %d", p.Owner.UID)
}

// secretLooking reports whether a path's name suggests credentials or keys,
// which must never be made world-readable however they were created.
func secretLooking(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case ".netrc", ".npmrc", ".pypirc", ".git-credentials", "credentials", "credentials.toml", ".credentials.toml",
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa", ".env":
		return true
	}
	for _, s := range []string{".key", ".pem", ".p12", ".pfx", ".gpg", ".asc"} {
		if strings.HasSuffix(base, s) {
			return true
		}
	}
	return strings.Contains(base, "secret") || strings.Contains(base, "token") || strings.Contains(base, "password")
}

func kindOf(path string) model.EffectKind {
	info, err := os.Lstat(path)
	if err != nil {
		return model.EffectFile
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return model.EffectSymlink
	case info.IsDir():
		return model.EffectDirectory
	case info.Mode()&0o111 != 0:
		return model.EffectExecutable
	default:
		return model.EffectFile
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
