package model

import "fmt"

// TransitionPhase describes how far a move of ownership between providers has
// got.
//
// The phases exist so that "partway through" is a nameable state rather than a
// shrug. Everything before TransitionVerified is safe by construction: the old
// installation is still there and still working, so an interrupted transition
// leaves a duplicate rather than a hole.
type TransitionPhase string

const (
	// TransitionPlanned means the target provider is known and nothing has
	// happened yet.
	TransitionPlanned TransitionPhase = "planned"
	// TransitionInstalled means the target provider now has the software, but it
	// has not been confirmed working. The old copy is untouched.
	TransitionInstalled TransitionPhase = "installed"
	// TransitionVerified means the new installation was confirmed working and
	// the old copy is eligible for removal. Both copies may still be present.
	TransitionVerified TransitionPhase = "verified"
	// TransitionCompleted means ownership has moved. OldRemoved says whether the
	// previous installation is gone.
	TransitionCompleted TransitionPhase = "completed"
	// TransitionFailed means a phase did not complete. Recoverable says whether
	// running it again would finish the job.
	TransitionFailed TransitionPhase = "failed"
	// TransitionBlocked means the transition cannot safely proceed at all, for
	// example because the target provider cannot install the software. Nothing on
	// the machine was changed.
	TransitionBlocked TransitionPhase = "blocked"
)

// Terminal reports whether the phase is a resting state: nothing more will happen
// without a new decision.
func (p TransitionPhase) Terminal() bool {
	return p == TransitionCompleted || p == TransitionBlocked
}

// SafeToRetry reports whether the machine is in a usable state if the transition
// is interrupted now. Everything before the old copy is removed is, by design.
func (p TransitionPhase) SafeToRetry() bool {
	switch p {
	case TransitionPlanned, TransitionInstalled, TransitionVerified, TransitionFailed:
		return true
	default:
		return false
	}
}

// Verification is the evidence that an installation actually works.
//
// It is a record rather than a boolean because "the provider says it installed
// it" and "the command runs" are different claims, and the second is what the
// user depends on. A transition is allowed to remove the old copy only when this
// evidence supports it.
type Verification struct {
	// Provider is the provider that was asked to confirm the software.
	Provider string `json:"provider"`
	// Present is whether that provider reports the software.
	Present bool `json:"present"`
	// OnPath is where the executable resolves, empty when the name is not an
	// executable name.
	OnPath string `json:"on_path,omitempty"`
	// PathOwner is the provider the executable on PATH actually belongs to.
	PathOwner string `json:"path_owner,omitempty"`
	// Resolved is whether PATH resolves the software to the provider that claims
	// it. False with Present true means something else is still winning, and
	// removing the old copy would break the command.
	Resolved bool `json:"resolved"`
	// Version is what the software reported about itself, when available.
	Version string `json:"version,omitempty"`
	// OnDisk is the executable found on disk when PATH did not resolve the name.
	OnDisk string `json:"on_disk,omitempty"`
	// Missing is true when the name is a command but no executable was found on
	// PATH or at any location the provider reported. An inventory entry without
	// a file behind it is not a working installation.
	Missing bool `json:"missing,omitempty"`
	// Method names the strongest evidence used, for reporting.
	Method string `json:"method,omitempty"`
	// Note explains a partial or surprising result.
	Note string `json:"note,omitempty"`
}

// OK reports whether the evidence is strong enough to remove the previous
// installation.
//
// Presence is required always. PATH corroboration is required when the software
// is named like an executable — if a command resolves to a *different* provider's
// copy, the transition has not actually taken effect and pulling the old copy out
// would break it. When the name cannot be looked up on PATH at all, the provider's
// own inventory is accepted with a note rather than treated as failure.
func (v Verification) OK() bool {
	if !v.Present || v.Missing {
		return false
	}
	if v.PathOwner != "" && v.PathOwner != v.Provider {
		// Something else owns the executable.
		return false
	}
	// Agreement between the inventory and PATH is confirmation. So is PATH having
	// nothing to say, because a package whose name is not a command cannot be
	// looked up at all.
	return v.Resolved || v.PathOwner == v.Provider || v.OnPath == ""
}

// Reason renders a one-line explanation of why verification did or did not pass.
func (v Verification) Reason() string {
	switch {
	case !v.Present:
		return v.Provider + " does not report it as installed"
	case v.Missing:
		return v.Provider + " lists it, but its executable is not on PATH or anywhere " + v.Provider + " reported"
	case v.PathOwner != "" && v.PathOwner != v.Provider:
		return fmt.Sprintf("%s is installed but the command resolves to %s's copy at %s",
			v.Provider, v.PathOwner, v.OnPath)
	case v.Resolved && v.OnDisk != "":
		return fmt.Sprintf("confirmed: %s is installed and its executable exists at %s", v.Provider, v.OnDisk)
	case v.Resolved:
		return fmt.Sprintf("confirmed: %s is installed and the command resolves to it", v.Provider)
	default:
		return v.Provider + "'s inventory confirms it, but it reported no path to corroborate with"
	}
}

// ProviderRef identifies a provider in a transition.
type ProviderRef struct {
	// Name is the provider's identifier.
	Name string `json:"name"`
	// Available is whether the provider's own tooling is present.
	Available bool `json:"available"`
	// Reason explains an unavailable provider.
	Reason string `json:"reason,omitempty"`
	// CanRemove is whether the provider is able to uninstall.
	CanRemove bool `json:"can_remove,omitempty"`
	// CanInstall is whether the provider is able to install.
	CanInstall bool `json:"can_install,omitempty"`
}

// Transition is a first-class move of ownership from one provider to another.
//
// The ordering of the phases is the whole point: the old installation is removed
// only after the new one has been confirmed working, so every failure mode leaves
// a working command on the machine.
type Transition struct {
	// Package is the desired entry that is moving.
	Package Package `json:"package"`
	// From is the incumbent provider.
	From ProviderRef `json:"from"`
	// To is the provider that should own the software.
	To ProviderRef `json:"to"`
	// Phase is how far the transition has got.
	Phase TransitionPhase `json:"phase"`
	// Verification is the evidence for the new installation.
	Verification Verification `json:"verification"`
	// Installed is the new installation, once the target has it.
	Installed *Installation `json:"installed,omitempty"`
	// Previous is the installation being replaced.
	Previous *Installation `json:"previous,omitempty"`
	// OldRemoved reports whether the previous installation is gone.
	OldRemoved bool `json:"old_removed"`
	// LeftBehind is the previous installation that could not be removed, with
	// the reason. This is a leftover duplicate, not a failure: ownership has
	// moved and the configuration is satisfied.
	LeftBehindReason string `json:"left_behind_reason,omitempty"`
	// Recoverable says whether running the transition again would finish it.
	Recoverable bool `json:"recoverable"`
	// Advice tells the user what to do about a failure or a leftover.
	Advice string `json:"advice,omitempty"`
	// Note is a short human summary of what happened.
	Note string `json:"note,omitempty"`
	// Timestamp records when the transition was completed. Used for history.
	Timestamp int64 `json:"timestamp,omitempty"`
	// Reversible reports whether this transition can be reversed.
	Reversible bool `json:"reversible,omitempty"`
	// Effects are what the move changed on the machine, observed while it ran,
	// or what it is expected to change when it has not run yet.
	Effects []Effect `json:"effects,omitempty"`
	// Warnings concern this move specifically, such as two providers writing
	// the same executable.
	Warnings []Warning `json:"warnings,omitempty"`
}

// NeedsWork reports whether the transition is not in a resting state.
func (t Transition) NeedsWork() bool { return !t.Phase.Terminal() }

// DeriveTransition works out a transition's current phase from what is actually
// on the machine, rather than from anything Starpoint remembers.
//
// This is what makes an interrupted transition recoverable with no journal: both
// copies present means the old one still needs removing, only the new one means it
// is done, and only the old one means nothing happened. Deriving the phase keeps
// the source of truth the configuration and the machine, not a log file that can
// disagree with both.
func DeriveTransition(pkg Package, from, to ProviderRef, previous, installed *Installation,
	verification Verification, canRemoveOld bool) Transition {
	t := Transition{
		Package:      pkg,
		From:         from,
		To:           to,
		Previous:     previous,
		Installed:    installed,
		Verification: verification,
	}
	switch {
	case installed == nil:
		// Nothing to remove or verify yet: the move simply has not started.
		// Whether it *can* start is the executor's question, not this function's.
		t.Phase = TransitionPlanned
		t.Recoverable = true
		if previous == nil {
			t.Note = "nothing to move: " + to.Name + " does not have it yet"
		} else {
			t.Note = "the software is still only provided by " + from.Name
		}
	case previous == nil:
		// Nothing to replace: this is an adoption, not a transition.
		t.Phase = TransitionCompleted
		t.OldRemoved = true
		if verification.OK() {
			t.Note = to.Name + " provides it"
		} else {
			t.Note = to.Name + " reports it installed but it could not be confirmed: " +
				verification.Reason()
		}
	case !verification.OK():
		// The new copy exists but is not the one a command would run.
		t.Phase = TransitionInstalled
		t.Recoverable = true
		t.Note = "the new installation is not usable yet: " + verification.Reason()
		t.Advice = fmt.Sprintf(
			"%s's copy is installed but not the one on PATH, so the %s copy was left in place",
			to.Name, from.Name)
	case !canRemoveOld:
		t.Phase = TransitionCompleted
		t.LeftBehindReason = fmt.Sprintf("%s cannot remove software", from.Name)
		t.Note = to.Name + " now provides it; the " + from.Name + " copy is still there"
		t.Advice = fmt.Sprintf("remove the leftover %s installation yourself if you no longer want it", from.Name)
	default:
		// Both copies present and the new one works: the old copy is next.
		t.Phase = TransitionVerified
		t.Recoverable = true
		t.Note = "the new installation is confirmed; the " + from.Name + " copy has not been removed"
	}
	return t
}
