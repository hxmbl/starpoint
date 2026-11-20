package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func TestElapsedStringFormatsDurations(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m 30s"},
		{3700 * time.Second, "1h 1m 40s"},
	}
	for _, c := range cases {
		if got := elapsedString(c.d); got != c.want {
			t.Errorf("elapsedString(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestProgressBarShowsCompletedRatio(t *testing.T) {
	bar := progressBar(2, 4)
	if !strings.HasSuffix(bar, "] 50%") {
		t.Errorf("progressBar(2, 4) should end with the 50 percent mark, got %q", bar)
	}
	if strings.Count(bar, "=") != 20 || strings.Count(bar, "-") != 20 {
		t.Errorf("progressBar(2, 4) should fill half the bar, got %q", bar)
	}
	if got := progressBar(4, 4); !strings.HasSuffix(got, "] 100%") || strings.Count(got, "-") != 0 {
		t.Errorf("progressBar(4, 4) should be full: %q", got)
	}
	if got := progressBar(0, 0); !strings.HasSuffix(got, "] 0%") {
		t.Errorf("progressBar(0, 0) should not divide by zero: %q", got)
	}
}

func TestUpdateDisplayNonTerminalPrintsProviderSummaries(t *testing.T) {
	var buf bytes.Buffer
	d := newUpdateDisplay(&buf, color.Off(), 2, false)
	d.Start()
	brewP := testsupport.NewFakeProvider(brew.Name, provider.KindBrew)
	cargoP := testsupport.NewFakeProvider(cargo.Name, provider.KindRust)

	d.ProviderStart(brewP, "actionlint")
	d.PackageDone(brewP, "actionlint")
	// Moving to another provider prints the previous one's summary.
	d.ProviderStart(cargoP, "qemu")
	d.PackageDone(cargoP, "qemu")
	d.Finalize()

	out := buf.String()
	if !strings.Contains(out, "Updating packages across providers...") {
		t.Errorf("the header should be shown:\n%s", out)
	}
	if !strings.Contains(out, "[brew] Completed:\n  actionlint") {
		t.Errorf("the completed provider summary should be printed when it is superseded:\n%s", out)
	}
	if !strings.Contains(out, "[cargo] Completed:\n  qemu") {
		t.Errorf("the final provider summary should be printed at Finalize:\n%s", out)
	}
}

func TestUpdateDisplayTerminalBlockShowsCurrentProvider(t *testing.T) {
	var buf bytes.Buffer
	d := newUpdateDisplay(&buf, color.Off(), 2, true)
	d.Start()
	brewP := testsupport.NewFakeProvider(brew.Name, provider.KindBrew)
	brewP.Activity = "compiling qemu"
	d.ProviderStart(brewP, "actionlint")
	d.PackageDone(brewP, "actionlint")

	d.mu.Lock()
	d.start = time.Now().Add(-12*time.Minute - 34*time.Second)
	d.pollOnceLocked()
	lines := strings.Join(d.blockLines(), "\n")
	d.mu.Unlock()

	for _, want := range []string{
		"Updating packages across providers...",
		"[brew] Completed:\n  actionlint",
		"[brew] Currently: compiling qemu",
		"Time elapsed: 12m 34s",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the block should contain %q:\n%s", want, lines)
		}
	}
	if !strings.HasSuffix(lines, "Progress: "+progressBar(1, 2)) {
		t.Errorf("the block should end with the progress line:\n%s", lines)
	}
	d.Finalize()
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Error("repaint should move the cursor up once the block exists")
	}
}
