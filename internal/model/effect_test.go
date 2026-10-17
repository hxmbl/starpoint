package model

import "testing"

func TestDiffEntryMarksLossOfInformationAsDestructive(t *testing.T) {
	added := DiffEntry("/c.toml", nil, &Package{Name: "ruff", Provider: "uv"})
	if added == nil || added.Change != ConfigAdd || added.Destructive {
		t.Errorf("adding an entry loses nothing: %+v", added)
	}
	removed := DiffEntry("/c.toml", &Package{Name: "ruff", Provider: "uv", Version: "1.0"}, nil)
	if removed == nil || removed.Change != ConfigRemove || !removed.Destructive || len(removed.Fields) != 2 {
		t.Errorf("dropping an entry is destructive and lists what it drops: %+v", removed)
	}
	filled := DiffEntry("/c.toml", &Package{Name: "ruff"}, &Package{Name: "ruff", Provider: "uv", Reason: "x"})
	if filled == nil || filled.Change != ConfigUpdate || filled.Destructive {
		t.Errorf("recording a provider the entry did not have adds information: %+v", filled)
	}
	moved := DiffEntry("/c.toml", &Package{Name: "ruff", Provider: "brew"}, &Package{Name: "ruff", Provider: "uv"})
	if moved == nil || !moved.Destructive || moved.Fields[0].From != "brew" || moved.Fields[0].To != "uv" {
		t.Errorf("overwriting a provider is destructive: %+v", moved)
	}
	repinned := DiffEntry("/c.toml", &Package{Name: "ruff", Version: "1.0"}, &Package{Name: "ruff", Version: "1.1"})
	if repinned == nil || !repinned.Destructive {
		t.Errorf("rewriting a pin is destructive: %+v", repinned)
	}
	reason := DiffEntry("/c.toml", &Package{Name: "ruff", Reason: "a"}, &Package{Name: "ruff", Reason: "b"})
	if reason == nil || reason.Destructive {
		t.Errorf("the reason is commentary: %+v", reason)
	}
	if DiffEntry("/c.toml", &Package{Name: "ruff"}, &Package{Name: "ruff"}) != nil {
		t.Error("an identical entry is no change")
	}
}

func TestActionIsDestructiveWhenAnyEffectIs(t *testing.T) {
	a := PlannedAction{Effects: []Effect{{Op: EffectCreate}}}
	if a.Destructive() {
		t.Error("a create removes nothing")
	}
	a.Effects = append(a.Effects, Effect{Op: EffectDelete, Destructive: true})
	if !a.Destructive() {
		t.Error("one destructive effect makes the action destructive")
	}
	b := PlannedAction{ConfigChanges: []ConfigChange{{Destructive: true}}}
	if !b.Destructive() {
		t.Error("a destructive config change makes the action destructive")
	}
}

func TestMergeEffectsDropsDuplicates(t *testing.T) {
	e := Effect{Op: EffectDelete, Kind: EffectFile, Path: "/a"}
	out := MergeEffects([]Effect{e}, e, Effect{Op: EffectCreate, Kind: EffectFile, Path: "/b"})
	if len(out) != 2 {
		t.Errorf("expected two distinct effects, got %+v", out)
	}
}
