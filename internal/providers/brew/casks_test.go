package brew

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// caskroomWith lays out a Caskroom in a temporary directory: one directory
// per cask, and an install receipt holding the given JSON for each cask
// whose value is not empty.
func caskroomWith(t *testing.T, receipts map[string]string) string {
	t.Helper()
	room := t.TempDir()
	for token, receipt := range receipts {
		meta := filepath.Join(room, token, ".metadata")
		if err := os.MkdirAll(meta, 0o755); err != nil {
			t.Fatal(err)
		}
		if receipt == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(meta, "INSTALL_RECEIPT.json"), []byte(receipt), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return room
}

// casksOnly sets up a Homebrew with the given casks, the given receipts, and
// no formulae, and returns what Requested says about the casks.
func casksOnly(t *testing.T, casks []string, receipts map[string]string, info string) (map[string]bool, func(string, ...string) bool) {
	t.Helper()
	p, r := newProvider(t)
	list := ""
	installed := make([]model.Installation, 0, len(casks))
	for _, c := range casks {
		list += c + "\n"
		installed = append(installed, model.Installation{Name: c})
	}
	r.Set(list, "brew", "list", "--cask", "-1")
	r.Set("", "brew", "list", "--formula", "--installed-on-request", "-1")
	r.Set(caskroomWith(t, receipts)+"\n", "brew", "--caskroom")
	if info != "" {
		r.Set(info, "brew", "info", "--json=v2", "--installed", "--cask")
	} else {
		r.SetFail(1, "Error: no info", "brew", "info", "--json=v2", "--installed", "--cask")
	}
	got, err := p.Requested(context.Background(), installed)
	if err != nil {
		t.Fatal(err)
	}
	return got, r.Ran
}

func wantReason(t *testing.T, got map[string]bool, name string, want string) {
	t.Helper()
	v, known := got[name]
	have := "unknown"
	switch {
	case known && v:
		have = "requested"
	case known:
		have = "dependency"
	}
	if have != want {
		t.Errorf("%s is %s, want %s (all: %v)", name, have, want, got)
	}
}

// The receipt's installed_on_request is the answer, both ways, and brew info
// is not needed when every cask has one.
func TestCaskReasonComesFromTheInstallReceipt(t *testing.T) {
	got, ran := casksOnly(t, []string{"firefox", "fuse-t"}, map[string]string{
		"firefox": `{"homebrew_version": "4.4.0", "installed_on_request": true, "installed_as_dependency": false}`,
		"fuse-t":  `{"homebrew_version": "5.0.3", "installed_on_request": false}`,
	}, "")
	wantReason(t, got, "firefox", "requested")
	wantReason(t, got, "fuse-t", "dependency")
	if ran("brew", "info", "--json=v2", "--installed", "--cask") {
		t.Error("brew info was run although every cask had a receipt")
	}
}

// An early receipt with a null installed_on_request (Homebrew/brew#20960)
// still says "dependency" through installed_as_dependency; a receipt that
// says neither is no answer.
func TestCaskReceiptWithNullReasonFallsBack(t *testing.T) {
	got, _ := casksOnly(t, []string{"a", "b"}, map[string]string{
		"a": `{"installed_on_request": null, "installed_as_dependency": true}`,
		"b": `{"installed_on_request": null, "installed_as_dependency": null}`,
	}, "")
	wantReason(t, got, "a", "dependency")
	wantReason(t, got, "b", "unknown")
}

// A cask installed before Homebrew wrote cask receipts (4.4.0) has none. It
// is a dependency when another installed cask depends on it, and otherwise
// unknown: never assumed requested.
func TestCaskWithoutReceiptIsADependencyOnlyWhenAnotherCaskNeedsIt(t *testing.T) {
	info := `{"formulae": [], "casks": [
		{"token": "macfuse-app", "depends_on": {"cask": ["homebrew/cask/macfuse"], "macos": {">=": ["10.15"]}}},
		{"token": "macfuse", "depends_on": {}},
		{"token": "iterm2", "depends_on": {"macos": {">=": ["12"]}}}
	]}`
	got, ran := casksOnly(t, []string{"macfuse-app", "macfuse", "iterm2"}, map[string]string{
		"macfuse-app": "", "macfuse": "", "iterm2": "",
	}, info)
	wantReason(t, got, "macfuse", "dependency")
	wantReason(t, got, "macfuse-app", "unknown")
	wantReason(t, got, "iterm2", "unknown")
	if !ran("brew", "info", "--json=v2", "--installed", "--cask") {
		t.Error("brew info should be asked for the casks without receipts")
	}
}

// Another cask's receipt lists its cask dependencies, so a dependency
// without a receipt of its own is still recognised when brew info fails.
func TestCaskDependencyFromAnotherCasksReceipt(t *testing.T) {
	got, _ := casksOnly(t, []string{"app", "helper"}, map[string]string{
		"app":    `{"installed_on_request": true, "runtime_dependencies": {"cask": [{"full_name": "helper", "version": "1.0", "declared_directly": true}]}}`,
		"helper": "",
	}, "")
	wantReason(t, got, "app", "requested")
	wantReason(t, got, "helper", "dependency")
}

// With neither the Caskroom nor brew info to go on, every cask is unknown.
func TestCasksAreUnknownWithNothingToGoOn(t *testing.T) {
	p, r := newProvider(t)
	r.Set("firefox\n", "brew", "list", "--cask", "-1")
	r.Set("jq\n", "brew", "list", "--formula", "--installed-on-request", "-1")
	r.SetFail(1, "Error: unknown command", "brew", "--caskroom")
	r.SetFail(1, "Error: broke", "brew", "info", "--json=v2", "--installed", "--cask")
	got, err := p.Requested(context.Background(), []model.Installation{{Name: "jq"}, {Name: "firefox"}})
	if err != nil {
		t.Fatal(err)
	}
	wantReason(t, got, "jq", "requested")
	wantReason(t, got, "firefox", "unknown")
}

// The older-Homebrew path (leaves) uses the same cask reasons.
func TestLeavesFallbackUsesCaskReceipts(t *testing.T) {
	p, r := newProvider(t)
	r.Set("firefox\nhelper\nmystery\n", "brew", "list", "--cask", "-1")
	r.SetFail(1, "Error: invalid option: --installed-on-request", "brew", "list", "--formula", "--installed-on-request", "-1")
	r.Set("jq\n", "brew", "leaves", "--installed-on-request")
	r.Set(caskroomWith(t, map[string]string{
		"firefox": `{"installed_on_request": true}`,
		"helper":  `{"installed_on_request": false}`,
		"mystery": "",
	})+"\n", "brew", "--caskroom")
	got, err := p.Requested(context.Background(), []model.Installation{{Name: "jq"}, {Name: "firefox"}, {Name: "helper"}, {Name: "mystery"}})
	if err != nil {
		t.Fatal(err)
	}
	wantReason(t, got, "jq", "requested")
	wantReason(t, got, "firefox", "requested")
	wantReason(t, got, "helper", "dependency")
	wantReason(t, got, "mystery", "unknown")
}
