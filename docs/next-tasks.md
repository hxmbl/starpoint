# Next tasks

This file tracks work that remains after the current implementation. Completed items are recorded in the completion summary so this list stays actionable.

## Remaining core work

1. **Recover interrupted package-manager operations.** Starpoint currently detects and safely cleans stale config-writer temporary files. Provider operations are still recovered by re-inspecting inventories and PATH when the command is rerun; Starpoint does not yet persist a journal for every provider subprocess. Add explicit operation records only where provider state alone cannot safely identify progress.
2. **Expand executable-to-package mappings.** The locator supports provider mappings and uses them for ownership attribution, but only a subset of providers maps executable names back to package names. Add mappings as provider inventory semantics are confirmed, especially for multi-binary packages.
3. **Improve project-context coverage.** Project detection reads Rust and Go toolchain declarations and contributes project ecosystem context to provider resolution. `install .` runs the ecosystem's own dependency installer (npm/pnpm/yarn, uv/pip, cargo, go, bundler) chosen from lockfiles; it never mutates project manifests. Add ecosystems with clear ownership and lockfile behavior (Poetry, Pipenv, Bun, Composer), and consider honouring declared toolchain versions.

## Remaining CLI and API work

4. **Publish the JSON schema as a machine-readable artifact.** `docs/json-schema.md` documents schema version 1 and the command shapes. Generate or validate a formal JSON Schema document from the actual Go output types so external clients can validate payloads without duplicating the prose contract.
5. **Expose shell completion installation guidance.** Bash, zsh, and fish completion scripts exist under `completions/`. Document installation paths and keep completions aligned with command aliases and flags.
6. **Improve PATH inventory controls.** `doctor` summarises the PATH inventory (counts, shadowed names, unknown owners; `--all` for everything) and PATH directories are de-duplicated by real path. `list` still prints every unaccounted executable; consider the same summary there and richer filters (by directory or owner) for very large PATHs, keeping the full data in machine output.

## Provider coverage

7. **Add more providers when ownership is reliable.** Bun, Deno, Nix, mise, asdf, Flatpak, Snap, winget, Chocolatey, and additional language providers remain candidates. Each provider should support useful inventory and ownership reporting before it is used for automatic resolution.
8. **Provider-specific version constraints.** Exact pins are supported where providers can honor them. Ranges and richer constraint syntax remain deferred until a concrete provider requires them; parsing must stay provider-specific.
9. **Inventory pass consolidation.** Planning and ownership reporting may query provider inventories more than once. Revisit if profiling shows this is a material latency problem.
10. **Newest-version lookups for more providers.** Pin conflicts are detected for pip, npm, pnpm, Homebrew, Cargo, RubyGems and APT. uv has no index query of its own; yarn, Go, DNF and Pacman are also unimplemented.

## Settled behavior

- Project configuration is additive to global configuration. A project entry with the same package name overrides the global entry; other global packages remain desired.
- `scope = "project"` is accepted as placement intent, but providers that cannot install into a project context report that limitation. Starpoint does not silently rewrite a project's manifest to manufacture support.
- `install .` records the toolchain a project needs (node, cargo, go, ...) in the project's own `.starpoint.toml`, creating it if necessary, and never installs a toolchain implicitly. Project dependencies are installed by the ecosystem's own tool after confirmation.
- `update` never moves a pin silently. A newer version than the pin is reported as a conflict and asked about; yes updates and rewrites the pin to the installed version, anything else (including `--no-confirm`) keeps it. `sync` still converges on the pin without asking.
- `sync` does not adopt an installation merely because it exists. `starpoint install` is the explicit adoption path and records the selected provider in the source of truth.
- An explicit provider in configuration wins. Otherwise an existing non-system owner is retained, known executable ownership mappings are considered, then ecosystem and project context guide a choice. Starpoint asks when multiple viable general providers remain ambiguous.
- Transition phases are reconstructed from provider inventory and executable resolution. Re-running a transition resumes based on observed machine state; the history store supports audit and explicit reversal.
- Prune removes only orphaned user-scoped software that a provider can enumerate and remove. It excludes system/global scope and configured package names.

## Completed in this implementation

- Crash recovery detection and safe cleanup for stale Starpoint TOML temporary files, with ambiguous or sole-copy data preserved. Config writes are flushed and renamed atomically, so an interrupted run cannot leave a half-written config; `starpoint recover` reports leftover files and `--discard` clears them.
- Per-config mutation locks around state-changing CLI operations.
- `starpoint prune`, with preview, confirmation, scope protections, and JSON output.
- Transition history and `starpoint transition --reverse`.
- PATH executable inventory with symlink/duplicate reporting in `list` and `doctor`.
- Resolver explanation through `starpoint explain <package>`.
- Versioned JSON output envelope and command-shape documentation.
- Bash, zsh, and fish completion scripts.
- TOML writes that preserve comments and unrelated file content while updating package entries.
- DNF and Pacman providers alongside the existing APT and Homebrew integrations. Neither can honour an arbitrary version pin, and both report that rather than claiming a capability they lack.
- Optional provider executable verification after install/update and during transitions.
- Rust `rust-toolchain` / `rust-toolchain.toml` and Go `go.mod` toolchain declaration detection.
