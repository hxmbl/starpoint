package providers

import (
	"context"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/apt"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/cargo"
	"github.com/starpoint/starpoint/internal/providers/gem"
	"github.com/starpoint/starpoint/internal/providers/npm"
	"github.com/starpoint/starpoint/internal/providers/pip"
	"github.com/starpoint/starpoint/internal/providers/pnpm"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// Each provider that can name its newest version parses its own tool's output.
// The fixtures are the shapes those tools print.
func TestLatestVersionParsesEachToolsOutput(t *testing.T) {
	cases := []struct {
		name  string
		build func(r *testsupport.FakeRunner) provider.LatestVersionProvider
		cmd   []string
		out   string
		pkg   string
		want  string
	}{
		{"pip", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { r.WithBin("pip3"); return pip.New(r) },
			[]string{"pip3", "index", "versions", "cowsay"}, "cowsay (6.1)\nAvailable versions: 6.1, 6.0\n", "cowsay", "6.1"},
		{"npm", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return npm.New(r) },
			[]string{"npm", "view", "typescript", "version"}, "5.6.3\n", "typescript", "5.6.3"},
		{"pnpm", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return pnpm.New(r) },
			[]string{"pnpm", "view", "typescript", "version"}, "5.6.3\n", "typescript", "5.6.3"},
		{"brew", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return brew.New(r) },
			[]string{"brew", "info", "--json=v2", "ripgrep"}, `{"formulae":[{"versions":{"stable":"14.1.1"}}],"casks":[]}`, "ripgrep", "14.1.1"},
		{"brew cask", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return brew.New(r) },
			[]string{"brew", "info", "--json=v2", "firefox"}, `{"formulae":[],"casks":[{"version":"131.0"}]}`, "firefox", "131.0"},
		{"cargo", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return cargo.New(r, "/home/u") },
			[]string{"cargo", "search", "ripgrep", "--limit", "1"}, "ripgrep = \"14.1.1\"    # fast grep\n... and 30 crates more\n", "ripgrep", "14.1.1"},
		{"cargo inexact", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return cargo.New(r, "/home/u") },
			[]string{"cargo", "search", "rg", "--limit", "1"}, "rg-x = \"1.0.0\"    # other\n", "rg", ""},
		{"gem", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return gem.New(r) },
			[]string{"gem", "search", "--remote", "--exact", "rake"}, "\n*** REMOTE GEMS ***\n\nrake (13.2.1)\n", "rake", "13.2.1"},
		{"apt", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return apt.New(r) },
			[]string{"apt-cache", "policy", "git"}, "git:\n  Installed: 1:2.39.2-1\n  Candidate: 1:2.39.5-0\n", "git", "1:2.39.5-0"},
		{"apt none", func(r *testsupport.FakeRunner) provider.LatestVersionProvider { return apt.New(r) },
			[]string{"apt-cache", "policy", "nope"}, "nope:\n  Installed: (none)\n  Candidate: (none)\n", "nope", ""},
	}
	for _, c := range cases {
		r := testsupport.NewFakeRunner()
		p := c.build(r)
		r.Set(c.out, c.cmd[0], c.cmd[1:]...)
		got, err := p.LatestVersion(context.Background(), model.Package{Name: c.pkg})
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
