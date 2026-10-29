package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// The audit found `str add --help` planning an install of a package
// called "--help" (and running it with --no-confirm). Help after any command is
// help for that command.
func TestHelpAfterEveryCommandPrintsThatCommandsHelp(t *testing.T) {
	cases := map[string]string{
		"install":    "Usage: str install",
		"add":        "Usage: str install",
		"i":          "Usage: str install",
		"remove":     "Usage: str remove",
		"uninstall":  "Usage: str remove",
		"rm":         "Usage: str remove",
		"update":     "Usage: str update",
		"upgrade":    "Usage: str update",
		"sync":       "Usage: str sync",
		"list":       "Usage: str list",
		"info":       "Usage: str info",
		"explain":    "Usage: str explain",
		"doctor":     "Usage: str doctor",
		"transition": "Usage: str transition",
		"runs":       "Usage: str runs",
		"prune":      "Usage: str prune",
		"recover":    "Usage: str recover",
		"init":       "Usage: str init",
	}
	for cmd, want := range cases {
		for _, flag := range []string{"--help", "-h"} {
			h := newHarness(t, "schema = 1\n")
			code, out, errOut := h.run(cmd, flag)
			if code != 0 {
				t.Errorf("%s %s: exit %d, stderr %q", cmd, flag, code, errOut)
			}
			if !strings.Contains(out, want) {
				t.Errorf("%s %s: expected %q in\n%s", cmd, flag, want, out)
			}
			if len(h.brew.InstallCalls)+len(h.uv.InstallCalls)+len(h.brew.RemoveCalls) != 0 {
				t.Errorf("%s %s must not touch any provider", cmd, flag)
			}
		}
	}
}

func TestHelpFlagIsNeverAPackageName(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	before := h.configBody()
	code, out, _ := h.run("add", "ripgrep", "--help", "--no-confirm")
	if code != 0 || !strings.Contains(out, "Usage: str install") {
		t.Fatalf("expected install help, got %d:\n%s", code, out)
	}
	if len(h.brew.InstallCalls) != 0 || h.configBody() != before {
		t.Error("asking for help must not install or record anything")
	}
}

func TestHelpCommandTakesACommandName(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	code, out, _ := h.run("help", "uninstall")
	if code != 0 || !strings.Contains(out, "Usage: str remove") {
		t.Errorf("help uninstall should show remove's help, got %d:\n%s", code, out)
	}
}

func TestUnknownOptionsAreNotPackageNames(t *testing.T) {
	for _, args := range [][]string{
		{"install", "--hepl"},
		{"add", "ripgrep", "--frobnicate", "--no-confirm"},
		{"remove", "-x", "ripgrep"},
		{"uninstall", "--nope"},
		{"info", "--bogus"},
		{"explain", "-q"},
	} {
		h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
		code, _, errOut := h.run(args...)
		if code != 2 {
			t.Errorf("%v: expected a usage error (2), got %d", args, code)
		}
		if !strings.Contains(errOut, "unknown option") {
			t.Errorf("%v: the error should name the option, got %q", args, errOut)
		}
		if len(h.brew.InstallCalls)+len(h.brew.RemoveCalls) != 0 {
			t.Errorf("%v: nothing may be installed or removed", args)
		}
	}
}

// firstJSON decodes the first JSON document of a command's output.
func firstJSON(t *testing.T, out string, target any) {
	t.Helper()
	if err := json.NewDecoder(strings.NewReader(out)).Decode(target); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
}

// The audit: `info git` on a machine where git is installed but not managed
// said "Status: missing", and named a desired provider explain disagreed with.
func TestInfoOnUnmanagedSoftwareSaysUnmanagedAndAgreesWithExplain(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.sys.With(model.Installation{Name: "git", Version: "2.43.0", Path: "/usr/bin/git"})
	h.apt.IsAvailable = true

	out := h.mustRun("info", "git")
	if !strings.Contains(out, "Status:            unmanaged") {
		t.Errorf("installed-but-unmanaged software is not missing:\n%s", out)
	}
	if strings.Contains(out, "missing") {
		t.Errorf("nothing about git is missing:\n%s", out)
	}

	var info struct {
		Package model.State `json:"package"`
	}
	firstJSON(t, h.mustRun("info", "git", "--json"), &info)
	var explain struct {
		Final model.Resolution `json:"final"`
	}
	firstJSON(t, h.mustRun("explain", "git", "--json"), &explain)
	if info.Package.Res.Provider != explain.Final.Provider {
		t.Errorf("info wants %q but explain picks %q", info.Package.Res.Provider, explain.Final.Provider)
	}
	if info.Package.Status != model.StatusUnmanaged {
		t.Errorf("status = %q", info.Package.Status)
	}
	if info.Package.Current == nil || info.Package.Current.Provider != "system" {
		t.Errorf("the current owner should still be reported, got %+v", info.Package.Current)
	}
}

func TestExplainMatchesWhatInstallRecords(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7", Path: "/opt/homebrew/bin/jq"})
	var explain struct {
		Final model.Resolution `json:"final"`
	}
	firstJSON(t, h.mustRun("explain", "jq", "--json"), &explain)
	if explain.Final.Provider != "brew" {
		t.Errorf("explain should see the existing brew copy, as install does; got %+v", explain.Final)
	}
	h.mustRun("install", "jq", "--no-confirm")
	if !strings.Contains(h.configBody(), `provider = "brew"`) {
		t.Errorf("install recorded something else:\n%s", h.configBody())
	}
}

func TestAnUnknownNameIsAskedAboutWhenSeveralProvidersCouldHaveIt(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.mustRun("install", "cowsay", "--no-confirm")
	if len(h.choices) != 1 {
		t.Fatalf("the provider choice should have been asked once, got %v", h.choices)
	}
	if !strings.Contains(h.choices[0], "brew") || !strings.Contains(h.choices[0], "uv") {
		t.Errorf("the question should list the candidates: %q", h.choices[0])
	}
	if !strings.Contains(h.configBody(), `reason = "provider chosen interactively"`) {
		t.Errorf("the choice should be recorded as the user's:\n%s", h.configBody())
	}

	h2 := newHarness(t, "schema = 1\n")
	h2.env.Choose = nil
	code, _, errOut := h2.run("install", "cowsay", "--no-confirm")
	if code == 0 || !strings.Contains(errOut, "--provider") {
		t.Errorf("without a terminal the ambiguity must fail and name --provider, got %d %q", code, errOut)
	}
	if len(h2.brew.InstallCalls)+len(h2.uv.InstallCalls) != 0 {
		t.Error("nothing may be installed on a guess")
	}
}

func TestAProviderNamedOnTheCommandLineIsRecordedAsTheUsersChoice(t *testing.T) {
	for _, args := range [][]string{
		{"install", "cowsay", "uv", "--no-confirm"},
		{"install", "cowsay", "--provider", "uv", "--no-confirm"},
	} {
		h := newHarness(t, "schema = 1\n")
		h.mustRun(args...)
		body := h.configBody()
		if !strings.Contains(body, `reason = "chosen by the user on the command line"`) {
			t.Errorf("%v: reason not recorded as the user's choice:\n%s", args, body)
		}
		if strings.Contains(body, "configuration explicitly selects") {
			t.Errorf("%v: the configuration did not select anything:\n%s", args, body)
		}
		if len(h.choices) != 0 {
			t.Errorf("%v: a named provider needs no question", args)
		}
	}
}
