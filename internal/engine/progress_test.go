package engine

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/brew"
)

// TestUpdateEmitsProviderProgressEvents checks the progress events Update
// emits while it runs: one start per provider call and one done per call, in
// order, so a progress display can track the operation.
func TestUpdateEmitsProviderProgressEvents(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})

	var events []Progress
	opts := Options{NoConfirm: true, Progress: func(ev Progress) { events = append(events, ev) }}
	if _, err := f.eng.Update(context.Background(), f.doc, opts); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(f.brew.UpdateCalls) != 1 {
		t.Fatalf("the package should have been updated once, got %d", len(f.brew.UpdateCalls))
	}
	if len(events) != 2 {
		t.Fatalf("expected a start and a done event, got %v", events)
	}
	want := []struct {
		kind     ProgressKind
		provider string
		pkg      string
	}{
		{ProgressProviderStart, brew.Name, "ripgrep"},
		{ProgressPackageDone, brew.Name, "ripgrep"},
	}
	for i, w := range want {
		ev := events[i]
		if ev.Kind != w.kind || ev.Provider.Name() != w.provider || ev.Package != w.pkg {
			t.Errorf("event %d: kind %v provider %q package %q, want kind %v provider %q package %q",
				i, ev.Kind, ev.Provider.Name(), ev.Package, w.kind, w.provider, w.pkg)
		}
	}
}
