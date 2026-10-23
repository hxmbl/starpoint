# Starpoint JSON Schema

This document describes Starpoint's machine-readable output envelope and payloads. Every JSON response is a single flat object: an integer `schema_version` plus the command's own fields. Nothing is nested under a command name or under `data`.

## Schema Version

Current schema version: `9`

All JSON output, including errors, includes a top-level `schema_version` field. The version applies to the complete response envelope and every command payload. It is independent of the TOML configuration `schema` field. Every response is `{ "schema_version": 9, ...command fields }`; errors are `{ "schema_version": 9, "error": "..." }` on stderr, and a refusal adds `code`, `exit_code` and, when the command had a plan, `refused` (see "Errors and refusals").

Version 9 is the onboarding pass: Starpoint as an ownership ledger over the package managers already on the machine. It adds the read-only `inventory` command (every package each provider reports, managed or not). Every field a version 8 consumer read keeps its meaning. See "Migrating from schema 8 to 9".

Version 8 says what `prune` left alone and when a pip install is destructive. `prune` reports every orphan it does not remove, because it lives outside the home directory or is a package manager, as a `prune-skipped` warning (severity `info`) naming the package, its provider and the reason; version 7 dropped them silently. `install .` reports a `requirements.txt` install with pip as a planned effect either way: inside a virtualenv (the active `VIRTUAL_ENV` whose pip is on PATH, or the project's `.venv`) a `modify` of that environment, named by the new `dependencies.command.virtualenv`; outside one a destructive `replace` of the Python packages it names, with the new warning `pip-outside-virtualenv`, which an unattended run refuses (exit 3) without `--allow-destructive`. Every field a version 7 consumer read keeps its meaning. See "Migrating from schema 7 to 8".

Version 7 says who each action ran as. Every `ActionResult` (and every applied or planned `Transition`) gains `run_as`: one `RunAs` per provider the action runs, with the account (`user`, `uid`, `gid`), whether Starpoint was `elevated`, whether it `dropped` privileges to run the provider as the invoking user, and, for an elevated run on behalf of an invoking user, the provider's `privilege` (`user` or `root`) and the `reason`. `Effect` gains `unexpected`, set on an ownership fix made after a provider ran as the invoking user, which also raises the new warning `root-owned-after-drop`. The warning `privilege-drop-failed` and the refusal codes `invoking-user-unresolved` and `privilege-drop-failed` are new. Every field a version 6 consumer read keeps its meaning. See "Migrating from schema 6 to 7".

Version 6 adds refusals and the ownership and permission fixes of elevated runs. A command refused by a safety rule exits 3 (an unattended run that would delete or replace something, without `--allow-destructive`) or 4 (a changing command run as root, without `--allow-root`), and its error document gains a stable `code`, the `exit_code`, and `refused`: the operation and the `ActionResult`s it would have carried out, every planned effect included. `Effect.op` gains `chown` and `chmod`, and `Effect` gains an optional `error`. `Warning` gains the code `permission-fix-failed`; `running-elevated` now also says whose environment the run acts on. Every field a version 5 consumer read keeps its meaning. See "Migrating from schema 5 to 6".

Version 5 makes every action say what it did. Each `ActionResult` gains its outcome (`result`), its provider and version `transitions`, the `config_changes` it makes, every `effect` on the machine (paths created, modified, replaced or deleted; packages installed, uninstalled or changed; commands that start or stop resolving; shell profiles that changed), a `destructive` flag, and the structured `warnings` that concern it. `Warning` gains a stable `code`. Every command that changes anything (`install`, `remove`, `install .`, `update`, `prune`, `transition`) carries the same operation fields (`operation`, `dry_run`, `config_changed`, `config_path`, `actions`, `warnings`, `summary`), and `sync` carries `summary` too. See "Actions and effects" below and "Migrating from schema 4 to 5".

Version 4 unifies the action vocabulary. `install`, `remove` and `install .` now report the changes they made as `actions`, a list of `ActionResult`, the same shape `sync`, `update` and `prune` already used. The `operations` key, and the engine's `OperationResult` and `PlannedAction` it carried, are gone from JSON output. `install .` flattens the results of everything it runs into one `actions` list. This is a breaking change: a consumer that read `operations[].applied` must read `actions` instead. See "Action vocabularies" under Migrating.

Version 3 adds the logging surface described below. Every command that existed at version 2 returns the same payload it did, so this bump is conservative: a consumer that pins the version should widen its check rather than change how it reads anything it already read.

Version 2 flattened the envelope. Version 1 documented this flat shape, but the implementation nested several commands under their own key (`sync`, `doctor`, `explain`) and others under `data`, and `info` could emit more than one document. Version 2 is the documented shape made real; the payload fields themselves are unchanged, but a consumer that checked `schema_version == 1` must update that check.

## Common Types

### Package
```json
{
  "name": "string",
  "version": "string | null",
  "provider": "string | null",
  "scope": "string | null",
  "channel": "string | null",
  "reason": "string | null",
  "origin": {
    "path": "string",
    "project": "boolean"
  }
}
```

### Installation
```json
{
  "name": "string",
  "version": "string | null",
  "path": "string | null",
  "provider": "string",
  "scope": "string | null",
  "pinned": "boolean"
}
```

### Resolution
```json
{
  "provider": "string",
  "reason": "string | null",
  "confidence": "certain | high | low | speculative",
  "ambiguous": "boolean",
  "alternatives": ["string"],
  "forced": "boolean"
}
```

### State
```json
{
  "package": "Package",
  "resolution": "Resolution",
  "desired": "Installation | null",
  "current": "Installation | null",
  "elsewhere": ["Installation"],
  "status": "correct | missing | provider-differs | version-differs | version-unsatisfiable | unmanaged",
  "notes": ["string"]
}
```

### Warning
```json
{
  "severity": "info | warning | error",
  "code": "string",
  "package": "string | null",
  "provider": "string | null",
  "message": "string"
}
```

`code` is stable: a message may be reworded, a code keeps its meaning. A
consumer should act on the code and show the message. Codes in use:

| Code | Meaning |
| --- | --- |
| `running-elevated` | Starpoint runs as root (euid 0) or with an elevated Windows token. Under sudo or doas from an ordinary user, the run acts on that user's environment and gives them what it creates there; otherwise `~`, `~/.local/bin`, the configuration and the shell profiles are the elevated account's. A changing command only gets this far with `--allow-root` or an interactive confirmation. |
| `root-owned-after-drop` | After a provider ran as the invoking user (privileges dropped), something owned by root was still found in that user's home. It was given back (a `chown` effect with `unexpected: true`); it points to a bug or to a provider that escalated on its own. |
| `privilege-drop-failed` | An elevated run for an invoking user cannot run providers as that user: the user's ids or home could not be resolved, or a process could not be started as them. On a read-only command providers run as root and this is a warning; a changing command is refused (see "Errors and refusals"). On an action (severity `error`), a provider command could not be started as the user and was not run as root instead. |
| `permission-fix-failed` | An elevated run could not give a path to the invoking user (`chown`) or widen a shared path's permissions (`chmod`); the effect's `error` says why. The path may not be usable without root. |
| `shared-executable` | Two providers write the same executable (uv and pip `--user` both use `~/.local/bin`); the old copy is removed before the new one installs, and restored if the new one fails. |
| `not-on-path` | The directory a provider installs commands into is not on PATH. |
| `command-removed` | A command that resolved on PATH before the action no longer does. |
| `shell-profile-changed` | A shell start-up file (`~/.profile`, `~/.bashrc`, `~/.zshrc`, ...) changed while the action ran. |
| `verification-failed` | The provider installed the package but its executable check failed. |
| `recorded-not-installed` | The decision was recorded in the configuration, but nothing was installed. |
| `transition-incomplete` | A provider move stopped short of a resting phase, or failed. |
| `transition-left-behind` | Ownership moved but the previous provider's copy is still present. |
| `pin-conflict` | A newer version exists than the pin; the pin was kept. |
| `pin-kept`, `already-current`, `update-unsupported`, `not-installed` | Why `update` left a package alone (severity `info`). |
| `discover-failed` | A provider could not list its packages, so `prune` considered none of them, or `inventory` shows none of them (including a provider whose health check or listing did not answer within the 20-second bound). |
| `duplicate-install` | `install` would add a second copy beside another provider's (or the operating system's); shown on the action before the confirmation. Unattended, the same condition is the `duplicate-install` refusal. |
| `provider-drift` | `sync` found a package installed through another provider than the configuration gives it. Nothing was moved or removed; the message names `starpoint transition <package>` and `starpoint adopt <package> <owner>`. |
| `owner-ambiguous` | `adopt --all` found a name installed on request through more than one provider and nobody to ask; it was left out. |
| `requested-unknown` | `inventory` could not ask a provider which of its packages were installed on purpose; they are all shown as `requested: unknown`. |
| `prune-skipped` | An orphan (installed, declared nowhere) that `prune` left alone, with its `package` and `provider`; the message gives the reason: it lives outside the invoking user's home directory (a system npm prefix, the interpreter's own site-packages), or it is a package manager (any registered provider, `corepack`, `starpoint`). Severity `info`; reported by dry runs and real runs alike. |
| `pip-outside-virtualenv` | `install .` runs `pip install -r requirements.txt` outside any virtualenv, so it installs into the user's site-packages (as root, the system's) and may upgrade or downgrade packages other software depends on. Its effect is destructive; creating a virtualenv (`python3 -m venv .venv`) makes Starpoint install there instead. |
| `toolchain-missing` | `install .` found the project's toolchain absent. |
| `duplicate-declaration`, `provider-unknown`, `provider-unavailable`, `project-scope-undeclared`, `owner-unavailable` | Configuration and availability problems found while planning. |
| `duplicate-copy`, `shadowed`, `missing`, `version-unsatisfiable` | `doctor` problems. |

## Actions and effects

Every command that changes anything reports the change as a list of
`ActionResult` under `actions`, with the same top-level operation fields:

```json
{
  "operation": "install | remove | update | prune | transition | transition-reverse",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary"
}
```

`transition` omits `config_changed` and `config_path`. `sync` keeps its own
shape (below) and adds `summary`.

### ActionResult

```json
{
  "action": "install | remove | update | adopt | relocate | record | reinstall",
  "package": "string",
  "provider": "string | null",
  "note": "string | null",
  "result": "planned | applied | failed | reported | not_run",
  "dry_run": "boolean",
  "destructive": "boolean",
  "error": "string | null",
  "transitions": ["ActionTransition"],
  "config_changes": ["ConfigChange"],
  "effects": ["Effect"],
  "warnings": ["Warning"],
  "run_as": ["RunAs"]
}
```

`result` is `planned` in a dry run, `applied` once done, `failed` when the
action was attempted and did not complete (its `effects` still list whatever
did change, and `error` says why), `reported` for a conflict Starpoint reports
but will not resolve, and `not_run` for a planned action an earlier failure
stopped. `destructive` is true when any effect or configuration change is.
Collections are always arrays, never null.

### RunAs

```json
{
  "provider": "string",
  "user": "string",
  "uid": "integer",
  "gid": "integer",
  "elevated": "boolean",
  "dropped": "boolean",
  "privilege": "user | root | null",
  "reason": "string | null"
}
```

The account a provider's commands ran as for this action, or will run as in a
plan. `run_as` lists one entry per provider the action runs: the action's
provider, then, for a relocation, the provider it moves away from. It is empty
for an action that runs no provider.

- An ordinary run: Starpoint's own account, `elevated: false`,
  `dropped: false`, no `privilege`.
- An elevated run under sudo or doas from an ordinary user: each provider is
  classified once per run. `privilege: "user"` providers ran as the invoking
  user (`dropped: true`, `user`/`uid`/`gid` are theirs); `privilege: "root"`
  providers ran as root. `reason` names why: `"uv installs tools into the
  user's home (~/.local/share/uv/tools, ~/.local/bin)"`, `"npm's global prefix
  /usr is a system location"`.
- A plain root run (no invoking user), or one whose invoking user could not be
  acted for: root, `elevated: true`, `dropped: false`, and `reason` says which.
- `uid` and `gid` are -1 on Windows, which has no numeric ids; nothing is
  ever dropped there.

| Provider | `privilege` under sudo or doas |
| --- | --- |
| `uv`, `pip`, `cargo`, `go`, `rustup`, `nix` | `user` |
| `npm`, `pnpm`, `yarn`, `gem` | `user` when the global prefix or directory (asked as the user) is in their home or owned by them, `root` otherwise |
| `brew` | `user` when the Homebrew prefix belongs to them, `root` otherwise (Homebrew then refuses root itself) |
| `apt`, `dnf`, `pacman`, `system`, `flatpak`, `snap` | `root` |

### ActionTransition

```json
{
  "kind": "provider | version",
  "from": "string | null",
  "to": "string | null",
  "phase": "planned | installed | verified | completed | failed | blocked | null"
}
```

A compact before-and-after: `{"kind": "provider", "from": "uv", "to": "pip"}`
or `{"kind": "version", "from": "0.5.0", "to": "0.6.0"}`. `phase` is set for a
provider move. A planned `update` without a pin reports `to: "newest"`, since
the version is only known once the provider has run. The full move record,
with verification, stays in the `Transition` type.

### ConfigChange

```json
{
  "path": "string",
  "change": "add | remove | update",
  "package": "string",
  "fields": [{ "field": "provider | version | scope | channel | reason", "from": "string | null", "to": "string | null" }],
  "creates_file": "boolean",
  "destructive": "boolean",
  "applied": "boolean"
}
```

A change is destructive when it removes an entry or overwrites a value the
file already held (a recorded provider, a version pin). Filling in a value the
entry did not have is not destructive; `reason` is commentary and never is.
`applied` is false in a dry run.

### Effect

```json
{
  "op": "create | modify | replace | delete | chown | chmod",
  "kind": "package | executable | symlink | file | directory | config | command | shell_profile",
  "path": "string | null",
  "name": "string | null",
  "from": "string | null",
  "to": "string | null",
  "provider": "string | null",
  "destructive": "boolean",
  "environment": "boolean",
  "evidence": "observed | reported | planned",
  "detail": "string | null",
  "error": "string | null",
  "unexpected": "boolean | absent"
}
```

One concrete change. `kind` says what was touched: a `package` as its provider
knows it (`from`/`to` are versions), a file, `symlink` (`from`/`to` are link
targets), `directory`, Starpoint's own `config` file, a `command`'s resolution
on PATH (`from`/`to` are resolved paths), or a `shell_profile`.

- `replace` means a different file now sits at the path: a link retargeted, a
  link turned into a script, or a file deleted and written again. `modify`
  means the file was rewritten in place. A file deleted and written again can
  appear as `modify` when the filesystem gives the new file the old inode
  (ext4 commonly does); both are destructive, so read `destructive` rather
  than telling the two apart.
- `destructive` marks anything that removes or overwrites what was there: every
  delete and replace, every in-place rewrite of a file, a command that stops
  resolving, a destructive configuration change. Shell profiles that are
  edited in place are not marked destructive but carry the
  `shell-profile-changed` warning. An `update` that rewrites its own
  executable is destructive in this sense: the previous version's file is gone.
- `chown` and `chmod` are the fixes an elevated run makes after an action, on
  the paths that action touched. `chown`: a path under the target user's home
  (or a provider state directory there) that ended up owned by root was given
  to that user; `from`/`to` are `uid:gid`. `chmod`: a path the action created
  in a shared location was made readable (and, for directories and
  executables, executable) by everyone; `from`/`to` are octal modes, and bits
  are only ever added. A directory's fixes below it are counted in `detail`
  rather than listed. Neither is destructive. `error` is set when the fix
  failed, together with a `permission-fix-failed` warning. After a provider
  that ran as the invoking user nothing should need a `chown`; one that does
  is still made, carries `unexpected: true`, and raises
  `root-owned-after-drop`. What a provider wrote as the user is never
  `chmod`ed: it keeps the user's own modes.
- `environment` marks effects that change what a shell sees: anything in a PATH
  directory, a command's resolution, a shell profile.
- `evidence` says how Starpoint knows. `observed`: the location was read
  immediately before and after the step and the difference was seen.
  `reported`: the provider said so (a path its install returned, or the
  package-level record of an install or uninstall the provider carried out),
  but the location was not watched beforehand. `planned`: a prediction, in a
  dry run or before the confirmation question.

Effects are net changes between the start and the end of an action. Starpoint
watches, around each provider operation: the provider's executable directory
(uv and pip's user bin; Cargo's `CARGO_INSTALL_ROOT/bin`, `CARGO_HOME/bin` or
`~/.cargo/bin`; Go's `GOBIN`, first `GOPATH` entry's `bin` or `~/go/bin`), the
directory of the provider's own binary (where Homebrew and npm put commands),
the path and directory of every installation already known for the package,
the directory of whatever the name resolves to on PATH, and the user's shell
profiles. The set is bounded: a directory that resolves to the home directory,
one of its ancestors or a filesystem root is never snapshotted. A move appears as a delete and a create, because
Starpoint sees paths, not system calls. Something a provider writes outside
those locations, and does not report, is not seen.

### Summary

```json
{
  "actions": "number",
  "by_action": { "install": "number" },
  "by_result": { "applied": "number" },
  "effects": "number",
  "destructive": "boolean",
  "destructive_effects": "number",
  "destructive_packages": ["string"],
  "warnings": { "warning": "number" },
  "config_files": ["string"]
}
```

The summary only counts and names; paths and details live in the actions,
once. `warnings` counts the top-level warnings by severity.

### Warnings at two levels

The top-level `warnings` is the complete list for the operation, each warning
once. Each action's `warnings` holds the warnings that concern it: those
raised by the action and every top-level warning that names its package. Every
action warning also appears at the top level. Warnings about the run itself,
such as `running-elevated`, name no package and appear only at the top level.

### Sync

`sync` reports the operation fields above (without `operation`) plus:

```json
{
  "converged": "boolean",
  "transitions": ["Transition"],
  "states": ["State"],
  "problems": ["string"],
  "project": "ProjectInfo | null"
}
```

`sync` never removes or moves software. A package whose configured provider
is not the one that has it produces no action and no `transitions` entry: it
keeps the status `provider-differs`, so `converged` is false, and the warning
`provider-drift` names the package, the configured `provider`, and in its
message the provider that has it and the commands that settle it
(`starpoint transition <package>`, `starpoint adopt <package> <owner>`).
`transitions` is therefore always empty for `sync`; `transition` reports
moves.

In a dry run, `states` and `converged` describe the machine as it is. After
a real run that changed something they describe the machine as the run left
it: `converged` is true only when every action applied and every package is
now satisfied.

### Inventory

`starpoint inventory [--provider <name>]...` only observes: providers are
probed for health and asked to list what they have installed (Discover),
nothing else. It works with no configuration, resolves nothing, installs
nothing, writes no configuration and leaves no audit entry. It reports:

```json
{
  "schema_version": 9,
  "providers": [
    {
      "name": "string",
      "available": "boolean",
      "reason": "string | null",
      "listed": "boolean",
      "count": "integer",
      "error": "string | null",
      "requested": "reported | listing | unknown | null"
    }
  ],
  "packages": [
    {
      "provider": "string",
      "name": "string",
      "version": "string | null",
      "path": "string | null",
      "scope": "global | user | project | null",
      "managed": "boolean",
      "requested": "requested | dependency | unknown"
    }
  ],
  "warnings": ["Warning"],
  "summary": { "packages": "integer", "managed": "integer", "unmanaged": "integer", "requested": "integer" }
}
```

`providers` has one entry per registered provider (or per `--provider`), in
registration order. `listed` is true when the provider was asked and
answered; it is false for an unavailable provider, one that cannot enumerate
(`reason` says so), and one whose listing failed (`error`, plus a
`discover-failed` warning naming the `provider`; the other providers are still
listed). Each provider has 20 seconds to answer (the same bound as the
per-package survey); one that has not answered by then is reported the same
way, with an `error` and warning message saying it timed out, so a hung
package manager never stalls the command. Health checks run first,
concurrently, each with its own 20 seconds; a provider whose health check
times out or is cancelled is reported the same way too (`available` false,
an `error`, and a `discover-failed` warning saying it could not be checked),
never as a quietly unavailable provider. `packages` are sorted by provider in registration order, then name.
`name` is the provider's own name. `path` is what the provider reported or,
failing that, the PATH entry attributed to the same provider; absent when
neither says. `managed` is true only when the configuration has an entry with
this `name` and this `provider`: an entry for the same name with another
provider, or with no provider recorded yet, leaves it `false`.

`requested` says whether the user asked for the package, as the provider
knows it. A provider's `requested` says how it knows (present only when it
was listed):

| Provider `requested` | Providers | Meaning |
| --- | --- | --- |
| `reported` | apt (`apt-mark showmanual`), dnf (`dnf repoquery --installed --queryformat '%{name} %{reason}\n'`), pacman (`pacman -Qqe`), brew (`brew list --formula --installed-on-request`; casks: each cask's install receipt) | The tool records why each package is installed. A package is `requested` or `dependency`, or `unknown` where the record does not say. dnf: only the `user` reason is `requested` and only `dependency` or `weak-dependency` (DNF 5: `Dependency`, `Weak Dependency`) is `dependency`; `group`, `unknown`/`None`, `clean` and DNF 5's `External User` are `unknown` (`--userinstalled` is not used: it also returns group and module-profile packages and unknown reasons). brew: with an older Homebrew that lacks that flag, `brew leaves --installed-on-request` names some requested formulae and the rest are `unknown`, never `dependency`; when the cask list cannot be read, only names `brew list --formula` confirms as formulae can be `dependency`, the rest are `unknown`. brew casks: `brew info --json=v2` carries no install reason for casks, so the reason is `installed_on_request` in the cask's install receipt (`<caskroom>/<token>/.metadata/INSTALL_RECEIPT.json`, written by Homebrew 4.4.0 and later; an early receipt's `installed_as_dependency: true` also means `dependency`). A cask with no receipt (installed before 4.4.0), or whose receipt says neither, is `dependency` when another installed cask depends on it (from the other casks' receipts or `brew info --json=v2 --installed --cask`), and otherwise `unknown`; a cask is never assumed `requested`. |
| `listing` | uv, cargo, npm, pnpm, go, rustup, nix, flatpak | What the tool lists is only what was installed on purpose (uv tools, `cargo install`, global packages at depth 0, installed Go binaries, toolchains, the Nix profile, Flatpak applications without runtimes), so every package is `requested`, except a package manager itself (any provider name, `corepack`, `starpoint`), which is `unknown`. |
| `unknown` | pip, gem, yarn, snap, system | The listing mixes requested software with dependencies and the tool has no record to tell them apart; every package is `unknown`. |

When a `reported` provider's question fails, its packages are all `unknown`
and the warning `requested-unknown` names the provider.

### Adopt

`adopt` reports the operation fields above (`operation: "adopt"`) plus:

```json
{
  "skipped": [{ "package": "string", "provider": "string | null", "reason": "string" }],
  "left_unmanaged": [{ "provider": "string", "dependency": "integer", "unknown": "integer" }]
}
```

Every action is `adopt` (`result` `planned` in a dry run, `applied`
otherwise) with one configuration change and no machine effect: adopting
never installs, uninstalls or moves anything. A package no provider has, or
that the named provider does not have, is an error and nothing is recorded;
software only the `system` provider has is never adopted. `skipped` names
what was looked at and left alone, with the reason (already recorded; with
`--all`, a name the configuration records with another provider, with the
`transition` or `adopt` command that settles it, or one several providers
claim, which also raises the warning `owner-ambiguous` when nobody could be
asked). `left_unmanaged` is `--all` only: per provider, how many unmanaged
packages it left because the provider calls them dependencies or cannot say
(`inventory`'s `requested`). The system provider is not counted.

### Install .

`install .` reports the operation fields above plus `dependencies`, the
project's own dependency installer:

```json
{
  "command": {
    "tool": "string",
    "args": ["string"],
    "dir": "string",
    "reason": "string",
    "virtualenv": "string | null"
  },
  "skipped": "string | null",
  "ran": "boolean",
  "dry_run": "boolean",
  "effects": ["Effect"]
}
```

`command` is absent when nothing says how to install the dependencies, and
`skipped` says why (or `declined`). `virtualenv` (version 8) is set only for a
pip install that goes into a virtualenv: the active `VIRTUAL_ENV` when the pip
on PATH is its own, otherwise the project's `.venv`, whose own pip is then
the `tool`. A pip install outside any virtualenv carries a destructive planned
effect, `{ "op": "replace", "kind": "package", "name": "the Python packages
requirements.txt names", "destructive": true, "evidence": "planned" }`, in
both a dry run and a real run, because what pip changes in site-packages is
not observed; inside a virtualenv the planned effect is a non-destructive
`modify` of the environment's directory. `npm ci` over an existing
`node_modules` and `uv sync` over an existing `.venv` are destructive planned
`replace` effects too.

### Example: a destructive removal

`starpoint remove ruff --yes --allow-destructive --json`, where uv had linked ruff into `~/.local/bin`:

```json
{
  "schema_version": 9,
  "operation": "remove",
  "dry_run": false,
  "config_changed": true,
  "config_path": "/home/u/.starpoint.toml",
  "actions": [
    {
      "action": "remove",
      "package": "ruff",
      "provider": "uv",
      "result": "applied",
      "dry_run": false,
      "destructive": true,
      "transitions": [],
      "config_changes": [
        {
          "path": "/home/u/.starpoint.toml",
          "change": "remove",
          "package": "ruff",
          "fields": [{ "field": "provider", "from": "uv" }],
          "destructive": true,
          "applied": true
        }
      ],
      "effects": [
        { "op": "delete", "kind": "package", "name": "ruff", "from": "0.5.0", "provider": "uv",
          "path": "/home/u/.local/bin/ruff", "destructive": true, "evidence": "reported", "detail": "uv uninstalls ruff" },
        { "op": "modify", "kind": "config", "path": "/home/u/.starpoint.toml", "name": "ruff",
          "destructive": true, "evidence": "observed", "detail": "remove ruff" },
        { "op": "delete", "kind": "symlink", "path": "/home/u/.local/bin/ruff",
          "from": "/home/u/.local/share/uv/tools/ruff/bin/ruff", "provider": "uv",
          "destructive": true, "environment": true, "evidence": "observed" },
        { "op": "delete", "kind": "command", "name": "ruff", "from": "/home/u/.local/bin/ruff", "provider": "uv",
          "destructive": true, "environment": true, "evidence": "observed", "detail": "ruff no longer resolves on PATH" }
      ],
      "warnings": [
        { "severity": "warning", "code": "command-removed", "package": "ruff", "provider": "uv",
          "message": "ruff no longer resolves on PATH (it was /home/u/.local/bin/ruff)" }
      ],
      "run_as": [
        { "provider": "uv", "user": "u", "uid": 1000, "gid": 1000, "elevated": false, "dropped": false }
      ]
    }
  ],
  "warnings": [
    { "severity": "warning", "code": "command-removed", "package": "ruff", "provider": "uv",
      "message": "ruff no longer resolves on PATH (it was /home/u/.local/bin/ruff)" }
  ],
  "summary": {
    "actions": 1, "by_action": { "remove": 1 }, "by_result": { "applied": 1 },
    "effects": 4, "destructive": true, "destructive_effects": 4,
    "destructive_packages": ["ruff"], "warnings": { "warning": 1 },
    "config_files": ["/home/u/.starpoint.toml"]
  }
}
```

## Errors and refusals

Every error is one JSON document on stderr:

```json
{ "schema_version": 9, "error": "string" }
```

A refusal is an error that a flag would have allowed. Nothing was changed. It
adds a stable `code`, the process `exit_code`, and, for a command that had
planned its work, `refused`:

```json
{
  "schema_version": 9,
  "error": "string",
  "code": "destructive-unattended | elevated-refused | invoking-user-unresolved | privilege-drop-failed | owner-ambiguous | duplicate-install | provider-ambiguous",
  "exit_code": 3,
  "package": "string (decision refusals, exit 5)",
  "candidates": ["string (decision refusals, exit 5)"],
  "refused": {
    "operation": "sync | install | remove | update | prune | transition | adopt",
    "actions": ["ActionResult"],
    "warnings": ["Warning"],
    "summary": "Summary"
  }
}
```

| Code | Exit | Meaning |
| --- | --- | --- |
| `destructive-unattended` | 3 | The run is unattended (`--yes`/`--no-confirm`, `no_confirm = true`, or stdin is not a terminal) and would delete or replace something: an action in `refused.actions` has `destructive: true`. Rerun with `--allow-destructive` (and `--yes` when there is no terminal at all). |
| `elevated-refused` | 4 | A command that changes anything ran as root or elevated, without `--allow-root` and without an interactive confirmation (only possible under sudo or doas at a terminal). `refused` is absent: the refusal happens before any planning. |
| `invoking-user-unresolved` | 4 | Under sudo or doas from an ordinary user, that user's uid, gid or home could not be resolved (or HOME could not be pointed at it). Starpoint will not run their providers as root instead, and `--allow-root` does not change that. `refused` is absent. |
| `privilege-drop-failed` | 4 | Under sudo or doas from an ordinary user, a process could not actually be started as that user (Starpoint runs `id -u` as them before changing anything and checks they can write their home): no right to switch users, a uid the system rejects, an unwritable home. `--allow-root` does not change it. `refused` is absent. |
| `duplicate-install` | 5 | An unattended `install` would add a second copy: another provider already has the software (`candidates` names it, whoever chose the target provider), or only the operating system has it and Starpoint picked the provider (`candidates` is `["system"]`). Rerun with `--allow-duplicate`, name the provider for a managed copy beside the system's, or use `adopt`/`transition`. `refused` holds the planned install. |
| `provider-ambiguous` | 5 | An unattended `sync` would install a package with no recorded provider whose provider is ambiguous. `package` is the first such package (the message names all), `candidates` the providers that could own it. Record a `provider` or install it with the provider named. `refused` holds the planned actions. |
| `owner-ambiguous` | 5 | `adopt <package>` without a provider found the software installed through more than one provider and had no terminal to ask on. `package` names it and `candidates` the providers that have it; name the owner (`starpoint adopt <package> <provider>`). `refused` is absent. |

Exit 5 is the decision refusal: a choice only the user can make was needed
and nobody could be asked. Such a refusal carries `package` and `candidates`.

Every action in `refused.actions` has `result: "planned"` and `dry_run: true`,
and lists every planned effect and configuration change, destructive or not.

Exit status for every command: `0` success, `1` failure, `2` usage error, `3`,
`4` and `5` the refusals above.

### Example: an unattended removal refused

`starpoint remove ruff --yes --json`:

```json
{
  "schema_version": 9,
  "error": "refusing to remove unattended: it would delete or replace something (ruff) and --yes (or --no-confirm) was given. Nothing was changed. Review the planned changes, then rerun with --allow-destructive to allow it",
  "code": "destructive-unattended",
  "exit_code": 3,
  "refused": {
    "operation": "remove",
    "actions": [
      {
        "action": "remove", "package": "ruff", "provider": "uv",
        "result": "planned", "dry_run": true, "destructive": true,
        "transitions": [],
        "config_changes": [
          { "path": "/home/u/.starpoint.toml", "change": "remove", "package": "ruff",
            "fields": [{ "field": "provider", "from": "uv" }], "destructive": true, "applied": false }
        ],
        "effects": [
          { "op": "delete", "kind": "package", "name": "ruff", "from": "0.5.0", "provider": "uv",
            "path": "/home/u/.local/bin/ruff", "destructive": true, "evidence": "planned", "detail": "uv uninstalls ruff" },
          { "op": "modify", "kind": "config", "path": "/home/u/.starpoint.toml", "name": "ruff",
            "destructive": true, "evidence": "planned", "detail": "remove ruff" },
          { "op": "delete", "kind": "symlink", "path": "/home/u/.local/bin/ruff", "provider": "uv",
            "destructive": true, "environment": true, "evidence": "planned", "detail": "the install location uv reports" }
        ],
        "warnings": [],
        "run_as": [
          { "provider": "uv", "user": "u", "uid": 1000, "gid": 1000, "elevated": false, "dropped": false }
        ]
      }
    ],
    "warnings": [],
    "summary": {
      "actions": 1, "by_action": { "remove": 1 }, "by_result": { "planned": 1 },
      "effects": 3, "destructive": true, "destructive_effects": 3,
      "destructive_packages": ["ruff"], "warnings": {},
      "config_files": ["/home/u/.starpoint.toml"]
    }
  }
}
```

### Example: a user-level provider run as the invoking user

`sudo starpoint install ruff --provider uv --allow-root --yes --json`, run by
alice (uid 1000). uv runs as alice, so what it writes is hers from the start
and there is nothing to fix:

```json
"effects": [
  { "op": "create", "kind": "package", "name": "ruff", "to": "0.6.9", "provider": "uv",
    "path": "/home/alice/.local/bin/ruff", "destructive": false, "evidence": "reported", "detail": "uv installs ruff" },
  { "op": "create", "kind": "symlink", "path": "/home/alice/.local/bin/ruff", "provider": "uv",
    "destructive": false, "environment": true, "evidence": "observed" }
],
"run_as": [
  { "provider": "uv", "user": "alice", "uid": 1000, "gid": 1000, "elevated": true, "dropped": true,
    "privilege": "user", "reason": "uv installs tools into the user's home (~/.local/share/uv/tools, ~/.local/bin)" }
]
```

### Example: ownership and permission fixes

A system provider still runs as root, and what it creates in shared locations
is widened. `sudo starpoint install prettier --provider npm --allow-root --yes
--json` with npm's prefix at `/usr/local` (root's) reports:

```json
"effects": [
  { "op": "create", "kind": "symlink", "path": "/usr/local/bin/prettier", "provider": "npm",
    "destructive": false, "environment": true, "evidence": "observed" },
  { "op": "chmod", "kind": "directory", "path": "/usr/local/lib/node_modules/prettier", "from": "0700", "to": "0755",
    "provider": "npm", "destructive": false, "evidence": "observed",
    "detail": "made readable (and, for directories and executables, executable) by everyone, so the elevated account's umask does not leave it usable only by root; 212 entries below" }
],
"run_as": [
  { "provider": "npm", "user": "root", "uid": 0, "gid": 0, "elevated": true, "dropped": false,
    "privilege": "root", "reason": "npm's global prefix /usr/local is a system location" }
]
```

Something left to root in the user's home after a dropped run is still given
back, and flagged:

```json
{ "op": "chown", "kind": "file", "path": "/home/alice/.cargo/.package-cache", "from": "0:0", "to": "1000:1000",
  "provider": "cargo", "destructive": false, "evidence": "observed", "unexpected": true,
  "detail": "given to alice, for whom the run acted, so it is not left owned by root; unexpected: cargo ran as alice, so nothing it wrote should have been root's" }
```

with a `root-owned-after-drop` warning on the action.

## Transition Schema

```json
{
  "package": "Package",
  "from": {
    "name": "string",
    "kind": "string"
  },
  "to": {
    "name": "string",
    "kind": "string"
  },
  "phase": "string",
  "previous": "Installation | null",
  "installed": "Installation | null",
  "verification": {
    "method": "string",
    "success": "boolean",
    "reason": "string | null"
  },
  "old_removed": "boolean",
  "left_behind_reason": "string | null",
  "recoverable": "boolean",
  "advice": "string | null",
  "note": "string | null",
  "effects": ["Effect"],
  "warnings": ["Warning"],
  "run_as": ["RunAs"]
}
```

`run_as` (version 7) is who the target provider, then the provider moved away
from, ran as. `effects` is what the move changed, observed while it ran (or predicted, in a
dry run); `warnings` carries move-specific warnings such as
`shared-executable`. Both are absent on a transition that has only been
inspected.

## ProjectInfo Schema

```json
{
  "root": "string",
  "dir": "string",
  "kind": "string",
  "marker": "string | null",
  "tools": ["string"],
  "toolchain": ["string"],
  "has_project_config": "boolean"
}
```

## Migrating from schema 1 to 2

Version 2 flattened the envelope. The payload fields are largely the same; what
changed is where they sit, and what `info` returns. Every response is still a
single object.

| Command | Version 1 | Version 2 |
| --- | --- | --- |
| `list` | `{schema_version, packages, unaccounted_path}` | unchanged |
| `init` | `{schema_version, path, created, replaced}` | unchanged |
| `transition` (listing) | `{schema_version, dry_run, transitions, pending}` | unchanged |
| `info` | `{schema_version, package}`, then for an unmanaged package or a pending move a **second** document: `{schema_version, data: {lookup}}` or `{schema_version, data: {transition}}` | one document: `{schema_version, package, lookup?, transition?}` |
| `explain` | `{schema_version, explain: {...}}` | `{schema_version, package, final, steps}` |
| `sync` | `{schema_version, sync: {...}}` | `{schema_version, ...sync fields}` |
| `doctor` | `{schema_version, doctor: {...}, providers, healthy}` | `{schema_version, starpoint, config, providers, ownership, path_inventory, path_summary, problems, healthy}` |
| `install`, `remove`, `install .` | `{schema_version, data: {...}}` | `{schema_version, operations, lookup?, dependencies?}` |
| `update` | `{schema_version, data: {operation, dry_run, actions, warnings, pin_conflicts}}` | `{schema_version, operation, dry_run, config_changed, config_path, actions, warnings, pin_conflicts}` |
| `transition` (applying) | `{schema_version, data: {operation, dry_run, transitions}}` | `{schema_version, operation, dry_run, transitions}` |
| `prune` | `{schema_version, data: {operation, dry_run, warnings}}` | `{schema_version, operation, dry_run, actions, warnings}`; `actions` is new |
| `recover` | `{schema_version, data: {...}}` | `{schema_version, operation, interrupted, cleaned, remaining, sole_copies}` |
| `runs` / `history` | `{schema_version, data: {executions}}`, `null` when empty | `{schema_version, executions}`, `[]` when empty |
| errors | `{schema_version: 1, error}` | `{schema_version: 2, error}` |

A consumer has to handle five things:

1. The version number moved from 1 to 2.
2. `sync`, `doctor`, `explain`, `update`, `transition`, `prune`, `recover`, `runs`, `install`, `remove` and `install .` moved their payload out of `data`, or out of a command-named key, to the top level. Read `payload["sync"]` as `payload`.
3. `info --json` is now parseable. Previously a provider survey or a pending transition arrived as a second JSON object, so `json.Unmarshal` failed with "Extra data"; read `payload["lookup"]` or `payload["transition"]` instead.
4. `doctor` dropped the `doctor` wrapper and the duplicate top-level `providers` and `healthy`. The fields themselves are unchanged and now appear once.
5. `prune` gained `actions`, so a dry run reports what it would remove instead of an empty result.

`list`, `init` and the `transition` listing changed only their version number.

### Action vocabularies

Up to version 3, two shapes described one change:

- `ActionResult` — `{action, package, provider, note, result}` — in `sync`, `update` and `prune`.
- `PlannedAction` — `{action, state, config_change, transition, note}` — in `install`, `remove` and `install .`, under `operations[].applied`.

Version 4 drops `PlannedAction` and `operations` from JSON output. Every command
that performs changes now reports `actions` as a list of `ActionResult`, and
`install .` flattens its results into that one list. A consumer that read
`operations[].applied` reads `actions` instead.

## Migrating from schema 4 to 5

Version 5 adds fields and gives one existing field a value; nothing a version 4
consumer read is removed or renamed.

1. The version number moved from 4 to 5.
2. `ActionResult.result` was always empty; it now carries the outcome
   (`planned`, `applied`, `failed`, `reported`, `not_run`).
3. `ActionResult` gains `dry_run`, `destructive`, `error`, `transitions`,
   `config_changes`, `effects` and `warnings`.
4. `install`, `remove` and `install .` gain the operation fields `operation`,
   `dry_run`, `config_changed`, `config_path`, `warnings` and `summary`, which
   version 4 had dropped along with `operations`. `update` and `prune` gain
   `summary` (and `prune` the config fields); `sync` gains `summary` and always
   carries `warnings`, as `[]` when empty.
5. Performing a transition gains `actions`, `warnings` and `summary` beside its
   `transitions`; `Transition` gains `effects` and `warnings`.
6. `Warning` gains `code`. `update`'s skip warnings now name their package.
7. `sync`'s `actions` now report what was applied (with its observed effects)
   rather than the plan entry, and a failed or unreached action says so in
   `result`.
8. `prune` reports a removal that failed as a `failed` action instead of only
   in the error message, and reports what it did remove before failing.

## Migrating from schema 5 to 6

Version 6 adds; nothing a version 5 consumer read is removed or renamed.

1. The version number moved from 5 to 6.
2. Errors can be refusals: exit status 3 or 4, with `code`, `exit_code` and
   (for status 3) `refused` in the error document. A consumer that treated
   every nonzero exit as "the command failed" keeps working; one that wants to
   retry with the right flag reads `code`.
3. Unattended runs that would delete or replace anything now fail with status
   3 unless `--allow-destructive` is passed, where version 5 proceeded. This is
   a behaviour change for scripts that pass `--yes` to `remove`, `sync`,
   `transition`, `prune`, or an `install`/`update` that overwrites something.
4. `Effect.op` gains `chown` and `chmod`, and `Effect` gains `error`. They only
   appear on elevated runs.
5. `Warning.code` gains `permission-fix-failed`.

## Migrating from schema 6 to 7

Version 7 adds; nothing a version 6 consumer read is removed or renamed.

1. The version number moved from 6 to 7.
2. `ActionResult` gains `run_as` (always an array) and `Transition` gains
   `run_as` (absent on a transition that has only been inspected). See
   `RunAs`.
3. `Effect` gains `unexpected`, only ever `true` and only on `chown`.
4. `Warning.code` gains `root-owned-after-drop` and `privilege-drop-failed`.
5. Refusal `code` gains `invoking-user-unresolved` and
   `privilege-drop-failed`, both exit 4. This is a behaviour change: under
   sudo or doas from an ordinary user whose ids or home cannot be resolved,
   version 6 fell back to acting on root's environment with `--allow-root`;
   version 7 refuses.
6. Behaviour: under sudo or doas from an ordinary user, user-level providers
   now run as that user, so their effects on an elevated run no longer
   include `chown` fixes, and what they create outside the user's home is no
   longer `chmod`-widened.

## Migrating from schema 7 to 8

Version 8 adds; nothing a version 7 consumer read is removed or renamed.

1. The version number moved from 7 to 8.
2. `Warning.code` gains `prune-skipped` (severity `info`): `prune`, dry run
   or not, names each orphan it left alone, with its `package`, `provider`
   and the reason in `message` (outside the home directory, or a package
   manager). Version 7 skipped them without a word.
3. `Warning.code` gains `pip-outside-virtualenv`.
4. `dependencies.command` gains `virtualenv`, set when a pip install goes
   into a virtualenv. When the project has a `.venv` with its own pip and no
   other virtualenv is active, that pip is now the `tool` (an absolute path);
   version 7 always ran `pip3` or `pip` from PATH.
5. Behaviour: a pip install outside any virtualenv is a destructive planned
   effect, so an unattended `install .` that would run one is refused with
   exit 3 and `destructive-unattended` unless `--allow-destructive`; an
   interactive run lists it with `!` before asking. A virtualenv is never
   required.

## Migrating from schema 8 to 9

Version 9 adds; nothing a version 8 consumer read is removed or renamed.

1. The version number moved from 8 to 9.
2. New command `inventory`, with its own payload (see "Inventory"):
   `providers`, `packages`, `warnings` and `summary`.
3. `discover-failed` is also raised by `inventory`, once per provider whose
   health check or listing failed or did not answer within the 20-second
   bound.
4. `Warning.code` gains `requested-unknown` and `owner-ambiguous`.
5. New command `adopt`, with the operation fields plus `skipped` and
   `left_unmanaged` (see "Adopt"); `refused.operation` gains `adopt`.
6. Refusal `code` gains `owner-ambiguous` with the new exit status 5, the
   decision refusal, whose error document carries `package` and
   `candidates`.
7. Behaviour: adopting (including `install .` recording a project's
   toolchain) never records a package the chosen provider neither reports
   nor owns on PATH; version 8 wrote such an entry with a
   `recorded-not-installed` warning.
8. Behaviour: `sync` no longer moves software between providers, so it never
   removes anything. Where version 8 planned a `relocate` action (and a
   `transitions` entry) for a provider mismatch, version 9 plans nothing,
   reports `provider-drift`, and leaves the package `provider-differs`.
   `relocate` actions now come only from `transition`.
9. Refusal `code` gains `duplicate-install` and `provider-ambiguous` (exit
   5), and `Warning.code` gains `duplicate-install`. Behaviour: an unattended
   `install` that would silently add a second copy, and an unattended `sync`
   that would pick a provider for an ambiguous package, are refused where
   version 8 went ahead. `install --allow-duplicate` is new.

## Versioning Policy

- Schema versions are integers starting at 1
- Incrementing the schema version indicates a breaking change
- Adding optional fields to existing objects is not a breaking change
- Removing or renaming fields requires a schema version increment
- Changes to enum values require a schema version increment when they change the meaning of an existing field or invalidate a consumer's interpretation. New enum values may be added as compatible extensions.

## Stability Guarantees

Fields marked as stable in this document will not be removed or renamed without a schema version increment. New fields may be added at any time without a schema version increment.

The implementation emits the current version from the report writer for every command, including errors. Every response goes through one envelope helper, so the version and the flat shape cannot drift per command. Keep this document and that envelope version aligned when changing machine output.

Tools consuming Starpoint's JSON output should:
1. Check the `schema_version` field to ensure compatibility
2. Ignore unknown fields (forward compatibility)
3. Treat missing optional fields as null/empty (backward compatibility)
