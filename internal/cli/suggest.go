package cli

import (
	"fmt"
	"sort"
	"strings"
)

// suggestCommand returns a "did you mean ...?" hint for an unknown command, or
// "" when nothing is close enough. It considers every accepted spelling so a
// typo like "instal" suggests "install" rather than nothing. Flag-like inputs
// are matched against known flags so a mistyped global flag does not get a
// command suggestion.
func suggestCommand(cmd string) string {
	if strings.HasPrefix(cmd, "-") {
		if s := suggestFlag(cmd, ""); s != "" {
			return fmt.Sprintf("\nDid you mean %q?", s)
		}
		return ""
	}
	best := closest(cmd, commandNames())
	if best == "" {
		return ""
	}
	return fmt.Sprintf("\nDid you mean %q?", best)
}

// suggestOption returns a "did you mean ...?" hint for an unknown option, or ""
// when nothing is close enough. cmd is the canonical command name (or empty for
// global-only context).
func suggestOption(opt, cmd string) string {
	best := suggestFlag(opt, cmd)
	if best == "" {
		return ""
	}
	return fmt.Sprintf(" Did you mean %q?", best)
}

// suggestFlag finds the closest known flag for opt. It checks global flags and,
// when cmd is a known command, that command's own flags.
func suggestFlag(opt, cmd string) string {
	candidates := make([]string, 0, len(globalFlags)+8)
	seen := make(map[string]bool, len(globalFlags)+8)
	add := func(flags []string) {
		for _, f := range flags {
			if !seen[f] {
				seen[f] = true
				candidates = append(candidates, f)
			}
		}
	}
	add(globalFlags)
	add([]string{"-h", "-v", "-f", "-p", "-n", "-y", "-V"})
	if c := canonicalCommand(cmd); c != "" {
		add(commandFlags[c])
	}
	return closest(opt, candidates)
}

// commandNames returns the accepted command spellings in a stable order,
// canonical names first so the suggestion prefers the name users see in help.
func commandNames() []string {
	seen := make(map[string]bool, len(aliases))
	names := make([]string, 0, len(aliases))
	for _, c := range commandHelp {
		_ = c
	}
	// Canonical names from commandHelp first.
	canon := make([]string, 0, len(commandHelp))
	for name := range commandHelp {
		canon = append(canon, name)
	}
	sort.Strings(canon)
	for _, name := range canon {
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	// Then aliases that aren't canonical names.
	alias := make([]string, 0, len(aliases))
	for name := range aliases {
		if !seen[name] {
			seen[name] = true
			alias = append(alias, name)
		}
	}
	sort.Strings(alias)
	names = append(names, alias...)
	return names
}

// closest returns the candidate within edit-distance threshold of input, or "".
// On ties, the earlier candidate in the slice wins, so callers control
// preference by ordering.
func closest(input string, candidates []string) string {
	if input == "" {
		return ""
	}
	best := ""
	bestDist := suggestionThreshold(input)
	for _, c := range candidates {
		d := levenshtein(strings.ToLower(input), strings.ToLower(c))
		if d < bestDist {
			bestDist = d
			best = c
		}
	}
	return best
}

// suggestionThreshold returns the maximum distance worth suggesting. Short
// inputs get a tight threshold to avoid silly matches; longer ones allow a
// couple of edits.
func suggestionThreshold(s string) int {
	n := len(s)
	switch {
	case n <= 2:
		return 1
	case n <= 4:
		return 2
	case n <= 8:
		return 3
	default:
		return 4
	}
}

// usageSuggestion returns a "did you mean ...?" suffix for a usage error, or
// "". It only acts on unknown-option errors; other usage errors are left alone.
func usageSuggestion(err error, cmd string) string {
	msg := err.Error()
	const marker = "unknown option "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	if len(rest) < 2 || rest[0] != '"' {
		return ""
	}
	end := strings.IndexByte(rest[1:], '"')
	if end < 0 {
		return ""
	}
	opt := rest[1 : 1+end]
	return suggestOption(opt, cmd)
}

// levenshtein returns the edit distance between a and b.
func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	// Two-row DP; strings are short (command/flag names).
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = del
			if ins < cur[j] {
				cur[j] = ins
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
