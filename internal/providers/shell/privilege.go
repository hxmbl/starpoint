package shell

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hxmbl/starpoint/internal/privdrop"
	"github.com/hxmbl/starpoint/internal/provider"
)

// UserPrivilege is the decision of a provider that always installs into the
// user's home.
func UserPrivilege(reason string) provider.PrivilegeDecision {
	return provider.PrivilegeDecision{Privilege: provider.PrivilegeUser, Reason: reason}
}

// RootPrivilege is the decision of a provider that installs system software.
func RootPrivilege(reason string) provider.PrivilegeDecision {
	return provider.PrivilegeDecision{Privilege: provider.PrivilegeRoot, Reason: reason}
}

// PrivilegeAt classifies a provider by the directory it installs into: as
// the user when the directory is inside the user's home or owned by the user
// (a Homebrew prefix on macOS, a user-owned /usr/local), as root otherwise.
// what names the directory for the reason ("npm's global prefix").
//
// An unknown directory means the provider could not say where it installs;
// it runs as root, as it did before privileges were dropped for anyone, and
// what it creates in shared locations is widened as before.
func PrivilegeAt(dir, what string, t provider.PrivilegeTarget) provider.PrivilegeDecision {
	dir = strings.TrimSpace(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return RootPrivilege(fmt.Sprintf("%s could not be determined, so it runs as root", what))
	}
	dir = filepath.Clean(dir)
	if t.Home != "" {
		home := filepath.Clean(t.Home)
		if rel, err := filepath.Rel(home, dir); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return UserPrivilege(fmt.Sprintf("%s %s is in %s's home", what, dir, t.User))
		}
	}
	if t.UID > 0 && privdrop.OwnedBy(dir, t.UID) {
		return UserPrivilege(fmt.Sprintf("%s %s belongs to %s", what, dir, t.User))
	}
	return RootPrivilege(fmt.Sprintf("%s %s is a system location", what, dir))
}
