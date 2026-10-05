package model

import "testing"

func TestVersionMatches(t *testing.T) {
	cases := []struct {
		pin      string
		observed string
		want     bool
		why      string
	}{
		{"", "", true, "an empty pin constrains nothing"},
		{"", "1.2.3", true, "no pin, any version is fine"},
		{"1.2.3", "1.2.3", true, "exact match"},
		{"1.2.3", "1.2.4", false, "different version"},
		{"1.2.3", "", false, "an unknown version cannot satisfy a pin"},
		{"==1.2.3", "1.2.3", true, "a requirements-style prefix is tolerated"},
		{"=1.2.3", "1.2.3", true, "a single equals is tolerated"},
		{" 1.2.3 ", "1.2.3", true, "whitespace is not significant"},
		{"1:2.43.0-1", "1:2.43.0-1", true, "Debian epochs and revisions compare exactly"},
		{"1:2.43.0-1", "2.43.0-1", false, "an epoch is part of the version"},
		{"2024.1", "2024.1.1", false, "no prefix semantics are invented"},
	}
	for _, tc := range cases {
		if got := VersionMatches(tc.pin, tc.observed); got != tc.want {
			t.Errorf("VersionMatches(%q, %q) = %v, want %v (%s)",
				tc.pin, tc.observed, got, tc.want, tc.why)
		}
	}
}

func TestScopeValid(t *testing.T) {
	for _, s := range []Scope{ScopeGlobal, ScopeUser, ScopeProject} {
		if !s.Valid() {
			t.Errorf("%q should be a valid scope", s)
		}
	}
	for _, s := range []Scope{"", "machine", "GLOBAL"} {
		if s.Valid() {
			t.Errorf("%q should not be a valid scope", s)
		}
	}
}

func TestStatusHealthyOnlyWhenCorrect(t *testing.T) {
	if !StatusCorrect.Healthy() {
		t.Error("correct must be healthy")
	}
	for _, s := range []Status{
		StatusMissing,
		StatusProviderDiffers,
		StatusVersionDiffers,
		StatusUnpinnedConflict,
	} {
		if s.Healthy() {
			t.Errorf("%q must require action", s)
		}
	}
}

func TestStateNeedsAction(t *testing.T) {
	healthy := State{Status: StatusCorrect}
	if healthy.NeedsAction() {
		t.Error("a correct state needs no action")
	}
	for _, s := range []Status{StatusMissing, StatusProviderDiffers, StatusVersionDiffers} {
		if !(State{Status: s}).NeedsAction() {
			t.Errorf("%q needs action", s)
		}
	}
}

func TestInstallationExists(t *testing.T) {
	if (*Installation)(nil).Exists() {
		t.Error("a nil installation does not exist")
	}
	if (&Installation{}).Exists() {
		t.Error("an installation without a name does not exist")
	}
	if !(&Installation{Name: "git"}).Exists() {
		t.Error("an installation with a name exists")
	}
}

func TestPackageDescribe(t *testing.T) {
	got := Package{Name: "ruff", Version: "0.6.0", Channel: "cask"}.Describe()
	want := "ruff==0.6.0 [cask]"
	if got != want {
		t.Errorf("Describe() = %q, want %q", got, want)
	}
	if got := (Package{Name: "git"}).Describe(); got != "git" {
		t.Errorf("Describe() = %q, want %q", got, "git")
	}
}

func TestPlanEmpty(t *testing.T) {
	if !(Plan{}).Empty() {
		t.Error("a plan with no actions is empty")
	}
	p := Plan{Actions: []PlannedAction{{Action: ActionInstall}}}
	if p.Empty() {
		t.Error("a plan with an action is not empty")
	}
}

func TestSortStates(t *testing.T) {
	states := []State{
		{Package: Package{Name: "zed"}},
		{Package: Package{Name: "alpha"}},
		{Package: Package{Name: "mid"}},
	}
	SortStates(states)
	if states[0].Package.Name != "alpha" || states[2].Package.Name != "zed" {
		t.Errorf("states not sorted by name: %+v", states)
	}
}

func TestWarningStringNamesItsSubject(t *testing.T) {
	w := Warning{Severity: SeverityWarn, Package: "ruff", Message: "not installed"}
	got := w.String()
	if got != "warning: ruff: not installed" {
		t.Errorf("String() = %q", got)
	}
	global := Warning{Severity: SeverityInfo, Message: "provider absent"}
	if global.String() != "info: starpoint: provider absent" {
		t.Errorf("String() = %q", global.String())
	}
}
