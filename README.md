# Starpoint

Starpoint is a declarative orchestration layer over the package managers and
developer tooling already on your machine.

It is not another package manager. Homebrew, APT, uv, Cargo, npm and Go still do
the actual work of installing software. Starpoint decides *who* should own a
piece of software, records that decision, and makes the machine converge on the
configuration you wrote.

```
config → desired state → provider resolution → current state → diff → provider operations
```

## Quick start

```sh
go build -o starpoint ./cmd/starpoint

./starpoint init                  # create ~/.starpoint.toml
./starpoint install ruff          # pick a provider, install, record ownership
./starpoint list                  # what is installed, where, and who owns it
./starpoint info ruff             # the full picture for one package
./starpoint sync --dry-run        # what would change
./starpoint sync                  # converge
./starpoint doctor                # inspect everything
```

## Configuration

`~/.starpoint.toml` is the source of truth. Starpoint writes to it as part of
normal operations: installing adds an entry, removing deletes one, and provider
resolution persists its decision. It is not a generated cache.

```toml
schema = 1

[[package]]
name = "ripgrep"

[[package]]
name = "ruff"
version = "0.6.0"        # optional hard pin
provider = "uv"          # optional; written by Starpoint, or set it yourself
scope = "user"           # global | user | project
channel = "cask"         # optional provider-specific hint (brew cask, npm tag)
reason = "…"             # why this provider, written by Starpoint
```

A project may also have its own `.starpoint.toml`. It applies when you run
Starpoint inside that directory and takes precedence over the global file for
the packages it declares.

### What Starpoint writes

`provider` is the ownership decision. Once written it is authoritative: `sync`
converges on it instead of re-deciding. If you deliberately move software to a
different provider by editing that field, Starpoint understands it as a
transition — it installs the new copy, verifies it, and only then removes the
old one. It never installs a second copy and leaves the first behind.

`version` is a hard pin. Starpoint will not move it. If a provider cannot deliver
it, or an update would contradict it, Starpoint reports the conflict instead of
editing the configuration to make the problem go away.

## Commands

| Command | Purpose |
| --- | --- |
| `starpoint init [--force]` | Create `~/.starpoint.toml` |
| `starpoint sync [--dry-run] [--no-confirm]` | Converge the machine onto the configuration |
| `starpoint doctor` | Inspect Starpoint, config, providers, ownership, inconsistencies |
| `starpoint list [--json]` | Show managed software, its location, scope and provider |
| `starpoint info <package>` | Show one package's state in detail |
| `starpoint install <package>` | Install and record (alias: `add`) |
| `starpoint install .` | Handle the current project |
| `starpoint remove <package>` | Uninstall and delete from config (alias: `uninstall`) |
| `starpoint update [--self]` | Update managed software, respecting pins |

Global options work anywhere on the line: `--json`, `--provider <name>`
(repeatable, restricts automatic resolution), `--force`, `--version`, `--help`.

### Output

Human output is the default; `--json` produces the same structured data the
engine produced, not a re-parse of the text. Every read command supports it, so
another tool never has to parse a table.

## Providers

Implemented in this release:

| Provider | Scope | Notes |
| --- | --- | --- |
| `system` | global | Read-only. Attributes `/usr/bin`-style software to the OS and refuses to mutate it. |
| `brew` | user | Formulae and casks. Cannot pin an arbitrary version, and says so. |
| `apt` | global | dpkg inventory, `pkg=version` pins, archive channel. |
| `uv` | user | Python tools and interpreters, `==` pins. |
| `rustup` | user | Rust toolchains: `rustc`, `cargo`, `rustfmt` and friends, plus the toolchains themselves. |
| `cargo` | user | Crates from `cargo install`, `--version` pins. |
| `npm` | user | Global packages, `name@version` and dist-tags. |
| `go` | user | `go install` binaries discovered in the Go bin directory. |

Each provider reports its own capabilities and its own typed errors. The core
asks what a provider can do rather than branching on provider names, so adding
an ecosystem does not mean touching the engine.

### rustup and Cargo are different providers

`cargo install` adds crates. `rustup toolchain install` provides the compiler.
On a rustup machine `rustc` and `cargo` are rustup's, not Cargo's, and Starpoint
says so:

```toml
[[package]]
name = "rustc"          # owned by rustup, resolved through the default toolchain

[[package]]
name = "stable"         # the toolchain itself; version = "1.99.0" pins a release
```

`starpoint install rustc` installs the default channel. `starpoint remove rustc`
is refused: it names the toolchain that provides it, and removing the default or
active toolchain would leave no Rust compiler. Remove the toolchain by name
instead, and only after choosing another default.

A bare version is not a package name, so `starpoint install 1.50.0` is rejected
with the two forms that do work: `starpoint install stable --version 1.50.0`, or
`--channel nightly`.

## Architecture

```
cmd/starpoint            entry point
internal/cli             argument parsing, confirmation, output selection
internal/report          human and JSON rendering
internal/engine          desired vs current comparison, plan, apply, add/remove/update
internal/resolve         which provider should own this package
internal/locator         what is on this machine and which provider put it there
internal/config          TOML schema, loading, merging, writing
internal/model           the core vocabulary: desired, current, status, plan
internal/provider        the integration contract, capabilities, typed errors
internal/providers/…     one package per ecosystem
internal/project         ecosystem and project-context detection
internal/exec            command execution, behind an interface so tests never shell out
internal/testsupport     scripted runner and fake providers
```

The core is deliberately boring: it parses configuration, models desired state,
detects current state, resolves providers, computes changes, executes provider
operations, persists resolved state, and exposes structured data. It contains no
package-manager knowledge beyond what lives in `resolve`.

## Development

```sh
make check     # fmt check, vet, and the full test suite
make build     # build ./bin/starpoint
make test      # go test ./...
make fmt       # gofmt -w
```

Tests never modify the machine: providers talk to an interface, and the test
suite substitutes a scripted command runner and in-memory providers.

## Deliberately not implemented

- HTTP API. The internal model is already machine-readable and `--json` exposes
  it. A server can be added later without reshaping anything.
- GUI.
- Most of the ecosystem list. dnf, pacman, nix, winget, choco, RubyGems, Composer,
  Maven/Gradle, NuGet, SwiftPM, CocoaPods, vcpkg, conda, Flatpak and Snap are all
  plausible and none are stubbed out; they are simply not written yet. See
  `docs/next-tasks.md`.
- A universal project dependency system. `starpoint install .` works from a
  narrow, documented rule set rather than pretending one command can understand
  every ecosystem.
- Comment-preserving configuration writes. Starpoint rewrites the file when it
  records a provider decision, and a TOML round trip cannot keep comments. This
  is why `starpoint init` prints the documentation instead of embedding it in a
  file that the first `install` would strip.

## Known limitations

- `scope = "project"` is recorded and displayed but not yet a placement Starpoint
  can install into. No provider advertises `ProjectInstall`.
- Cross-provider ownership requires the filesystem. A package whose binary name
  differs from its package name and which no provider tracks is reported as
  present but unattributed rather than guessed at. Homebrew's `rust` formula
  provides a `rustc` executable but has no package by that name, so Starpoint
  cannot learn from Homebrew that a second `rustc` exists — it reports rustup as
  the owner and, when the Homebrew copy shadows it on `PATH`, says so in `doctor`.
- Version comparison is exact. `>=1.2` and `^2.0` are not understood; they belong
  to the provider that owns the versioning scheme.
- `starpoint update` on a Rust toolchain runs `rustup toolchain install` for the
  channel, which moves a channel forward. A concrete toolchain pinned in the
  configuration is never moved.