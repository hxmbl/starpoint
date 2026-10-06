package engine

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
)

// SurveyTimeout bounds one round of concurrent lookups.
//
// Every provider runs a subprocess. The runner kills a command when its context
// is cancelled, so a hung package manager costs this timeout rather than hanging
// Starpoint — but the bound still matters, because "slow" and "hung" look the
// same from outside and a tool that gives up should say so rather than stall.
const SurveyTimeout = 20 * time.Second

// Finding is one provider's answer to "do you have this software?".
type Finding struct {
	// Provider is the provider that was asked.
	Provider string `json:"provider"`
	// Available reports whether the provider's tooling is present.
	Available bool `json:"available"`
	// Reason explains an unavailable provider.
	Reason string `json:"reason,omitempty"`
	// CanDiscover reports whether the provider can answer at all. A provider that
	// cannot enumerate its inventory is skipped rather than asked pointlessly.
	CanDiscover bool `json:"can_discover"`
	// Has reports whether the provider claims the software.
	Has bool `json:"has"`
	// Installation is the provider's record of it, when it has it.
	Installation *model.Installation `json:"installation,omitempty"`
	// Error is why the lookup failed, if it did.
	Error string `json:"error,omitempty"`
	// DurationMS is how long this provider took, in milliseconds.
	DurationMS float64 `json:"duration_ms"`
}

// durationMS converts a duration for JSON, where a raw nanosecond count is a
// number nobody reads correctly.
func durationMS(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// Survey is the result of asking every provider at once.
//
// The point is not speed alone. Resolution normally proceeds one provider at a
// time and stops at the first hit, which is efficient but blind: it cannot
// distinguish "the only provider that has this" from "several providers have this
// and the configuration picks one of them". Asking everyone answers both.
type Survey struct {
	// Package is what was looked for.
	Package model.Package `json:"package"`
	// Findings is one entry per provider, in registration order.
	Findings []Finding `json:"findings"`
	// DurationMS is the wall time of the round, in milliseconds.
	DurationMS float64 `json:"duration_ms"`
	// TimedOut reports that at least one provider did not answer in time.
	TimedOut bool `json:"timed_out"`
}

// Owners returns the findings for available providers that claim the software.
func (s Survey) Owners() []Finding {
	var out []Finding
	for _, f := range s.Findings {
		if f.Available && f.CanDiscover && f.Has {
			out = append(out, f)
		}
	}
	return out
}

// OwnerNames lists the providers that claim the software.
func (s Survey) OwnerNames() []string {
	owners := s.Owners()
	names := make([]string, 0, len(owners))
	for _, f := range owners {
		names = append(names, f.Provider)
	}
	return names
}

// Any reports whether at least one provider claims the software.
func (s Survey) Any() bool { return len(s.Owners()) > 0 }

// SummaryBrief reports only which providers have the software.
//
// This is what a command says while it is already working: a machine with nine
// absent package managers does not need nine lines explaining it, and a provider
// that cannot enumerate its inventory is only worth mentioning where the user
// asked what Starpoint can see.
func (s Survey) SummaryBrief() []string {
	var out []string
	for _, f := range s.Findings {
		if f.Available && f.CanDiscover && f.Has {
			out = append(out, fmtFinding(f))
		}
	}
	return out
}

// Unaskable names the providers that are installed but cannot report what they
// have. This weakens any "nobody has it" claim, so it is surfaced wherever that
// claim is made rather than left implicit.
func (s Survey) Unaskable() []string {
	var out []string
	for _, f := range s.Findings {
		if f.Available && !f.CanDiscover {
			out = append(out, f.Provider)
		}
	}
	return out
}

// NotInstalled names the providers that are not on this machine.
func (s Survey) NotInstalled() []string {
	var out []string
	for _, f := range s.Findings {
		if !f.Available {
			out = append(out, f.Provider)
		}
	}
	return out
}

// Summary renders the survey in full, for a command whose job is to report what
// Starpoint can see.
func (s Survey) Summary() []string {
	var out []string
	if owners := s.SummaryBrief(); len(owners) > 0 {
		out = append(out, strings.Join(owners, "; "))
	}
	if silent := s.Unaskable(); len(silent) > 0 {
		out = append(out, "cannot be asked: "+strings.Join(silent, ", "))
	}
	if absent := s.NotInstalled(); len(absent) > 0 {
		out = append(out, "not installed: "+strings.Join(absent, ", "))
	}
	return out
}

// fmtFinding describes one provider that has the software.
func fmtFinding(f Finding) string {
	if f.Installation == nil {
		return f.Provider
	}
	if f.Installation.Version == "" {
		return f.Provider
	}
	return f.Provider + " " + f.Installation.Version
}

// Survey asks every provider concurrently whether it has the software.
//
// Concurrency is bounded in the only way that matters: the result channel is
// buffered to the number of providers, so a provider that finishes after the
// caller stopped waiting can always deliver and exit. Nothing here can leak a
// goroutine or block on send.
func (e *Engine) Survey(ctx context.Context, pkg model.Package, opts Options) Survey {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout(SurveyTimeout))
	defer cancel()

	available := e.health(ctx)
	all := e.reg.All()
	findings := make([]Finding, len(all))
	results := make(chan int, len(all))

	var wg sync.WaitGroup
	for i, p := range all {
		findings[i] = Finding{
			Provider:    p.Name(),
			Available:   available[p.Name()].Available,
			CanDiscover: p.Capabilities().Discover,
		}
		findings[i].Reason = available[p.Name()].Reason

		if !findings[i].Available || !findings[i].CanDiscover {
			// Nothing to ask. A provider that cannot enumerate its inventory has
			// no answer to give, which is different from answering "no" and worth
			// keeping visible.
			continue
		}
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			begin := time.Now()
			inst, err := e.providerInstallation(ctx, p, pkg)
			f := &findings[i]
			f.DurationMS = durationMS(time.Since(begin))
			switch {
			case ctx.Err() != nil:
				f.Error = "timed out"
			case err != nil:
				f.Error = err.Error()
			case inst != nil:
				f.Has = true
				f.Installation = inst
			}
			results <- i
		}(i, p)
	}
	wg.Wait()
	close(results)

	timedOut := false
	for i := range results {
		if findings[i].Error == "timed out" {
			timedOut = true
		}
	}
	return Survey{
		Package:    pkg,
		Findings:   findings,
		DurationMS: durationMS(time.Since(start)),
		TimedOut:   timedOut,
	}
}

// Options.Timeout lets a caller widen or drop the bound. A zero value keeps the
// default.
func (o Options) Timeout(def time.Duration) time.Duration {
	if o.SurveyTimeout > 0 {
		return o.SurveyTimeout
	}
	return def
}

// sortFindingsByDuration puts the slowest providers first, which is how one
// usually reads a survey when the point is what to make faster.
func sortFindingsByDuration(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		return findings[i].DurationMS > findings[j].DurationMS
	})
}
