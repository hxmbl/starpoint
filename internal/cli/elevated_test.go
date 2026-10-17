package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/elevation"
)

// notElevated is the status of an ordinary user, for test environments that
// must behave the same whoever runs the suite.
func notElevated() elevation.Status { return elevation.Status{InvokingUID: -1, InvokingGID: -1} }

func plainRoot() elevation.Status {
	return elevation.Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root", InvokingUID: -1, InvokingGID: -1}
}

func viaSudo(home string) elevation.Status {
	return elevation.Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root",
		InvokingUser: "alice", Via: "sudo", InvokingUID: 4242, InvokingGID: 4243, InvokingHome: home}
}

// elevated makes the harness run as root, recording where HOME was pointed.
func (h *harness) elevated(s elevation.Status) *[]string {
	homes := &[]string{}
	h.env.Elevation = func() elevation.Status { return s }
	h.env.SetHome = func(home string) error { *homes = append(*homes, home); return nil }
	return homes
}

func TestChangingCommandsAreRefusedAsRoot(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(plainRoot())

	code, _, errOut := h.run("install", "cowsay", "--provider", "uv", "--yes")
	if code != ExitRefusedElevated {
		t.Fatalf("expected exit %d, got %d\n%s", ExitRefusedElevated, code, errOut)
	}
	for _, want := range []string{"error [" + CodeElevatedRefused + "]", "--allow-root", "Nothing was changed"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the refusal should say %q:\n%s", want, errOut)
		}
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
	var refused bool
	for _, e := range h.withEvent(auditlog.EventGuard) {
		if e.Result == "refused" && e.Elevated && strings.Contains(e.Detail, CodeElevatedRefused) {
			refused = true
		}
	}
	if !refused {
		t.Error("the refusal is logged")
	}
}

func TestElevatedRefusalJSON(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(plainRoot())
	code, out, errOut := h.run("--json", "install", "cowsay", "--provider", "uv", "--yes")
	if code != ExitRefusedElevated || strings.TrimSpace(out) != "" {
		t.Fatalf("expected exit %d and an empty stdout, got %d\n%s", ExitRefusedElevated, code, out)
	}
	d := decodeRefusal(t, errOut)
	if d.Code != CodeElevatedRefused || d.ExitCode != ExitRefusedElevated || d.Refused != nil {
		t.Errorf("wrong document: %+v", d)
	}
}

func TestAllowRootLetsAChangingCommandRun(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	homes := h.elevated(plainRoot())
	h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root")
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("the install should run: %+v", h.uv.InstallCalls)
	}
	if len(*homes) != 0 {
		t.Errorf("plain root acts on its own environment; HOME must not move: %v", *homes)
	}
}

func TestReadOnlyCommandsAndDoctorRunElevated(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	h.elevated(plainRoot())
	h.unattended()
	for _, args := range [][]string{
		{"list"}, {"doctor"}, {"info", "jq"}, {"explain", "jq"}, {"runs"}, {"transition"},
		{"install", "cowsay", "--provider", "uv", "--dry-run"}, {"sync", "--dry-run"}, {"logging"}, {"recover"},
	} {
		code, out, errOut := h.run(args...)
		if code == ExitRefusedElevated || strings.Contains(errOut, CodeElevatedRefused) {
			t.Errorf("starpoint %s must not be refused as root (exit %d)\n%s%s", strings.Join(args, " "), code, out, errOut)
		}
	}
}

func TestSudoUserCanConfirmInteractively(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	home := t.TempDir()
	homes := h.elevated(viaSudo(home))
	h.ask(true)

	h.mustRun("install", "cowsay", "--provider", "uv")
	if len(h.questions) == 0 || !strings.Contains(h.questions[0], "alice") || !strings.Contains(h.questions[0], home) {
		t.Fatalf("the first question must say whose environment changes: %q", h.questions)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("a confirmed elevated install runs: %+v", h.uv.InstallCalls)
	}
	if len(*homes) == 0 || (*homes)[0] != home {
		t.Errorf("HOME must point at the invoking user's home, got %v", *homes)
	}
	var allowed bool
	for _, e := range h.withEvent(auditlog.EventGuard) {
		if e.Result == "allowed" && strings.Contains(e.Detail, "alice") {
			allowed = true
		}
	}
	if !allowed {
		t.Error("the interactive approval is logged")
	}
}

func TestSudoUserDecliningIsARefusal(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(viaSudo(t.TempDir()))
	h.ask(false)
	code, _, errOut := h.run("install", "cowsay", "--provider", "uv")
	if code != ExitRefusedElevated {
		t.Fatalf("declining is a refusal, got %d\n%s", code, errOut)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
}

func TestYesDoesNotAnswerTheRootQuestion(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(viaSudo(t.TempDir()))
	code, _, errOut := h.run("install", "cowsay", "--provider", "uv", "--yes")
	if code != ExitRefusedElevated || !strings.Contains(errOut, "--yes does not answer") {
		t.Fatalf("an unattended elevated run needs --allow-root, got %d\n%s", code, errOut)
	}
	if len(h.questions) != 0 {
		t.Errorf("--yes means do not ask: %q", h.questions)
	}
}

func TestPlainRootCannotBeConfirmedInteractively(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(plainRoot())
	code, _, _ := h.run("install", "cowsay", "--provider", "uv")
	if code != ExitRefusedElevated {
		t.Fatalf("without an invoking user only --allow-root allows it, got %d", code)
	}
	if len(h.questions) != 0 {
		t.Errorf("nothing should be asked: %q", h.questions)
	}
}

func TestSudoWithoutAResolvableUserActsOnRootsEnvironment(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	s := viaSudo("")
	homes := h.elevated(s)
	h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root")
	if len(*homes) != 0 {
		t.Errorf("an invoking user without a known home is not targeted: %v", *homes)
	}
}

func TestChangingCommandClassification(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"init"}, true},
		{[]string{"sync"}, true},
		{[]string{"sync", "--dry-run"}, false},
		{[]string{"install", "ruff"}, true},
		{[]string{"add", "ruff", "-n"}, false},
		{[]string{"remove", "ruff"}, true},
		{[]string{"update"}, true},
		{[]string{"prune"}, true},
		{[]string{"transition"}, false},
		{[]string{"transition", "--all"}, false},
		{[]string{"transition", "ruff"}, true},
		{[]string{"move", "--reverse", "ruff"}, true},
		{[]string{"transition", "ruff", "--dry-run"}, false},
		{[]string{"recover"}, false},
		{[]string{"recover", "--discard"}, true},
		{[]string{"logging"}, false},
		{[]string{"logging", "prune"}, true},
		{[]string{"logs", "config"}, true},
		{[]string{"list"}, false},
		{[]string{"doctor"}, false},
		{[]string{"info", "ruff"}, false},
		{[]string{"explain", "ruff"}, false},
		{[]string{"runs"}, false},
		{[]string{"help"}, false},
	}
	for _, tc := range cases {
		if got := changingCommand(tc.args[0], tc.args[1:]); got != tc.want {
			t.Errorf("changingCommand(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}
