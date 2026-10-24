package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// barrierHealthProvider answers its health check only once every provider in
// the group has started its own, so a sequential prober never gets past the
// first one before the deadline.
type barrierHealthProvider struct {
	*testsupport.FakeProvider
	started *sync.WaitGroup
	all     chan struct{}
}

func (b *barrierHealthProvider) Health(ctx context.Context) provider.Health {
	b.started.Done()
	h := b.FakeProvider.Health(ctx)
	select {
	case <-b.all:
	case <-time.After(2 * time.Second):
		h.Available = false
		h.Reason = "probed alone: the other providers were not being probed at the same time"
	}
	return h
}

// Each first health probe is a subprocess; asking a dozen package managers
// one after another made every command wait for their sum. They run side by
// side, and are still memoised.
func TestHealthProbesProvidersConcurrently(t *testing.T) {
	reg := provider.NewRegistry()
	names := []string{"a", "b", "c", "d"}
	var started sync.WaitGroup
	started.Add(len(names))
	all := make(chan struct{})
	go func() { started.Wait(); close(all) }()
	for _, n := range names {
		reg.Register(&barrierHealthProvider{FakeProvider: testsupport.NewFakeProvider(n, provider.KindPython), started: &started, all: all})
	}
	eng := New(reg, nil, WithStateDir(t.TempDir()), WithHome(t.TempDir()))

	got := eng.health(context.Background())
	for _, n := range names {
		if !got[n].Available {
			t.Errorf("%s: %s", n, got[n].Reason)
		}
	}
	// Memoised: a second call probes nothing (the barrier would otherwise
	// panic on a negative WaitGroup counter).
	again := eng.health(context.Background())
	if len(again) != len(names) {
		t.Errorf("second call returned %d providers", len(again))
	}
}
