package dnf

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// DNF 4 puts each entry on its own line, DNF 5 runs them together with the
// trailing space; both read the same.
func TestRequestedReadsUserInstalled(t *testing.T) {
	for _, out := range []string{"git \nripgrep \n", "git ripgrep "} {
		p, r := newProvider(t)
		r.Set(out, "dnf", "repoquery", "--userinstalled", "--queryformat", "%{name} ")
		got, err := p.Requested(context.Background(), []model.Installation{{Name: "git"}, {Name: "ripgrep"}, {Name: "pcre2"}})
		if err != nil {
			t.Fatal(err)
		}
		if !got["git"] || !got["ripgrep"] || got["pcre2"] {
			t.Errorf("%q: got %v", out, got)
		}
		if _, ok := got["pcre2"]; !ok {
			t.Errorf("pcre2 should be known to be a dependency: %v", got)
		}
	}
}
