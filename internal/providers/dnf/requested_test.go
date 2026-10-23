package dnf

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// The command both DNF versions answer: every installed package with the
// reason DNF recorded for it.
var reasonQuery = []string{"repoquery", "--installed", "--queryformat", `%{name} %{reason}\n`}

func installedNames(names ...string) []model.Installation {
	out := make([]model.Installation, 0, len(names))
	for _, n := range names {
		out = append(out, model.Installation{Name: n})
	}
	return out
}

// Each reason, as DNF 4 spells it (lowercase, hyphenated, a newline of its
// own after each entry, hence the blank lines) and as DNF 5 does
// (capitalised, with spaces, only the newline the format asks for). Only a
// user reason is requested; only a definite dependency reason is a
// dependency; everything else stays unknown.
func TestRequestedReadsEachInstallReason(t *testing.T) {
	versions := map[string]string{
		"dnf4": "git user\n\n" +
			"ripgrep user\n\n" +
			"pcre2 dependency\n\n" +
			"libfoo weak-dependency\n\n" +
			"vim-enhanced group\n\n" +
			"old-rpm unknown\n\n" +
			"leftover clean\n\n",
		"dnf5": "git User\n" +
			"ripgrep User\n" +
			"pcre2 Dependency\n" +
			"libfoo Weak Dependency\n" +
			"vim-enhanced Group\n" +
			"old-rpm None\n" +
			"leftover Clean\n" +
			"hand-installed External User\n",
	}
	want := map[string]*bool{}
	yes, no := true, false
	want["git"], want["ripgrep"] = &yes, &yes
	want["pcre2"], want["libfoo"] = &no, &no
	for _, n := range []string{"vim-enhanced", "old-rpm", "leftover", "hand-installed", "not-in-output"} {
		want[n] = nil
	}
	for version, out := range versions {
		p, r := newProvider(t)
		r.Set(out, "dnf", reasonQuery...)
		got, err := p.Requested(context.Background(), installedNames(
			"git", "ripgrep", "pcre2", "libfoo", "vim-enhanced", "old-rpm", "leftover", "hand-installed", "not-in-output"))
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		for name, w := range want {
			g, known := got[name]
			switch {
			case w == nil && known:
				t.Errorf("%s: %s should be unknown, got %v", version, name, g)
			case w != nil && (!known || g != *w):
				t.Errorf("%s: %s = %v (known %v), want %v", version, name, g, known, *w)
			}
		}
		if _, asked := got["stray"]; asked {
			t.Errorf("%s: only installed names are answered: %v", version, got)
		}
	}
}

// --userinstalled also returns group and module-profile packages and those
// with an unknown reason, so it is not the question asked any more.
func TestRequestedDoesNotUseUserinstalled(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git User\n", "dnf", reasonQuery...)
	if _, err := p.Requested(context.Background(), installedNames("git")); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Calls {
		if c == "dnf repoquery --userinstalled --queryformat %{name} " {
			t.Errorf("Requested still asks --userinstalled: %v", r.Calls)
		}
	}
}

// A package installed for two architectures is requested when either was,
// and a dependency only when both were; a mix of dependency and unknown
// stays unknown.
func TestRequestedCombinesArchitectures(t *testing.T) {
	p, r := newProvider(t)
	r.Set("glibc Dependency\nglibc User\nzlib Dependency\nzlib Dependency\nlibgcc Dependency\nlibgcc None\n", "dnf", reasonQuery...)
	got, err := p.Requested(context.Background(), installedNames("glibc", "zlib", "libgcc"))
	if err != nil {
		t.Fatal(err)
	}
	if !got["glibc"] {
		t.Errorf("glibc was requested for one architecture: %v", got)
	}
	if v, ok := got["zlib"]; !ok || v {
		t.Errorf("zlib is a dependency for both: %v", got)
	}
	if _, ok := got["libgcc"]; ok {
		t.Errorf("libgcc is partly unknown, so unknown: %v", got)
	}
}

// When the reasons cannot be read, Requested fails, and inventory shows the
// provider's packages as unknown with a requested-unknown warning. A
// listing with no reason column leaves every name unknown.
func TestRequestedFallsBackToUnknown(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "Unknown queryformat tag", "dnf", reasonQuery...)
	if _, err := p.Requested(context.Background(), installedNames("git")); err == nil {
		t.Error("a failed query is an error, which inventory reports as unknown")
	}

	p, r = newProvider(t)
	r.Set("git\nripgrep\n", "dnf", reasonQuery...)
	got, err := p.Requested(context.Background(), installedNames("git", "ripgrep"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("names with no reason are unknown: %v", got)
	}
}
