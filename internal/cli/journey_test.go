package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// journeyCommands reads the README's "From one machine to another" block:
// every `str …` line, without its comment or its output redirection.
func journeyCommands(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	start := strings.Index(text, "<!-- journey:start -->")
	end := strings.Index(text, "<!-- journey:end -->")
	if start < 0 || end < start {
		t.Fatal("README.md lost its journey block (<!-- journey:start --> … <!-- journey:end -->)")
	}
	var out [][]string
	for _, line := range strings.Split(text[start:end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "str ") {
			continue
		}
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if i := strings.Index(line, ">"); i >= 0 {
			line = line[:i]
		}
		out = append(out, strings.Fields(line)[1:])
	}
	return out
}

// The README's quick start for the inventory-to-recovery journey runs as
// written: capture on one machine, compare on another, and the plan step
// the README shows is the one the plan prints. Steps 1 to 4 change nothing.
func TestReadmeJourneyRunsAsWritten(t *testing.T) {
	steps := journeyCommands(t)
	if len(steps) != 5 {
		t.Fatalf("want 5 journey steps in the README, got %d: %v", len(steps), steps)
	}

	// The machine you have: jq installed on purpose through brew.
	src := newHarness(t, "schema = 1\n")
	src.brew.With(model.Installation{Name: "jq", Version: "1.7.1"})
	src.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: src.brew, Marks: map[string]bool{"jq": true}})
	srcBefore := src.configBody()
	src.mustRun(steps[0]...)
	manifest := src.mustRun(steps[1]...)
	if !strings.Contains(manifest, "name = \"jq\"") || !strings.Contains(manifest, "provider = \"brew\"") {
		t.Fatalf("step 2 should capture jq from brew:\n%s", manifest)
	}
	if out := src.mustRun(steps[2]...); !strings.Contains(out, "Portability estimate") {
		t.Errorf("step 3 should print the estimate:\n%s", out)
	}
	src.assertUntouched(srcBefore)

	// The machine you want: nothing installed yet.
	dst := newHarness(t, "schema = 1\n")
	testsupport.WriteFile(t, filepath.Join(dst.env.WorkDir, "setup.toml"), manifest)
	dstBefore := dst.configBody()
	plan := dst.mustRun(steps[3]...)
	want := strings.Join(append([]string{"str"}, strings.TrimSuffix(strings.Join(steps[4], " "), " --dry-run")), " ")
	if !strings.Contains(plan, "1. "+want) {
		t.Errorf("the README's step 5 (%q) should be the plan's first step:\n%s", want, plan)
	}
	dst.assertUntouched(dstBefore)

	// Step 5 as the README writes it is a dry run: still nothing changes.
	dst.mustRun(steps[4]...)
	dst.assertUntouched(dstBefore)
}

// The top-level help leads with the same journey and says which commands
// only read.
func TestHelpPointsAtTheJourney(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	out := h.mustRun("help")
	for _, want := range []string{
		"From one machine to another (steps 1 to 4 change nothing)",
		"str inventory --format toml > setup.toml",
		"str inventory --portability",
		"str inventory --compare setup.toml",
		"Read only: list, inventory, info, explain, doctor, runs",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in the top-level help:\n%s", want, out)
		}
	}
	inv := h.mustRun("inventory", "--help")
	if !strings.Contains(inv, "Moving a setup to another machine") || !strings.Contains(inv, "--compare setup.toml") {
		t.Errorf("inventory --help should show the journey:\n%s", inv)
	}
}
