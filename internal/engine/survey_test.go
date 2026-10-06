package engine

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/providers/uv"
	"github.com/starpoint/starpoint/internal/testsupport"
)

func TestSurveyFindsEveryProviderThatHasTheSoftware(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	// Two providers own copies of the same software, which is precisely the
	// situation a first-match lookup cannot see.
	f.brew.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/brew/bin/ruff"})
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})

	s := f.eng.Survey(context.Background(), model.Package{Name: "ruff"}, Options{})
	if got := s.OwnerNames(); len(got) != 2 || got[0] != brew.Name || got[1] != uv.Name {
		t.Fatalf("expected both owners, got %v", got)
	}
	if !s.Any() {
		t.Error("the survey found nothing")
	}
	for _, f := range s.Owners() {
		if f.Installation == nil {
			t.Errorf("an owner must carry its installation record: %+v", f)
		}
	}
}

func TestSurveyReportsWhenNothingHasIt(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	s := f.eng.Survey(context.Background(), model.Package{Name: "nothinghere"}, Options{})
	if s.Any() {
		t.Errorf("nothing should have it, got %v", s.OwnerNames())
	}
	if s.TimedOut {
		t.Error("fakes answer immediately")
	}
}

// A provider that is not installed, and one that cannot enumerate its inventory,
// are both skipped — but for different reasons, and both stay visible. Treating
// "cannot ask" as "does not have it" would make Starpoint reinstall software on
// every sync.
func TestSurveyDistinguishesAbsentFromUnaskable(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	// system is available and can discover, so it is asked and answers no.
	// apt is not installed.
	// uv here is available and can discover.
	s := f.eng.Survey(context.Background(), model.Package{Name: "ruff"}, Options{})

	byName := map[string]Finding{}
	for _, f := range s.Findings {
		byName[f.Provider] = f
	}
	if got := byName["apt"]; got.Available {
		t.Error("apt is not installed on this fake machine")
	}
	if got := byName[system.Name]; !got.Available || !got.CanDiscover {
		t.Errorf("system is available and can discover: %+v", got)
	}
	if byName[system.Name].Has {
		t.Error("system should answer no for a package nobody installed")
	}
}

func TestSurveySkipsProvidersThatCannotDiscover(t *testing.T) {
	reg := provider.NewRegistry()
	blind := testsupport.NewFakeProvider("blind", provider.KindSystem)
	blind.Caps = provider.Capabilities{Discover: false, Install: true}
	reg.Register(blind)

	eng := New(reg, nil)
	s := eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	if len(s.Findings) != 1 {
		t.Fatalf("expected one finding, got %+v", s.Findings)
	}
	if s.Findings[0].CanDiscover {
		t.Error("a provider that cannot enumerate must say so")
	}
	if s.Findings[0].Has {
		t.Error("it must never be recorded as having anything")
	}
	if got := s.Unaskable(); len(got) != 1 || got[0] != "blind" {
		t.Errorf("the summary must still say it could not ask: %v", got)
	}
}

// Concurrency is the reason this exists, so it has to be observable rather than
// assumed: if the lookups were serial, the total time would be the sum.
func TestSurveyAsksProvidersConcurrently(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	const slow = 120 * time.Millisecond
	for _, p := range []*testsupport.FakeProvider{f.brew, f.uv, f.rustup, f.cargo, f.npm} {
		p.InstalledDelay = slow
	}

	start := time.Now()
	s := f.eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	elapsed := time.Since(start)

	if elapsed > slow*3 {
		t.Errorf("lookups look serial: five %v providers took %v", slow, elapsed)
	}
	if len(s.Findings) == 0 {
		t.Fatal("no findings")
	}
}

// A provider that never answers must not be able to hang the command. This is
// the failure mode concurrency introduces, so it is the one worth pinning down.
func TestSurveyDoesNotHangOnASlowProvider(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.InstalledDelay = time.Minute // would hang forever if unbounded

	done := make(chan Survey, 1)
	go func() {
		done <- f.eng.Survey(context.Background(), model.Package{Name: "x"},
			Options{SurveyTimeout: 80 * time.Millisecond})
	}()

	select {
	case s := <-done:
		if !s.TimedOut {
			t.Error("a provider that did not answer in time must be reported as such")
		}
		for _, f := range s.Findings {
			if f.Provider == brew.Name && f.Has {
				t.Error("a timed-out lookup must not claim the software")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the survey hung despite a timeout")
	}
}

func TestSurveyHonoursAnAlreadyCancelledContext(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.InstalledDelay = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := f.eng.Survey(ctx, model.Package{Name: "x"}, Options{})
	if s.Any() {
		t.Error("a cancelled survey cannot find anything")
	}
}

func TestSurveyCapturesProviderErrorsWithoutFailing(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.InstalledError = errors.New("brew exploded")

	s := f.eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	var found bool
	for _, f := range s.Findings {
		if f.Provider != brew.Name {
			continue
		}
		found = true
		if f.Error != "brew exploded" {
			t.Errorf("the failure should be recorded, got %q", f.Error)
		}
		if f.Has {
			t.Error("a failed lookup must not claim the software")
		}
	}
	if !found {
		t.Fatal("brew is missing from the findings")
	}
}

// One slow provider must not delay reporting on the others' answers.
func TestSurveyReportsEveryProviderEvenWhenOneIsSlow(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.InstalledDelay = 50 * time.Millisecond
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})

	s := f.eng.Survey(context.Background(), model.Package{Name: "ruff"},
		Options{SurveyTimeout: time.Second})
	if !s.Any() {
		t.Error("the fast provider's answer should survive a slow sibling")
	}
	var slowest Finding
	for _, f := range s.Findings {
		if f.DurationMS > slowest.DurationMS {
			slowest = f
		}
	}
	if slowest.Provider != brew.Name {
		t.Errorf("per-provider timings should be attributed, slowest was %s", slowest.Provider)
	}
}

func TestSurveyOrderIsStable(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	first := f.eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	second := f.eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	if len(first.Findings) != len(second.Findings) {
		t.Fatalf("finding count changed between runs")
	}
	for i := range first.Findings {
		if first.Findings[i].Provider != second.Findings[i].Provider {
			t.Errorf("finding %d: %q then %q — order must not depend on scheduling",
				i, first.Findings[i].Provider, second.Findings[i].Provider)
		}
	}
}

// Concurrent lookups must actually overlap; if the engine serialised them for any
// reason, a fast provider's answer would arrive after the slow ones and the
// "everyone at once" promise would be false.
func TestSurveyActuallyOverlapsLookups(t *testing.T) {
	reg := provider.NewRegistry()
	var inFlight, peak int64
	for i, name := range []string{"a", "b", "c", "d"} {
		p := testsupport.NewFakeProvider(name, provider.KindSystem)
		p.Caps = provider.Capabilities{Discover: true, Install: true}
		p.InstalledHook = func(ctx context.Context, _ model.Package) {
			cur := atomic.AddInt64(&inFlight, 1)
			for {
				old := atomic.LoadInt64(&peak)
				if cur <= old || atomic.CompareAndSwapInt64(&peak, old, cur) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			atomic.AddInt64(&inFlight, -1)
		}
		p.InstalledDelay = 60 * time.Millisecond
		_ = i
		reg.Register(p)
	}

	eng := New(reg, nil)
	start := time.Now()
	eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	if got := atomic.LoadInt64(&peak); got < 4 {
		t.Errorf("only %d lookups overlapped; they are running serially", got)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("four 60ms lookups took %v, which is more than one round", elapsed)
	}
}

func TestSurveySummaryNamesOwnersAndOmissions(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.With(model.Installation{Name: "ruff", Version: "0.5.0"})

	s := f.eng.Survey(context.Background(), model.Package{Name: "ruff"}, Options{})
	summary := s.Summary()
	joined := ""
	for _, line := range summary {
		joined += line + "\n"
	}
	if !strings.Contains(joined, brew.Name+" 0.5.0") {
		t.Errorf("the owning provider and its version should appear:\n%s", joined)
	}
	if !strings.Contains(joined, "not installed: apt") {
		t.Errorf("absent providers should be collapsed into one line:\n%s", joined)
	}
	if strings.Contains(joined, system.Name+" ") {
		t.Errorf("a provider that simply does not have it should not be listed:\n%s", joined)
	}
}

// A provider that is installed but cannot enumerate its inventory weakens any
// "nobody has it" claim, so it has to be nameable wherever that claim is made.
func TestSurveyNamesProvidersThatCannotBeAsked(t *testing.T) {
	reg := provider.NewRegistry()
	blind := testsupport.NewFakeProvider("blind", provider.KindSystem)
	blind.Caps = provider.Capabilities{Discover: false, Install: true}
	reg.Register(blind)
	eng := New(reg, nil)

	s := eng.Survey(context.Background(), model.Package{Name: "x"}, Options{})
	if got := s.Unaskable(); len(got) != 1 || got[0] != "blind" {
		t.Fatalf("the unaskable provider must be nameable, got %v", got)
	}
	// The brief form stays quiet: a command that is merely working should not
	// explain every provider it cannot consult.
	if len(s.SummaryBrief()) != 0 {
		t.Errorf("the brief summary should be empty, got %v", s.SummaryBrief())
	}
	full := strings.Join(s.Summary(), "\n")
	if !strings.Contains(full, "cannot be asked: blind") {
		t.Errorf("the full summary must say so: %v", s.Summary())
	}
}
