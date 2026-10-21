package provider

import (
	"context"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
)

// Privilege is the account a provider's commands run as when Starpoint runs
// elevated on behalf of an invoking user (sudo or doas from an ordinary
// account).
type Privilege string

const (
	// PrivilegeUser providers install into the invoking user's home, or into
	// a prefix that user owns, so their commands run as that user: root adds
	// nothing but root-owned files in the user's home.
	PrivilegeUser Privilege = "user"
	// PrivilegeRoot providers install into system locations, where root is
	// the point: apt, dnf, pacman, flatpak, snap, and npm, pnpm, yarn, gem or
	// Homebrew when their prefix is a system directory.
	PrivilegeRoot Privilege = "root"
)

// PrivilegeTarget is the invoking user a classification is for.
type PrivilegeTarget struct {
	User string
	UID  int
	Home string
}

// PrivilegeDecision is a provider's classification and why.
type PrivilegeDecision struct {
	Privilege Privilege
	// Reason is one sentence for the run_as record and --verbose output.
	Reason string
}

// Privileged is implemented by every built-in provider: it says which
// account its commands need under an elevated run for t.
//
// The classification is per provider and per machine, not per command: a
// provider's install, removal, update and inventory all run as the same
// account, so that what one command writes the next can read and replace.
// Providers whose answer depends on where they install (npm's global prefix,
// Homebrew's prefix, gem's installation directory) ask through q, which runs
// read-only queries as the invoking user, with the user's own configuration.
// A provider that does not implement this interface runs as root.
type Privileged interface {
	Privilege(ctx context.Context, q exec.Runner, t PrivilegeTarget) PrivilegeDecision
}

// InstallDirProvider is implemented by providers that know the directory a
// package lands in beyond its executable, such as npm's
// <prefix>/lib/node_modules/<name>. An elevated run that installs there as
// root makes what it created readable by everyone, as it does for the
// executable directory; under the user's home the directory is checked for
// anything left to root.
type InstallDirProvider interface {
	InstallDirs(ctx context.Context, pkg model.Package) []string
}
