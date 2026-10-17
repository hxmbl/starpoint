# Next tasks

This file tracks work that remains after the current implementation. Completed items are recorded in the completion summary so this list stays actionable.

## Remaining core work

1. **Recover interrupted package-manager operations.** Starpoint currently detects and safely cleans stale config-writer temporary files. Provider operations are still recovered by re-inspecting inventories and PATH when the command is rerun; Starpoint does not yet persist a journal for every provider subprocess. Add explicit operation records only where provider state alone cannot safely identify progress.
2. **Expand executable-to-package mappings.** The locator supports provider mappings and uses them for ownership attribution, but only a subset of providers maps executable names back to package names. Add mappings as provider inventory semantics are confirmed, especially for multi-binary packages.
3. **Improve project-context coverage.** Project detection reads Rust and Go toolchain declarations and contributes project ecosystem context to provider resolution. `install .` runs the ecosystem's own dependency installer (npm/pnpm/yarn, uv/pip, cargo, go, bundler) chosen from lockfiles; it never mutates project manifests. Add ecosystems with clear ownership and lockfile behavior (Poetry, Pipenv, Bun, Composer), and consider honouring declared toolchain versions.

## Remaining CLI and API work

4. **Publish the JSON schema as a machine-readable artifact.** `docs/json-schema.md` documents schema version 2 and the command shapes. Generate or validate a formal JSON Schema document from the actual Go output types so external clients can validate payloads without duplicating the prose contract.
5. **Generate shell completions.** Bash, zsh, and fish completions are hand-written; a test (internal/cli/completions_test.go) checks them against the command, flag and provider tables. Generating them from those tables would remove the duplication. The zsh and fish files have not been exercised in a real shell in CI.
6. **Improve PATH inventory controls.** `doctor` summarises the PATH inventory (counts, shadowed names, unknown owners; `--all` for everything) and PATH directories are de-duplicated by real path. `list` still prints every unaccounted executable; consider the same summary there and richer filters (by directory or owner) for very large PATHs, keeping the full data in machine output.

7. **One action vocabulary in JSON output.** `sync`, `update` and `prune` report `actions` as `ActionResult` (`{action, package, provider, note, result}`), while `install`, `remove` and `install .` report `operations[].applied` as the engine's `PlannedAction` (`{action, state, ...}`). The latter nests the whole `State`, so it is the richer shape; the former is the one the schema originally described for every command. Pick one and apply it everywhere, which is a breaking change and needs a schema version increment.

## Provider coverage

8. **Add more providers when ownership is reliable.** Bun, Deno, Nix, mise, asdf, Flatpak, Snap, winget, Chocolatey, and additional language providers remain candidates. Each provider should support useful inventory and ownership reporting before it is used for automatic resolution.
9. **Provider-specific version constraints.** Exact pins are supported where providers can honor them. Ranges and richer constraint syntax remain deferred until a concrete provider requires them; parsing must stay provider-specific.
10. **Inventory pass consolidation.** Planning and ownership reporting may query provider inventories more than once. Revisit if profiling shows this is a material latency problem.
11. **Newest-version lookups for more providers.** Pin conflicts are detected for pip, npm, pnpm, Homebrew, Cargo, RubyGems and APT. uv has no index query of its own; yarn, Go, DNF and Pacman are also unimplemented.

## Settled behavior

- Project configuration is additive to global configuration. A project entry with the same package name overrides the global entry; other global packages remain desired.
- `scope = "project"` is accepted as placement intent, but providers that cannot install into a project context report that limitation. Starpoint does not silently rewrite a project's manifest to manufacture support.
- `install .` records the toolchain a project needs (node, cargo, go, ...) in the project's own `.starpoint.toml`, creating it if necessary, and never installs a toolchain implicitly. Project dependencies are installed by the ecosystem's own tool after confirmation.
- `update` never moves a pin silently. A newer version than the pin is reported as a conflict and asked about; yes updates and rewrites the pin to the installed version, anything else (including `--no-confirm`) keeps it. `sync` still converges on the pin without asking.
- `sync` does not adopt an installation merely because it exists. `starpoint install` is the explicit adoption path and records the selected provider in the source of truth.
- An explicit provider in configuration wins. Otherwise an existing non-system owner is retained, known executable ownership mappings are considered, then ecosystem and project context guide a choice. Starpoint asks when multiple viable general providers remain ambiguous.
- Transition phases are reconstructed from provider inventory and executable resolution. Re-running a transition resumes based on observed machine state; the history store supports audit and explicit reversal.
- Removing every configured package in one command is refused without `--allow-empty`. Removing one package is ordinary and never refused.
- Prune removes only orphaned user-scoped software that a provider can enumerate and remove. It excludes system/global scope and configured package names. It refuses when the configuration declares no packages, because there is then no source of truth to tell managed software from orphans, and it lists its candidates in both the human and JSON forms of `--dry-run`.

## Completed in this implementation

- Crash recovery detection and safe cleanup for stale Starpoint TOML temporary files, with ambiguous or sole-copy data preserved. Config writes are flushed and renamed atomically, so an interrupted run cannot leave a half-written config; `starpoint recover` reports leftover files and `--discard` clears them.
- Per-config mutation locks around state-changing CLI operations.
- `starpoint prune`, with preview, confirmation, scope protections, and JSON output. It refuses when the configuration declares no packages, and its `--dry-run` lists the candidates in both human and JSON output.
- Transition history and `starpoint transition --reverse`.
- PATH executable inventory with symlink/duplicate reporting in `list` and `doctor`.
- Resolver explanation through `starpoint explain <package>`.
- Versioned JSON output envelope and command-shape documentation. Every command emits one flat object under schema version 2, so the documented shape and the emitted shape cannot drift.
- `info --json` emits a single document: the provider survey or a pending transition travels inside the same response instead of in a second JSON object.
- `prune` refuses to run when the configuration declares no packages, and `--dry-run` lists the candidates in both human and JSON output.
- Removed the vestigial `--record` flag, which was accepted but did nothing (recording is automatic), and the unused `internal/starpointcontext` package, which duplicated `internal/project`.
- An append-only audit log of every command and every change to the machine, in `~/.starpoint/logs/`, rotated daily. Failures and refusals are recorded, not just successes.
- Retention across every store — audit log, transition history, run history — applied automatically on every command, configurable with `retention_days` (default 30). `history.Prune` existed but nothing ever called it.
- `starpoint logging [prune|config]`, reporting and configuring retention, plus generation of the system's own rotation configuration: logrotate on Linux, newsyslog on macOS, nothing on Windows. Starpoint writes only inside its own state directory and prints the privileged install step instead of taking it.
- A guard on `remove`: removing every configured package in one command requires `--allow-empty`. A single removal is unaffected, and a dry run is still allowed to describe the change.
- All of Starpoint's records now live in a state directory derived from the global configuration path, so a run against another configuration — including the test suite — writes nothing into a real home directory.
- Bash, zsh, and fish completion scripts.
- TOML writes that preserve comments and unrelated file content while updating package entries.
- DNF and Pacman providers alongside the existing APT and Homebrew integrations. Neither can honour an arbitrary version pin, and both report that rather than claiming a capability they lack.
- Optional provider executable verification after install/update and during transitions.
- Rust `rust-toolchain` / `rust-toolchain.toml` and Go `go.mod` toolchain declaration detection.
