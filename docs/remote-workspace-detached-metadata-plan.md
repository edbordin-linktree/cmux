# Remote Workspace Metadata + Detached Mutation Plan

This document records the current local-fork design for Craft-oriented remote workspace control. It supersedes the earlier idea of a separate journal, a materialized index, leases, or cmux-owned semantic surface keys.

The important split is:

- Local Mac `cmux` lists and attaches detached workspaces through the host registry.
- Remote-host `cmux` commands, as injected into SSH terminals by `cmuxd-remote`, first use the existing authenticated relay back to the Swift UI.
- If that relay is unavailable, the remote-host `cmux` falls back to a restricted local snapshot backend in `cmuxd-remote` and mutates `~/.cmux/daemon/<slot>/workspace-snapshot.json` directly.

The remote snapshot is the source of truth whenever the Swift UI reconnects. A detached or temporarily disconnected workspace can therefore keep accepting a small set of supervisor/script operations on the remote host.

## Cmux Refactor

### Implemented Capabilities

The current branch adds these workspace-level primitives:

```bash
cmux metadata set --workspace <workspace> <key> <value>
cmux metadata set --workspace <workspace> <key> --value-json <json>
cmux metadata get --workspace <workspace> <key> --json
cmux metadata list --workspace <workspace> [--prefix <prefix>] --json
cmux metadata clear --workspace <workspace> <key>

cmux workspace lookup \
  --metadata craft:project-id=<id> \
  --metadata craft:task-id=<id> \
  [--include-detached] \
  --json

cmux tree --workspace <workspace> --json
```

Hidden metadata is persisted in local session state and in `RemoteWorkspaceSnapshotV1` as `metadataEntries`. It is not rendered in the sidebar. `workspace lookup` searches attached workspaces first; `--include-detached` also searches remote snapshots discovered through the detached host registry.

Remote detached fallback currently supports this restricted subset:

```bash
cmux metadata set|get|list|clear --workspace current ...
cmux workspace lookup --metadata <key=value> [--include-detached] --json
cmux tree --workspace current --json
cmux new-pane --workspace current --type terminal|browser [--direction <dir>] [--url <url>] [--command <cmd>] [--focus true|false]
cmux new-surface --workspace current --type terminal|browser [--pane <pane>] [--url <url>] [--command <cmd>] [--focus true|false]
cmux new-split <dir> --workspace current [--surface <surface>] [--type terminal|browser] [--url <url>] [--command <cmd>] [--focus true|false]
cmux close-surface --workspace current --surface <surface>
```

The remote fallback is intentionally small. UI-only commands still require an attached Swift UI and should fail clearly when only the detached snapshot backend is available. In particular, detached fallback does not currently support focus/window movement, `rename-tab`, `send`, `send-key`, or browser automation/navigation commands.

### Remote Execution Model

Remote terminal sessions get enough environment to operate without the Mac relay:

```text
CMUX_WORKSPACE_ID
CMUX_TAB_ID
CMUX_SURFACE_ID
CMUX_PANEL_ID
CMUX_REMOTE_DAEMON_SLOT
CMUX_SOCKET_PATH
PATH=$HOME/.cmux/bin:$PATH
```

When the relay is alive, remote `cmux` commands use the normal live Swift socket path. When the relay is gone, the same remote `cmux` command loads the snapshot for `CMUX_REMOTE_DAEMON_SLOT` or resolves it by `CMUX_WORKSPACE_ID`.

Terminal creation in detached fallback creates the backing PTY through the persistent daemon first, then records the resulting `remotePTYSessionId` in the snapshot. Browser creation records only URL/title. This is why `new-pane --type terminal --command <cmd>` is the preferred detached supervisor primitive: it starts a live remote process without needing a later `send`/`send-key` replay.

### Snapshot Lifecycle

Remote snapshots now have a status:

- `detached`: shown by Host Manager and `ssh-workspace-list-detached`.
- `live`: a checkpoint for an attached/reconnected workspace; intentionally hidden from detached listings.

Detach writes a `detached` snapshot and removes the local workspace. Attach restores the workspace using the original snapshot `workspaceId`, preserving the UUID across detach/attach, then rewrites the snapshot as `live` instead of eagerly clearing it. This keeps a recoverable remote copy and gives remote scripts a stable target if the Mac relay drops again.

Swift reconnect reconciliation is remote-wins:

- Fetch the remote snapshot.
- If the snapshot hash/revision differs from the last applied local snapshot, rebuild local layout from the remote snapshot.
- Validate terminal PTYs during restore; missing PTYs become lost placeholders.
- After successful attached local mutations, write a fresh `live` snapshot.

### Tested Behavior

The current E2E test on `ed@tdb` validated:

- live multi-pane workspace detach;
- remote headless `metadata set`;
- remote headless `tree`;
- remote headless browser pane creation;
- remote headless terminal pane creation with a startup command;
- reattach with the original workspace UUID preserved;
- restored terminal process output from the detached-created PTY;
- creating another remote terminal after reattach.

### Still Deliberately Unsupported

The detached backend is not a full replacement for the Swift UI socket. Scripts should expect these to require an attached UI:

- focus, selection, window placement, workspace movement;
- tab renaming and cosmetic title changes;
- terminal input injection with `send` / `send-key`;
- browser automation and navigation after creation;
- local Mac-only browser state, cookies, scroll, devtools, and WKWebView session data.

## Craft Follow-Up Change

Craft should treat cmux as two layers:

- workspace and surface discovery/manipulation primitives that work while attached or detached;
- UI/operator conveniences that are best-effort and attached-only.

The current Craft cmux provider still has three brittle patterns:

- workspace lookup by structured title prefix in `_mux_ws_ref`;
- agent/dashboard lookup by tab title in `_mux_surface_by_tab_title`;
- generic named panes stored in visible `set-status` / `list-status` keys.

Those should move to hidden metadata.

### Workspace Identity

Use hidden metadata for workspace identity. Do not add `workspace.kind`; the project workspace is a special attached-UI control surface, and task workspaces are discoverable by the fields Craft already owns.

Suggested workspace keys:

```text
craft:schema-version=1
craft:project-id=<stable project slug/id>
craft:project-dir=<absolute path>
craft:task-id=<task id>              # task workspaces only
craft:task-dir=<absolute path>       # task workspaces only
```

Titles remain human-readable sidebar labels only. Lookup should be:

```bash
cmux workspace lookup \
  --metadata craft:project-id="$project_id" \
  --metadata craft:task-id="$task_id" \
  --include-detached \
  --json
```

For the project workspace, lookup by `craft:project-id` and absence of `craft:task-id` can be handled by Craft logic after reading matches; cmux does not need a `workspace.kind` field.

### Surface Identity

Craft should keep semantic surface keys in workspace metadata. Cmux continues to expose opaque `surface_id`s.

Suggested keys:

```text
craft:surface:agent
craft:surface:architect
craft:surface:orchestrator
craft:surface:dashboard
craft:surface:diffhub-review
craft:surface:github-pr
craft:surface:devin-session
craft:surface:buildkite-status
```

Each value should be JSON. Minimum shape:

```json
{
  "surface_id": "surface-or-uuid",
  "type": "terminal|browser",
  "purpose": "agent",
  "title": "optional display title",
  "url": "optional browser url",
  "agent": "claude|codex|opencode",
  "updated_at": "2026-05-28T00:00:00Z"
}
```

Craft owns the semantic key and any richer policy. Cmux only stores and returns the opaque value.

### Refactor Recipe

Add a small cmux provider layer that wraps only JSON-producing commands:

```bash
_cmux_workspace_lookup_by_metadata
_cmux_metadata_get
_cmux_metadata_set_json
_cmux_metadata_clear
_cmux_tree_json
_cmux_surface_exists
_cmux_surface_from_metadata
_cmux_record_surface
```

Then refactor existing helpers:

- `_mux_ws_ref`: replace title-prefix matching with `workspace lookup --metadata ... --include-detached --json`.
- `_mux_surface_by_tab_title`: replace tab-title matching with `metadata get craft:surface:<key>` plus `tree` existence validation.
- `ensure_task_session`: after creating a task workspace, immediately write project/task metadata. For existing workspaces, lookup by metadata and only use title as cosmetic refresh.
- `spawn_task_pane`: create or reuse `craft:surface:agent`; for remote supervisor use `new-pane/new-split --type terminal --command "$cmd"` instead of `send` + `send-key`.
- `pane_is_running`: read `craft:surface:agent`, then verify the `surface_id` appears in `cmux tree`.
- `kill_task_pane`: close the recorded `surface_id`, then clear `craft:surface:agent`.
- `mux_spawn_named_pane`, `mux_send_to_pane`, `mux_pane_exists`, `mux_kill_named_pane`: replace visible status keys `craft:pane:<name>` with hidden `craft:surface:<name>` metadata.
- Dashboard / Diffhub / GitHub PR / Devin helpers: use the same surface metadata pattern. Creation may work detached if it is just `new-pane --type browser --url <url>` or `new-pane --type terminal --command <cmd>`; focus, renaming, and browser automation remain attached-only best effort.

The old discoverer special case should go away. It becomes a normal agent surface launched with a different provider/configuration if still needed.

### Attached vs Detached Rules for Craft

Operations that should work in attached or detached remote workspaces:

- find task workspace by metadata;
- read/write Craft metadata;
- inspect tree;
- create terminal surfaces with startup command;
- create browser surfaces with initial URL;
- close a known surface.

Operations that should be attached-only:

- focus/select a workspace or surface;
- move workspace between windows;
- rename tab/workspace for cosmetics;
- send interactive input to an already-running terminal;
- browser automation after creation.

Craft should surface attached-only failures as non-fatal operator convenience failures where possible. Supervisor-critical logic should be built from the attached-or-detached subset.

## TODO: Concurrency Control

Once the detached metadata store is working end to end, revisit locking or another lightweight concurrency-control mechanism for remote snapshot metadata and layout mutations. The first pass is intentionally simple, but concurrent remote scripts should not be able to silently overwrite each other's metadata or layout changes long term.
