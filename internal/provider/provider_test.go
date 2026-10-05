package provider

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
)

func TestErrorCarriesKindAndUnwraps(t *testing.T) {
	cause := errors.New("boom")
	err := &Error{Provider: "brew", Op: "install", Kind: ErrNetwork, Message: "index unreachable", Err: cause}

	if !errors.Is(err, cause) {
		t.Error("the underlying cause must be unwrappable")
	}
	got, ok := AsProviderError(err)
	if !ok {
		t.Fatal("AsProviderError must recognise a provider error")
	}
	if got.Kind != ErrNetwork {
		t.Errorf("kind lost: %q", got.Kind)
	}
	if IsNotFound(err) {
		t.Error("a network error is not a not-found error")
	}
}

func TestIsNotFoundThroughWrapping(t *testing.T) {
	inner := &Error{Provider: "uv", Kind: ErrNotFound}
	wrapped := fmt.Errorf("while syncing: %w", inner)
	if !IsNotFound(wrapped) {
		t.Error("a wrapped not-found error must still be recognised")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("an ordinary error is not a not-found error")
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{Provider: "apt", Op: "install", Kind: ErrFailed, Message: "broken"}
	if e.Error() != "apt install: broken" {
		t.Errorf("Error() = %q", e.Error())
	}
	e2 := &Error{Provider: "apt", Op: "install", Kind: ErrFailed, Err: errors.New("raw")}
	if e2.Error() != "apt install: raw" {
		t.Errorf("Error() = %q", e2.Error())
	}
}

// stubProvider is a minimal implementation used to exercise the registry.
type stubProvider struct {
	name   string
	health Health
}

func (s *stubProvider) Name() string        { return s.name }
func (s *stubProvider) Description() string { return "stub " + s.name }
func (s *stubProvider) Kind() Kind          { return KindSystem }
func (s *stubProvider) Capabilities() Capabilities {
	return Capabilities{Discover: true}
}
func (s *stubProvider) Health(context.Context) Health { return s.health }
func (s *stubProvider) Discover(context.Context) ([]model.Installation, error) {
	return nil, nil
}
func (s *stubProvider) Installed(context.Context, model.Package) (*model.Installation, error) {
	return nil, nil
}
func (s *stubProvider) Install(context.Context, Request) (*model.Installation, error) {
	return nil, nil
}
func (s *stubProvider) Remove(context.Context, model.Package) error { return nil }
func (s *stubProvider) Update(context.Context, model.Package) (*model.Installation, error) {
	return nil, nil
}

func TestRegistryPreservesRegistrationOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "b", health: Health{Name: "b"}})
	r.Register(&stubProvider{name: "a", health: Health{Name: "a"}})

	got := r.Names()
	if len(got) != 2 || got[0] != "b" || got[1] != "a" {
		t.Errorf("registration order must be preserved for stable output, got %v", got)
	}
	if _, ok := r.Get("a"); !ok {
		t.Error("registered providers must be retrievable")
	}
	if _, ok := r.Get("missing"); ok {
		t.Error("an unregistered name must not resolve")
	}
}

func TestReRegisteringReplacesWithoutDuplicating(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "x", health: Health{Name: "x"}})
	r.Register(&stubProvider{name: "x", health: Health{Name: "x", Available: true}})

	if len(r.Names()) != 1 {
		t.Fatalf("re-registration must not duplicate, got %v", r.Names())
	}
	p, _ := r.Get("x")
	if p.Health(context.Background()).Available != true {
		t.Error("the later registration must win")
	}
}

func TestAvailableToleratesIndividualFailures(t *testing.T) {
	r := NewRegistry()
	r.Register(&stubProvider{name: "ok", health: Health{Name: "ok", Available: true}})
	r.Register(&stubProvider{name: "bad", health: Health{Name: "bad", Reason: "not installed"}})

	got := r.Available(context.Background())
	if len(got) != 2 {
		t.Fatalf("a failing provider must not hide the others, got %+v", got)
	}
	byName := map[string]Health{}
	for _, h := range got {
		byName[h.Name] = h
	}
	if !byName["ok"].Available {
		t.Error("the healthy provider should be reported available")
	}
	if byName["bad"].Reason == "" {
		t.Error("an unavailable provider must carry a reason")
	}
}

func TestRequestVersionAccessor(t *testing.T) {
	r := Request{Package: model.Package{Name: "x", Version: "1.2.3"}}
	if r.Version() != "1.2.3" {
		t.Errorf("Version() = %q", r.Version())
	}
	if (Request{}).Version() != "" {
		t.Error("an unset version must be empty")
	}
}

func TestKindValuesAreDistinct(t *testing.T) {
	seen := map[Kind]bool{}
	for _, k := range []Kind{
		KindSystem, KindBrew, KindApt, KindDnf, KindPacman, KindNix, KindWinget, KindChoco,
		KindPython, KindRust, KindNode, KindGo, KindRuby, KindPHP, KindDotnet, KindJVM,
		KindSwift, KindCocoa, KindVcpkg, KindConda, KindFlatpak, KindSnap,
	} {
		if seen[k] {
			t.Errorf("duplicate kind %q", k)
		}
		seen[k] = true
	}
}
