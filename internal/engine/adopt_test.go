package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

// Adopting records what is already there. A package the chosen provider does
// not have is not there to adopt, and the configuration must stay untouched:
// an entry recorded anyway is an install the next sync performs.
func TestAdoptOnlyNeverRecordsAPackageThatIsNotThere(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	before := f.configBody()
	_, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ripgrep", Provider: "brew"}, Options{AdoptOnly: true, NoConfirm: true})
	if err == nil || !provider.IsNotFound(err) {
		t.Fatalf("adopting something that is not installed must fail with not-found, got %v", err)
	}
	if got := f.configBody(); got != before {
		t.Errorf("nothing may be recorded:\n%s", got)
	}
	if len(f.brew.InstallCalls) != 0 {
		t.Error("adopting must never install")
	}
}

// Installed through another provider is not installed through the one named.
func TestAdoptOnlyRefusesAnotherProvidersCopy(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	before := f.configBody()
	_, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ruff", Provider: "brew"}, Options{AdoptOnly: true, NoConfirm: true})
	if err == nil || !strings.Contains(err.Error(), "installed through uv") {
		t.Fatalf("expected a not-found naming uv, got %v", err)
	}
	if f.configBody() != before {
		t.Error("nothing may be recorded")
	}
}

func TestAdoptOnlyRecordsWhatTheProviderHas(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.With(model.Installation{Name: "jq", Version: "1.7"})
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "jq", Provider: "brew"}, Options{AdoptOnly: true, NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.configBody(), `provider = "brew"`) || len(f.brew.InstallCalls) != 0 {
		t.Errorf("jq should be recorded with brew and nothing installed:\n%s", f.configBody())
	}
	if len(res.Applied) != 1 || res.Applied[0].Action != model.ActionAdopt {
		t.Errorf("expected one adopt action, got %+v", res.Applied)
	}
}
