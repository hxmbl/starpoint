# Starpoint JSON Schema

This document describes Starpoint's machine-readable output envelope and payloads. Every JSON response has an integer `schema_version` at the top level. Most command payloads are direct fields in that response; generic operation results and errors are nested under `data`.

## Schema Version

Current schema version: `1`

All JSON output, including errors, includes a top-level `schema_version` field. The version applies to the complete response envelope and every command payload. It is independent of the TOML configuration `schema` field. Generic operation results use `{ "schema_version": 1, "data": { ... } }`; errors use `{ "schema_version": 1, "error": "..." }`.

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

### `starpoint info <package>`

```json
{
  "package": "State"
}
```

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

### `starpoint install` / `starpoint remove` / `starpoint update`

```json
{
  "operation": "string",
  "dry_run": "boolean",
  "config_changed": "boolean",
  "config_path": "string | null",
  "actions": ["ActionResult"],
  "warnings": ["Warning"]
}
```

`update` adds `pin_conflicts`, one per pinned package with a newer version
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

## Versioning Policy

- Schema versions are integers starting at 1
- Incrementing the schema version indicates a breaking change
- Adding optional fields to existing objects is not a breaking change
- Removing or renaming fields requires a schema version increment
- Changes to enum values require a schema version increment when they change the meaning of an existing field or invalidate a consumer's interpretation. New enum values may be added as compatible extensions.

## Stability Guarantees

Fields marked as stable in this document will not be removed or renamed without a schema version increment. New fields may be added at any time without a schema version increment.

The implementation currently emits version `1` consistently from the report writer, including generic data and error responses. Keep this document and that envelope version aligned when changing machine output.

Tools consuming Starpoint's JSON output should:
1. Check the `schema_version` field to ensure compatibility
2. Ignore unknown fields (forward compatibility)
3. Treat missing optional fields as null/empty (backward compatibility)
