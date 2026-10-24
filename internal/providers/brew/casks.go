package brew

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// caskReceipt is the part of a cask's install receipt
// (<caskroom>/<token>/.metadata/INSTALL_RECEIPT.json) that says why it is
// installed. Homebrew writes the receipt for casks installed by Homebrew
// 4.4.0 or later; casks installed earlier have none until they are
// reinstalled or upgraded. The booleans are kept raw: receipts in the wild
// hold null for them (Homebrew/brew#20960), which says nothing.
type caskReceipt struct {
	InstalledOnRequest    json.RawMessage `json:"installed_on_request"`
	InstalledAsDependency json.RawMessage `json:"installed_as_dependency"`
	RuntimeDependencies   struct {
		Cask []struct {
			FullName string `json:"full_name"`
		} `json:"cask"`
	} `json:"runtime_dependencies"`
}

// caskReceiptFile is where Homebrew keeps a cask's install receipt.
func caskReceiptFile(caskroom, token string) string {
	return filepath.Join(caskroom, token, ".metadata", "INSTALL_RECEIPT.json")
}

// rawBool reads a JSON true or false; anything else (absent, null, a string)
// is no answer.
func rawBool(raw json.RawMessage) (value, ok bool) {
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// reason is the receipt's answer: installed on request (true) or as a
// dependency (false). installed_on_request decides; early receipts also
// carry installed_as_dependency, which can say "dependency" on its own.
func (r caskReceipt) reason() (requested, ok bool) {
	if v, ok := rawBool(r.InstalledOnRequest); ok {
		return v, true
	}
	if dep, ok := rawBool(r.InstalledAsDependency); ok && dep {
		return false, true
	}
	return false, false
}

// caskToken turns a cask's full name ("user/tap/foo") into the token
// (`foo`) `brew list --cask` prints and the Caskroom is keyed by.
func caskToken(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

// caskReasons says why each installed cask is there: true for installed on
// request, false for installed as a dependency; a cask with no answer is
// left out, so it reads as unknown.
//
// The answer comes from the cask's install receipt. A cask with no receipt,
// or one that does not say, is a dependency when another installed cask
// depends on it (`depends_on cask:`, read from the other casks' receipts
// and, when that is not enough, from `brew info --json=v2 --installed
// --cask`); otherwise it stays unknown. It is never assumed to be requested:
// casks can be dependencies of other casks. `brew info --json` itself
// carries no install reason for casks, only the receipt does.
func (p *Provider) caskReasons(ctx context.Context, casks map[string]bool) map[string]bool {
	out := map[string]bool{}
	if len(casks) == 0 {
		return out
	}
	caskroom := ""
	if res, err := p.runner.Run(ctx, "brew", "--caskroom"); err == nil && !res.Failed() {
		caskroom = strings.TrimSpace(shell.FirstLine(res.Stdout))
	}
	dependedOn := map[string]bool{}
	var undecided []string
	for name := range casks {
		var receipt caskReceipt
		read := false
		if caskroom != "" {
			if data, err := os.ReadFile(caskReceiptFile(caskroom, name)); err == nil && json.Unmarshal(data, &receipt) == nil {
				read = true
			}
		}
		if read {
			for _, dep := range receipt.RuntimeDependencies.Cask {
				if t := caskToken(dep.FullName); t != "" && t != name {
					dependedOn[t] = true
				}
			}
			if v, ok := receipt.reason(); ok {
				out[name] = v
				continue
			}
		}
		undecided = append(undecided, name)
	}
	if len(undecided) == 0 {
		return out
	}
	needInfo := false
	for _, name := range undecided {
		if !dependedOn[name] {
			needInfo = true
			break
		}
	}
	if needInfo && ctx.Err() == nil {
		for dependent, deps := range p.installedCaskDependencies(ctx) {
			for _, dep := range deps {
				if t := caskToken(dep); t != "" && t != caskToken(dependent) {
					dependedOn[t] = true
				}
			}
		}
	}
	for _, name := range undecided {
		if dependedOn[name] {
			out[name] = false
		}
	}
	return out
}

// installedCaskDependencies reads each installed cask's `depends_on cask:`
// list from `brew info --json=v2 --installed --cask`, keyed by token. A
// failure is no information, never an error: the casks it would have
// decided stay unknown.
func (p *Provider) installedCaskDependencies(ctx context.Context) map[string][]string {
	res, err := p.runner.Run(ctx, "brew", "info", "--json=v2", "--installed", "--cask")
	if err != nil || res.Failed() {
		return nil
	}
	var info struct {
		Casks []struct {
			Token     string `json:"token"`
			DependsOn struct {
				Cask json.RawMessage `json:"cask"`
			} `json:"depends_on"`
		} `json:"casks"`
	}
	if json.Unmarshal([]byte(res.Stdout), &info) != nil {
		return nil
	}
	out := map[string][]string{}
	for _, c := range info.Casks {
		var many []string
		var one string
		switch {
		case json.Unmarshal(c.DependsOn.Cask, &many) == nil:
		case json.Unmarshal(c.DependsOn.Cask, &one) == nil && one != "":
			many = []string{one}
		}
		if len(many) > 0 {
			out[c.Token] = many
		}
	}
	return out
}
