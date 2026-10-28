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

Starpoint runs on Linux and macOS. There are no Windows builds.

```sh
go install github.com/hxmbl/starpoint/cmd/starpoint@latest
# or, from a checkout:
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

When Starpoint cannot tell which provider should own a package (`fastfetch`
with both Homebrew and apt here, or `ripgrep` with Homebrew and Cargo) it asks,
and an unattended run refuses rather than guess. `[settings] providers` answers
that question ahead of time, best first:

```toml
[settings]
providers = ["brew", "cargo"]
```

The first listed provider that is one of the tied candidates, is available, and
can honour the entry's version pin, channel and scope decides, and the recorded
`reason` says so. It is a preference, not a restriction: a `provider` written
in the entry, `--provider` on the command line, an existing install and a
name's known owner (`rustc` is rustup's) all still win, providers it does not
list are still considered, and when no listed provider fits Starpoint asks as
before. `starpoint explain <package>` shows whether the preference decided.
A project's own list replaces the global one inside that project.

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
| Python with requirements.txt | `pip3 install -r requirements.txt` (or `pip`); the project's `.venv/bin/pip` when it has one and no other virtualenv is active |
| Rust (Cargo.toml) | `cargo fetch` |
| Go (go.mod) | `go mod download` |
| Ruby (Gemfile) | `bundle install` |

The command is shown and confirmed before anything changes (declining changes
nothing at all), `--dry-run` prints it without running it, and `--no-confirm`
runs it unattended. Two installers are exact and delete what is already there:
`npm ci` replaces an existing `node_modules`, and `uv sync` removes packages the
lockfile does not list from an existing `.venv`. Those are listed as destructive
(`!`) effects, so an unattended run needs `--allow-destructive` for them.

A `requirements.txt` install depends on where pip installs. Inside a virtualenv
(an active `VIRTUAL_ENV` whose pip is the one on PATH, or the project's own
`.venv`, whose pip is then used) it only changes that environment and is an
ordinary effect. Outside one, pip installs into your user site-packages (as
root, the system's) and may upgrade or downgrade packages other software depends
on, so it is a destructive (`!`) planned effect with a `pip-outside-virtualenv`
warning suggesting `python3 -m venv .venv`: an unattended run needs
`--allow-destructive`, and as root the usual `--allow-root` rule applies too. A
virtualenv is suggested, never required. A Python project with neither uv
configuration nor requirements.txt is reported rather than guessed at.

### What Starpoint writes

`provider` is the ownership decision. Once written it is authoritative: `sync`
converges on it instead of re-deciding. If you deliberately move software to a
different provider by editing that field, `sync` reports the mismatch as drift
(`provider-drift`) and changes nothing; `starpoint transition <package>` makes
the move — it installs the new copy, verifies it, and only then removes the
old one — and `starpoint adopt <package> <provider>` instead records the
provider that really has it. `sync` never removes software.

`version` is a hard pin. Starpoint will not move it on its own. If a provider
cannot deliver it, Starpoint reports the conflict instead of editing the
configuration to make the problem go away. When `starpoint update` finds a newer
version than a pin allows, it explains the conflict and asks:

```
!!! There is a new version available for ruff (0.6.0), but config says 0.5.0
Update anyway? [y/N]
```

Yes lists the update, with the pin rewrite marked `!`, and asks once more
before it updates the package and rewrites the pin to the version now
installed; no (or `--no-confirm`, or no terminal, which includes a `yes` piped
into stdin) keeps the pin; `--dry-run` reports the
conflict. `--json` lists them under `pin_conflicts`. Newer versions are known for
pip, npm, pnpm, Homebrew, Cargo, RubyGems and APT; for other providers a pin is
simply reported as left unchanged.

### What Starpoint records

Every command Starpoint runs is written to its own log, alongside a record of
each change it made to the machine: what was installed, removed, updated or moved
between providers, which provider was asked, and whether it worked. A command
that fails is recorded with the failure, and so is a destructive operation
Starpoint refused to perform. The log is JSON Lines, rotated daily, in
`~/.starpoint/logs/`.

Records older than the retention window are deleted automatically, on every
command, so there is nothing to remember to run. The window is set with
`retention_days` in the `[settings]` table of `~/.starpoint.toml` and defaults to
30 days:

```toml
[settings]
retention_days = 90
```

Retention covers the audit log, the transition history and the run history. It is
read from the global file only, so a project you have checked out cannot shorten
or lengthen it. A window that is unset, zero or negative means the default, never
"keep nothing".

`starpoint logging` reports what is kept, how long, and whether the system's own
log rotation has been pointed at it; `starpoint logging prune` applies the window
now and says what went. Retention does not depend on either — they are for
looking, not for keeping.

Removing every configured package in one command is refused unless
`--allow-empty` says you mean it. Removing one is always allowed: the guard is
about bulk, not about the configuration ever becoming empty.

### Handing the logs to the system logger

Starpoint's own retention already deletes expired logs without privileges, on
every platform it supports. `starpoint logging config` additionally writes the
system logger's own rotation configuration, so that logs still inside the window
are compressed and their count is capped:

- **Linux** generates a `logrotate` stanza. This is the easy case — logrotate
  exists, its configuration is a plain text file, and the generated content says
  where to copy it.
- **macOS** generates a `newsyslog` line. This is the harder case: newsyslog runs
  as root from `/etc` and has no per-user configuration, so the generated file is
  written to Starpoint's own directory and the install is left to you.
- **Windows** has no configuration generated. Windows' event log keeps its own
  retention settings.

Starpoint never writes to `/etc` or `/var`. A tool that manages other people's
software has no business taking a privilege escalation on their behalf, so the
generated file goes next to the logs it governs and the command that installs it
is printed for you to run.

## Commands

| Command | Purpose |
| --- | --- |
| `starpoint init [--force]` | Create `~/.starpoint.toml`; exits 1 without changing anything if it exists, `--force` replaces it |
| `starpoint sync [--dry-run] [--no-confirm]` | Converge the machine onto the configuration: install what is missing, reconcile pins, and record the owner of entries that are installed but have no provider yet (a config-only change). Never removes or moves software; a provider mismatch is reported as drift. An entry with no provider that only the OS has is asked about before a managed copy is added (default no), and refused unattended with `duplicate-install` (exit 5) |
| `starpoint transition [pkg...] [--all] [--dry-run] [--no-confirm]` | Show or perform provider transitions (alias: `move`) |
| `starpoint transition --reverse <package>` | Reverse the most recent transition when safe |
| `starpoint doctor [--all]` | Inspect Starpoint, config, providers, ownership, inconsistencies, PATH; counts unmanaged software per provider and points at `starpoint adopt` (information, not a problem) |
| `starpoint inventory [--provider <name>]` | List every package each provider reports, managed or not, matched by provider and name; observes only (Health and Discover), works with no configuration, writes nothing — not even the audit log, and an elevated run chowns nothing; each provider gets 20 s to answer; says per package whether it was installed on request, as a dependency, or unknown |
| `starpoint inventory --format toml [package...] [--pin-versions]` | Print a reviewable manifest ([[package]] entries that parse back as configuration) to stdout; Starpoint writes no file. By default: what the configuration already manages (copied as written) plus what a provider says was installed on request. Dependencies, system software, packages a provider cannot classify, and a name two providers report are left out and listed with a reason in the comment header, as is a provider that could not list. Naming packages captures exactly those. Versions are pinned only with `--pin-versions` and only for providers that can install a chosen version |
| `starpoint adopt <package> [provider]`, `starpoint adopt --all` | Record the owner of software that is already installed; never installs, removes or moves anything. `--all` takes everything a provider says was installed on request; dependencies, unclassifiable packages and system software stay unmanaged. `--dry-run`, `--no-confirm` |
| `starpoint list [--managed] [--provider <name>]` | Show managed software, its location, scope and provider (alias: `ls`) |
| `starpoint info <package>` | Show one package's state in detail (alias: `show`) |
| `starpoint explain <package>` | Explain how Starpoint resolves ownership |
| `starpoint install <package> [provider]` | Install and record (aliases: `add`, `i`); `--version`, `--channel`, `--project`, `--dry-run`, `--no-confirm`. Never adds a second copy silently: unattended, a copy another provider has (or, unless you name the provider, one only the OS has) is refused with `duplicate-install` unless `--allow-duplicate` |
| `starpoint install .` | Install the current project's declared packages and dependencies; record its toolchain in the project config |
| `starpoint remove <package> [provider]` | Uninstall and delete from config (aliases: `uninstall`, `rm`); `--dry-run`, `--no-confirm`, `--allow-empty` |
| `starpoint update [--self] [--dry-run] [--no-confirm]` | Update managed software; asks before moving past a pin (alias: `upgrade`) |
| `starpoint prune [--dry-run] [--no-confirm]` | Preview and remove orphaned user-scoped installations inside your home directory; never a package manager itself, and every orphan left alone is listed with the reason; refuses when the configuration declares no packages |
| `starpoint recover [--discard] [--no-confirm]` | Inspect and clear files left by an interrupted run |
| `starpoint runs [--package <name>] [--directory <dir>]` | Show where Starpoint has been run (alias: `history`) |
| `starpoint logging [prune\|config]` | Show, age out, or configure Starpoint's own logging (alias: `logs`) |
| `starpoint help [command]` | General help, or one command's help (same as `<command> --help`) |

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

**Unattended runs that destroy something.** A run nobody reviews — `--yes` or
`--no-confirm`, `no_confirm = true`, or stdin that is not a terminal (a piped
`yes` counts: it answered without seeing the list) — is refused if it would
delete or replace anything, that is if any planned effect or configuration
change is marked destructive. Pass `--allow-destructive` to mean it. The
refusal exits 3 and prints every planned action and effect; under `--json` the
error document carries `code: "destructive-unattended"` and the whole plan, so
an agent calling Starpoint sees exactly what would have happened. Interactive
runs are unchanged: the confirmation step already lists every effect, the
destructive ones marked `!`. Dry runs are never refused.

```
$ starpoint remove ruff --yes
error [destructive-unattended]: refusing to remove unattended: it would delete or replace something (ruff) and --yes (or --no-confirm) was given. Nothing was changed. Review the planned changes, then rerun with --allow-destructive to allow it

Planned changes (none were made; ! removes or overwrites something):
  remove ruff via uv
      ! would uninstall ruff 0.5.0 (uv) at ~/.local/bin/ruff
      ! would edit ~/.starpoint.toml: remove ruff
      ! would delete symlink ~/.local/bin/ruff → ~/.local/share/uv/tools/ruff/bin/ruff
      ! ruff would stop resolving to ~/.local/bin/ruff
```

**What prune never touches.** `starpoint prune` only removes software inside the
home directory of the user it acts for (under `sudo`, the invoking user's; in a
plain root shell, root's own), and never a package manager: no registered
provider (npm, pip, uv, pnpm, yarn, cargo, ...), `corepack` or `starpoint`
itself, wherever it lives. A system npm prefix such as `/usr/lib/node_modules`
or the interpreter's own site-packages belongs to the machine, whoever runs
Starpoint. Each orphan it leaves alone for either reason is still listed, with
the reason, as a `prune-skipped` warning in human output and in `--json`.

**Running as root.** A command that changes anything is refused when Starpoint
runs as root (effective uid 0) or with an elevated Windows token, unless
`--allow-root` is given. Under `sudo` or `doas` at a terminal, answering yes to
a question that names whose environment will change stands in for the flag;
`--yes` does not answer it. Read-only commands and `doctor` always run. The
refusal exits 4 with `code: "elevated-refused"`.

```
$ sudo starpoint install ripgrep --yes
error [elevated-refused]: refusing to install as root (effective user id 0, root). Nothing was changed. Run it as your own user, or pass --allow-root to mean it (--yes does not answer this question; an unattended elevated run needs --allow-root)
```

Under `sudo` or `doas` from an ordinary user, Starpoint acts on *that user's*
environment, not root's: `HOME` is pointed at their home (from `SUDO_USER` /
`SUDO_UID` / `SUDO_GID` or `DOAS_USER`, resolved through the user database)
before anything reads it, so the configuration, `~/.local/bin`,
`~/.cargo/bin` and the shell profiles are theirs.

**Providers run as the account their work needs.** Providers that install into
the user's home run *as the user*: Starpoint starts their commands with the
user's uid, primary gid and supplementary groups (set by the kernel between
fork and exec, so the provider never runs as root) and an environment built
for the user rather than inherited from root. System providers run as root,
where root is the point, with root's own `HOME`: npm, gem and pip write caches
and logs under `$HOME`, and as root they would otherwise leave root-owned files
in the user's home.

| Provider | Under sudo/doas it runs as |
|---|---|
| `uv`, `pip` (always `--user`), `cargo`, `go`, `rustup`, `nix` | the invoking user |
| `npm`, `pnpm`, `yarn`, `gem` | the user when their global prefix / directory (asked as the user) is in the user's home or owned by them; root when it is a system location such as `/usr` |
| `brew` | the user when the Homebrew prefix belongs to them (Homebrew runs as the owner of its prefix and refuses root); otherwise root, which Homebrew itself then refuses |
| `apt`, `dnf`, `pacman`, `system`, `flatpak`, `snap` | root |
| `install .` (a project's own installer) | the user when the project directory is theirs |

A provider is classified once per run, so its inventory, install and
verification all run as the same account. The user's environment is `HOME`,
`USER`, `LOGNAME`, their login shell, a `PATH` that leads with their tool
directories (`~/.cargo/bin` or `$CARGO_HOME/bin`, Go's bin, `~/.local/bin`,
`PNPM_HOME`, `~/bin`) followed by sudo's own `PATH` minus anything in root's
home, their own `XDG_RUNTIME_DIR`, terminal, locale, proxy and certificate
settings, and each provider's configuration family (`UV_*`, `PIP_*`,
`CARGO_*`, `npm_config_*`, `GOPROXY`, ...). Location variables
(`CARGO_HOME`, `RUSTUP_HOME`, `GOPATH`, `GOBIN`, `UV_TOOL_DIR`,
`PYTHONUSERBASE`, `XDG_*_HOME`, ...) are kept only when they point into the
user's home, and nothing that points into root's home is ever passed on.

Before a changing command, Starpoint checks that it really can start a process
as the user (it runs `id -u` as them and checks they can write their home). If
the invoking user's ids or home cannot be resolved, or that check fails, the
command is refused with exit 4 and `code: "invoking-user-unresolved"` or
`"privilege-drop-failed"`, even with `--allow-root`: Starpoint does not quietly
run the user's providers as root instead. Read-only commands still run, with a
`privilege-drop-failed` warning. A plain root shell (or `sudo` from root), with
no invoking user, keeps acting on root's own environment as before.

What a run still has to fix:

- what a root provider creates in shared locations (`/usr/local`, `/opt`,
  npm's `<prefix>/lib/node_modules/<package>`, ...) is made readable by
  everyone, and its directories and executables executable by everyone (0755
  at least, 0644 for other files), so root's umask does not leave it usable
  only by root. Bits are only ever added; anything that existed before, and
  anything that looks like a key, token or credential, is left alone. What a
  provider wrote as the user keeps the user's own modes.
- anything found owned by root under the user's home (the paths an action
  touched, and the providers' state directories there: Cargo's home, Go's
  module and build caches, rustup's home, uv's tool environments and cache,
  pip's user site-packages, npm's cache and global modules) is given back to
  the user. After a provider that ran as the user nothing should be, so such a
  fix is marked `unexpected` and raises a `root-owned-after-drop` warning: it
  points to a bug or to a provider that escalated on its own.
- Starpoint's own configuration, audit log, history and locks are written by
  Starpoint itself and given back to the user at the end of every command.
  (Writing them as the user would mean switching the Starpoint process's own
  effective uid, which in Go applies to every thread at once and races with
  the provider commands it starts; the files are few and its own, so the
  chown-back is exact.)

Every fix is reported as an effect (`chown` or `chmod`, with evidence
`observed`) on the action that caused it and is in the audit log; a fix that
fails raises a `permission-fix-failed` warning. Every action reports `run_as`
(who each of its providers ran as, and whether privileges were dropped) in
JSON, in the audit log and, with `--verbose`, in human output:

```
$ sudo starpoint install ruff --provider uv --yes --allow-root --verbose
installed ruff via uv
  ...
  uv runs as alice (uid 1000, gid 1000), privileges dropped: uv installs tools into the user's home (~/.local/share/uv/tools, ~/.local/bin)
```

On Windows there is nothing to drop: an elevated administrator token is the
same account with more rights and carries no invoking user, so providers run
elevated and no ownership is reassigned; the `--allow-root` guard applies.

Exit status: `0` success, `1` failure, `2` usage error, `3` refused unattended
destructive run, `4` refused elevated run, `5` refused because a decision only
you can make was needed with nobody to ask (the refusal names the
candidates). A refusal changes nothing.

Global options work anywhere on the line: `--json`, `--provider <name>`
(repeatable, restricts automatic resolution), `--force`, `--verbose` (`-v`),
`--color <when>`, `--allow-destructive`, `--allow-root`, `--version`,
`--help`.
`--help` (or `-h`) after a command prints that command's help, as does
`starpoint help <command>`; it is never taken as a package name. Any other
unrecognised `-`-prefixed token is a usage error rather than a package name.

### Output

Human output is the default; `--json` produces the same structured data the
engine produced, not a re-parse of the text. Every read command supports it, so
another tool never has to parse a table.

Human output is colorized when it is going to a terminal and the environment
allows it. `--color always` forces color — into a pipe, into a pager, into a
log — and `--color never` turns it off; `auto`, the default, colors a terminal
and nothing else. `NO_COLOR` turns color off under `auto`, as does an unset or
`dumb` `TERM`. Color is decoration only: with the escapes stripped the output
reads the same, so `--json` is never colored and nothing has to be parsed out
of it.

Every change says what it does to the machine. Each action lists its effects:
the package installed or uninstalled, every file and link created, rewritten,
replaced or deleted in the places the provider writes to, commands that start
or stop resolving on PATH, shell profiles that changed, and the configuration
entries edited. Effects are observed on disk before and after each step, not
inferred, and anything that removes or overwrites something is marked
destructive. Before asking for confirmation every effect is listed, because a
question about changing the machine must not hide any of the changes behind a
count; destructive ones are marked with a `!`. In a human-readable result,
destructive effects are always listed and the rest are counted; `--dry-run`
lists every effect, and `--verbose` lists every effect of a real run too. Warnings carry a stable code
and are attached to the package they concern. Running as root, or as an
elevated Windows administrator, is reported on every changing command, saying
whose environment the run acts on.

Effects are watched in a bounded set of places: each provider's executable
directory (uv's and pip's user bin, Cargo's `CARGO_INSTALL_ROOT`/`CARGO_HOME`
bin or `~/.cargo/bin`, Go's `GOBIN`, `GOPATH/bin` or `~/go/bin`), the directory
of the provider's own binary, the known installations, what the name resolves
to on PATH, and the shell profiles. The home directory itself is never
snapshotted, even when a variable points there.

## Providers

Implemented in this release:

| Provider | Scope | Notes |
| --- | --- | --- |
| `system` | global | Read-only. Attributes `/usr/bin`-style software to the OS and refuses to mutate it. |
| `brew` | user | Formulae and casks. Cannot pin an arbitrary version, and says so. |
| `apt` | global | dpkg inventory, `pkg=version` pins, archive channel. |
| `dnf` | global | RPM inventory and versioned package installation. |
| `pacman` | global | Pacman inventory and repository installation. Arch repositories serve current versions only, so it cannot honour a version pin and says so rather than building a command that cannot work. |
| `nix` | user | Profile packages through `nix profile`, installed as `nixpkgs#name`. Uses the unified CLI with experimental features enabled for its own invocations, so a Nix that has not enabled flakes still works. A Nix older than 2.4, or one still driven through `nix-env`, is reported unsupported rather than half-managed. |
| `flatpak` | global | The system installation: application IDs, runtimes excluded, a branch (`stable`, `beta`) as the channel. Per-user installations are not enumerated or modified. |
| `snap` | global | snapd's inventory and channels. Never grants classic confinement on its own: that is a decision about unrestricted access, and the failure says so. |
| `uv` | user | Python tools and interpreters, `==` pins. |
| `pip` | user | Python packages, installed with `--user` so pip never needs root. Locations come from `pip show -f`: the console script when there is one, otherwise the package directory. Only what `pip list --user` names is user scope; the interpreter's own site-packages are reported as global. |
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
this project's development host is implemented, plus the non-Windows package
managers the model needed next: Nix profiles, Flatpak's system installation and
Snap. Docker and Podman manage containers rather than software on the machine.
Bun, Deno, winget, Chocolatey and version managers remain deferred.

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
what was left behind and, with `--discard`, clears it. JSON response shapes are
documented in [`docs/json-schema.md`](docs/json-schema.md).

Shell completions live in `completions/` and complete every command spelling,
each command's own flags, and provider names after `--provider`:

```
# bash
cp completions/starpoint.bash ~/.local/share/bash-completion/completions/starpoint
# zsh (any directory on $fpath, before compinit runs)
cp completions/starpoint.zsh ~/.zfunc/_starpoint
# fish
cp completions/starpoint.fish ~/.config/fish/completions/
```

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

No provider having it yet is not an error — it is the normal start of an
install — so an ordinary run says nothing about it: the confirmation already
says what will be installed and by whom. `--verbose` states it plainly
(`neofetch: not installed by any provider yet`), and names any provider that is
installed yet unable to enumerate its inventory, because that weakens the claim.
`starpoint info` always gives that qualification, since "no provider has it" is
its whole answer there.

When nothing on the machine and nothing about the name settles who should own
it, Starpoint asks. The project you run it in only settles it for a name with
that ecosystem's own shape: inside a Go repository `starpoint install
golang.org/x/tools/gopls` goes to `go`, but `starpoint install neofetch` is
resolved exactly as it would be anywhere else — a Go project says what the
project is written in, not where your tools come from, and `go install` cannot
install a bare name at all. (An npm scoped name, `@scope/name`, is the Node
equivalent; Rust, Python and Ruby names have no shape to check.) When the
question is asked anyway, the project's own provider is offered first among the
alternatives, and `go` is never offered for a name that is not a module path. That includes the common case of one system package manager
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

`sync` does not perform transitions: a package whose configured provider is
not the one that has it is reported as a `provider-drift` warning naming
`starpoint transition <package>` and `starpoint adopt <package> <owner>`, and
it stays unconverged. `starpoint transition` shows every pending move with its
phase and evidence, and `--json` exposes the whole record for other tools.

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
internal/elevation       whether the run is elevated, and whose environment it acts on
internal/privdrop        the invoking user's identity and environment for dropped providers
internal/align           ownership and permission fixes for elevated runs
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
make test-race # the suite under the race detector
make test-root # the tests that change owners and drop privileges for real (uses sudo)
```

Tests never modify the machine: providers talk to an interface, and the test
suite substitutes a scripted command runner and in-memory providers.

## Deliberately deferred

- HTTP API. The internal model is already machine-readable and `--json` exposes
  it. A server can be added later without reshaping anything.
- GUI.
- Windows. There are no Windows builds; the elevated-token handling below is
  kept so `go build` still works there, but Windows is not supported.
- Additional ecosystems such as Bun, Deno, mise/asdf, winget, Chocolatey,
  Composer, Maven/Gradle, NuGet, SwiftPM, CocoaPods, vcpkg and Conda remain
  candidates.
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
- Flatpak is managed as the system installation. An app installed with
  `flatpak install --user` is not enumerated and installing the same ID through
  Starpoint creates a separate system copy; Flatpak treats the two installations
  as independent, and Starpoint follows it rather than moving apps between them.
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
- Privilege dropping and ownership fixes run on Linux and macOS. On Windows an
  elevated token has no invoking user to return to and does not change file
  ownership the way root does, so nothing is dropped or reassigned there; the
  `--allow-root` guard still applies.
- Under sudo, a provider is classified once per run by where it installs. npm,
  pnpm, yarn and gem with a system prefix still run as root (and their shared
  files are widened); a per-command choice (say, `npm -g` as root but `npm ls`
  as the user) is not made, so a provider's inventory and its installs always
  agree on whose files they are.
- A planned `cargo install` names the command a crate installs when Cargo's
  inventory knows it (an installed crate) or the crate is a known rename
  (ripgrep → rg, fd-find → fd, bottom → btm, ...). For any other crate it
  assumes the crate's own name and says so; crates.io knows the binary names,
  but planning does not go to the network.
- `starpoint update` on a Rust toolchain runs `rustup toolchain install` for the
  channel, which moves a channel forward. A concrete toolchain pinned in the
  configuration is never moved.

## License

MIT — see [LICENSE](LICENSE).
