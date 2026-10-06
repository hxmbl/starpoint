# Next tasks

Recorded, not implemented. Each entry is scoped so it can become a Linear issue
without further design work.

## Providers

The compatibility target is the machine: every package manager installed on this
project's development host now has a provider. What follows is what is installed
here but deliberately has none, and why.

1. **Bun and Deno** — installed here, and deliberately not providers. Neither can
   be relied on to enumerate what it has installed: on this host `bun pm ls -g`
   fails because its global directory has no `package.json`, and Deno's global
   model is script/JSR-oriented rather than a package inventory. A provider that
   cannot answer "what do you own?" would make `starpoint list` and `starpoint info`
   report software as missing when it is present, which is worse than not
   supporting the ecosystem. If Bun or Deno gain a listable global inventory, add
   them the way `pnpm` was added.
2. **Docker and Podman** — installed here, and deliberately not providers. They
   manage containers, not software on the machine. If Starpoint ever grows
   container support it needs a new placement concept (`ScopeContainer`), not a
   provider.
3. **Nix** — not installed here, and deliberately last. Nix is a package
   *system*, not a package manager; pretending otherwise is how Starpoint would end
   up becoming a Nix clone.
4. **dnf / pacman / apt-adjacent Linux managers** — needed before Starpoint can
   claim to be cross-platform in practice. Same shape as apt: the classification
   and the `=version`/`--needed` handling are the only differences.
5. **winget / Chocolatey** — required for Windows, which is the other half of the
   cross-platform claim.
6. **Executable-to-package mapping** — Homebrew's `rust` formula provides `rustc`
   without having a package by that name, so a provider asked "do you have rustc"
   correctly answers no. Every ecosystem has this shape. Either give providers a way
   to declare the executables a package provides, or accept that ownership of a
   shadowing executable is only detectable by comparing paths — which `doctor` now
   does.
7. **mise / asdf** — version managers are a category Starpoint does not model.
   rustup was added as a provider rather than as a placement concept, which is a
   precedent but not a settled answer.

## Transitions

8. **Transition history** — a transition's phase is derived from the machine
   rather than remembered, which is what makes it recoverable with no journal. The
   cost is that Starpoint cannot say *when* something moved, or *who* asked. A
   small `[[transition]]` history section in the global config would answer both
   without reintroducing a state file that can drift.
9. **Rollback** — `starpoint transition --reverse` would move ownership back. The
   machinery is the same operation with the providers swapped, so it is mostly a
   command; what it needs is a decision about whether Starpoint should record the
   prior owner well enough to be able to.
10. **Deeper verification** — verification combines the provider's inventory with
    a `PATH` comparison. A third source of evidence would be running the software
    (`ruff --version`) and checking it answers. That needs an optional provider
    capability so only providers that can self-check pay for it.

## Core

11. **`starpoint prune`** — remove software that Starpoint-managed providers
    installed but which is no longer in any configuration file. Must be explicit
    and must refuse to touch `system`- or `global`-scoped providers. Today an
    orphaned uv tool is reported by `doctor` and left alone, which is safe but
    leaves cruft.
12. **Version constraint expressions** — `version` is currently an exact string.
    `>=1.2`, `^2.0` and `~1.4` are what people actually write. Parsing them
    belongs in the provider that owns the versioning scheme, not in the core.
13. **Concurrency between processes** — the concurrent *provider lookup* is
    internal and safe, but two `starpoint` processes can still race on the same
    provider, and now also on the same transition: both could install, verify and
    remove at once. Needs a lock file with the provider identity in it, and a clear
    error rather than a wait.
14. **One inventory pass** — `install` asks every provider for the survey and then
    asks them again, in precedence order, inside `findExisting`. The two are merged
    only on paper because `findExisting` encodes an order (managing providers
    before the read-only system provider) that a flat survey does not. Merging them
    for real means making the precedence explicit in the survey rather than
    implicit in a loop, and is worth roughly a second per command.
15. **Verification running the software** — see item 10; the survey already
    collects provider-reported versions, so the remaining piece is asking the
    software itself.
16. **Project toolchain declarations** — a Rust project declares a toolchain in
    `rust-toolchain.toml`, a Go project in `go.mod`, and Starpoint currently
    ignores all of them. Reading them is a project-context feature, not a provider
    feature, and it needs a decision about whether a project may pin the machine's
    toolchain.

## CLI and API

17. **`starpoint explain <package>`** — print the resolution reasoning chain: which
    rules were considered and why the winner won. `info` shows the answer,
    `transition` shows the argument for a move; this would cover provider choice.
18. **Stable JSON schema document** — the shapes are already emitted, and the
    transition record is the newest. They need a documented, versioned schema file
    before anyone depends on them.
19. **`starpoint completions`** — shell completion for bash, zsh and fish.
20. **Comment-preserving configuration writes** — Starpoint re-encodes the whole
    file when it records a provider decision, so hand-written comments are lost.
    Needs a position-preserving TOML editor, or an explicit `# starpoint:generated`
    region Starpoint may rewrite and a human region it never touches. The second is
    much cheaper and may be the right one.

## Design questions still open

- Should a project-local `.starpoint.toml` be able to *narrow* the global
  configuration (this package at this version, and nothing else), or only override
  individual entries? Right now it merges, which means a project cannot ask for
  "exactly this, nothing else".
- Is project-scoped software ever going to be real? Go, Rust and Node all support
  installing into a project-local location, and Starpoint currently treats
  `scope = "project"` as a label rather than a placement it can honour. Decide
  before more providers advertise `ProjectInstall`.
- Should Starpoint manage anything it did not install? Today adoption is explicit
  (`starpoint add` on something already present). Letting `sync` adopt silently
  would be convenient and would make the configuration stop meaning "what I asked
  for".
- How far does the "known owner" idea extend? The resolver trusts an owner hint
  over whichever provider happens to have a copy, which is right for `rustc` and
  would be wrong for a name shared by two unrelated ecosystems. The hint is derived
  from the owning provider's own vocabulary so the two cannot drift, but the table
  should stay small enough to review by eye.