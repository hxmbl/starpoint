package cli

import (
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// no_confirm in the configuration makes sync unattended exactly as --yes
// does: no question is asked, and so the unattended guards apply.
func TestSyncHonoursNoConfirmFromTheConfiguration(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[settings]\nno_confirm = true\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.ask(true)
	h.mustRun("sync")
	if h.asked() {
		t.Errorf("no_confirm should skip the confirmation: %v", h.questions)
	}
	if len(h.brew.InstallCalls) != 1 {
		t.Errorf("the configured change still happens, got %d installs", len(h.brew.InstallCalls))
	}
}

// no_confirm never weakens a safeguard: an unattended sync still refuses to
// pick a provider nobody chose, and to add a managed copy beside the
// operating system's, even with a person at the terminal.
func TestNoConfirmInTheConfigurationKeepsSyncsUnattendedGuards(t *testing.T) {
	t.Run("ambiguous provider", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[settings]\nno_confirm = true\n\n[[package]]\nname = \"ripgrep\"\n")
		h.ask(true)
		before := h.configBody()
		code, _, errOut := h.run("sync", "--json")
		if code != ExitRefusedDecision || decodeDecision(t, errOut).Code != CodeProviderAmbiguous {
			t.Fatalf("expected provider-ambiguous, got %d\n%s", code, errOut)
		}
		if len(h.brew.InstallCalls) != 0 || h.configBody() != before || h.asked() {
			t.Error("a refusal asks nothing and changes nothing")
		}
	})
	t.Run("managed copy beside the system's", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[settings]\nno_confirm = true\n\n[[package]]\nname = \"zsh\"\n")
		h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
		h.uv.IsAvailable = false
		h.cargo.IsAvailable = false
		h.npm.IsAvailable = false
		h.ask(true)
		code, _, errOut := h.run("sync", "--json")
		if code != ExitRefusedDecision || decodeDecision(t, errOut).Code != CodeDuplicateInstall {
			t.Fatalf("expected duplicate-install, got %d\n%s", code, errOut)
		}
		if len(h.brew.InstallCalls) != 0 {
			t.Error("nothing may be installed")
		}
	})
	t.Run("dry run", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[settings]\nno_confirm = true\n\n[[package]]\nname = \"ripgrep\"\n")
		if code, out, errOut := h.run("sync", "--dry-run"); code != 0 {
			t.Fatalf("a dry run is never refused, got %d\n%s\n%s", code, out, errOut)
		}
	})
}
