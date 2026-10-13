package cli

import (
	"strings"
	"testing"
)

// The audit found `starpoint add --help` planning an install of a package
// called "--help" (and running it with --no-confirm). Help after any command is
// help for that command.
func TestHelpAfterEveryCommandPrintsThatCommandsHelp(t *testing.T) {
	cases := map[string]string{
		"install":    "Usage: starpoint install",
		"add":        "Usage: starpoint install",
		"i":          "Usage: starpoint install",
		"remove":     "Usage: starpoint remove",
		"uninstall":  "Usage: starpoint remove",
		"rm":         "Usage: starpoint remove",
		"update":     "Usage: starpoint update",
		"upgrade":    "Usage: starpoint update",
		"sync":       "Usage: starpoint sync",
		"list":       "Usage: starpoint list",
		"info":       "Usage: starpoint info",
		"explain":    "Usage: starpoint explain",
		"doctor":     "Usage: starpoint doctor",
		"transition": "Usage: starpoint transition",
		"runs":       "Usage: starpoint runs",
		"prune":      "Usage: starpoint prune",
		"recover":    "Usage: starpoint recover",
		"init":       "Usage: starpoint init",
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
	if code != 0 || !strings.Contains(out, "Usage: starpoint install") {
		t.Fatalf("expected install help, got %d:\n%s", code, out)
	}
	if len(h.brew.InstallCalls) != 0 || h.configBody() != before {
		t.Error("asking for help must not install or record anything")
	}
}

func TestHelpCommandTakesACommandName(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	code, out, _ := h.run("help", "uninstall")
	if code != 0 || !strings.Contains(out, "Usage: starpoint remove") {
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
