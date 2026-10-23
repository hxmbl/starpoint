package brew

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestRequestedReadsInstalledOnRequest(t *testing.T) {
	p, r := newProvider(t)
	r.Set("firefox\n", "brew", "list", "--cask", "-1")
	r.Set("jq\nripgrep\n", "brew", "list", "--formula", "--installed-on-request", "-1")
	got, err := p.Requested(context.Background(), []model.Installation{{Name: "jq"}, {Name: "ripgrep"}, {Name: "oniguruma"}, {Name: "firefox"}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"jq": true, "ripgrep": true, "oniguruma": false, "firefox": true}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (known %v), want %v", k, g, ok, v)
		}
	}
}

// An older Homebrew without `list --installed-on-request` falls back to
// leaves, which cannot name a requested formula another formula depends on:
// those stay unknown instead of being called dependencies.
func TestRequestedFallsBackToLeavesAndLeavesTheRestUnknown(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "Error: invalid option: --installed-on-request", "brew", "list", "--formula", "--installed-on-request", "-1")
	r.Set("jq\n", "brew", "leaves", "--installed-on-request")
	got, err := p.Requested(context.Background(), []model.Installation{{Name: "jq"}, {Name: "oniguruma"}})
	if err != nil {
		t.Fatal(err)
	}
	if !got["jq"] {
		t.Errorf("jq is a requested leaf: %v", got)
	}
	if _, known := got["oniguruma"]; known {
		t.Errorf("oniguruma must stay unknown, not a dependency: %v", got)
	}
}

// When the cask list cannot be read, a name missing from the on-request
// formula list may well be a cask, which is always installed on request:
// it must not be called a dependency. Formulae brew still lists as such
// keep their answer.
func TestRequestedLeavesCasksUnknownWhenTheCaskListFails(t *testing.T) {
	installed := []model.Installation{{Name: "jq"}, {Name: "oniguruma"}, {Name: "firefox"}}
	for _, formulaList := range []bool{true, false} {
		p, r := newProvider(t)
		r.SetFail(1, "Error: cask listing broke", "brew", "list", "--cask", "-1")
		r.Set("jq\n", "brew", "list", "--formula", "--installed-on-request", "-1")
		if formulaList {
			r.Set("jq\noniguruma\n", "brew", "list", "--formula", "-1")
		} else {
			r.SetFail(1, "Error: formula listing broke too", "brew", "list", "--formula", "-1")
		}
		got, err := p.Requested(context.Background(), installed)
		if err != nil {
			t.Fatal(err)
		}
		if !got["jq"] {
			t.Errorf("formula list %v: jq was installed on request: %v", formulaList, got)
		}
		if v, known := got["firefox"]; known {
			t.Errorf("formula list %v: firefox may be a cask, so unknown, not %v", formulaList, v)
		}
		v, known := got["oniguruma"]
		switch {
		case formulaList && (!known || v):
			t.Errorf("oniguruma is a formula not installed on request, so a dependency: %v", got)
		case !formulaList && known:
			t.Errorf("with no formula list oniguruma could be a cask, so unknown: %v", got)
		}
	}
}
