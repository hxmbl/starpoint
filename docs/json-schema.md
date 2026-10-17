# Starpoint JSON Schema

This document describes Starpoint's machine-readable output envelope and payloads. Every JSON response is a single flat object: an integer `schema_version` plus the command's own fields. Nothing is nested under a command name or under `data`.

## Schema Version

Current schema version: `3`

All JSON output, including errors, includes a top-level `schema_version` field. The version applies to the complete response envelope and every command payload. It is independent of the TOML configuration `schema` field. Every response is `{ "schema_version": 3, ...command fields }`; errors are `{ "schema_version": 3, "error": "..." }`.

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
  "package": "string | null",
  "provider": "string | null",
  "message": "string"
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
  "actions": [
    {
      "action": "install | remove | update | adopt | relocate | record | reinstall",
      "package": "string",
      "provider": "string | null",
      "note": "string | null",
      "result": "string | null"
    }
  ],
  "transitions": ["Transition"],
  "states": ["State"],
  "warnings": ["Warning"],
  "problems": ["string"],
  "project": "ProjectInfo | null"
}
```

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
  "operations": ["OperationResult"],
  "lookup": ["Survey"]
}
```

`lookup` is the provider survey that preceded the choice, so a JSON consumer can
see which providers were asked and what each answered.

### `starpoint remove <package>`

```json
{
  "operations": ["OperationResult"]
}
```

An `OperationResult` is what the engine produced for one package:

```json
{
  "states": ["State"],
  "applied": ["PlannedAction"],
  "transitions": ["Transition"],
  "config_changed": "boolean",
  "config_path": "string | null",
  "warnings": ["Warning"],
  "dry_run": "boolean"
}
```

A `PlannedAction` is the engine's own record of one change, which nests the
whole `State` it applied to:

```json
{
  "action": "install | remove | update | adopt | relocate | record | reinstall",
  "state": "State",
  "config_change": "ConfigEdit | null",
  "transition": "Transition | null",
  "note": "string | null"
}
```

Note that `install` and `remove` report `applied` as `PlannedAction`, whereas
`sync`, `update` and `prune` report `actions` as `ActionResult` below. The two
shapes are not interchangeable; see "Action vocabularies" under Migrating.

### `starpoint update`

```json
{
  "operation": "update",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"],
  "pin_conflicts": ["PinConflict"]
}
```

An `ActionResult` is one applied or planned action, in the flat form used by
`sync`, `update` and `prune`:

```json
{
  "action": "install | remove | update | adopt | relocate | record | reinstall",
  "package": "string",
  "provider": "string | null",
  "note": "string | null",
  "result": "string | null"
}
```

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
  "actions": ["ActionResult"],
  "warnings": ["Warning"]
}
```

`prune` removes user-scoped software that no configuration declares. `--dry-run` reports the same `actions` it would perform. With no configuration, or a configuration that declares no packages, prune refuses rather than treat every user-scoped package as an orphan.

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
  "operations": ["OperationResult"],
  "dependencies": {
    "command": {
      "tool": "string",
      "args": ["string"]
    },
    "ran": "boolean"
  }
}
```

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
  "note": "string | null"
}
```

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

Two shapes describe one change, and they are not interchangeable:

- `ActionResult` — `{action, package, provider, note, result}` — in `sync`, `update` and `prune`.
- `PlannedAction` — `{action, state, config_change, transition, note}` — in `install`, `remove` and `install .`, under `operations[].applied`.

`PlannedAction` nests the whole `State` the action applied to, so it is the
richer shape; `ActionResult` is the one the schema originally described for
every command. Unifying them is a breaking change and is tracked under
Remaining CLI and API work.

## Versioning Policy

- Schema versions are integers starting at 1
- Incrementing the schema version indicates a breaking change
- Adding optional fields to existing objects is not a breaking change
- Removing or renaming fields requires a schema version increment
- Changes to enum values require a schema version increment when they change the meaning of an existing field or invalidate a consumer's interpretation. New enum values may be added as compatible extensions.

## Stability Guarantees

Fields marked as stable in this document will not be removed or renamed without a schema version increment. New fields may be added at any time without a schema version increment.

The implementation emits version `2` from the report writer for every command, including errors. Every response goes through one envelope helper, so the version and the flat shape cannot drift per command. Keep this document and that envelope version aligned when changing machine output.

Tools consuming Starpoint's JSON output should:
1. Check the `schema_version` field to ensure compatibility
2. Ignore unknown fields (forward compatibility)
3. Treat missing optional fields as null/empty (backward compatibility)
