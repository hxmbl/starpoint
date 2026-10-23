package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// hangingProvider never answers Discover on its own. With honour set it
// returns when its context is cancelled, as a provider whose subprocess the
// runner kills does; without it, it ignores cancellation altogether and only
// returns when the test ends.
type hangingProvider struct {
	*testsupport.FakeProvider
	honour  bool
	release chan struct{}
}

func (h *hangingProvider) Discover(ctx context.Context) ([]model.Installation, error) {
	if h.honour {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-h.release:
			return nil, nil
		}
	}
	<-h.release
	return nil, nil
}

// One package manager that hangs costs the discovery timeout, not the whole
// command: the hung provider is reported as failed (discover-failed, timed
// out) and every other provider is listed as usual, within the bound.
func TestInventoryBoundsAHangingProviderAndListsTheRest(t *testing.T) {
	for _, honour := range []bool{true, false} {
		name := "ignores-cancellation"
		if honour {
			name = "honours-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			reg := provider.NewRegistry()
			stuck := &hangingProvider{FakeProvider: testsupport.NewFakeProvider("stuck", provider.KindSystem), honour: honour, release: make(chan struct{})}
			stuck.Caps = provider.Capabilities{Discover: true}
			t.Cleanup(func() { close(stuck.release) })
			reg.Register(stuck)
			for _, n := range []string{"fast-a", "fast-b"} {
				p := testsupport.NewFakeProvider(n, provider.KindPython)
				p.Caps = provider.Capabilities{Discover: true}
				p.With(model.Installation{Name: n + "-tool", Version: "1.0"})
				reg.Register(p)
			}
			eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))
			const bound = 150 * time.Millisecond
			eng.SetDiscoverTimeout(bound)

			start := time.Now()
			done := make(chan Inventory, 1)
			go func() { done <- eng.Inventory(context.Background(), nil, nil) }()
			var inv Inventory
			select {
			case inv = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("inventory hung on a provider that never answers")
			}
			// The bound, then at most the same again as grace for stragglers.
			if took := time.Since(start); took > 2*bound+2*time.Second {
				t.Errorf("inventory took %s; the bound is %s", took, bound)
			}

			rows := map[string]InventoryProvider{}
			for _, r := range inv.Providers {
				rows[r.Name] = r
			}
			if r := rows["stuck"]; r.Listed || !strings.Contains(r.Error, "timed out") {
				t.Errorf("the hung provider is reported as timed out, not listed: %+v", r)
			}
			for _, n := range []string{"fast-a", "fast-b"} {
				if r := rows[n]; !r.Listed || r.Count != 1 || r.Error != "" {
					t.Errorf("%s still answers within the bound: %+v", n, r)
				}
			}
			listed := map[string]bool{}
			for _, it := range inv.Items {
				listed[it.Provider+"/"+it.Name] = true
			}
			if !listed["fast-a/fast-a-tool"] || !listed["fast-b/fast-b-tool"] || len(inv.Items) != 2 {
				t.Errorf("the other providers' packages are listed: %+v", inv.Items)
			}
			var warned bool
			for _, w := range inv.Warnings {
				if w.Code == model.WarnDiscoverFailed && w.Provider == "stuck" && strings.Contains(w.Message, "timed out") {
					warned = true
				}
			}
			if !warned {
				t.Errorf("a discover-failed warning names the hung provider and says it timed out: %+v", inv.Warnings)
			}
		})
	}
}

// The default bound is the survey's: package managers hang the same way
// whichever command asks them.
func TestInventoryDefaultsToTheSurveyTimeout(t *testing.T) {
	eng := New(provider.NewRegistry(), nil, WithStateDir(t.TempDir()))
	if got := eng.DiscoverTimeout(); got != SurveyTimeout {
		t.Errorf("DiscoverTimeout() = %s, want %s", got, SurveyTimeout)
	}
	eng.SetDiscoverTimeout(time.Second)
	if got := eng.DiscoverTimeout(); got != time.Second {
		t.Errorf("DiscoverTimeout() = %s after setting 1s", got)
	}
}

// hangingRequested lists normally but never answers which packages were
// requested until its context is cancelled.
type hangingRequested struct{ *testsupport.FakeProvider }

func (h *hangingRequested) Requested(ctx context.Context, _ []model.Installation) (map[string]bool, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// The requested query is the same package manager as the listing, so it
// shares the bound: a provider whose listing answered but whose requested
// query hangs is still listed, every package shown as unknown, with a
// requested-unknown warning saying it timed out.
func TestInventoryBoundsTheRequestedQuery(t *testing.T) {
	reg := provider.NewRegistry()
	slow := &hangingRequested{testsupport.NewFakeProvider("slow", provider.KindSystem)}
	slow.Caps = provider.Capabilities{Discover: true}
	slow.With(model.Installation{Name: "git"})
	reg.Register(slow)
	eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))
	eng.SetDiscoverTimeout(150 * time.Millisecond)

	done := make(chan Inventory, 1)
	go func() { done <- eng.Inventory(context.Background(), nil, nil) }()
	var inv Inventory
	select {
	case inv = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("inventory hung on a requested query that never answers")
	}
	if len(inv.Providers) != 1 || !inv.Providers[0].Listed || inv.Providers[0].Requested != "unknown" {
		t.Errorf("the provider is listed, with requested unknown: %+v", inv.Providers)
	}
	if len(inv.Items) != 1 || inv.Items[0].Requested != RequestedUnknown {
		t.Errorf("git is listed as unknown: %+v", inv.Items)
	}
	var warned bool
	for _, w := range inv.Warnings {
		if w.Code == model.WarnRequestedUnknown && strings.Contains(w.Message, "timed out") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a requested-unknown warning says the query timed out: %+v", inv.Warnings)
	}
}

// slowHealthProvider hangs in its health check. With honour set it returns
// once its context ends (as a provider whose `--version` subprocess the
// runner kills does) and reports itself unavailable, the way dnf, pacman,
// flatpak, nix and snap do when that command fails; without it, it ignores
// cancellation until the test releases it. hangs counts down: after that
// many hung checks it answers at once, so a later probe can succeed.
type slowHealthProvider struct {
	*testsupport.FakeProvider
	honour  bool
	release chan struct{}
	hangs   int
	calls   int
}

func (s *slowHealthProvider) Health(ctx context.Context) provider.Health {
	s.calls++
	h := s.FakeProvider.Health(ctx)
	if s.hangs == 0 {
		return h
	}
	s.hangs--
	if s.honour {
		select {
		case <-ctx.Done():
			h.Available = false
			h.Reason = ctx.Err().Error()
			return h
		case <-s.release:
			return h
		}
	}
	<-s.release
	return h
}

// A provider whose health check hangs costs its own bound, not the others':
// every other provider, including ones registered after it, is still
// probed, listed and counted, and the hung one is a failed provider with a
// discover-failed warning rather than a silently unavailable one.
func TestInventoryBoundsAHangingHealthCheckAndListsTheRest(t *testing.T) {
	for _, honour := range []bool{true, false} {
		name := "ignores-cancellation"
		if honour {
			name = "honours-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			reg := provider.NewRegistry()
			// Registered first, so a shared sequential deadline would have
			// been spent on it before the others were probed.
			stuck := &slowHealthProvider{FakeProvider: testsupport.NewFakeProvider("stuck", provider.KindSystem), honour: honour, release: make(chan struct{}), hangs: 1}
			stuck.Caps = provider.Capabilities{Discover: true}
			stuck.With(model.Installation{Name: "stuck-tool", Version: "1.0"})
			t.Cleanup(func() { close(stuck.release) })
			reg.Register(stuck)
			for _, n := range []string{"fast-a", "fast-b"} {
				p := testsupport.NewFakeProvider(n, provider.KindPython)
				p.Caps = provider.Capabilities{Discover: true}
				p.With(model.Installation{Name: n + "-tool", Version: "1.0"})
				reg.Register(p)
			}
			eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))
			const bound = 150 * time.Millisecond
			eng.SetDiscoverTimeout(bound)

			start := time.Now()
			done := make(chan Inventory, 1)
			go func() { done <- eng.Inventory(context.Background(), nil, nil) }()
			var inv Inventory
			select {
			case inv = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("inventory hung on a provider whose health check never answers")
			}
			// Health bound plus grace, then the listing round.
			if took := time.Since(start); took > 3*bound+2*time.Second {
				t.Errorf("inventory took %s; the bound is %s", took, bound)
			}

			rows := map[string]InventoryProvider{}
			for _, r := range inv.Providers {
				rows[r.Name] = r
			}
			if r := rows["stuck"]; r.Listed || r.Available || !strings.Contains(r.Error, "timed out") {
				t.Errorf("the hung provider is a failed provider saying it timed out: %+v", r)
			}
			for _, n := range []string{"fast-a", "fast-b"} {
				if r := rows[n]; !r.Available || !r.Listed || r.Count != 1 || r.Error != "" {
					t.Errorf("%s is still probed and listed: %+v", n, r)
				}
			}
			if len(inv.Items) != 2 {
				t.Errorf("only the other providers' packages are listed: %+v", inv.Items)
			}
			var warned bool
			for _, w := range inv.Warnings {
				if w.Code == model.WarnDiscoverFailed && w.Provider == "stuck" && strings.Contains(w.Message, "timed out") {
					warned = true
				}
			}
			if !warned {
				t.Errorf("a discover-failed warning names the hung provider and says it timed out: %+v", inv.Warnings)
			}
		})
	}
}

// A health check cut short by its deadline is not a verdict: the next
// question asks again and gets the real answer, instead of the provider
// staying "unavailable" for the rest of the process.
func TestTimedOutHealthCheckIsNotRemembered(t *testing.T) {
	reg := provider.NewRegistry()
	slow := &slowHealthProvider{FakeProvider: testsupport.NewFakeProvider("slow", provider.KindSystem), honour: true, release: make(chan struct{}), hangs: 1}
	slow.Caps = provider.Capabilities{Discover: true}
	slow.With(model.Installation{Name: "slow-tool", Version: "1.0"})
	defer close(slow.release)
	reg.Register(slow)
	eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))
	eng.SetDiscoverTimeout(100 * time.Millisecond)

	first := eng.Inventory(context.Background(), nil, nil)
	if len(first.Providers) != 1 || first.Providers[0].Error == "" {
		t.Fatalf("the first, hung health check fails the provider: %+v", first.Providers)
	}
	if got := eng.health(context.Background())["slow"]; !got.Available {
		t.Errorf("a later health check asks again and finds it available: %+v", got)
	}
	second := eng.Inventory(context.Background(), nil, nil)
	if r := second.Providers[0]; !r.Listed || r.Count != 1 || r.Error != "" {
		t.Errorf("a later inventory lists it: %+v", r)
	}
	if slow.calls != 2 {
		t.Errorf("health was probed %d times; want 2 (the timed-out one, then once more, memoised)", slow.calls)
	}
}

// A cancelled command is not remembered as unavailable either.
func TestCancelledHealthIsNotRemembered(t *testing.T) {
	reg := provider.NewRegistry()
	slow := &slowHealthProvider{FakeProvider: testsupport.NewFakeProvider("slow", provider.KindSystem), honour: true, release: make(chan struct{}), hangs: 1}
	defer close(slow.release)
	reg.Register(slow)
	eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := eng.health(ctx)["slow"]; got.Available {
		t.Fatalf("a cancelled check reports unavailable: %+v", got)
	}
	if got := eng.health(context.Background())["slow"]; !got.Available {
		t.Errorf("the next check asks again: %+v", got)
	}
}
