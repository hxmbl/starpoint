# Starpoint Codebase Map

## Overview
Starpoint is a Go CLI that sits above the package managers and toolchains already
on the machine (brew, apt, dnf, pacman, uv, pip, cargo, rustup, npm, pnpm, yarn,
gem, go, the OS itself) and turns one TOML file into a converged machine. It never
installs anything itself and never flattens its providers into one abstraction: it
decides who should own each piece of software, delegates the real work, records the
decision, and reports what actually changed.

The core pipeline is exactly what the README draws:

```
config → desired state → provider resolution → current state → diff → provider operations
```

## Entry Point
**`cmd/starpoint/main.go`** - The whole of the executable
- Installs signal handling (interrupt, SIGTERM) as a context, then hands over to
  `internal/cli`
- Wires an `Env`: the three streams, the working directory, the confirmation
  and choose prompts, and `Interactive`, which is true only when a person can
  answer at a terminal
- The version exists only through the build stamp (ldflags
  `-X github.com/hxmbl/starpoint/internal/cli.Version=...`, from `git describe`)
  — there is no version constant to edit
- Every line of behaviour lives under `internal/`

## Core Modules

### The Command Layer
**`internal/cli/cli.go`** - Dispatch, flags, and per-command bodies
- Seventeen canonical commands, most with aliases: `init`, `sync`, `list`
  (`ls`), `inventory`, `adopt`, `info` (`show`), `explain`, `doctor`, `install` (`add`, `i`),
  `remove` (`uninstall`, `rm`), `transition` (`move`), `update` (`upgrade`),
  `runs` (`history`), `prune`, `recover`, `logging` (`logs`), `help`
- Global flags (`--json`, `--force`, `--provider`, `--context`, `--color`,
  `--allow-destructive`, `--allow-root`) are accepted anywhere on the line,
  because `starpoint sync --json` and `starpoint --json sync` are the same
  sentence to a user
- `--help` and `--version` only mean Starpoint's own before the subcommand:
  after it, `install --version 1.2.3` pins a version
- `sync` drops every relocation from its plan (`withoutMoves`): a provider
  mismatch is a `provider-drift` warning pointing at `transition` and
  `adopt`, so sync never removes software. Before that, `besideSystem` takes
  out each entry with no provider whose only copy is the operating system's,
  as the install of a managed copy beside it: asked about at a terminal
  (default no), refused unattended with `duplicate-install` (exit 5)
- `guardElevated` runs before dispatch; every command that changes anything is
  guarded, confirmed, audited, and locked (see below)

**`internal/cli/guard.go`** - The refusals
- Exit codes 0, 1 and 2 are the conventional ones; refusals get their own so a
  script can tell "it would not do this without being told it may" from "it
  tried and failed": 3 (`destructive-unattended`), 4 (`elevated-refused`)
  and 5, a decision only the user can make with nobody to ask
  (`owner-ambiguous`), which names the candidates
- A `RefusalError` carries the plan it refused, so the caller sees exactly what
  would have happened; nothing was changed, and the refusal is audited
- An unattended run (`--yes`, no terminal, or a piped answer) that would delete
  or replace anything is refused unless `--allow-destructive`; an interactive
  run is not guarded here — its confirmation lists every effect, the
  destructive ones marked `!`, before asking; a Proceed? question never
  collapses effects into a count (result summaries still do, unless
  `--verbose` or a dry run)

**`internal/cli/confirm.go` and `prompt.go`** - Asking before changing
- Any machine-touching step becomes a `change` (verb, target, provider, the
  effects expected) before it happens
- `PromptConfirm` never assumes yes: a terminal is prompted, a pipe is read
  once, and a stream that ends without an answer is an error naming
  `--no-confirm`, because silently declining would leave the caller unsure
  whether anything happened

**`internal/cli/providerarg.go`** - The bare provider name
- `starpoint install ripgrep brew` and `starpoint install brew ripgrep` both
  name the provider; only the first or last token is considered, never a
  middle one, because `install brew` must keep installing the software called
  brew, and naming the provider twice is an error rather than a guess

**`internal/cli/adopt.go`** - `adopt <pkg> [provider]` and `adopt --all`
- Ownership bookkeeping through the engine's AdoptOnly path: config-only,
  previewed and confirmed; never installs, removes or moves. `--all` takes
  only what `inventory` calls requested; two owners are asked about, or
  refused (exit 5) with nobody to ask

**`internal/cli/elevated.go`** - What changes when the run is root
- Four things: whose environment the run acts on (under sudo or doas it is the
  invoking user's, retargeted before anything reads HOME), who each provider
  runs as, whether a changing command runs at all (`--allow-root`, or an
  explicit yes at a terminal), and what alignment happens afterwards
- A run that cannot resolve or act for its invoking user reports
  `invoking-user-unresolved` or `privilege-drop-failed`; read-only commands
  still run, carrying the warning and an honest `run_as`
- `retarget()` sets HOME to the invoking user's home before `elevationStatus`
  or anywhere else reads it

**`internal/cli/logging.go` and `help.go`** - Self-documentation
- `logging` shows, prunes or configures Starpoint's own record-keeping;
  `help.go` owns the aliases table and per-command flags — the same table the
  shell completions are checked against

### Vocabulary
**`internal/model/model.go`** - The provider-agnostic core types
- `Package` (desired, with opaque `channel` and `reason`), `Installation`
  (observed, with `Path` and `Pinned`), `Scope` (global/user/project — a
  placement concept several providers can serve)
- `Status` is the verdict: `correct`, `missing`, `provider-differs` (a
  transition, not a plain install), `version-differs`,
  `version-unsatisfiable`, and `unmanaged` — on the machine but not owned, so
  "missing" would be the opposite of the truth
- `State` carries `Elsewhere`, the other providers that have it, which is how
  Starpoint avoids installing duplicates

**`internal/model/effect.go`, `transition.go`, `runas.go`**
- `EffectOp`: create, modify, replace, delete, chown, chmod — with kinds
  (package, executable, symlink, …) and evidence (planned vs observed)
- `TransitionPhase`: `planned → installed → verified → completed`, plus
  `failed` and `blocked`; everything before the old copy is removed is safe to
  retry by construction
- `RunAs` says who a provider's commands ran as: account, whether Starpoint
  was elevated, whether it dropped privileges, and why

### Configuration
**`internal/config/config.go`** - The source of truth, and the writing of it
- One global file (`~/.starpoint.toml`) plus project-local `.starpoint.toml`
  files; the merged view is deliberately project-over-global, the more
  specific statement of intent wins
- Starpoint writes to the configuration as part of normal operations:
  installing adds an entry, removing deletes one, and provider resolution
  persists its decision — this is managed state, never a generated cache
- An edit lands in the file that owns the entry, and every write goes through
  a temporary file, fsync, and rename
- `settings` changes behaviour but never desired state: `providers` restricts
  resolution, `no_confirm` quietens asks, `retention_days` (global only)
  bounds the records
- The TOML `schema` field (currently 1) is a separate version from the JSON
  envelope's — they are not related and must not be conflated

### Provider Resolution
**`internal/resolve/resolve.go`** - Who should own it
- Three rules in order of authority: an explicit `provider` in the config is a
  decision, not a hint; an existing installation keeps ownership (replacing a
  working install because a scoring function prefers another is exactly the
  duplicate-install failure mode this project exists to avoid); otherwise an
  ecosystem-native provider beats a general-purpose system manager
- The ecosystem of the project in the current directory is a weak signal:
  it only decides for a name with that ecosystem's shape (a Go module path, an
  npm `@scope/name`). A bare name like `neofetch` typed in a Go repository
  falls through to the system package manager as it would anywhere else, and
  `go` is never offered as an alternative for a name that is not a module path
  (`golang.LooksLikeModulePath`)
- When the rules produce no clear winner it reports the ambiguity —
  confidence, alternatives — rather than guessing, and the CLI asks (the
  current project's provider is offered first among the alternatives)
- The hints are deliberately small: `rg` maps to Rust, `ripgrep` does not,
  because `brew install ripgrep` is what almost everyone means

**`internal/locator/locator.go`** - "Is this on the machine, and whose is it?"
- Locating is separate from installing on purpose: most providers can only
  report what they installed themselves
- Inspects real PATH directories, attributes symlinks by both visible path
  and resolved target, and collapses repeated or aliased directories
  (`/bin` and `/usr/bin` on merged-/usr) to one read — reading it twice
  reported every file as a duplicate of itself
- Two links to one program is not a shadowing problem; a different file with
  the same name is

### The Provider Contract
**`internal/provider/provider.go`** - What an integration implements
- `Provider` is not a lowest common denominator: `Capabilities` advertises
  what a provider can do (discover, install, update, pin, channels, verify,
  project install), and typed `Error` values (`not-found`, `unsupported`,
  `permission`, `network`, `conflict`, `failed`) let the core diagnose without
  string-matching subprocess output
- Optional interfaces carry the rest: `Verifier` (run the executable),
  `ContextAwareProvider` (install into a project directory),
  `ExecutableMappingProvider` (attributed system binaries),
  `RequestedReporter` and `RequestedByConstruction` (installed on request
  versus pulled in as a dependency, for `inventory` and `adopt --all`),
  `ExecutableDirProvider` (uv and pip overlap on `~/.local/bin`, which
  matters mid-transition), `StateDirProvider` (whose caches to give back after
  an elevated run), `LatestVersionProvider` (a pin that is satisfied vs one
  holding the package back), `Privileged` and `InstallDirProvider`
- The `Kind` constants reach further than the built-in set (nix, winget,
  snap, …) — categories ahead of integrations
- The `Registry` keeps providers in registration order; `Available` probes
  health but tolerates a broken provider rather than stopping everything

**`internal/provider/privilege.go`** - Who runs what when elevated

### The Built-in Providers
**`internal/providers/providers.go`** - The wiring
- One line per provider in `New`; `DefaultRegistry` is the release's set, and
  `Privileges` hands each provider a runner that starts its commands as the
  classified account

**Registration order is not cosmetic.** The engine asks providers for an
existing installation before falling back to path attribution: `system`,
`brew`, `apt`, `dnf`, `pacman`, `uv`, `pip`, `rustup`, `cargo`, `npm`, `pnpm`,
`yarn`, `gem`, `go`. `rustup` is ahead of `cargo` because `rustc` and `cargo`
are rustup's while `cargo install` is Cargo's — a loose rule would make the
wrong one the owner. Two tools are absent on purpose: Docker and Podman manage
containers, and Bun and Deno cannot enumerate what they have installed — a
provider that cannot answer "what do you own?" would make Starpoint's central
promise false.

**`internal/providers/system/`** - Preinstalled OS software, deliberately
read-only (no install, remove or update), so `doctor` can say `/usr/bin/git` is
the OS's and removal would be destructive

**`internal/providers/shell/`** - Shared provider plumbing, not a package
manager: capability helpers, the mapping tie-break when several executables
claim a package (exact name wins, then shortest), and `PrivilegeAt`, which
classifies by directory — inside the user's home or a user-owned prefix is
user, an unknown directory is root, as it always was

**`internal/providers/<brew,apt,dnf,pacman,uv,pip,rustup,cargo,npm,pnpm,yarn,gem,golang>/`**
- One package per ecosystem, each owning every provider-specific assumption:
  how to ask what is installed, how to install, how to verify. They parse
  their tools' real output, pin versions where the tool allows it, and arrange
  themselves around the provider contract rather than the other way round

### The Engine
**`internal/engine/engine.go`** - Convergence lives here
- `Plan` evaluates every desired package against the machine: config-named
  provider first, then any managing provider — the read-only `system` provider
  matches almost every command on a Unix machine, so it is asked last — then
  path attribution (see `findExisting`)
- Provider health is memoised per Engine: probing is a subprocess per
  provider, a single `install` asks for it several times over, and every step
  of a command must reason about the same machine rather than three slightly
  different samples of it. The first probe of each provider runs concurrently
  with the others. Only a probe that finished on its own is kept; one
  cut short by a deadline or cancellation is asked again next time
- Also `Duplicates`, `Shadowed`, `Explain`, `Discovery` and `Prune` (installs
  no longer in the configuration)

**`internal/engine/inventory.go`** - `starpoint inventory`
- Health and Discover only, concurrently per provider, each under its own
  bound (a health check that times out is a failed provider with a
  `discover-failed` warning): what each provider
  reports, marked managed when the configuration has that (provider, name).
  No resolver, no per-package lookups, no writes; a failing provider is a
  `discover-failed` warning, not a stop

**`internal/engine/survey.go`** - One round of concurrent "do you have this?"
- `SurveyTimeout` is 20 seconds; a hung package manager costs the timeout, not
  the run — and providers that cannot enumerate are skipped rather than asked
  pointlessly

**`internal/engine/apply.go`** - Applying the plan
- Ordering is deliberate: provider decisions are persisted to the config
  before the machine is touched, so an interrupted run leaves the config
  describing intent; installs happen before removals, so a relocation never
  leaves nothing installed in between
- A failure in one package does not stop the others: partial progress is real
  progress, and the caller is told exactly what failed

**`internal/engine/observe.go`** - What actually changed
- A provider operation is a subprocess Starpoint cannot see inside. Instead of
  claiming, it snaps the places operations write to immediately before and
  after and reports the difference: never "remove probably deletes these",
  always "these were there, and now they are not" — where a location could not
  be watched beforehand, the effect's evidence says so

**`internal/engine/describe.go`** - The prediction side of the same bargain,
replaced by observations when the action runs

**`internal/engine/transition.go`** - Moving software between providers
- Phases instead of installs: `installed` (target has it, old untouched) →
  `verified` (confirmed working, old eligible for removal) → `completed`;
  `Verify` combines provider inventory with PATH evidence because either
  alone is misleading — installed but not in effect is how a transition breaks
  a working command
- Interrupted transitions are nameable, reversible (`transition --reverse`
  uses the history), and safe to retry until the old copy is removed

### Privilege
**`internal/elevation/`** - Detecting elevation, and what it means
- Unix: effective uid 0, and the invoking user can be named (SUDO_USER,
  DOAS_USER) and acted for. Windows: an elevated administrator token is the
  same account, so there is nothing to drop back to

**`internal/privdrop/`** - The identity user-level providers run as
- uv, pip --user, cargo, go, rustup, and npm, pnpm, yarn, gem or brew when
  their prefix is the user's run as the invoking user; apt, dnf, pacman and
  everything system-prefix run as root, where root is the point
- The environment is built, not filtered from root's: the user's HOME, USER,
  LOGNAME and PATH order; a `locations` table of provider paths keeps a
  variable only when every path inside it lies in the user's home — anything
  pointing into root's home would send the install somewhere the user cannot
  write

**`internal/exec/`** - The only way anything runs a command
- `Runner` is the seam every provider is tested through; `Identity` starts a
  command as another user with an environment built for them

**`internal/providers/privileges.go`** - The per-run classification table:
each provider is asked once through `Privileged`, so inventory, install and
verification all run as the same account

### Alignment
**`internal/align/align.go`** - Keeping root's leftovers harmless
- Under the target home, entries owned by root and changed since the run
  started are chowned to that user (ancestors too, up to but never including
  the home); outside it, what the provider created is widened — bits only
  added, never removed, and anything that existed before or looks like a
  secret is left alone
- Every fix is an effect on the action that caused it, so it reaches the audit
  log; a fix found after a provider already ran as the user is marked
  `unexpected` and warned about, because nothing should have been needed
- Bounded walks (`DefaultMaxEntries` 200 000): a provider that writes more is
  reported partly aligned rather than walked forever

### Output
**`internal/report/report.go`** - Humans and machines, same results
- JSON is a first-class path, not a wrapper around the text: another tool
  reads the structured data the engine produced, never a parsed table
- One flat envelope per response: `{schema_version: 9, ...}` — errors too, on
  stderr, with refusal `code`, `exit_code` and, when there was a plan,
  `refused`
- **`docs/json-schema.md`** documents schema 9 top to bottom: common types,
  `ActionResult` and its `run_as`, effects, summaries, refusal examples, and
  the v1→v9 migration history. Any envelope change needs a bump plus a
  migration section there; removing or renaming fields is breaking

**`internal/report/actions.go`** - `ActionResult`, the one shape every
changing command reports: outcome, transitions, config changes, effects,
warnings, `run_as`

**`internal/color/color.go`** - Decoration, never information: every line
reads the same with the escapes stripped, and the conventional rules
(`--color`, NO_COLOR, dumb TERM, follows the writer) apply

### Records
**`internal/auditlog/`** - What this tool did to my machine, and when
- Append-only JSON Lines, rotated by day, best effort always: a log that
  cannot be written is a problem to report, never a reason to refuse to
  install software

**`internal/history/`** - Completed transitions, enabling reversal
- Deliberately the opposite of the audit log: curated, editable back out of by
  an explicit reversal, stored in `history.json`
- A corrupt history does not stop the command, but it is not passed unnoticed
  either: silently recording nothing makes a transition that succeeded look
  like one that never happened

**`internal/tracking/`** - `runs.json`, where software has been run
**`internal/retention/`** - Everything ages out
- Applied on every command, not remembered by the user; the default window is
  30 days and zero or negative means the default, never "keep nothing" — the
  failure mode of a zero retention is a tool deleting the audit trail it was
  supposed to keep
**`internal/lock/`** - flock-style locks per config file, so two Starpoint
processes do not converge at once
**`internal/recovery/`** - Interrupted configuration writes
- A leftover `.tmp` is inspected on the next run: promotable back as the
  config, discardable when safe, and never deletable when it is the sole copy
  of the user's declared state — discarding that destroys the configuration
  outright rather than reverting an aborted write
**`internal/oslog/`** - Asks the system logger to help with compression
- Generates logrotate (Linux) or newsyslog (macOS) text into Starpoint's own
  directory and tells the user the one command to install it; never writes to
  `/etc` itself, because a tool that manages other people's software has no
  business doing privilege escalation on somebody's behalf

### Project Awareness
**`internal/project/`** - `install .`
- Detects the ecosystem by marker file (go, rust, node, python, ruby) and
  finds the project's root. Kept deliberately narrow: Starpoint does not
  invent a universal dependency system
- Deps are installed by each ecosystem's own tool, chosen from the files the
  project actually has, in the project root; project-local configuration
  remains the source of truth for what the project wants

### Test Support
**`internal/testsupport/`** - The seam that keeps tests off the machine
- `FakeRunner`: scripted responses keyed "cmd args…", every call recorded, so
  tests assert the exact commands a provider would run
- `FakeProvider`: a provider contract with canned `Installation`s and
  configurable capabilities
- `TempHome`, `WriteConfig`, `MustDoc` for scratch configurations

## Data Flow

```
~/.starpoint.toml (+ project .starpoint.toml)   internal/config
    ↓ desired packages
resolve + health (memoized)                     engine.Plan → model.State
    ↓ status: correct / missing / provider-differs / version-*
survey + locator                                engine.findExisting
    ↓ model.Plan (actions, transitions, planned effects)
confirmation/guards                             cli
    ↓
config decisions persisted first                engine.Apply phase 1
    ↓
provider operations (install → update, removals last)   engine.Apply
    ↓
observed effects, alignment, run_as             engine.observe + align
    ↓
report: one envelope, human text or --json      internal/report
    ↓
audit log, history, runs, retention             record-keeping
```

## State on Disk

**Global configuration**: `~/.starpoint.toml`
- `schema = 1`, `settings`, and `[[package]]` entries; project-local
  `.starpoint.toml` files sit beside the project's marker files

**State directory** (`.starpoint/` beside the global config, so `~/.starpoint/`
by default):
- Audit log (JSON Lines, rotated by day)
- `history.json` - transition history, for `transition --reverse`
- `runs.json` - execution records
- Mutation locks for the current configuration
- Recovery journal — the `.tmp` files an interrupted write leaves behind
- The OS logger configuration generated by `starpoint logging config`

## Key Design Decisions

1. **The config is the source of truth, and Starpoint manages it** — install,
   remove and resolution all write it; nothing treats it as disposable
2. **Declarative but not Nix** — Starpoint decides placement and delegates the
   work; it does not build a hermetic world
3. **Provider-aware, not a lowest common denominator** — capabilities and
   typed errors instead of pretending a Python tool and a Homebrew formula
   install the same way
4. **Registration order encodes ownership** — rustup before cargo is not
   decoration; it decides who answers "rustc"
5. **A provider that cannot say what it owns is out** — Bun and Deno are
   absent on purpose, as are Docker and Podman (containers, not software)
6. **Transitions, not blind installs** — moving Python from brew to uv is
   installed, verified, then uninstalled, and an interruption leaves a
   duplicate rather than a hole
7. **Effects are observed, not claimed** — before/after snapshots replace
   predictions when an action runs
8. **Refusals have their own exit codes** — 3 and 4 mean "nothing was
   changed", and the refusal carries the plan it refused
9. **Privilege drop, not privilege sprawl** — user-level providers run as the
   sudo user with an environment built for them; alignment gives back what
   root left behind, bits only added
10. **Nothing load-bearing is best effort** — the audit log can be
    unwritable; the configuration write cannot be
11. **Retention is automatic and positive-by-default** — zero days means the
    default, never "keep nothing"
12. **Color is decoration** — stripping it loses nothing

## Dependencies

- `github.com/BurntSushi/toml` - the configuration
- `golang.org/x/sys` - elevation and process identity
- `golang.org/x/term` - terminal detection

That is the whole direct set, on purpose; each addition needs a reason.

## Testing

93 test files, roughly half the tree, in every internal package. `make check`
(gofmt, vet, full suite) is the commit gate.

- `internal/testsupport` - `FakeRunner` (scripted commands, recorded calls) and
  `FakeProvider` (canned installations), so a provider test runs a script, not
  a package manager
- Root-only tests: `TestAsRoot*` in `internal/align`, `internal/privdrop` and
  `internal/cli` (chown/chmod alignment, dropping to another account). They
  skip in an ordinary run and run only under
  `make test-root`, which builds three test binaries into `/tmp` and executes
  them with `sudo STARPOINT_ROOT_TESTS=1 … -test.run AsRoot`. CI runs them on
  Linux only; `TestAsRoot` fatals when the environment variable is set
  without root
- The two levels are separate: privilege *classification* (which provider
  wants which account under sudo) is tested without root in
  `internal/providers/privileges_test.go`
- `internal/cli/completions_test.go` reads `completions/starpoint.bash`,
  `.zsh` and `.fish` and fails if any command spelling, alias, command flag,
  global flag or provider name is missing — a new command, flag or provider
  means updating all three hand-written files
- A bare `go build ./cmd/starpoint` in the repo root drops a gitignored
  `starpoint` binary into the tree root: a local artifact, never part of a
  release

## Building and Releasing

- **`Makefile`** - `check` = fmt-check + vet + test (the gate), `build` stamps
  the version, `test-race`, `cover`, `lint`, `test-root`
- **`.golangci.yml`** - version 2, standard linters; the errcheck exclusions
  are deliberate with the reasoning in a comment: stream writes (a CLI cannot
  recover from a failed print), best-effort `os.Remove` and `File.Close` on
  already-failing paths, and everything in `_test.go`. Locally `check` does
  not run golangci-lint, but CI does, so a change that would fail `lint`
  fails the PR instead
- **`.goreleaser.yaml`** - linux/darwin × amd64/arm64 (no Windows builds),
  CGO_ENABLED=0, archives carrying the completions, Homebrew formula rendered
  with `skip_upload: true`
- **`.github/workflows/ci.yml`** - reusable (`workflow_call`): lint job; `make
  check` on Ubuntu and macOS; race detector, `go mod tidy` verification and
  the sudo root tests on Linux only; a cross-build job vetting and
  compiling all six GOOS/GOARCH targets
- **`.github/workflows/release.yml`** - fires on `v*.*.*` tags, runs `ci.yml`
  as a gate, then GoReleaser publishes and the workflow pushes the rendered
  formula to `Hxmbl/homebrew-tap` over an SSH key. Release actions are pinned
  by commit SHA; artifacts are never built or uploaded by hand

`docs/` holds exactly one file: `docs/json-schema.md`, the JSON contract — the
single place a consumer needs beyond the binary itself.
