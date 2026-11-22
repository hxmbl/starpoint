package cli

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/provider"
	"golang.org/x/term"
)

// updateDisplay renders the progress of a running `str update`.
//
// While the engine updates packages it emits progress events through
// Options.Progress. The display keeps one block on screen that is redrawn in
// place: the header, the per-provider completion lists so far, the provider
// being updated with its current package or activity, the elapsed time and
// the progress of completed work. In a terminal the block is repainted with
// ANSI cursor control; without one, each provider's completion is printed as
// it happens instead of being redrawn.
type updateDisplay struct {
	w        io.Writer
	p        color.Palette
	terminal bool

	mu            sync.Mutex
	start         time.Time
	total         int
	done          int
	headerPrinted bool
	// rows is the number of terminal rows the live block occupies on screen; 0
	// until the first repaint. Wrapped lines count as multiple rows so a narrow
	// terminal is cleared fully.
	rows int
	stop chan struct{}
	wg   sync.WaitGroup
	// finished holds the providers the engine has moved past.
	finished []finishedProvider
	// current is the provider the engine is updating right now, or nil
	// before the first ProviderStart.
	current   *currentState
	finalized bool
}

// finishedProvider is a provider that is no longer active: the block shows
// its completed and failed packages once another provider is running.
type finishedProvider struct {
	name   string
	pkgs   []string
	failed []string
}

// currentState is the provider the engine is updating right now.
type currentState struct {
	provider provider.Provider
	// pkg is the package the engine passed to the provider's Update call.
	pkg string
	// activity is the stage the provider last reported through
	// provider.ActivityReporter; empty when it cannot report one.
	activity string
	// pkgs are the packages the engine has finished with under this provider.
	pkgs []string
	// failed are the packages whose update call returned an error.
	failed []string
}

// newUpdateDisplay builds the display. total is the number of updates the
// operation is expected to do; terminal says whether stdout is a character
// terminal, which decides in-place redraws versus printed summaries.
func newUpdateDisplay(w io.Writer, p color.Palette, total int, terminal bool) *updateDisplay {
	return &updateDisplay{w: w, p: p, terminal: terminal, total: total}
}

// Start begins the display and starts the activity poller.
func (d *updateDisplay) Start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.start = time.Now()
	d.renderLocked()
	d.stop = make(chan struct{})
	d.wg.Add(1)
	go d.pollLoop()
}

// ProviderStart records the engine starting a provider update.
func (d *updateDisplay) ProviderStart(pr provider.Provider, pkg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.providerStartLocked(pr, pkg)
	d.renderLocked()
}

// PackageDone records the engine finishing a package's update call. Failed
// updates count toward progress but are not listed as completed.
func (d *updateDisplay) PackageDone(pr provider.Provider, pkg string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.packageDoneLocked(pr, pkg, err)
	d.renderLocked()
}

// onEvent applies one engine progress event.
func (d *updateDisplay) onEvent(pr engine.Progress) {
	switch pr.Kind {
	case engine.ProgressProviderStart:
		d.ProviderStart(pr.Provider, pr.Package)
	case engine.ProgressPackageDone:
		d.PackageDone(pr.Provider, pr.Package, pr.Err)
	}
}

// providerStartLocked is ProviderStart without the render.
func (d *updateDisplay) providerStartLocked(pr provider.Provider, pkg string) {
	if d.current != nil && d.current.provider.Name() != pr.Name() {
		// Another provider is taking over: its summary joins the block's
		// list of finished providers.
		if !d.terminal {
			d.printSummaryLocked(d.current)
		}
		d.finished = append(d.finished, finishedProvider{name: d.current.provider.Name(), pkgs: d.current.pkgs, failed: d.current.failed})
		d.current = nil
	}
	if d.current == nil {
		d.current = &currentState{provider: pr, pkg: pkg}
		return
	}
	d.current.provider = pr
	d.current.pkg = pkg
	d.current.activity = ""
}

// packageDoneLocked is PackageDone without the render.
func (d *updateDisplay) packageDoneLocked(pr provider.Provider, pkg string, err error) {
	d.done++
	if d.current == nil || d.current.provider.Name() != pr.Name() {
		return
	}
	if err != nil {
		if !slices.Contains(d.current.failed, pkg) {
			d.current.failed = append(d.current.failed, pkg)
		}
		return
	}
	if !slices.Contains(d.current.pkgs, pkg) {
		d.current.pkgs = append(d.current.pkgs, pkg)
	}
}

// Finalize stops the display. In a terminal the final block stays on screen;
// without one, the last provider's summary is printed now.
func (d *updateDisplay) Finalize() {
	d.mu.Lock()
	if d.finalized {
		d.mu.Unlock()
		return
	}
	d.finalized = true
	if d.stop != nil {
		close(d.stop)
	}
	d.mu.Unlock()
	d.wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.terminal {
		d.repaintLocked()
		return
	}
	d.printSummaryLocked(d.current)
}

// printSummaryLocked prints a provider's completed and failed packages, the
// way the non-terminal display records a provider when it stops being the
// current one.
func (d *updateDisplay) printSummaryLocked(c *currentState) {
	if c == nil || (len(c.pkgs) == 0 && len(c.failed) == 0) {
		return
	}
	name := c.provider.Name()
	if len(c.pkgs) > 0 {
		fmt.Fprintf(d.w, "[%s] Completed:\n", name)
		for _, p := range c.pkgs {
			fmt.Fprintf(d.w, "  %s\n", p)
		}
	}
	if len(c.failed) > 0 {
		fmt.Fprintf(d.w, "[%s] Failed:\n", name)
		for _, p := range c.failed {
			fmt.Fprintf(d.w, "  %s\n", p)
		}
	}
	if len(c.pkgs) == 0 && len(c.failed) == 0 {
		fmt.Fprintf(d.w, "[%s] (no packages completed)\n", name)
	}
	fmt.Fprintln(d.w)
}

// pollLoop polls the active provider for its current activity every 250ms
// while it updates, re-rendering when it reports one.
func (d *updateDisplay) pollLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-d.stop:
			return
		case <-ticker.C:
			d.mu.Lock()
			d.pollOnceLocked()
			d.mu.Unlock()
		}
	}
}

// pollOnceLocked asks the active provider for its current activity and
// re-renders the terminal block so elapsed time keeps ticking. Non-terminal
// displays have no live block to refresh.
func (d *updateDisplay) pollOnceLocked() {
	if d.current == nil {
		return
	}
	if ar, ok := d.current.provider.(provider.ActivityReporter); ok {
		if act := ar.CurrentActivity(); act != "" {
			d.current.activity = act
		}
	}
	if d.terminal {
		d.renderLocked()
	}
}

// renderLocked redraws: in a terminal, in place; otherwise, prints the header
// once.
func (d *updateDisplay) renderLocked() {
	if d.terminal {
		d.repaintLocked()
		return
	}
	if !d.headerPrinted {
		d.headerPrinted = true
		fmt.Fprintln(d.w, d.p.Bold("Updating packages across providers..."))
	}
}

// repaintLocked redraws the block in place: the cursor sits just after the
// last row of the previous block, so it moves up one row per terminal row
// and erases to the end of the screen. Wrapped lines count as multiple rows.
func (d *updateDisplay) repaintLocked() {
	lines := d.blockLines()
	width := d.termWidth()
	if d.rows > 0 {
		fmt.Fprintf(d.w, "\x1b[%dA\x1b[J", d.rows)
	}
	for _, l := range lines {
		fmt.Fprintln(d.w, l)
	}
	d.rows = blockRows(lines, width)
}

func (d *updateDisplay) termWidth() int {
	f, ok := d.w.(*os.File)
	if !ok {
		return 0
	}
	w, _, err := term.GetSize(int(f.Fd()))
	if err != nil || w <= 0 {
		return 0
	}
	return w
}

// blockRows counts the terminal rows a block occupies. When width is unknown,
// each line is one row; otherwise wrapped lines count as multiple rows.
func blockRows(lines []string, width int) int {
	if width <= 0 {
		return len(lines)
	}
	rows := 0
	for _, l := range lines {
		n := color.VisibleLen(l)
		if n == 0 {
			rows++
			continue
		}
		rows += (n + width - 1) / width
	}
	return rows
}

// blockLines builds the block: header, the finished providers' summaries,
// the active provider and its current package or activity, elapsed time and
// progress.
func (d *updateDisplay) blockLines() []string {
	var lines []string
	lines = append(lines, d.p.Bold("Updating packages across providers..."), "")
	for _, f := range d.finished {
		if len(f.pkgs) == 0 && len(f.failed) == 0 {
			continue
		}
		if len(f.pkgs) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] Completed:", f.name))
			lines = append(lines, indentedPkgs(f.pkgs)...)
		}
		if len(f.failed) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] Failed:", f.name))
			lines = append(lines, indentedPkgs(f.failed)...)
		}
		if len(f.pkgs) == 0 && len(f.failed) == 0 {
			lines = append(lines, fmt.Sprintf("[%s] (no packages completed)", f.name))
		}
		lines = append(lines, "")
	}
	if d.current != nil {
		name := d.current.provider.Name()
		if len(d.current.pkgs) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] Completed:", name))
			lines = append(lines, indentedPkgs(d.current.pkgs)...)
			lines = append(lines, "")
		}
		if len(d.current.failed) > 0 {
			lines = append(lines, fmt.Sprintf("[%s] Failed:", name))
			lines = append(lines, indentedPkgs(d.current.failed)...)
			lines = append(lines, "")
		}
		switch {
		case d.current.activity != "":
			lines = append(lines, fmt.Sprintf("[%s] Currently: %s", name, d.current.activity))
		case d.current.pkg != "":
			lines = append(lines, fmt.Sprintf("[%s] Currently: %s", name, d.current.pkg))
		default:
			lines = append(lines, "Waiting for "+name+"...")
		}
		lines = append(lines, "")
	}
	lines = append(lines, d.p.Dim("Time elapsed: "+elapsedString(time.Since(d.start))))
	lines = append(lines, d.p.Dim("Progress: "+progressBar(d.done, d.total)))
	return lines
}

func indentedPkgs(pkgs []string) []string {
	out := make([]string, 0, len(pkgs))
	for _, p := range pkgs {
		out = append(out, "  "+p)
	}
	return out
}

// elapsedString renders d as "12m 34s" style text: hours, then minutes, then
// seconds, without leading zeros or empty components.
func elapsedString(d time.Duration) string {
	d = d.Truncate(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", int(h), int(m), int(s))
	case m > 0:
		return fmt.Sprintf("%dm %ds", int(m), int(s))
	default:
		return fmt.Sprintf("%ds", int(s))
	}
}

// progressBar renders the progress of completed work as a bar and a percent,
// e.g. "[====------------------------] 27%".
func progressBar(done, total int) string {
	const width = 40
	if total <= 0 {
		total = 1
	}
	if done < 0 {
		done = 0
	}
	if done > total {
		done = total
	}
	filled := done * width / total
	return fmt.Sprintf("[%s] %d%%",
		strings.Repeat("=", filled)+strings.Repeat("-", width-filled),
		done*100/total)
}
