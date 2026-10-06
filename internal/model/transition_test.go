package model

import (
	"strings"
	"testing"
)

func TestDeriveTransitionPlanned(t *testing.T) {
	// Nothing installed anywhere yet: the move has not started.
	tr := DeriveTransition(Package{Name: "ruff"},
		ProviderRef{Name: "brew"}, ProviderRef{Name: "uv"}, nil, nil,
		Verification{}, true)
	if tr.Phase != TransitionPlanned {
		t.Fatalf("expected planned, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("a planned transition is resumable")
	}
	if !tr.NeedsWork() {
		t.Error("a planned transition is not a resting state")
	}
}

func TestDeriveTransitionDoesNotDecideWhetherAMoveCanStart(t *testing.T) {
	// Absence of an installation means "not started", never "impossible". Whether
	// the target can actually install is the executor's question, decided from
	// provider capabilities — not something observable from the machine.
	tr := DeriveTransition(Package{Name: "ruff"},
		ProviderRef{Name: "brew"}, ProviderRef{Name: "apt", Available: false}, nil, nil,
		Verification{}, true)
	if tr.Phase != TransitionPlanned {
		t.Fatalf("expected planned, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("installing the provider later should let it proceed")
	}
}

func TestDeriveTransitionCompletedWhenNothingToReplace(t *testing.T) {
	inst := &Installation{Name: "ruff", Provider: "uv"}
	tr := DeriveTransition(Package{Name: "ruff"},
		ProviderRef{Name: "brew"}, ProviderRef{Name: "uv"}, nil, inst,
		Verification{Provider: "uv", Present: true, Resolved: true}, true)
	if tr.Phase != TransitionCompleted {
		t.Fatalf("expected completed, got %q", tr.Phase)
	}
	if !tr.OldRemoved {
		t.Error("with nothing to replace, the previous copy counts as gone")
	}
}

func TestDeriveTransitionVerifiedWhenBothCopiesArePresent(t *testing.T) {
	// The interrupted-transition shape: the new copy works and the old one is
	// still installed.
	previous := &Installation{Name: "ruff", Provider: "brew"}
	installed := &Installation{Name: "ruff", Provider: "uv"}
	tr := DeriveTransition(Package{Name: "ruff"},
		ProviderRef{Name: "brew", CanRemove: true}, ProviderRef{Name: "uv", CanInstall: true},
		previous, installed,
		Verification{Provider: "uv", Present: true, Resolved: true}, true)
	if tr.Phase != TransitionVerified {
		t.Fatalf("expected verified, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("a verified-but-unfinished transition is resumable")
	}
	if !strings.Contains(tr.Note, "not been removed") {
		t.Errorf("the note should say what is pending, got %q", tr.Note)
	}
}

func TestDeriveTransitionInstalledWhenTheNewCopyIsNotUsable(t *testing.T) {
	previous := &Installation{Name: "ruff", Provider: "brew"}
	installed := &Installation{Name: "ruff", Provider: "uv"}
	tr := DeriveTransition(Package{Name: "ruff"},
		ProviderRef{Name: "brew", CanRemove: true}, ProviderRef{Name: "uv", CanInstall: true},
		previous, installed,
		Verification{Provider: "uv", Present: true, OnPath: "/brew/bin/ruff", PathOwner: "brew"}, true)
	if tr.Phase != TransitionInstalled {
		t.Fatalf("expected installed, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("the transition should be resumable")
	}
	if !strings.Contains(tr.Note, "not usable") {
		t.Errorf("the note should say the new copy is not usable yet, got %q", tr.Note)
	}
	if tr.Advice == "" {
		t.Error("the user should be told the old copy is safe")
	}
}

func TestDeriveTransitionCompletedWithLeftoverWhenOldCannotRemove(t *testing.T) {
	previous := &Installation{Name: "python", Provider: "system"}
	installed := &Installation{Name: "python", Provider: "uv"}
	tr := DeriveTransition(Package{Name: "python"},
		ProviderRef{Name: "system"}, ProviderRef{Name: "uv", CanInstall: true},
		previous, installed,
		Verification{Provider: "uv", Present: true, Resolved: true}, false)
	if tr.Phase != TransitionCompleted {
		t.Fatalf("ownership did move, so the phase is completed; got %q", tr.Phase)
	}
	if !strings.Contains(tr.LeftBehindReason, "cannot remove") {
		t.Errorf("the leftover reason should name the limitation, got %q", tr.LeftBehindReason)
	}
	if tr.NeedsWork() {
		t.Error("a leftover is not unfinished work")
	}
}

func TestVerificationOK(t *testing.T) {
	cases := []struct {
		name string
		v    Verification
		want bool
		why  string
	}{
		{"absent", Verification{Provider: "uv"}, false, "presence is required"},
		{"present and resolved", Verification{Provider: "uv", Present: true, Resolved: true, OnPath: "/uv/bin/ruff"}, true, "both agree"},
		{"present, not on path", Verification{Provider: "uv", Present: true}, true, "no executable name to corroborate"},
		{"present, path is a different provider", Verification{
			Provider: "uv", Present: true, OnPath: "/brew/bin/ruff", PathOwner: "brew",
		}, false, "another provider still wins the command"},
		{"present, path matches this provider", Verification{
			Provider: "uv", Present: true, OnPath: "/uv/bin/ruff", PathOwner: "uv",
		}, true, "PATH agrees with the inventory"},
		{"present, path unattributed", Verification{
			Provider: "uv", Present: true, OnPath: "/srv/ruff",
		}, false, "a path that resolves somewhere unverified is not confirmation"},
	}
	for _, tc := range cases {
		if got := tc.v.OK(); got != tc.want {
			t.Errorf("%s: OK() = %v, want %v (%s)", tc.name, got, tc.want, tc.why)
		}
	}
}

func TestVerificationReason(t *testing.T) {
	cases := []struct {
		v    Verification
		want string
	}{
		{Verification{Provider: "uv"}, "uv does not report it as installed"},
		{Verification{Provider: "uv", Present: true, OnPath: "/brew/bin/ruff", PathOwner: "brew"},
			"uv is installed but the command resolves to brew's copy at /brew/bin/ruff"},
		{Verification{Provider: "uv", Present: true, Resolved: true, OnPath: "/uv/bin/ruff"},
			"confirmed: uv is installed and the command resolves to it"},
		{Verification{Provider: "uv", Present: true},
			"uv's inventory confirms it, but it reported no path to corroborate with"},
	}
	for _, tc := range cases {
		if got := tc.v.Reason(); got != tc.want {
			t.Errorf("Reason() = %q, want %q", got, tc.want)
		}
	}
}

func TestPhaseProperties(t *testing.T) {
	terminal := []TransitionPhase{TransitionCompleted, TransitionBlocked}
	for _, p := range terminal {
		if !p.Terminal() {
			t.Errorf("%q is a resting state", p)
		}
	}
	for _, p := range []TransitionPhase{TransitionPlanned, TransitionInstalled, TransitionVerified, TransitionFailed} {
		if p.Terminal() {
			t.Errorf("%q is not a resting state", p)
		}
		if !p.SafeToRetry() {
			t.Errorf("%q must be safe to retry: the old copy is still there", p)
		}
	}
	// Everything before completion leaves a working command behind.
	for _, p := range []TransitionPhase{TransitionCompleted, TransitionBlocked} {
		if p.SafeToRetry() {
			t.Errorf("%q has nothing left to retry", p)
		}
	}
}

func TestTransitionNeedsWork(t *testing.T) {
	if (Transition{Phase: TransitionCompleted}).NeedsWork() {
		t.Error("completed needs no work")
	}
	if (Transition{Phase: TransitionVerified}).NeedsWork() != true {
		t.Error("verified still needs the removal")
	}
}
