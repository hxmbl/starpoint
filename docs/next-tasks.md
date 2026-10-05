# Next tasks

Recorded, not implemented. Each entry is scoped so it can become a Linear issue
without further design work.

## Providers

1. **rustup** — toolchains are a real ownership question: `rustc` is on PATH from
   rustup, not from `cargo`, and Starpoint currently reports it as missing
   because no provider claims it. This is the single most visible gap on a Rust
   machine today. It needs `rustup toolchain list/install/uninstall`, toolchain
   channels (stable/beta/nightly), and path resolution via `rustup which`.
2. **dnf / pacman** — same shape as apt; the classification and the
   `=version`/`--needed` handling are the only differences.
3. **RubyGems** — needed for `Gemfile` projects; the locator already attributes
   `~/.local/share/gem` to `gem` before the provider exists.
4. **pnpm / Yarn** — distinct global-store layouts and distinct lockfile
   semantics. Do not implement them as npm aliases.
5. **winget / Chocolatey** — required before Starpoint can claim to be
   cross-platform in practice; the macOS and Linux paths are done.
6. **Mise / asdf** — version managers are a category Starpoint does not yet
   model. Decide whether they are providers or a placement concept first.
7. **Flatpak / Snap** — application-level installs, where "installed" and
   "running" differ. Needs a decision about what `list` should say.
8. **Nix** — deliberately last. Nix is a package *system*, not a package
   manager; pretending otherwise is how Starpoint would end up becoming a Nix
   clone.

## Core

9. **`starpoint prune`** — remove software that Starpoint-managed providers
   installed but which is no longer in any configuration file. Must be explicit
   and must refuse to touch `system`- or `global`-scoped providers. Today an
   orphaned uv tool is reported by `doctor` and left alone, which is safe but
   leaves cruft.
10. **Ownership history** — when Starpoint removes an installation during a
    relocation, the fact that it did so is worth remembering. A small
    `[[history]]` section, or an OS-level log depending on which survives
    review.
11. **Version constraint expressions** — `version` is currently an exact string.
    `>=1.2`, `^2.0` and `~1.4` are what people actually write. Parsing them
    belongs in the provider that owns the versioning scheme, not in the core.
12. **Concurrency** — two `starpoint sync` runs can race on the same provider.
    Needs a lock file with the provider identity in it, and a clear error rather
    than a wait.

## CLI and API

13. **`starpoint explain <package>`** — print the resolution reasoning chain:
    which rules were considered and why the winner won. `info` shows the answer;
    this would show the argument.
14. **Stable JSON schema document** — the shapes are already emitted; they need a
    documented, versioned schema file before anyone depends on them.
15. **`starpoint completions`** — shell completion for bash, zsh and fish.
16. **Comment-preserving configuration writes** — Starpoint currently re-encodes
    the whole file when it records a provider decision, so hand-written comments
    are lost. Needs a position-preserving TOML editor (or an explicit
    `# starpoint:generated` region that Starpoint is allowed to rewrite and a
    human region it never touches). The second option is much cheaper and may be
    the right one.

## Design questions still open

- Should a project-local `.starpoint.toml` be able to *narrow* the global
  configuration (this package at this version, and nothing else), or only
  override individual entries? Right now it merges, which means a project cannot
  ask for "exactly this, nothing else".
- Is project-scoped software ever going to be real? Go, Rust and Node all support
  installing into a project-local location, and Starpoint currently treats
  `scope = "project"` as a label rather than a placement it can honour. Decide
  before more providers advertise `ProjectInstall`.
- Should Starpoint manage anything it did not install? Today adoption is
  explicit (`starpoint add` on something already present). Letting `sync`
  adopt silently would be convenient and would make the configuration stop
  meaning "what I asked for".