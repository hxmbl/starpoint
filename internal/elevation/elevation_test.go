package elevation

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestNoWarningWhenNotElevated(t *testing.T) {
	if w := (Status{}).Warning(); w != nil {
		t.Fatalf("an ordinary run must not warn: %+v", w)
	}
}

func TestElevatedWarningNamesHomeAndInvokingUser(t *testing.T) {
	w := Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root", InvokingUser: "alice"}.Warning()
	if w == nil {
		t.Fatal("an elevated run must warn")
	}
	if w.Code != model.WarnElevated || w.Severity != model.SeverityWarn {
		t.Errorf("wrong code or severity: %+v", w)
	}
	for _, want := range []string{"/root", "~/.local/bin", "alice", "outside the user's normal environment"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("message should mention %q: %s", want, w.Message)
		}
	}
}

func TestDetectMatchesTheProcess(t *testing.T) {
	s := Detect()
	if s.Elevated && s.Mechanism == "" {
		t.Error("an elevated status must say how it was detected")
	}
}

func TestTargetIsTheInvokingUserWhenKnown(t *testing.T) {
	s := Status{Elevated: true, InvokingUser: "alice", Via: "sudo", InvokingUID: 1000, InvokingGID: 1000, InvokingHome: "/home/alice"}
	got, ok := s.Target()
	if !ok || !reflect.DeepEqual(got, Target{User: "alice", UID: 1000, GID: 1000, Home: "/home/alice"}) {
		t.Fatalf("got %+v %v", got, ok)
	}
	w := s.Warning()
	for _, want := range []string{"through sudo for alice", "/home/alice", "run as alice (uid 1000, gid 1000)", "system providers", "readable and executable by everyone"} {
		if !strings.Contains(w.Message, want) {
			t.Errorf("message should mention %q: %s", want, w.Message)
		}
	}
}

func TestNoTargetWithoutAResolvableOrdinaryUser(t *testing.T) {
	base := Status{Elevated: true, InvokingUser: "alice", InvokingUID: 1000, InvokingGID: 1000, InvokingHome: "/home/alice"}
	for name, s := range map[string]Status{
		"not elevated":  func() Status { c := base; c.Elevated = false; return c }(),
		"no user":       func() Status { c := base; c.InvokingUser = ""; return c }(),
		"sudo as root":  func() Status { c := base; c.InvokingUser = "root"; c.InvokingUID = 0; return c }(),
		"uid zero":      func() Status { c := base; c.InvokingUID = 0; return c }(),
		"unknown uid":   func() Status { c := base; c.InvokingUID = -1; return c }(),
		"unknown gid":   func() Status { c := base; c.InvokingGID = -1; return c }(),
		"no home found": func() Status { c := base; c.InvokingHome = ""; return c }(),
	} {
		if _, ok := s.Target(); ok {
			t.Errorf("%s: there should be no target", name)
		}
	}
}

func TestDetectHasNoTargetWhenNotElevated(t *testing.T) {
	s := Detect()
	if s.Elevated {
		t.Skip("the suite is running elevated")
	}
	if _, ok := s.Target(); ok {
		t.Error("an ordinary run acts on its own environment")
	}
}

func TestUnresolvedOnlyForAnOrdinaryUserWhoCannotBeResolved(t *testing.T) {
	base := Status{Elevated: true, InvokingUser: "alice", Via: "sudo", InvokingUID: 1000, InvokingGID: 1000, InvokingHome: "/home/alice"}
	if msg := base.Unresolved(); msg != "" {
		t.Errorf("a resolvable user is not unresolved: %s", msg)
	}
	for name, s := range map[string]Status{
		"not elevated": {InvokingUser: "alice"},
		"plain root":   {Elevated: true},
		"sudo as root": {Elevated: true, InvokingUser: "root", InvokingUID: 0},
	} {
		if msg := s.Unresolved(); msg != "" {
			t.Errorf("%s: a plain root run is not an unresolved one: %s", name, msg)
		}
	}
	noHome := base
	noHome.InvokingHome = ""
	noHome.lookupErr = "home directory /home/alice: no such file or directory"
	msg := noHome.Unresolved()
	for _, want := range []string{"home directory", "alice", "sudo", "no such file"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the reason should mention %q: %s", want, msg)
		}
	}
	noIDs := base
	noIDs.InvokingUID, noIDs.InvokingGID = -1, -1
	if msg := noIDs.Unresolved(); !strings.Contains(msg, "user id, group id") {
		t.Errorf("missing ids are named: %s", msg)
	}
}

func TestTargetCarriesTheSupplementaryGroups(t *testing.T) {
	s := Status{Elevated: true, InvokingUser: "alice", InvokingUID: 1000, InvokingGID: 1000, InvokingHome: "/home/alice", InvokingGroups: []int{1000, 27, 999}}
	got, ok := s.Target()
	if !ok || !reflect.DeepEqual(got.Groups, []int{1000, 27, 999}) {
		t.Fatalf("groups: %+v", got)
	}
	got.Groups[0] = 0
	if s.InvokingGroups[0] != 1000 {
		t.Error("the target's groups are a copy")
	}
}
