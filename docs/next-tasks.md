# Next tasks

Recorded, not implemented. Each entry is scoped so it can become a Linear issue.

## Core

1. **Concurrency protection** — prevent two Starpoint processes from modifying the same provider or transition simultaneously. Fail clearly when a conflicting operation is already running.

2. **`starpoint prune`** — explicitly remove orphaned software installed by Starpoint-managed providers. Refuse to prune software owned by system- or global-scoped providers.

3. **Transition history and reversal** — record the previous owner and enough context to support `starpoint transition --reverse`. Keep transition phases derived from the machine so interrupted transitions remain recoverable. <--- too hard to do?

4. **Detect local binaries** — discover executables available on `PATH` that are not accounted for by known provider inventories. Report their paths and whether ownership is unknown, rather than incorrectly reporting them as missing or assuming they are unmanaged. Account for symlinks and multiple executables with the same name.

5. **Executable-to-package mapping** — let providers declare which executables their packages supply. Use this alongside inventory and PATH comparisons to resolve ownership accurately.

6. **Project toolchain declarations** — read existing project declarations such as `rust-toolchain.toml` and `go.mod`. Define how these declarations interact with machine-wide toolchain configuration before implementing automatic changes.

## CLI and API

7. **`starpoint explain <package>`** — show the provider-resolution reasoning chain, including the rules considered and why the selected provider won.

8. **Stable JSON schema** — document and version the JSON output already emitted by Starpoint, including transition records.

9. **Shell completions** — provide completions for bash, zsh and fish.

10. **Comment-preserving configuration writes** — stop provider decisions from destroying hand-written TOML comments. Prefer a generated region Starpoint can rewrite while preserving human-maintained configuration.

## Providers

11. **Linux package managers** — add dnf, pacman and other necessary Linux providers. Match existing apt behavior while accounting for each manager's package classification and install flags.

12. **Provider self-verification** — optionally verify an installation by running the installed executable, such as `ruff --version`, in addition to checking provider inventory and PATH.

## Design decisions

- **Project configuration semantics** — decide whether a project-local `.starpoint.toml` can declare an exact, self-contained package set or only override global entries.
- **Project-scoped installation** — define what `scope = "project"` means operationally before expanding support for project-local installations.
- **Adoption during sync** — decide whether `sync` may adopt software that already exists without an explicit `starpoint add`. Preserve explicit adoption unless there's a compelling reason to change it.
- **Ownership hints** — document when a known-owner hint takes precedence over other providers, particularly when package names overlap across ecosystems.

## Deliberately deferred

- **Bun and Deno providers** — reconsider when their installed software can be inventoried reliably enough for `list` and `info`.
- **Nix support** — defer until Starpoint has a clear integration model that does not turn it into a Nix clone.
- **mise and asdf** — defer until version-manager ownership and placement have a defined model.
- **Version constraints** — keep exact version matching until there is a concrete need for ranges. Constraint parsing should remain provider-specific.
- **Inventory-pass consolidation** — revisit if profiling shows the duplicate provider survey is a meaningful performance problem.