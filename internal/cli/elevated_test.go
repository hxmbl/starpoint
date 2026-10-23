package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
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
// Dropping privileges is assumed to work (the probe is replaced): the suite
// does not run as root, and the fake providers start no processes. The
// real-root tests use the real probe.
func (h *harness) elevated(s elevation.Status) *[]string {
	homes := &[]string{}
	h.env.Elevation = func() elevation.Status { return s }
	h.env.SetHome = func(home string) error { *homes = append(*homes, home); return nil }
	if h.env.DropProbe == nil {
		h.env.DropProbe = func(context.Context, *exec.Identity) error { return nil }
	}
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

func TestSudoWithoutAResolvableUserIsRefused(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	homes := h.elevated(viaSudo(""))
	code, _, errOut := h.run("install", "cowsay", "--provider", "uv", "--yes", "--allow-root")
	if code != ExitRefusedElevated {
		t.Fatalf("an invoking user who cannot be resolved must not become a plain root run: exit %d\n%s", code, errOut)
	}
	for _, want := range []string{"error [" + CodeInvokingUserUnresolved + "]", "home directory of alice", "Nothing was changed", "not run alice's providers as root"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the refusal should say %q:\n%s", want, errOut)
		}
	}
	if len(*homes) != 0 || len(h.uv.InstallCalls) != 0 {
		t.Errorf("nothing may be targeted or installed: homes %v, installs %d", *homes, len(h.uv.InstallCalls))
	}
	var logged bool
	for _, e := range h.withEvent(auditlog.EventGuard) {
		if e.Result == "refused" && strings.HasPrefix(e.Detail, CodeInvokingUserUnresolved) {
			logged = true
		}
	}
	if !logged {
		t.Error("the refusal is in the audit log")
	}

	// A read-only command still runs, and says why providers run as root.
	code, out, _ := h.run("doctor", "--json")
	if code != 0 && code != 1 {
		t.Fatalf("doctor exited %d", code)
	}
	if !strings.Contains(out, model.WarnPrivilegeDropFailed) {
		t.Errorf("a read-only command carries the warning:\n%s", out)
	}
}

func TestAFailedPrivilegeDropIsRefused(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.env.DropProbe = func(context.Context, *exec.Identity) error {
		return errors.New("could not start a process as alice (uid 4242): operation not permitted")
	}
	h.elevated(viaSudo(h.home))
	code, _, errOut := h.run("install", "cowsay", "--provider", "uv", "--yes", "--allow-root", "--json")
	if code != ExitRefusedElevated {
		t.Fatalf("expected exit %d, got %d\n%s", ExitRefusedElevated, code, errOut)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(errOut), &doc); err != nil {
		t.Fatalf("one JSON document: %v\n%s", err, errOut)
	}
	if doc["code"] != CodePrivilegeDropFailed || !strings.Contains(doc["error"].(string), "operation not permitted") {
		t.Errorf("wrong refusal: %v", doc)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
}

func TestRunAsSaysWhoEachProviderRanAs(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(viaSudo(h.home))

	op := decodeOperation(t, h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root", "--json"))
	if len(op.Actions) != 1 || len(op.Actions[0].RunAs) != 1 {
		t.Fatalf("one action, one identity: %+v", op.Actions)
	}
	ra := op.Actions[0].RunAs[0]
	want := model.RunAs{Provider: "uv", User: "alice", UID: 4242, GID: 4243, Elevated: true, Dropped: true,
		Privilege: "user", Reason: ra.Reason}
	if ra != want || ra.Reason == "" {
		t.Errorf("uv runs as alice with privileges dropped: %+v", ra)
	}

	op = decodeOperation(t, h.mustRun("install", "jq", "--provider", "brew", "--yes", "--allow-root", "--json"))
	ra = op.Actions[0].RunAs[0]
	if ra.Provider != "brew" || ra.Dropped || !ra.Elevated || ra.Privilege != "root" || ra.User == "alice" {
		t.Errorf("a provider classified root keeps Starpoint's own account: %+v", ra)
	}

	var logged bool
	for _, e := range h.withEvent(auditlog.EventAction) {
		if e.Package == "cowsay" && len(e.RunAs) == 1 && e.RunAs[0].Dropped && e.RunAs[0].User == "alice" {
			logged = true
		}
	}
	if !logged {
		t.Error("the audit log records who the action ran as")
	}

	if out := h.mustRun("install", "httpie", "--provider", "uv", "--yes", "--allow-root"); strings.Contains(out, "runs as") {
		t.Errorf("only --verbose lists identities:\n%s", out)
	}
	out := h.mustRun("install", "ruff", "--provider", "uv", "--yes", "--allow-root", "--verbose")
	if !strings.Contains(out, "uv runs as alice (uid 4242, gid 4243), privileges dropped") {
		t.Errorf("--verbose says who the provider ran as:\n%s", out)
	}
}

func TestRunAsOfOrdinaryAndPlainRootRuns(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.elevated(notElevated())
	op := decodeOperation(t, h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--json"))
	ra := op.Actions[0].RunAs[0]
	if ra.Elevated || ra.Dropped || ra.Privilege != "" || ra.UID != os.Getuid() {
		t.Errorf("an ordinary run reports its own account: %+v", ra)
	}

	h2 := newHarness(t, "schema = 1\n")
	h2.elevated(plainRoot())
	op = decodeOperation(t, h2.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root", "--json"))
	ra = op.Actions[0].RunAs[0]
	if !ra.Elevated || ra.Dropped || !strings.Contains(ra.Reason, "plain root run") {
		t.Errorf("a plain root run drops nothing and says why: %+v", ra)
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

// Under sudo the run acts on the user's environment, but commands that stay
// root are given root's own home, never the user's.
func TestElevatedRunGivesRootCommandsRootsHome(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	home := t.TempDir()
	h.elevated(viaSudo(home))
	h.env.Environ = func() []string { return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=C"} }
	h.env.RootHome = func() string { return "/root" }

	h.mustRun("list")
	env := h.env.privileges.RootEnv
	if got := (&exec.Identity{Env: env}).Getenv("HOME"); got != "/root" {
		t.Errorf("root commands get root's home, got HOME=%q in %v", got, env)
	}
	if got := (&exec.Identity{Env: env}).Getenv("LANG"); got != "C" {
		t.Errorf("the rest of the environment is kept: %v", env)
	}
	for _, kv := range env {
		if kv == "HOME="+home {
			t.Errorf("the user's home must not reach a root command: %v", env)
		}
	}
}

// When the user database cannot tell root's home from the user's (sudo kept
// HOME), the home Starpoint started with is used; when neither differs, root
// commands keep Starpoint's environment rather than get an invented home.
func TestRootHomeIsNeverTheUsersHome(t *testing.T) {
	e := &Env{RootHome: func() string { return "/home/alice" }}
	if got := e.rootHome("/root", "/home/alice"); got != "/root" {
		t.Errorf("rootHome = %q, want /root", got)
	}
	if got := e.rootHome("/home/alice/", "/home/alice"); got != "" {
		t.Errorf("rootHome = %q, want empty when nothing differs from the user's home", got)
	}
	if env := rootEnvironment([]string{"HOME=/home/alice"}, ""); env != nil {
		t.Errorf("no root home means no replacement environment, got %v", env)
	}
}
