package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
)

type inventoryDoc struct {
	SchemaVersion int `json:"schema_version"`
	Providers     []struct {
		Name      string `json:"name"`
		Available bool   `json:"available"`
		Listed    bool   `json:"listed"`
		Count     int    `json:"count"`
		Error     string `json:"error"`
	} `json:"providers"`
	Packages []struct {
		Provider string `json:"provider"`
		Name     string `json:"name"`
		Version  string `json:"version"`
		Scope    string `json:"scope"`
		Managed  bool   `json:"managed"`
	} `json:"packages"`
	Warnings []model.Warning `json:"warnings"`
	Summary  struct {
		Packages  int `json:"packages"`
		Managed   int `json:"managed"`
		Unmanaged int `json:"unmanaged"`
	} `json:"summary"`
}

// Managed is matched on (provider, name): a configuration entry naming the
// same software with another provider does not make another provider's copy
// managed.
func TestInventoryMatchesManagedByProviderAndName(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n\n[[package]]\nname = \"ruff\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	var doc inventoryDoc
	h.decode(h.mustRun("inventory", "--json"), &doc)
	if doc.SchemaVersion != report.SchemaVersion || report.SchemaVersion != 9 {
		t.Fatalf("schema_version = %d (writer %d), want 9", doc.SchemaVersion, report.SchemaVersion)
	}
	got := map[string]bool{}
	for _, p := range doc.Packages {
		got[p.Provider+"/"+p.Name] = p.Managed
	}
	if managed, ok := got["brew/ripgrep"]; !ok || !managed {
		t.Errorf("brew/ripgrep should be listed and managed: %v", got)
	}
	if managed, ok := got["uv/ruff"]; !ok || managed {
		t.Errorf("uv/ruff is configured for brew, so uv's copy is unmanaged: %v", got)
	}
	if doc.Summary.Packages != 2 || doc.Summary.Managed != 1 || doc.Summary.Unmanaged != 1 {
		t.Errorf("summary = %+v", doc.Summary)
	}
}

// Inventory observes and nothing else: no install, no lookup per package, no
// configuration write, no audit log or state directory, and no configuration
// needed in the first place.
func TestInventoryWritesNothingAndNeedsNoConfiguration(t *testing.T) {
	h := newHarness(t, "")
	if err := os.Remove(h.config); err != nil {
		t.Fatal(err)
	}
	h.brew.With(model.Installation{Name: "jq", Version: "1.7"})
	h.brew.InstalledHook = func(context.Context, model.Package) { t.Error("inventory must not look packages up one by one") }
	h.uv.InstalledHook = h.brew.InstalledHook
	out := h.mustRun("inventory")
	if !strings.Contains(out, "jq") || !strings.Contains(out, "unmanaged") {
		t.Errorf("jq should be listed as unmanaged:\n%s", out)
	}
	if len(h.brew.InstallCalls)+len(h.brew.RemoveCalls)+len(h.uv.InstallCalls) != 0 {
		t.Error("inventory must not install or remove anything")
	}
	if _, err := os.Stat(h.config); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inventory must not create a configuration: %v", err)
	}
	stateDir, _ := config.StateDir(h.config)
	if _, err := os.Stat(filepath.Join(stateDir, "logs")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inventory must not write an audit log: %v", err)
	}
}

func TestInventoryProviderFilter(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq"})
	h.uv.With(model.Installation{Name: "ruff"})
	var doc inventoryDoc
	h.decode(h.mustRun("inventory", "--provider", "uv", "--json"), &doc)
	if len(doc.Packages) != 1 || doc.Packages[0].Name != "ruff" {
		t.Errorf("--provider uv should list only uv's packages: %+v", doc.Packages)
	}
	if len(doc.Providers) != 1 || doc.Providers[0].Name != "uv" {
		t.Errorf("--provider uv should report only uv: %+v", doc.Providers)
	}
	if code, _, _ := h.run("inventory", "--provider", "nosuch"); code != 2 {
		t.Errorf("an unknown provider is a usage error, got exit %d", code)
	}
}

// A provider that cannot list is named with discover-failed; the others are
// still reported.
func TestInventoryReportsAFailingProviderAndContinues(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.DiscoverError = errors.New("brew exploded")
	h.uv.With(model.Installation{Name: "ruff"})
	var doc inventoryDoc
	h.decode(h.mustRun("inventory", "--json"), &doc)
	found := false
	for _, w := range doc.Warnings {
		if w.Code == model.WarnDiscoverFailed && w.Provider == "brew" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a discover-failed warning for brew: %+v", doc.Warnings)
	}
	if len(doc.Packages) != 1 || doc.Packages[0].Name != "ruff" {
		t.Errorf("uv's packages should still be listed: %+v", doc.Packages)
	}
	for _, p := range doc.Providers {
		if p.Name == "brew" && (p.Listed || p.Error == "") {
			t.Errorf("brew should be reported as not listed, with its error: %+v", p)
		}
		if p.Name == "apt" && p.Available {
			t.Errorf("apt is unavailable in the harness: %+v", p)
		}
	}
}
