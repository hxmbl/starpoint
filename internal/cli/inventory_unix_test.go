//go:build linux || darwin

package cli

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hxmbl/starpoint/internal/align"
	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
)

// ownerOf is the uid that owns path.
func ownerOf(t *testing.T, path string) uint32 {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Sys().(*syscall.Stat_t).Uid
}

// "Writes nothing" includes ownership. Every other command ends by giving
// Starpoint's own records (the configuration, the state directory, the
// locks) back to the user an elevated run acts for; inventory must not
// chown anything, not even what earlier runs left behind. The policy is
// pointed at the suite's own uid and its chowns recorded, so this runs as an
// ordinary user; TestAsRootInventoryChownsNothing checks the real thing.
func TestInventoryElevatedGivesNothingBack(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq"})
	home := filepath.Dir(h.config)
	h.elevated(viaSudo(home))
	var chowned []string
	h.env.AdjustAlignment = func(p *align.Policy) {
		p.FromUID = os.Getuid()
		p.Chown = func(path string, _, _ int) error {
			chowned = append(chowned, path)
			return nil
		}
	}
	touch := func() {
		// Changed since the run started, so the policy would take it.
		if err := os.Chmod(h.config, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	touch()
	if code, out, errOut := h.run("inventory"); code != 0 {
		t.Fatalf("inventory exit %d\n%s%s", code, out, errOut)
	}
	if len(chowned) != 0 {
		t.Errorf("inventory gave files to the invoking user: %v", chowned)
	}
	if entries := h.withEvent(auditlog.EventAction); len(entries) != 0 {
		t.Errorf("inventory recorded an action (the ownership fix) in the audit log: %+v", entries)
	}
	stateDir, _ := config.StateDir(h.config)
	if _, err := os.Stat(filepath.Join(stateDir, "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inventory must not write an audit log: %v", err)
	}

	// The same harness running a command that writes still reaches the
	// ownership fix, so the check above can tell the two apart.
	touch()
	if code, out, errOut := h.run("list"); code != 0 {
		t.Fatalf("list exit %d\n%s%s", code, out, errOut)
	}
	var gaveConfig bool
	for _, p := range chowned {
		if p == h.config {
			gaveConfig = true
		}
	}
	if !gaveConfig {
		t.Errorf("list should give the configuration back: chowned %v", chowned)
	}
}
