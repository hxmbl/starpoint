package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/golang"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// installInGoProject reproduces `starpoint install neofetch` typed inside a Go
// repository on a machine with Homebrew, Go and other language providers.
func installInGoProject(t *testing.T) (*harness, *testsupport.FakeProvider) {
	t.Helper()
	h := newHarness(t, "schema = 1\n")
	goP := testsupport.NewFakeProvider(golang.Name, provider.KindGo)
	goP.ExeDir = t.TempDir()
	h.env.Registry.Register(goP)
	// brew puts the commands it installs somewhere, so the install has an
	// executable effect as well as the package and the configuration edit.
	h.brew.ExeDir = t.TempDir()

	projectDir := t.TempDir()
	testsupport.WriteFile(t, projectDir+"/go.mod", "module example.com/demo\n\ngo 1.27\n")
	h.env.WorkDir = projectDir
	return h, goP
}

func TestBareNameInAGoProjectIsInstalledByTheSystemManager(t *testing.T) {
	h, goP := installInGoProject(t)
	h.ask(false)

	out := h.mustRun("install", "neofetch")
	if !strings.Contains(out, "Install neofetch (brew)") {
		t.Fatalf("neofetch should be offered from the system package manager, not go:\n%s", out)
	}
	if strings.Contains(out, "(go)") || strings.Contains(out, "Go project") {
		t.Errorf("a Go repository is not a reason to go-install a system tool:\n%s", out)
	}
	for _, q := range h.choices {
		if strings.Contains(q, " go,") || strings.HasSuffix(q, " go. Which provider should own it?") {
			t.Errorf("go cannot install a bare name and must not be offered: %q", q)
		}
	}
	if len(goP.InstallCalls) != 0 || len(h.brew.InstallCalls) != 0 {
		t.Error("declining must install nothing")
	}
}

// A Proceed? question must never hide what it will change behind a count.
func TestTheConfirmationListsEveryChange(t *testing.T) {
	h, _ := installInGoProject(t)
	h.ask(false)

	out := h.mustRun("install", "neofetch")
	prompt := out[strings.Index(out, "About to change your machine:"):]
	if strings.Contains(prompt, "other change(s)") || strings.Contains(prompt, "--verbose lists them") {
		t.Errorf("the confirmation must list every change, not count them:\n%s", out)
	}
	for _, want := range []string{
		"would install neofetch (brew)",
		"would create executable " + h.brew.ExeDir + "/neofetch",
		"would edit " + h.config + ": add neofetch",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the confirmation should show %q:\n%s", want, out)
		}
	}
}

// Accepting the offered default is not overruling Starpoint, and the
// confirmation must not say it is.
func TestAcceptingTheOfferedProviderIsNotDescribedAsAnOverride(t *testing.T) {
	h, _ := installInGoProject(t)
	h.ask(false)

	out := h.mustRun("install", "neofetch")
	if len(h.choices) == 0 {
		t.Fatalf("neofetch beside language providers should still be asked about:\n%s", out)
	}
	if strings.Contains(out, "not the one Starpoint would choose") {
		t.Errorf("the harness picked the offered default; it is not an override:\n%s", out)
	}

	// Naming a provider on the command line is still described as the user's call.
	out = h.mustRun("install", "neofetch", "--provider", "npm")
	if !strings.Contains(out, "not the one Starpoint would choose") {
		t.Errorf("a provider named on the command line is the user's choice:\n%s", out)
	}
}

// The prompt-only change leaves non-interactive summaries alone: a finished
// install still counts the effects that are not destructive.
func TestTheResultSummaryStillCollapsesUnlessVerbose(t *testing.T) {
	h, _ := installInGoProject(t)
	h.ask(true)

	out := h.mustRun("install", "neofetch")
	after := out[strings.LastIndex(out, "installed neofetch"):]
	if !strings.Contains(after, "other change(s); --verbose lists them") {
		t.Errorf("the result summary keeps its collapsed form:\n%s", out)
	}
}

// The same rule holds for every Proceed? question, sync's included.
func TestSyncConfirmationListsEveryChange(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.ExeDir = t.TempDir()
	h.ask(false)

	out := h.mustRun("sync")
	if !h.asked() {
		t.Fatalf("sync should ask before installing:\n%s", out)
	}
	if strings.Contains(out, "other change(s)") {
		t.Errorf("the confirmation must list every change, not count them:\n%s", out)
	}
	if !strings.Contains(out, "would create executable "+h.brew.ExeDir+"/ripgrep") {
		t.Errorf("the executable the install creates should be listed:\n%s", out)
	}
}
