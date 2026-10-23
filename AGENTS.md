# Development Guide for starpoint

Starpoint is a declarative orchestration layer **above** the package managers
and toolchains already on the machine (brew, apt, dnf, pacman, uv, pip, cargo,
rustup, npm, pnpm, yarn, gem, go, shell, system). One TOML file describes
desired state; Starpoint decides *who* should own each piece of software,
delegates the real work to the owning tool, records the decision in the config,
and observes what changed. It is not another package manager and not a Nix-like
hermetic world: it must stay idempotent, observable, and honest about what it
did — including in machine-readable form (`--json`).

## Build Commands

```sh
make build    # bin/starpoint, version stamped via ldflags from git describe
make run      # build and run the local binary
```

Building without the Makefile: `go build -ldflags "-X
github.com/hxmbl/starpoint/internal/cli.Version=$(VERSION)" -o bin/starpoint
./cmd/starpoint`. There is no version constant to edit — the version only
exists through that stamp.

A bare `go build ./cmd/starpoint` in the repo root drops a `starpoint` binary
into the tree root. It is gitignored; never commit it, and never treat it as
part of a release.

## Test Commands

```sh
make test        # go test ./...
make test-race   # the suite under the race detector
make cover       # per-package coverage
make tidy        # go mod tidy && go mod verify
```

## Code Quality

```sh
make fmt-check   # gofmt; part of check
make vet         # part of check
make lint        # golangci-lint (version "2" config in .golangci.yml)
make check       # fmt-check + vet + test — THE commit gate
```

`make check` must pass before anything is committed. `make lint` is not in
`check` locally, but CI runs golangci-lint as its own job, so a change that
would fail `make lint` fails the PR anyway.

The errcheck exclusions in `.golangci.yml` are deliberate, with the reasoning
in a comment above them: stream writes (`fmt.Fprint*`) because a CLI cannot
recover from a failed print, `os.Remove` and `(*os.File).Close` because the
paths that must detect a failed close (lock release, durable config writes)
already check it explicitly, and all of errcheck in `_test.go` because tests
assert outcomes explicitly. Do not "fix" code so it no longer matches the
exclusion rationale, and do not add new exclusion-shaped bugs nearby.

## Root-only tests

Some tests change owners for real (chown/chmod alignment) and start processes
as another account (privilege drop). They need root and live in
`internal/align/align_root_test.go`, `internal/privdrop/privdrop_root_test.go`
and `internal/cli/elevated_root_test.go`, named `TestAsRoot*`.

```sh
make test-root
```

This builds three test binaries into `/tmp` (as the invoking user, so nothing
root-owned lands in the Go caches) and runs them under
`sudo STARPOINT_ROOT_TESTS=1`, filtered to `-test.run AsRoot`.

Do **not** run `sudo` in a normal session to run these. They skip in an
ordinary run (`requireRoot` skips), and fatally fail if `STARPOINT_ROOT_TESTS=1`
is set without root. CI runs them on Linux only (`.github/workflows/ci.yml`);
CI is what tells you whether your change broke the privilege path. Privilege
*classification* (which provider wants which account under sudo) is tested
without root in `internal/providers/privileges_test.go` — keep those two levels
distinct when writing new tests.

## Project Layout

Go 1.27, module `github.com/hxmbl/starpoint`. Entry point is
`cmd/starpoint`, which only calls `cli.Run`. Everything else is `internal/`.

- `internal/cli/` — command dispatch, aliases, flags, prompting, confirmation,
  guard rules, help text, colorized human output. Tests beside the code.
- `internal/config/` — the TOML configuration is owned here: global
  `~/.starpoint.toml`, per-project `.starpoint.toml`, the state directory
  (`.starpoint/` beside the config: mutation locks, audit log, run and
  transition history, recovery journal).
- `internal/engine/` — turns configuration plus observed machine state into a
  plan, and applies the plan.
- `internal/resolve/` — provider resolution: the user describes what they want,
  not how to get it; this decides who owns the package and persists the
  decision into the config.
- `internal/locator/` — "is this on the machine, and where does it come from?"
  Locating is separate from installing on purpose: most providers can only
  report what they installed themselves.
- `internal/provider/` — the provider contract (`Provider`, `Capabilities`,
  optional `Verifier`, `ContextAwareProvider`, `ExecutableMappingProvider`).
- `internal/providers/` — the built-in set, one package per ecosystem, wired
  together in `providers.go`'s `DefaultRegistry` (see below), plus the
  privilege classification table in `privileges.go`.
- `internal/exec/` — the only way providers run external commands.
- `internal/testsupport/` — `FakeRunner`, a scripted runner so no provider test
  touches the real machine.
- `internal/elevation/` — detect that Starpoint is elevated and what that means
  for whose environment the run acts on. `internal/privdrop/` runs user-level
  providers as the invoking user. `internal/align/` fixes ownership and
  permissions (chown/chmod) afterward.
- `internal/report/` — every JSON response goes through one envelope helper
  here. Human output and the JSON envelope are both written from results.
- `internal/auditlog/`, `tracking/`, `history/`, `retention/`, `oslog/` — what
  Starpoint did and when; execution and transition records; aging records out.
- `internal/lock/`, `internal/recovery/` — file locking for mutations, and the
  recovery of interrupted configuration writes.
- `internal/model/`, `internal/project/`, `internal/color/` — core types; "what
  does the project in the current directory need" (`starpoint install .`);
  color rules.

## The config is managed state

`~/.starpoint.toml` is the source of truth and Starpoint writes it as part of
normal operations: `install` adds an entry, `remove` deletes one, and resolved
provider placement is persisted there. It is not a generated cache. Its TOML
`schema` field is a separate version from the JSON envelope's
`schema_version` — do not conflate them.

All config writes go through the lock/recovery machinery in `internal/config`,
`internal/lock` and `internal/recovery`, because an interrupted write to the
one file Starpoint depends on is self-inflicted damage. Never hand-write config
serialization or reformat the TOML an expectation file assumes; if you change
the config shape, keep the invariant that Starpoint manages the file itself,
and let the config tables in `internal/config` speak for reality.

## The JSON contract

`--json` output is a public interface, versioned in `docs/json-schema.md`
(currently envelope version 7, with the v1→v7 migration history at the bottom
of that file). Every response is one flat object with a `schema_version`;
warnings, refusal codes, effects and `run_as` entries carry stable codes and
fields that tools read mechanically.

Rules when touching report or output code:

- Any change to the envelope, command payload shapes, refusal codes or warning
  codes needs a schema bump plus a migration section in
  `docs/json-schema.md`. Refusal codes are part of the contract: changing
  them without a bump and docs migration is a breaking change.
- Adding optional fields may be compatible; removing or renaming is not. When
  unsure, bump and document — the doc's Versioning Policy says exactly this.
- Never break `--json` consumers silently. The doc and the report writer must
  move together.

## Completions are guarded

`completions/starpoint.bash`, `starpoint.zsh` and `starpoint.fish` are
hand-written and guarded by `internal/cli/completions_test.go`, which checks
every command spelling, alias, flag and provider name against all three files.
**Any** new command, alias, flag or provider requires updating all three files.
The guard test will catch drift; do not weaken the test to escape it.

## Providers

Adding an ecosystem: one package under `internal/providers/<name>/` implementing
the contract in `internal/provider`, its own tests, a one-line registration in
`DefaultRegistry`, entries in all three completions, and a row (if needed) in
the privilege classification and the schema's provider table.

- Registration order is not cosmetic. The engine asks providers for an existing
  installation before falling back to path attribution, so the more specific
  owner comes first: `rustup` is registered ahead of `cargo` because `rustc`
  and `cargo` are rustup's while `cargo install` is Cargo's.
- Do not bypass the provider abstraction by shelling out directly from core
  code. `internal/exec` is the execution interface, and `testsupport.FakeRunner`
  is how tests script it.
- Docker and Podman (containers, not installed software) and Bun and Deno
  (cannot enumerate what they own) are absent on purpose. A provider that
  cannot answer "what do you own?" would make Starpoint's central promise
  false — don't add one without an answer.

## Release machinery

`make build` and the repo-root binary are development artifacts. Real releases
belong to the pipeline: `.goreleaser.yaml` builds the matrix
(linux/darwin × amd64/arm64; no Windows builds, CGO_ENABLED=0, version via `-X ...Version`)
and renders the Homebrew formula; `.github/workflows/release.yml` fires on a
`v*.*.*` tag, runs `ci.yml` as a gate, then pushes the rendered formula to the
`Hxmbl/homebrew-tap` over SSH. The release workflow's actions are pinned by
commit SHA — do not bump or unpin them casually. Do not build or upload release
artifacts by hand.

## Repository state

`docs/` holds exactly one file, `docs/json-schema.md`.
`docs/next-tasks.md` was deleted deliberately; do not reference, recreate or
search for it. Direct dependencies are minimal on purpose (`BurntSushi/toml`,
`golang.org/x/sys`, `golang.org/x/term`); each new dependency needs a reason.

## Commits

House style is short imperative subjects that describe what the behavior now
does, not tenses or prefixes (`Refuse changing commands as root without
--allow-root; act for the sudo user`, `Watch Cargo's and Go's executable
directories`, `Fix CI: ...`). Run `make check` before committing; CI enforces
the same suite plus golangci-lint, cross-platform builds and the root tests.
