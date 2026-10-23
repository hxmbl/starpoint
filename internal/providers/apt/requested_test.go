package apt

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestRequestedReadsManualMarks(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\nripgrep\nlibc6\n", "apt-mark", "showmanual")
	got, err := p.Requested(context.Background(), []model.Installation{
		{Name: "git"}, {Name: "libpcre2-8-0"}, {Name: "libc6:arm64"}, {Name: "ripgrep"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"git": true, "libpcre2-8-0": false, "libc6:arm64": true, "ripgrep": true}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (known %v), want %v", k, g, ok, v)
		}
	}
}

func TestRequestedFailureIsAnError(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(100, "E: broken", "apt-mark", "showmanual")
	if _, err := p.Requested(context.Background(), []model.Installation{{Name: "git"}}); err == nil {
		t.Fatal("a failed apt-mark must not read as 'everything is a dependency'")
	}
}
