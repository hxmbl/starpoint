package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// systemOnlyHarness declares zsh with no provider on a machine where only
// the operating system has it and brew is the one provider that could add
// a managed copy.
func systemOnlyHarness(t *testing.T, config string) *harness {
	t.Helper()
	h := newHarness(t, config)
	h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
	h.uv.IsAvailable = false
	h.cargo.IsAvailable = false
	h.npm.IsAvailable = false
	return h
}

const zshNoProvider = "schema = 1\n\n[[package]]\nname = \"zsh\"\n"

func (h *harness) nothingTouched(before string) {
	h.t.Helper()
	if n := len(h.brew.InstallCalls); n != 0 {
		h.t.Errorf("no managed copy may be installed, got %d installs", n)
	}
	if n := len(h.brew.RemoveCalls) + len(h.sys.RemoveCalls); n != 0 {
		h.t.Errorf("sync never removes anything, got %d removals", n)
	}
	if h.configBody() != before {
		h.t.Errorf("the configuration must not change:\n%s", h.configBody())
	}
}

// Unattended, an entry with no provider whose only copy is the operating
// system's is refused with install's code and exit 5, naming the package,
// the system as the copy that is there, and the install it would have made.
func TestSyncRefusesAManagedCopyBesideTheSystemsUnattended(t *testing.T) {
	h := systemOnlyHarness(t, zshNoProvider)
	h.unattended()
	before := h.configBody()

	code, out, errOut := h.run("sync", "--no-confirm", "--json")
	if code != ExitRefusedDecision {
		t.Fatalf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, ExitRefusedDecision, out, errOut)
	}
	r := decodeDecision(t, errOut)
	if r.Code != CodeDuplicateInstall || r.ExitCode != ExitRefusedDecision || r.Package != "zsh" ||
		strings.Join(r.Candidates, ",") != "system" {
		t.Errorf("refusal = %+v", r)
	}
	doc := decodeRefusal(t, errOut)
	if doc.SchemaVersion != 11 || doc.Refused == nil || len(doc.Refused.Actions) != 1 ||
		doc.Refused.Actions[0].Action != string(model.ActionInstall) || doc.Refused.Actions[0].Package != "zsh" ||
		doc.Refused.Actions[0].Provider != "brew" {
		t.Errorf("the refusal carries the planned install of a managed copy: %+v", doc.Refused)
	}
	if !strings.Contains(doc.Error, "str install zsh brew") || !strings.Contains(doc.Error, `provider = "system"`) {
		t.Errorf("the message names both ways to settle it: %s", doc.Error)
	}
	h.nothingTouched(before)

	// Without --json and without a terminal: the same refusal.
	h2 := systemOnlyHarness(t, zshNoProvider)
	h2.unattended()
	if code, _, errOut := h2.run("sync"); code != ExitRefusedDecision || !strings.Contains(errOut, "operating system") {
		t.Errorf("no terminal is unattended too: exit %d\n%s", code, errOut)
	}
	h2.nothingTouched(before)
}

// A dry run is never refused: it shows the install the run would ask about,
// with the duplicate-install warning on it, and changes nothing.
func TestSyncDryRunShowsTheManagedCopyItWouldAskAbout(t *testing.T) {
	h := systemOnlyHarness(t, zshNoProvider)
	h.unattended()
	before := h.configBody()
	got := decodeOperation(t, h.mustRun("sync", "--dry-run", "--json"))
	if len(got.Actions) != 1 || got.Actions[0].Action != "install" || got.Actions[0].Provider != "brew" {
		t.Fatalf("the dry run plans the managed copy: %+v", got.Actions)
	}
	if !hasCode(got.Actions[0].Warnings, CodeDuplicateInstall) {
		t.Errorf("the planned install carries duplicate-install: %+v", got.Actions[0].Warnings)
	}
	if hasCode(got.Warnings, model.WarnProviderDrift) {
		t.Errorf("an entry with no provider is not drift from a configured one: %+v", got.Warnings)
	}
	h.nothingTouched(before)
}

// At a terminal the managed copy is its own question, and declining it
// (the default) installs nothing, records nothing, and says what to do.
func TestSyncAsksBeforeAManagedCopyBesideTheSystemsAndNoInstallsNothing(t *testing.T) {
	h := systemOnlyHarness(t, zshNoProvider)
	h.ask(false)
	before := h.configBody()
	code, out, errOut := h.run("sync")
	if code != 0 {
		t.Fatalf("declining is not an error: exit %d\n%s\n%s", code, out, errOut)
	}
	if len(h.questions) != 1 || !strings.Contains(h.questions[0], "operating system already provides zsh") ||
		!strings.Contains(h.questions[0], "through brew") {
		t.Errorf("sync should ask about the managed copy, got %q", h.questions)
	}
	if !strings.Contains(out+errOut, "str install zsh brew") {
		t.Errorf("a no says how to get the copy later:\n%s\n%s", out, errOut)
	}
	h.nothingTouched(before)
}

// A yes at the terminal installs the managed copy, records brew as its
// owner, and leaves the operating system's copy where it is.
func TestSyncInstallsTheManagedCopyWhenThePersonSaysYes(t *testing.T) {
	h := systemOnlyHarness(t, zshNoProvider)
	h.ask(true)
	h.mustRun("sync")
	if len(h.questions) < 1 || !strings.Contains(h.questions[0], "Install a managed copy through brew") {
		t.Errorf("the managed copy is asked about first, got %q", h.questions)
	}
	if len(h.brew.InstallCalls) != 1 {
		t.Errorf("a yes installs the managed copy, got %d installs", len(h.brew.InstallCalls))
	}
	if len(h.brew.RemoveCalls)+len(h.sys.RemoveCalls) != 0 {
		t.Error("the operating system's copy is left alone")
	}
	if !strings.Contains(h.configBody(), `provider = "brew"`) {
		t.Errorf("the owner of the managed copy is recorded:\n%s", h.configBody())
	}
}

// Naming the provider in the entry is a decision: sync neither asks nor
// refuses on the system copy's account, exactly as before.
func TestSyncWithANamedProviderIsNotAskedAboutTheSystemCopy(t *testing.T) {
	h := systemOnlyHarness(t, "schema = 1\n\n[[package]]\nname = \"zsh\"\nprovider = \"brew\"\n")
	h.unattended()
	before := h.configBody()
	code, out, errOut := h.run("sync", "--no-confirm", "--json")
	if code != 0 {
		t.Fatalf("a named provider is not refused: exit %d\n%s\n%s", code, out, errOut)
	}
	got := decodeOperation(t, out)
	if hasCode(got.Warnings, CodeDuplicateInstall) {
		t.Errorf("a named provider is not a duplicate-install question: %+v", got.Warnings)
	}
	if !hasCode(got.Warnings, model.WarnProviderDrift) {
		t.Errorf("a configured provider the system copy differs from stays drift: %+v", got.Warnings)
	}
	h.nothingTouched(before)

	// And keeping the operating system's copy is a decision too.
	h2 := systemOnlyHarness(t, "schema = 1\n\n[[package]]\nname = \"zsh\"\nprovider = \"system\"\n")
	h2.unattended()
	before = h2.configBody()
	if code, out, errOut := h2.run("sync", "--no-confirm"); code != 0 {
		t.Fatalf("provider = system keeps the system's copy: exit %d\n%s\n%s", code, out, errOut)
	}
	h2.nothingTouched(before)
}
