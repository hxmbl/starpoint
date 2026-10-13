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

`starpoint install .` installs what the project in the current directory needs:

1. the packages its `.starpoint.toml` declares;
2. the toolchain its ecosystem expects (node/npm, cargo/rustc, go, python3,
   ruby/gem), recorded — never installed — in the project's `.starpoint.toml`,
   which is created if needed, when the owner can be identified;
3. its dependencies, through the ecosystem's own tool run in the project root:

| Project | Command |
| --- | --- |
| Node with `packageManager` in package.json | that manager (`pnpm install`, `yarn install`, `npm ci`/`npm install`) |
| Node with pnpm-lock.yaml / yarn.lock / package-lock.json | `pnpm install` / `yarn install` / `npm ci` |
| Node without a lockfile | `npm install` |
| Python with uv.lock, or `[tool.uv]` in pyproject.toml | `uv sync` |
| Python with requirements.txt | `pip3 install -r requirements.txt` (or `pip`) |
| Rust (Cargo.toml) | `cargo fetch` |
| Go (go.mod) | `go mod download` |
| Ruby (Gemfile) | `bundle install` |

The command is shown and confirmed before anything changes (declining changes
nothing at all), `--dry-run` prints it without running it, and `--no-confirm`
runs it unattended. A Python project with neither uv configuration nor
requirements.txt is reported rather than guessed at.

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
| `starpoint init [--force]` | Create `~/.starpoint.toml`; exits 1 without changing anything if it exists, `--force` replaces it |
| `starpoint sync [--dry-run] [--no-confirm]` | Converge the machine onto the configuration |
| `starpoint transition [pkg...]` | Show or perform provider transitions (alias: `move`) |
| `starpoint transition --reverse <package>` | Reverse the most recent transition when safe |
| `starpoint doctor` | Inspect Starpoint, config, providers, ownership, inconsistencies |
| `starpoint list [--json]` | Show managed software, its location, scope and provider |
| `starpoint info <package>` | Show one package's state in detail |
| `starpoint install <package> [provider]` | Install and record (alias: `add`) |
| `starpoint install .` | Install the current project's declared packages and dependencies; record its toolchain in the project config |
| `starpoint remove <package>` | Uninstall and delete from config (alias: `uninstall`) |
| `starpoint update [--self]` | Update managed software, respecting pins |
| `starpoint explain <package>` | Explain how Starpoint resolves ownership |
| `starpoint prune` | Preview and remove orphaned user-scoped installations |
| `starpoint recover [--discard]` | Inspect and clear files left by an interrupted run |
| `starpoint runs` | Show where Starpoint has been run |

A provider may be named after the package names, before them, or with
`--provider`, and a version may be written inline:

```
starpoint install ripgrep brew
starpoint install brew ripgrep
starpoint install ripgrep --provider brew
starpoint install ripgrep@14.1.0 brew
starpoint install @types/node@5.0.0 npm
```

A single token is always a package name — `starpoint install brew` installs the
software called brew — and naming two providers is an error rather than a guess.

**Confirmation.** Anything that installs, removes or updates software describes
what it is about to do and waits for an answer: which packages, through which
provider, and whether anything is going to be installed at all. Recording
software that is already present is shown as a *record* rather than an install,
because no mutation happens. `--no-confirm` (or `no_confirm = true` in the
configuration) skips the question; a stream that provides no answer at all is
refused rather than assumed.

Global options work anywhere on the line: `--json`, `--provider <name>`
(repeatable, restricts automatic resolution), `--force`, `--version`, `--help`.
`--help` (or `-h`) after a command prints that command's help, as does
`starpoint help <command>`; it is never taken as a package name. Any other
unrecognised `-`-prefixed token is a usage error rather than a package name.

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
| `dnf` | global | RPM inventory and versioned package installation. |
| `pacman` | global | Pacman inventory and repository installation. Arch repositories serve current versions only, so it cannot honour a version pin and says so rather than building a command that cannot work. |
| `uv` | user | Python tools and interpreters, `==` pins. |
| `pip` | user | Python packages, installed with `--user` so pip never needs root. Locations come from `pip show -f`: the console script when there is one, otherwise the package directory. |
| `rustup` | user | Rust toolchains: `rustc`, `cargo`, `rustfmt` and friends, plus the toolchains themselves. |
| `cargo` | user | Crates from `cargo install`, `--version` pins. |
| `npm` | user | Global packages, `name@version` and dist-tags. |
| `pnpm` | user | pnpm globals. Own store and bin directory, and reports when that bin directory is not on PATH. |
| `yarn` | user | Yarn **classic** globals only. Berry has no global package manager and says so. |
| `gem` | global | RubyGems, including bundled gems marked `default:`. |
| `go` | user | `go install` binaries discovered in the Go bin directory. |

Each provider reports its own capabilities and its own typed errors. The core
asks what a provider can do rather than branching on provider names, so adding
an ecosystem does not mean touching the engine.

**The compatibility target is the machine.** Every package manager installed on
this project's development host is implemented. Docker and Podman manage
containers rather than software on the machine. Bun, Deno, Nix, and version
managers remain deferred until Starpoint can report their ownership and
placement reliably; see `docs/next-tasks.md`.

### Operations and completion

`doctor` reports provider health, configuration and ownership, interrupted config
writes, plus a summary of the executables on `PATH`: counts, names shadowed by
an earlier entry, and executables whose owner is unknown. `doctor --all` lists
every entry; `--json` always carries the full inventory. A directory PATH names
twice (or via a symlink, such as `/bin` → `/usr/bin`) is read once. `list` shows
configured software and unaccounted executables. Provider transitions use
observed inventory and executable resolution to resume after interruption;
`starpoint runs` exposes transition history and `transition --reverse` attempts
a safe reversal.

State-changing commands share a per-config lock. The lock prevents two Starpoint
processes from racing on the same source of truth.

Configuration is written to a sibling temporary file, flushed, and renamed into
place, so a crash mid-write cannot leave a half-written config: the rename is the
only step that publishes it. If a run is interrupted between those steps, the
leftover `.tmp` file makes Starpoint refuse to modify configuration rather than
layer new writes on top of a change the user never saw. `starpoint recover` reports
what was left behind and, with `--discard`, clears it. Bash, zsh, and fish completion
definitions are in `completions/`; JSON response shapes are documented in
[`docs/json-schema.md`](docs/json-schema.md).

### Asking every provider at once

Before resolving a package, `install` asks **every** provider concurrently
whether it has that software, and says what it found:

```
$ starpoint install rake gem
rake: also present via system
About to change your machine:
  Record rake (gem)
      already installed at /Library/Ruby/Gems/2.6.0
      then: no software is installed or removed; the existing copy is recorded as owned by gem
```

`none found on installed providers` is not an error — a package can perfectly well
be installable by a provider that does not have it yet — but it is stated plainly,
and qualified when a provider is installed yet unable to enumerate its inventory,
because that weakens the claim.

When nothing on the machine and nothing about the name settles who should own
it, Starpoint asks. That includes the common case of one system package manager
beside language providers: `starpoint install cowsay` on a machine with apt, uv
and npm offers apt as the default but asks, because "the Debian package" and "the
PyPI package" are different choices. Without a terminal the command fails and
names `--provider`. A provider named on the command line is recorded with the
reason `chosen by the user on the command line`, a prompt answer as `provider
chosen interactively`.

`starpoint info <package>` uses the same lookup for software it does not manage,
so it names *every* provider that has it rather than the first one that answered.
On this machine, `starpoint info rake` reports that `system` provides it and that
`gem` also has 12.3.3 — the duplicate a sequential lookup would have hidden.

The point is not speed. Resolution otherwise proceeds one provider at a time and
stops at the first hit, which cannot tell "the only provider that has this" from
"several providers have this and the configuration picks one of them".

The cost is real: this is one extra round of subprocesses per provider, bounded by
a 20-second timeout so a hung package manager cannot hang Starpoint. `--json`
reports the survey and how long it took.

### Provider transitions

When software is installed through one provider and the configuration says it
should come from another, that is a transition — a first-class operation with its
own phases, its own command and its own evidence, not an install followed by a
remove.

```
starpoint transition                 # what would move, and how far each has got
starpoint transition ruff             # move it, with confirmation
starpoint transition --dry-run ruff   # describe it, change nothing
```

The ordering is the safety property. The phases are `planned` → `installed` →
`verified` → `completed`, and the incumbent copy is removed **only** after the new
one is confirmed working:

- **Confirmed** means two things agree: the target provider reports the software,
  and the command on `PATH` resolves to that provider's copy. A provider that
  says it installed something while a different provider still owns the command is
  treated as *not* installed, because it is not. When the command is not on `PATH`
  at all, the executable must exist on disk where the provider reported it (or in
  its bin directory); an inventory entry with no file behind it does not count.
- **Every early exit leaves the old command working.** A failed install, a failed
  verification and a failed removal all leave the incumbent in place, so the worst
  outcome of a transition is a duplicate rather than a hole.
- **Recovery needs no journal.** The phase is derived from what is on the machine:
  both copies present means the old one still needs removing, only the new one means
  it is done, only the old one means nothing happened. Running `starpoint transition`
  again finishes an interrupted transition instead of starting over.
- **Shared executable paths are handed over, not raced.** uv and `pip --user`
  both write console scripts to `~/.local/bin`, so installing the new copy first
  would overwrite (or be refused by) the old one, and removing the old copy would
  then delete the new one. When the target's executable path is already occupied,
  Starpoint removes the old copy first, installs and verifies the new one, and
  reinstalls the old copy (at its previous version where the provider can pin) if
  anything fails. After any removal the new copy is verified again and reinstalled
  if the removal took its executable with it; `completed` is never reported over a
  missing command.
- **A provider that cannot remove** gets a completed transition with the leftover
  named, not a failure. Ownership did move; `doctor` reports the duplicate.

`sync` performs transitions too, and `sync --dry-run` shows them with their phase
and evidence. `--json` exposes the whole record for other tools.

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

## Deliberately deferred

- HTTP API. The internal model is already machine-readable and `--json` exposes
  it. A server can be added later without reshaping anything.
- GUI.
- Additional ecosystems such as Bun, Deno, Nix, mise/asdf, winget, Chocolatey,
  Composer, Maven/Gradle, NuGet, SwiftPM, CocoaPods, vcpkg, Conda, Flatpak and
  Snap remain candidates. See `docs/next-tasks.md`.
- A universal project dependency system. `starpoint install .` delegates to each
  ecosystem's own installer, chosen by a narrow, documented rule set, rather
  than pretending one command can understand every ecosystem.

## Known limitations

- `scope = "project"` is intent. Providers that cannot install into a project
  context report the limitation; Starpoint does not rewrite manifests implicitly.
- Cross-provider ownership requires the filesystem. A package whose binary name
  differs from its package name and which no provider tracks is reported as
  present but unattributed rather than guessed at. Homebrew's `rust` formula
  provides a `rustc` executable but has no package by that name, so Starpoint
  cannot learn from Homebrew that a second `rustc` exists — it reports rustup as
  the owner and, when the Homebrew copy shadows it on `PATH`, says so in `doctor`.
- Version comparison is exact. `>=1.2` and `^2.0` are not understood; they belong
  to the provider that owns the versioning scheme.
- `gem list --local` does not say which installation home a gem is in, so `gem`
  reports the placement that applies by default. `gem list -d <name>` is the way to
  tell two homes apart.
- The yarn classic command surface is implemented from its documented behaviour;
  only Yarn Berry could be verified on the host, which correctly reports that it
  cannot manage globals. A real yarn 1.x install has not been exercised.
- pnpm's global bin directory is outside `PATH` on a default macOS install, so
  `doctor` says so rather than letting a pnpm-installed command look missing.
- Provider health is probed once per command and memoised. A single `install`
  previously swept every provider three times over; it now sweeps once, and every
  step of a command reasons about one sample of the machine rather than three.
- The concurrent lookup is bounded by a timeout, and its result channel is buffered
  to the number of providers, so a provider that answers late can always deliver
  and exit. Nothing in it can leak a goroutine or block on a send.
- Whether there is a terminal is decided by asking the operating system, not by
  inspecting the file mode. `/dev/null` is a character device, so a mode-based check
  calls it a terminal, prints a prompt into the void, and reads the resulting
  end-of-input as an answer.
- A cancelled provider command is reported as a cancellation. A killed process
  yields "signal: killed", which is an ordinary nonzero exit; without checking the
  context first, a lookup that timed out looked like a provider that merely failed
  and every provider then classified the silence as its own kind of problem.
- Commands run in their own process group and are cancelled as a group. Package
  managers are rarely one process — `brew` is a Ruby script that shells out to git
  and curl — so killing only the direct child would leave those grandchildren
  writing to the filesystem after Starpoint had moved on. On platforms without
  process groups, cancellation stops at the direct child.
- `starpoint update` on a Rust toolchain runs `rustup toolchain install` for the
  channel, which moves a channel forward. A concrete toolchain pinned in the
  configuration is never moved.

## License

MIT — see [LICENSE](LICENSE).
