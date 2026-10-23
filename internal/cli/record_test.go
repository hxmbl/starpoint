package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// An entry with no provider whose software is already installed gets its
// owner recorded by sync: a configuration-only change, shown and confirmed
// like any other, and never an install.
func TestSyncRecordsTheOwnerOfAnInstalledEntry(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7"})

	before := h.configBody()
	dry := h.mustRun("sync", "--dry-run")
	if !strings.Contains(dry, "record") || h.configBody() != before {
		t.Errorf("a dry run shows the record and writes nothing:\n%s", dry)
	}

	h.mustRun("sync")
	if !h.asked() {
		t.Error("recording an owner is a change, so sync asks first")
	}
	if !strings.Contains(h.configBody(), `provider = "brew"`) {
		t.Errorf("the owner should be recorded:\n%s", h.configBody())
	}
	if len(h.brew.InstallCalls)+len(h.brew.RemoveCalls) != 0 {
		t.Error("recording installs and removes nothing")
	}
	if out := h.mustRun("sync"); !strings.Contains(out, "In sync") {
		t.Errorf("the second run has nothing to record:\n%s", out)
	}
}

func TestSyncDoesNotRecordTheOperatingSystemAsAnOwner(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"git\"\nprovider = \"system\"\n\n[[package]]\nname = \"zsh\"\n")
	h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
	h.sys.With(model.Installation{Name: "git", Path: "/usr/bin/git"})
	got := decodeOperation(t, h.mustRun("sync", "--dry-run", "--json"))
	for _, a := range got.Actions {
		if a.Action == "record" && a.Provider == "system" {
			t.Errorf("system software is never recorded as owned: %+v", a)
		}
	}
}
