# Starpoint JSON Schema

This document describes Starpoint's machine-readable output envelope and payloads. Every JSON response is a single flat object: an integer `schema_version` plus the command's own fields. Nothing is nested under a command name or under `data`.

## Schema Version

Current schema version: `5`

All JSON output, including errors, includes a top-level `schema_version` field. The version applies to the complete response envelope and every command payload. It is independent of the TOML configuration `schema` field. Every response is `{ "schema_version": 5, ...command fields }`; errors are `{ "schema_version": 5, "error": "..." }`.

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
| `running-elevated` | Starpoint runs as root (euid 0) or with an elevated Windows token. `~`, `~/.local/bin`, the configuration and the shell profiles are the elevated account's, so effects may land outside the invoking user's environment. Reported, never refused. |
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
| `discover-failed` | A provider could not list its packages, so `prune` considered none of them. |
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
  "warnings": ["Warning"]
}
```

`result` is `planned` in a dry run, `applied` once done, `failed` when the
action was attempted and did not complete (its `effects` still list whatever
did change, and `error` says why), `reported` for a conflict Starpoint reports
but will not resolve, and `not_run` for a planned action an earlier failure
stopped. `destructive` is true when any effect or configuration change is.
Collections are always arrays, never null.

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
  "op": "create | modify | replace | delete",
  "kind": "package | executable | symlink | file | directory | config | command | shell_profile",
  "path": "string | null",
  "name": "string | null",
  "from": "string | null",
  "to": "string | null",
  "provider": "string | null",
  "destructive": "boolean",
  "environment": "boolean",
  "evidence": "observed | reported | planned",
  "detail": "string | null"
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
(uv, pip), the directory of the provider's own binary (where Homebrew, Cargo
and npm put commands), the path and directory of every installation already
known for the package, the directory of whatever the name resolves to on PATH,
and the user's shell profiles. A move appears as a delete and a create, because
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

### Example: a destructive removal

`starpoint remove ruff --yes --json`, where uv had linked ruff into `~/.local/bin`:

```json
{
  "schema_version": 5,
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

## Command-Specific Schemas

The following examples show payload fields; the actual response also has the top-level `schema_version` field.

### `starpoint list`

```json
{
  "packages": [
    {
      "name": "string",
      "path": "string | null",
      "scope": "string",
      "provider": "string",
      "version": "string | null",
      "status": "string",
      "desired": "boolean",
      "pinned": "boolean",
      "channel": "string | null"
    }
  ]
}
```

`unaccounted_path` lists PATH executables with no known owner, the same entries the human table shows below the package list.

### `starpoint info <package>`

```json
{
  "package": "State"
}
```

When ownership is moving, a `transition` field carries the pending `Transition`. When the package is installed but unmanaged, a `lookup` field carries the provider survey that explains it (`{ "package": "Package", "findings": ["Finding"], "duration_ms": "number", "timed_out": "boolean" }`). Both are optional and always appear in the same single document.

### `starpoint explain <package>`

```json
{
  "package": "Package",
  "final": "Resolution",
  "steps": [
    {
      "rule": "string",
      "outcome": "string",
      "provider": "string | null",
      "reason": "string | null",
      "applied": "boolean"
    }
  ]
}
```

### `starpoint sync`

```json
{
  "dry_run": "boolean",
  "converged": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "transitions": ["Transition"],
  "states": ["State"],
  "warnings": ["Warning"],
  "problems": ["string"],
  "project": "ProjectInfo | null",
  "summary": "Summary"
}
```

An action that ran reports what was observed; a planned action that an earlier
failure stopped is reported with `result: "not_run"`.

### `starpoint doctor`

```json
{
  "starpoint": {
    "version": "string",
    "path": "string | null",
    "provider": "string | null"
  },
  "config": {
    "path": "string",
    "exists": "boolean",
    "schema": "number",
    "package_count": "number",
    "files": ["string"],
    "project_file": "string | null",
    "writable": "boolean"
  },
  "providers": [
    {
      "name": "string",
      "description": "string",
      "kind": "string",
      "available": "boolean",
      "reason": "string | null",
      "binary": "string | null",
      "version": "string | null",
      "capabilities": {
        "discover": "boolean",
        "install": "boolean",
        "remove": "boolean",
        "update": "boolean",
        "pin_version": "boolean",
        "channels": "boolean",
        "search": "boolean",
        "project_install": "boolean",
        "can_install_in_context": "boolean",
        "verify": "boolean",
        "default_scope": "string"
      },
      "contextAware": "boolean",
      "hasExecutableMapping": "boolean"
    }
  ],
  "ownership": ["State"],
  "path_inventory": [
    {
      "installation": "Installation | null",
      "path": "string",
      "attributed": "boolean",
      "reason": "string | null",
      "duplicate": "boolean",
      "symlink": "boolean",
      "target": "string | null"
    }
  ],
  "path_summary": {
    "executables": "number",
    "directories": "number",
    "shadowed": "number",
    "unknown_owner": "number"
  },
  "problems": ["Warning"],
  "healthy": "boolean"
}
```

`path_inventory` is always complete, whatever the human output shows. Each PATH
directory is read once even when PATH names it several times or through a
symlink; `duplicate` is true only for a different file shadowed by an earlier
entry with the same name.

### `starpoint install <package>`

```json
{
  "operation": "install",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary",
  "lookup": ["Survey"]
}
```

If an install fails part-way, the response still describes what the command
did before the failure (and whatever the failed step left on disk) before the
error is reported on stderr.

`lookup` is the provider survey that preceded the choice, so a JSON consumer can
see which providers were asked and what each answered.

### `starpoint remove <package>`

```json
{
  "operation": "remove",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary"
}
```

`--dry-run` lists every effect the removal would have: the package, each file
Starpoint can attribute to it, the command that would stop resolving, and the
configuration entry.

### `starpoint update`

```json
{
  "operation": "update",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary",
  "pin_conflicts": ["PinConflict"]
}
```

Each update carries a `version` transition. A pin rewritten with the user's
approval is a destructive `ConfigChange` of the `version` field. A package
`update` left alone is a coded `info` warning naming the package.

`pin_conflicts` has one entry per pinned package with a newer version
available:

```json
{
  "package": "string",
  "provider": "string",
  "pinned": "string",
  "available": "string",
  "resolution": "kept | updated | pending",
  "config_path": "string | null"
}
```

### `starpoint transition`

```json
{
  "dry_run": "boolean",
  "transitions": ["Transition"],
  "pending": "number"
}
```

Performing transitions (`starpoint transition <package>`, and
`--reverse`) reports the operation fields with one `relocate` action per move,
plus the full `transitions` records:

```json
{
  "operation": "transition | transition-reverse",
  "dry_run": "boolean",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary",
  "transitions": ["Transition"]
}
```

### `starpoint recover`

```json
{
  "operation": "recover",
  "interrupted": ["InterruptedOperation"],
  "sole_copies": ["InterruptedOperation"]
}
```

With `--discard`, the response reports what was removed and what was not:

```json
{
  "operation": "recover",
  "cleaned": ["InterruptedOperation"],
  "remaining": ["InterruptedOperation"],
  "sole_copies": ["InterruptedOperation"]
}
```

An `InterruptedOperation` describes one file left behind by an interrupted run:

```json
{
  "type": "temp_config",
  "path": "string",
  "timestamp": "RFC3339 string",
  "recoverable": "boolean",
  "sole_copy": "boolean",
  "reason": "string"
}
```

`sole_copy` means the configuration this file was writing does not exist, so the
file holds the user's only copy of their declared state. Those are never deleted
by `recover --discard` and never cleaned up automatically; they appear in
`sole_copies` so a caller can offer to restore them. `remaining` lists files that
could not be removed, with `reason` explaining why; when it is non-empty the
command exits non-zero.

### `starpoint prune`

```json
{
  "operation": "prune",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary"
}
```

`prune` removes user-scoped software that no configuration declares. `--dry-run` reports the same `actions` it would perform, with every file each would delete. A removal that fails is reported as a `failed` action, after the ones that succeeded, and the command exits non-zero. With no configuration, or a configuration that declares no packages, prune refuses rather than treat every user-scoped package as an orphan.

### `starpoint runs`

```json
{
  "executions": [
    {
      "package_name": "string",
      "directory": "string",
      "timestamp": "RFC3339 string",
      "working_dir": "string | null"
    }
  ]
}
```

### `starpoint install .`

```json
{
  "operation": "install",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "summary": "Summary",
  "dependencies": {
    "command": {
      "tool": "string",
      "args": ["string"]
    },
    "skipped": "string | null",
    "ran": "boolean",
    "dry_run": "boolean",
    "effects": ["Effect"]
  }
}
```

`dependencies.effects` is what the ecosystem's installer changed in the
dependency directory (one level deep: `node_modules` created, a lockfile
rewritten) and in the shell profiles, observed around the run; in a dry run,
one planned effect naming the directory it will write into.

### `starpoint logging`

```json
{
  "state_dir": "string",
  "audit_log_dir": "string",
  "files": [
    {
      "name": "audit-2026-10-17.jsonl",
      "path": "string",
      "day": "YYYY-MM-DD",
      "time": "RFC3339 string",
      "bytes": "number"
    }
  ],
  "entry_count": "number",
  "retention_days": "number",
  "retention_source": "config | default",
  "os": {
    "goos": "linux | darwin | windows | ...",
    "supported": "boolean",
    "mechanism": "logrotate | newsyslog | unsupported",
    "path": "string | null",
    "system_path": "string | null",
    "content": "string | null",
    "note": "string | null"
  },
  "os_configured": "boolean"
}
```

`files` is oldest first. `entry_count` is how many entries are inside the
retention window, which is what will still be there after the next pass.
`retention_source` distinguishes a window the user set from the default, because
an unconfigured retention window is still a promise about how long records live.

`os.path` is always inside Starpoint's own state directory. Starpoint never
writes to `system_path`, which is where the system logger expects such a file; the
privileged step is left to the user.

### `starpoint logging prune`

```json
{
  "operation": "logging.prune",
  "retention_days": "number",
  "audit_logs_removed": "number",
  "history_entries_removed": "number",
  "run_records_removed": "number",
  "problems": ["string"]
}
```

Retention is applied on every command, so this reports and forces the effect
rather than being how it happens. It exits non-zero when `problems` is non-empty.

### `starpoint logging config`

```json
{
  "operation": "logging.config",
  "goos": "string",
  "supported": "boolean",
  "mechanism": "logrotate | newsyslog | unsupported",
  "path": "string | null",
  "written": "boolean",
  "system_path": "string | null",
  "install": "string | null",
  "note": "string | null"
}
```

`written` is false when the configuration is already current, and when the
platform has none. A file in the logging directory that Starpoint did not write
is never overwritten.

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
  "warnings": ["Warning"]
}
```

`effects` is what the move changed, observed while it ran (or predicted, in a
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
