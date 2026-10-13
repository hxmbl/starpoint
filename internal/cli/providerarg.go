package cli

import (
	"fmt"
	"strings"

	"github.com/hxmbl/starpoint/internal/model"
)

// splitProviderArg pulls a bare provider name out of a command's positional
// arguments, at either end.
//
// `--provider brew` stays supported; this is the short form, and the trailing
// position reads best because the package name is the subject:
//
//	starpoint install ripgrep brew
//	starpoint install brew ripgrep
//
// The rules are deliberately narrow, because a token that names a provider is
// very often also the name of a package — `install brew` has to keep installing
// the software called brew.
//
//   - A single token is never a provider.
//   - Only the first or the last token is considered, never one in the middle,
//     where it is far more likely to be a package being operated on.
//   - A token carrying an inline version is never a provider, so
//     `install ripgrep@1.2.3` is a spec rather than a request to use a provider
//     called "ripgrep@1.2.3", and `install npm@10 ripgrep` keeps the version.
//   - Naming the provider twice is an error rather than a silent preference,
//     because guessing which one was meant is the kind of quiet decision this
//     tool exists to avoid.
func (e *Env) splitProviderArg(args []string, alreadyRestricted []string) (string, []string, error) {
	bare := bareTokens(args)

	if len(bare) < 2 {
		return "", args, nil
	}

	first, last := bare[0], bare[len(bare)-1]
	firstIsProvider := e.isProviderName(first)
	lastIsProvider := e.isProviderName(last)

	if firstIsProvider && lastIsProvider && first != last {
		return "", nil, fmt.Errorf(
			"%w: %q and %q are both providers; name one, or say --provider <name>",
			ErrUsage, first, last)
	}
	if firstIsProvider || lastIsProvider {
		named, index := last, len(bare)-1
		if firstIsProvider {
			named, index = first, 0
		}
		for _, given := range alreadyRestricted {
			if given == named {
				return "", nil, fmt.Errorf(
					"%w: %q is given twice, as --provider and as an argument; name the provider once",
					ErrUsage, named)
			}
		}
		return named, removeBareToken(args, named, index), nil
	}
	return "", args, nil
}

// isProviderName reports whether a token names a provider this build implements.
//
// The check is against the registry rather than a hardcoded list, so a provider
// that does not exist here is never mistaken for one that does, and adding a
// provider needs no change here.
func (e *Env) isProviderName(token string) bool {
	if token == "" || strings.HasPrefix(token, "-") {
		return false
	}
	// An inline version means this is a package spec, not a provider name.
	if model.HasExplicitVersion(token) {
		return false
	}
	_, ok := e.registry().Get(token)
	return ok
}

// valueTakingOptions consume the token after them, so that value is never
// mistaken for a package name or a provider.
var valueTakingOptions = map[string]bool{
	"--provider": true, "-p": true,
	"--version": true, "-V": true,
	"--channel": true,
	"--config":  true, "-c": true,
}

// bareTokens returns the positional arguments: everything that is not an option
// and not the value of one.
func bareTokens(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			if valueTakingOptions[arg] {
				i++
			}
			continue
		}
		out = append(out, arg)
	}
	return out
}

// removeBareToken deletes one positional occurrence of token — the nth among the
// bare tokens — while leaving every other argument, flags and values included,
// exactly where it was.
//
// It walks the arguments the same way bareTokens does, including the values that
// belong to options, because the index it is given counts bare tokens and nothing
// else. Counting anything else here would silently remove the wrong argument.
func removeBareToken(args []string, token string, bareIndex int) []string {
	out := make([]string, 0, len(args))
	bare := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			out = append(out, arg)
			if valueTakingOptions[arg] && i+1 < len(args) {
				i++
				out = append(out, args[i])
			}
			continue
		}
		if bare == bareIndex && arg == token {
			bare++
			continue
		}
		bare++
		out = append(out, arg)
	}
	return out
}
