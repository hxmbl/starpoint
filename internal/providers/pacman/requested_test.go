package pacman

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestRequestedReadsExplicitInstalls(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\nneovim\n", "pacman", "-Qqe")
	got, err := p.Requested(context.Background(), []model.Installation{{Name: "git"}, {Name: "neovim"}, {Name: "glibc"}})
	if err != nil {
		t.Fatal(err)
	}
	if !got["git"] || !got["neovim"] || got["glibc"] {
		t.Errorf("got %v", got)
	}
}
