package providers

import (
	"context"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"sync"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

// Privileges decides, for one run, which account each provider's commands
// run as, and hands each provider a runner that does it.
//
// Only an elevated run on behalf of an invoking user (Identity set) has a
// choice to make. Each provider is classified once, through its Privileged
// method: PrivilegeUser providers get a runner that starts every command as
// the user, PrivilegeRoot providers one that runs as root, as Starpoint
// does. A provider that does not classify itself runs as root. The decision
// is remembered for the run, so a provider's inventory, install and
// verification all run as the same account, and run_as reports what actually
// happened.
type Privileges struct {
	// Identity is the invoking user's identity, or nil when nothing is
	// dropped: an ordinary run, a plain root run, or an elevated run whose
	// privileges could not be dropped.
	Identity *exec.Identity
	// Elevated is whether Starpoint itself runs as root or elevated.
	Elevated bool
	// Unavailable says why an elevated run with an invoking user has no
	// Identity (it could not be resolved, or dropping privileges failed). It
	// is reported in run_as.
	Unavailable string

	mu        sync.Mutex
	decisions map[string]provider.PrivilegeDecision
}

// home is the home directory providers are built for.
func (pv *Privileges) home() string {
	if pv != nil && pv.Identity != nil && pv.Identity.Home != "" {
		return pv.Identity.Home
	}
	home, _ := os.UserHomeDir()
	return home
}

// dropping reports whether this run starts any command as another user.
func (pv *Privileges) dropping() bool { return pv != nil && pv.Identity != nil }

// target is the invoking user, for classification.
func (pv *Privileges) target() provider.PrivilegeTarget {
	return provider.PrivilegeTarget{User: pv.Identity.User, UID: pv.Identity.UID, Home: pv.Identity.Home}
}

// Decide classifies p for this run. Without an Identity every provider runs
// as Starpoint itself, and the decision is PrivilegeRoot when elevated.
func (pv *Privileges) Decide(ctx context.Context, p provider.Provider) provider.PrivilegeDecision {
	if !pv.dropping() || p == nil {
		return provider.PrivilegeDecision{Privilege: provider.PrivilegeRoot}
	}
	pv.mu.Lock()
	if d, ok := pv.decisions[p.Name()]; ok {
		pv.mu.Unlock()
		return d
	}
	pv.mu.Unlock()
	d := provider.PrivilegeDecision{Privilege: provider.PrivilegeRoot,
		Reason: p.Name() + " does not say where it installs, so it runs as root"}
	if c, ok := p.(provider.Privileged); ok {
		d = c.Privilege(ctx, exec.RealRunner{As: pv.Identity}, pv.target())
		if d.Privilege != provider.PrivilegeUser {
			d.Privilege = provider.PrivilegeRoot
		}
	}
	pv.mu.Lock()
	defer pv.mu.Unlock()
	if pv.decisions == nil {
		pv.decisions = map[string]provider.PrivilegeDecision{}
	}
	if prior, ok := pv.decisions[p.Name()]; ok {
		return prior
	}
	pv.decisions[p.Name()] = d
	return d
}

// Runner is the runner for the provider get returns.
func (pv *Privileges) Runner(get func() provider.Provider) exec.Runner {
	if !pv.dropping() {
		return exec.RealRunner{}
	}
	asUser := exec.RealRunner{As: pv.Identity}
	asRoot := exec.RealRunner{}
	return exec.Chooser{Choose: func(ctx context.Context) exec.Runner {
		if pv.Decide(ctx, get()).Privilege == provider.PrivilegeUser {
			return asUser
		}
		return asRoot
	}}
}

// RunAs reports the account p's commands run as in this run.
func (pv *Privileges) RunAs(ctx context.Context, p provider.Provider) model.RunAs {
	name := ""
	if p != nil {
		name = p.Name()
	}
	if pv.dropping() {
		d := pv.Decide(ctx, p)
		if d.Privilege == provider.PrivilegeUser {
			return model.RunAs{Provider: name, User: pv.Identity.User, UID: pv.Identity.UID, GID: pv.Identity.GID,
				Elevated: true, Dropped: true, Privilege: string(d.Privilege), Reason: d.Reason}
		}
		self := Self()
		self.Provider, self.Elevated, self.Privilege, self.Reason = name, true, string(d.Privilege), d.Reason
		return self
	}
	self := Self()
	self.Provider = name
	if pv != nil && pv.Elevated {
		self.Elevated = true
		self.Reason = "plain root run: there is no invoking user to act for"
		if pv.Unavailable != "" {
			self.Reason = pv.Unavailable
		}
	}
	return self
}

var (
	selfOnce sync.Once
	self     model.RunAs
)

// Self is the account Starpoint itself runs as.
func Self() model.RunAs {
	selfOnce.Do(func() {
		self = model.RunAs{UID: os.Getuid(), GID: os.Getgid()}
		if u, err := user.Current(); err == nil {
			self.User = u.Username
		} else if runtime.GOOS != "windows" {
			self.User = strconv.Itoa(self.UID)
		}
	})
	return self
}
