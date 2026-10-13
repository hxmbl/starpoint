package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/providers"
)

func readCompletion(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "completions", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The completions are hand-written for three shells; this keeps them honest
// about every command spelling, every flag and every provider name.
func TestCompletionsCoverEveryCommandFlagAndProvider(t *testing.T) {
	var spellings []string
	for spelling := range aliases {
		spellings = append(spellings, spelling)
	}
	sort.Strings(spellings)
	var names []string
	for _, p := range providers.DefaultRegistry().All() {
		names = append(names, p.Name())
	}

	for _, file := range []string{"starpoint.bash", "starpoint.zsh", "starpoint.fish"} {
		body := readCompletion(t, file)
		words := map[string]bool{}
		for _, w := range regexp.MustCompile(`[A-Za-z0-9.-]+`).FindAllString(body, -1) {
			words[w] = true
		}
		has := func(token string) bool {
			if strings.HasPrefix(token, "--") && strings.HasSuffix(file, ".fish") {
				return strings.Contains(body, "-l "+strings.TrimPrefix(token, "--"))
			}
			return words[token]
		}
		for _, s := range spellings {
			if !has(s) {
				t.Errorf("%s does not complete the command %q", file, s)
			}
		}
		for cmd, flags := range commandFlags {
			for _, f := range flags {
				if !has(f) {
					t.Errorf("%s does not complete %s for %s", file, f, cmd)
				}
			}
		}
		for _, f := range globalFlags {
			if !has(f) {
				t.Errorf("%s does not complete the global %s", file, f)
			}
		}
		for _, n := range names {
			if !has(n) {
				t.Errorf("%s does not complete the provider %q", file, n)
			}
		}
		for _, stale := range []string{" golang", " shell "} {
			if strings.Contains(body, stale) {
				t.Errorf("%s names %q, which is not a provider", file, strings.TrimSpace(stale))
			}
		}
	}
}

// Every command's own help lists every flag it accepts.
func TestCommandHelpListsEveryFlag(t *testing.T) {
	for cmd, flags := range commandFlags {
		for _, f := range flags {
			if !strings.Contains(commandHelp[cmd], f) {
				t.Errorf("help for %s does not mention %s", cmd, f)
			}
		}
	}
}

// The general usage names the things the audit found missing.
func TestUsageListsCommandsAliasesAndKeyOptions(t *testing.T) {
	var b strings.Builder
	printUsage(&b)
	usage := b.String()
	for _, want := range []string{
		"explain", "add", "uninstall", "install .", "--dry-run", "--no-confirm",
		"--self", "prune", "recover [--discard]", "doctor [--all]", "upgrade", "ls", "show", "move", "history",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage does not mention %q", want)
		}
	}
}
