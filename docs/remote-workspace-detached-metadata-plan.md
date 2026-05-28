# cmux Remote Workspace Capabilities for Craft

Source branch:

```text
https://github.com/edbordin-linktree/cmux/tree/local/remote-workspace-snapshots
```

This branch combines two layers:

- an unmerged upstream remote-session base from `manaflow-ai/cmux#4807` that runs a persistent `cmuxd-remote` daemon on the SSH host;
- local additions on this branch that snapshot and restore workspace layout around that daemon.

## Build And Run This Fork

Use a tagged debug build so the fork can run beside the installed release build of cmux:

```bash
git clone https://github.com/edbordin-linktree/cmux.git
cd cmux
git checkout local/remote-workspace-snapshots
./scripts/setup.sh
CMUX_SKIP_ZIG_BUILD=1 ./scripts/reload.sh --tag rwsnap --launch
```

`CMUX_SKIP_ZIG_BUILD=1` is useful on machines where Homebrew has Zig `0.16.x` instead of the Ghostty helper's expected Zig `0.15.x`. Omit it if the local Zig toolchain is already compatible and you want the helper built normally.

The tag is the isolation boundary. With `--tag rwsnap`, `reload.sh` creates:

- app name `cmux DEV rwsnap.app`;
- bundle id `com.cmuxterm.app.debug.rwsnap`;
- Swift app socket `/tmp/cmux-debug-rwsnap.sock`;
- local cmuxd socket `~/Library/Application Support/cmux/cmuxd-dev-rwsnap.sock`;
- debug log `/tmp/cmux-debug-rwsnap.log`;
- separate DerivedData under `~/Library/Developer/Xcode/DerivedData/cmux-rwsnap`.

Use the tag-bound CLI helper for commands against this fork:

```bash
CMUX_TAG=rwsnap scripts/cmux-debug-cli.sh list-workspaces
CMUX_TAG=rwsnap scripts/cmux-debug-cli.sh ssh-workspace-list-detached --json
```

Avoid `/tmp/cmux-cli` for dogfooding this branch because it points at the most recently reloaded dev build, not necessarily this tagged fork.

### Running Alongside Release cmux On The Mac

The release app and this fork can be open at the same time:

- Keep `/Applications/cmux.app` running as usual.
- Launch the fork with `CMUX_SKIP_ZIG_BUILD=1 ./scripts/reload.sh --tag rwsnap --launch`.
- Use `CMUX_TAG=rwsnap scripts/cmux-debug-cli.sh ...` for fork CLI commands.
- Use the normal release-installed `cmux` command for release CLI commands.
- To stop only the fork, quit `cmux DEV rwsnap`.
- To replace the fork build, rerun `reload.sh` with the same tag; it only kills and replaces the matching tagged app.

Do not use an untagged debug app for this workflow. Untagged debug builds share default debug identity and socket paths, which makes it easy to target the wrong cmux instance.

### Remote Host Coexistence With Release Daemons

The fork does not need to replace the release remote daemon globally.

cmux uploads remote daemon binaries under versioned paths:

```text
~/.cmux/bin/cmuxd-remote/<version>/<goos>-<goarch>/cmuxd-remote
```

The shared remote wrapper is:

```text
~/.cmux/bin/cmux
```

That wrapper is intentionally a dispatcher. For commands running inside an attached remote cmux session, it reads `CMUX_SOCKET_PATH`, derives the relay port, then reads:

```text
~/.cmux/relay/<relay_port>.daemon_path
```

and executes the daemon binary recorded for that relay/session. This lets release cmux and this fork coexist on the same remote host, even if their `cmuxd-remote` versions differ.

Outside an attached session, the wrapper falls back to:

```text
~/.cmux/bin/cmuxd-remote-current
```

That symlink points at the daemon binary from the most recent cmux bootstrap on the host, so do not rely on the fallback when testing mixed versions. Prefer commands from inside the intended remote cmux session, or use the Mac-side tagged CLI and Host Manager.

When a detached workspace is created, the Mac host registry records the daemon path for that workspace. `ssh-workspace-list-detached`, Host Manager, and attach use that captured daemon path, so fork-created detached snapshots continue to use the fork daemon binary.

Keep these rules when running release and fork together:

- Let cmux bootstrap manage `~/.cmux/bin/cmux` and `~/.cmux/bin/cmuxd-remote-current`; do not manually replace them.
- Do not delete versioned daemon binaries while workspaces from that version may be live or detached.
- Release and fork can both have live persistent daemon slots under `~/.cmux/daemon/<slot>/`; a slot is one remote workspace container, not one PTY.
- If the remote wrapper appears to target the wrong daemon, reconnect with the desired Mac app so bootstrap rewrites the relay metadata and wrapper mapping.
- Clean up test snapshots with `cmux ssh-workspace-snapshot-clear` and stale registry entries with `cmux ssh-host-forget`; avoid blanket-deleting `~/.cmux/daemon` if release workspaces may still be active.

## General Model

The remote host does not run the full cmux workspace UI. The Swift app on the Mac still owns the real workspace model while attached: sidebar entries, split layout, tabs, browser surfaces, focus, and most workspace commands are Swift/UI state.

The remote host owns long-lived terminal processes. The base branch from PR `manaflow-ai/cmux#4807` adds a persistent `cmuxd-remote` daemon under `~/.cmux/daemon/<slot>/`. That daemon keeps PTY sessions alive and exposes primitives such as `pty.attach` and `pty.detach`, so a terminal process can survive the Mac UI closing its surface or losing the relay.

This branch builds a workspace illusion on top of that PTY layer:

- while attached, Swift periodically writes a workspace snapshot to the remote daemon slot;
- when the user detaches, Swift stores a final `detached` snapshot, detaches terminal surfaces from their PTYs, releases local browser/UI surfaces, and removes the workspace from the Mac sidebar;
- while detached or disconnected, scripts on the remote host can perform a restricted set of mutations against that snapshot, including metadata, layout, names, and status banners;
- while detached or disconnected, scripts can also send input to known terminal surfaces through authenticated daemon PTY-session RPCs;
- when the user attaches or reconnects, Swift fetches the remote snapshot, rebuilds the workspace UI locally, and reattaches terminal surfaces to the still-running remote PTYs.

So the workspace appears to be maintained on the server, but that is mostly a snapshot/recreate mechanism. Terminal process state is genuinely server-side because the daemon owns the PTYs. Browser state, split-tree rendering, sidebar presence, focus, and most UI behavior are reconstructed by the Mac app from the saved snapshot.

The snapshot layer intentionally keeps browser recovery shallow: browser surfaces reopen at the saved URL, but WKWebView cookies, scroll position, back/forward state, devtools, and local data stores are not part of the remote snapshot.

### Daemon Slot Semantics

A persistent daemon slot is not one PTY. It is the server-side container for one remote workspace instance.

In practice, each `cmux ssh ...` remote workspace gets a unique slot such as `ssh-f605ac69-bcc0-406f-8d88-6c2a9220879e`. That slot corresponds to:

- one daemon directory: `~/.cmux/daemon/<slot>/`;
- one persistent `cmuxd-remote serve --persistent-server --slot <slot>` daemon;
- one workspace snapshot body and metadata sidecar;
- many PTY sessions, one per terminal/agent surface in that workspace.

PTYs are identified separately by `session_id` values, usually derived from workspace and surface IDs. New terminal surfaces created while attached or detached create additional PTYs inside the same slot. Attach reuses the original slot so Swift can fetch the snapshot and reconnect each terminal surface to its PTY.

## Current Branch Additions

This branch adds detached remote workspace snapshots, hidden workspace metadata, snapshot-backed workspace status banners, a Host Manager UI, and a restricted remote-headless command path for scripts running on an SSH host.

The core behavior Craft should rely on is:

- A remote workspace can be detached from the Mac UI while its layout snapshot and PTYs remain on the remote host.
- The Mac can list detached workspaces and attach them later.
- Swift stores, fetches, and clears a specific workspace snapshot through the per-slot `cmuxd-remote` daemon RPCs.
- Detached workspace discovery uses a one-shot `cmuxd-remote workspace-snapshot-list-all --json` command over SSH, not ad hoc shell filesystem commands.
- A `cmux` command running inside a remote cmux terminal first tries the normal relay back to the Swift UI.
- If the relay is unavailable, that remote `cmux` command can operate on the local remote snapshot for a restricted set of commands.
- Detached `cmux send` and `cmux send-key` resolve a terminal surface from the snapshot, then call authenticated daemon PTY-session RPCs; they do not create temporary attachments.
- Detached workspace/tab rename commands and status-banner commands mutate the remote snapshot directly, so the restored Swift UI reflects the remote-side state.
- On reconnect/attach, the remote snapshot wins and Swift rebuilds local layout from it.

## Architecture Diagrams

### CLI Command Paths

```mermaid
flowchart LR
  subgraph Local["Local Mac session"]
    LCLI["cmux CLI"]
    LSocket["Swift UI socket"]
    LUI["Swift app workspace model"]
    LCLI --> LSocket --> LUI
  end

  subgraph Attached["Attached remote session"]
    RCLI["remote cmux wrapper"]
    RelayAuth["relay auth + command mapping"]
    SSHRelay["SSH relay / tunnel"]
    ASocket["Swift UI socket on Mac"]
    AUI["Swift app workspace model"]
    RCLI --> RelayAuth --> SSHRelay --> ASocket --> AUI
  end

  subgraph Detached["Detached or relay-unavailable remote session"]
    DCLI["remote cmux wrapper"]
    Headless["cmuxd-remote headless CLI fallback"]
    SlotDaemon["cmuxd-remote persistent daemon"]
    Snapshot["workspace-snapshot.json"]
    Meta["workspace-snapshot.meta.json"]
    PTY["persistent daemon PTY hub"]
    DCLI --> Headless
    Headless --> SlotDaemon
    Headless -->|layout, metadata, name, status mutations| Snapshot
    Headless -->|workspace title metadata| Meta
    Headless -->|send / send-key| SlotDaemon
    SlotDaemon -->|PTY session writes| PTY
  end
```

### Remote Daemon and Snapshot Layers

```mermaid
flowchart TB
  subgraph Mac["Mac"]
    UI["Swift UI\nsidebar, split tree, browser views, focus"]
    AppSocket["local app socket"]
    HostRegistry["detached-hosts.json\nhost registry only"]
    UI <--> AppSocket
    UI --> HostRegistry
  end

  subgraph Host["Remote SSH host"]
    Wrapper["~/.cmux/bin/cmux\nremote wrapper"]
    Slot["~/.cmux/daemon/<slot>/\none remote workspace container"]
    Daemon["cmuxd-remote\npersistent daemon"]
    PTYs["PTY sessions\nmany per slot"]
    Body["workspace-snapshot.json\nlayout + panes + metadata + statuses"]
    Sidecar["workspace-snapshot.meta.json\nlist/attach metadata"]
    RelaySocket["relay_socket\nslot-scoped Swift relay address"]

    Wrapper --> Daemon
    Slot --> Daemon
    Slot --> Body
    Slot --> Sidecar
    Slot --> RelaySocket
    Daemon <-->|RPC-backed atomic read/write| Body
    Daemon <-->|RPC-backed atomic read/write| Sidecar
    Daemon --> PTYs
  end

  UI <-->|attached relay + workspace.snapshot RPCs| Daemon
  UI -.->|relay bootstrap writes| RelaySocket
  HostRegistry -->|list detached hosts via SSH| Wrapper
  Wrapper -.->|workspace-snapshot-list-all reads metadata| Sidecar
```

### Checkpoint and Reconnect Flow

```mermaid
sequenceDiagram
  participant Swift as Swift UI
  participant Daemon as cmuxd-remote slot daemon
  participant Snap as Remote snapshot files
  participant RemoteCLI as remote cmux CLI

  Swift->>Swift: Workspace layout, metadata, name, or status changes
  Swift->>Daemon: workspace.snapshot.store(status=live)
  Daemon->>Snap: Atomic body + sidecar write

  Note over Swift,RemoteCLI: Relay later drops or workspace is detached

  RemoteCLI->>Snap: Read current snapshot
  RemoteCLI->>Daemon: Create PTY for new terminal surface
  RemoteCLI->>Snap: Write mutated snapshot
  RemoteCLI->>Snap: Resolve terminal surface to session_id
  RemoteCLI->>Daemon: pty.send / pty.send_key

  Note over Swift,Snap: Swift reconnects or attaches

  Swift->>Daemon: workspace.snapshot.fetch
  Daemon->>Snap: Read body + sidecar
  Daemon-->>Swift: Snapshot body + metadata
  Swift->>Swift: Remote snapshot wins, rebuild workspace
  Swift->>Daemon: pty.attach for terminal surfaces
  Swift->>Daemon: workspace.snapshot.store(status=live)
```

### Detach and Attach Flow

```mermaid
flowchart TD
  Start["Remote workspace attached in Swift UI"] --> Validate["Validate persistent daemon slot + capability"]
  Validate --> Capture["Capture split tree, panes, browser URLs, metadata"]
  Capture --> StoreDetached["RPC workspace.snapshot.store(status=detached)"]
  StoreDetached --> DetachPTY["pty.detach terminal/agent surfaces"]
  DetachPTY --> TearDown["Release local browser/UI surfaces"]
  TearDown --> RemoveSidebar["Remove workspace from Mac sidebar"]
  RemoveSidebar --> Listed["Host Manager / list-detached can discover snapshot"]

  Listed --> Resolve["Attach: resolve host + slot from workspace ID"]
  Resolve --> Fetch["RPC workspace.snapshot.fetch"]
  Fetch --> Rebuild["Recreate Swift workspace with original workspaceId"]
  Rebuild --> Layout["Replay split tree and recreate panes"]
  Layout --> AttachPTY["pty.attach terminal surfaces"]
  Layout --> ReopenBrowsers["Reopen browsers at saved URLs"]
  AttachPTY --> StoreLive["RPC workspace.snapshot.store(status=live)"]
  ReopenBrowsers --> StoreLive
  StoreLive --> Attached["Workspace attached in Swift UI again"]
```

## Current cmux Capabilities

### Local Mac Commands

These commands are intended for the Mac-side cmux app/CLI:

```bash
cmux ssh-workspace-detach --workspace <workspace> [--json]
cmux ssh-workspace-list-detached [--host <host>] [--json] [--timeout <secs>]
cmux ssh-workspace-attach --workspace-id <uuid> [--host <host>] [--slot <slot>] [--json]
cmux ssh-workspace-snapshot-clear (--workspace-id <uuid> | --host <host> --slot <slot>) [--force]
cmux ssh-host-list [--json]
cmux ssh-host-forget --host <host> [--force]
```

`ssh-workspace-list-detached` only shows snapshots whose remote metadata status is `detached`. Attached/reconnected workspaces keep a `live` checkpoint snapshot on the remote host, but those are intentionally hidden from detached listings.

### Workspace Metadata

Hidden metadata is stored with the workspace and is included in remote snapshots. It is not rendered in the sidebar.

```bash
cmux metadata set --workspace <workspace> <key> <value>
cmux metadata set --workspace <workspace> <key> --value-json <json>
cmux metadata get --workspace <workspace> <key> --json
cmux metadata list --workspace <workspace> [--prefix <prefix>] --json
cmux metadata clear --workspace <workspace> <key>
```

Metadata is persisted in:

- local session persistence;
- `RemoteWorkspaceSnapshotV1.metadataEntries`;
- remote snapshot store/fetch round trips;
- detached remote snapshot mutations.

Visible status banners are separate from hidden metadata. They are persisted in `RemoteWorkspaceSnapshotV1.statusEntries` and are restored after the workspace session snapshot is replayed.

### Metadata Lookup

Workspace lookup can use hidden metadata instead of title prefixes:

```bash
cmux workspace lookup \
  --metadata craft:project-id=<id> \
  --metadata craft:task-id=<id> \
  --include-detached \
  --json
```

Attached workspaces are searched first. With `--include-detached`, cmux also searches detached remote snapshots. From the Mac app this uses the host registry; from a remote/headless cmux session this scans that remote host's daemon snapshots.

### Remote-Headless Commands

Inside a remote cmux terminal, the injected `cmux` wrapper has:

```text
CMUX_WORKSPACE_ID
CMUX_TAB_ID
CMUX_SURFACE_ID
CMUX_PANEL_ID
CMUX_REMOTE_DAEMON_SLOT
CMUX_SOCKET_PATH
PATH=$HOME/.cmux/bin:$PATH
```

When the Mac relay is gone, these commands fall back to mutating or reading the remote snapshot directly:

```bash
cmux metadata set|get|list|clear --workspace current ...
cmux workspace lookup --metadata <key=value> [--include-detached] --json
cmux tree --workspace current --json
cmux ssh <canonical-host> [--cwd <path>] [--name <title>] [--json] [-- <cmd>]
cmux new-pane --workspace current --type terminal|browser [--direction <dir>] [--url <url>] [--command <cmd>] [--focus true|false]
cmux new-surface --workspace current --type terminal|browser [--pane <pane>] [--url <url>] [--command <cmd>] [--focus true|false]
cmux new-split <dir> --workspace current [--surface <surface>] [--type terminal|browser] [--url <url>] [--command <cmd>] [--focus true|false]
cmux close-surface --workspace current --surface <surface>
cmux rename-workspace [--workspace current] '<title>'
cmux rename-window [--workspace current] '<title>'
cmux rename-tab [--workspace current] --surface <surface> '<title>'
cmux set-status [--workspace current] <key> <value> [--icon <icon>] [--color <color>] [--priority <n>]
cmux clear-status [--workspace current] <key>
cmux list-status [--workspace current]
cmux send [--workspace current] --surface <surface> -- '<text>'
cmux send-key [--workspace current] --surface <surface> <key>
```

The remote wrapper accepts `--json` before or after the command for these daemon-relayed commands, so both `cmux --json new-surface ...` and `cmux new-surface ... --json` are valid.

Remote/headless `cmux workspace lookup` is a query command. If the Swift relay is available, it routes to Swift and searches the workspaces Swift knows about. If the relay is unavailable and `--include-detached` is absent, it returns an empty match set. If `--include-detached` is present, it scans all detached snapshots under the current remote host's daemon root and matches by metadata.

Detached-safe remote commands can run without `CMUX_SOCKET_PATH` when the remote workspace context environment is present (`CMUX_WORKSPACE_ID` and/or `CMUX_REMOTE_DAEMON_SLOT`). In that case the wrapper skips the relay attempt and goes straight to the headless snapshot/daemon path. If a Swift relay socket is present and reachable, it remains authoritative; server-side Swift errors are returned to the caller instead of being masked by a detached fallback.

Remote `cmux ssh <host>` is the workspace creation primitive for supervisor scripts. Use the canonical SSH destination that the Mac-side cmux app can also use for this host, for example `ed@tdb`, rather than `localhost`. When the Swift relay is available, the wrapper sends `workspace.remote.ssh_create` to the Mac app so the new workspace is created through the normal attached UI path and appears in the sidebar. When the relay is unavailable, the wrapper only supports same-host destinations (`localhost`, the current hostname, or `user@current-host`) and falls back to creating a detached snapshot directly on the remote host: it creates a new persistent daemon slot, starts the workspace's initial terminal PTY in the requested remote working directory, writes `workspace-snapshot.json` / `.meta.json`, and returns the new `workspace_id`, `surface_id`, and `persistent_daemon_slot`.

Known limitation: the attached and detached paths need to agree on the host string. `localhost` is reliable for proving same-host in the detached fallback, but it is not safe when the Swift relay is available because Swift would interpret `localhost` from the Mac. Scripts should prefer a stable host alias/name that works from the Mac and also matches the remote host's hostname or `$HOSTNAME`/`$HOST` for detached fallback. If those cannot be made to agree, detached same-host workspace creation may need to use `localhost` while attached workspace creation uses the Mac-reachable host name.

Remote `cmux new-workspace` remains the generic UI workspace command. It routes to Swift when attached, but it does not create detached remote snapshots when the UI is unavailable.

For detached-capable mutations, `--workspace current` means the caller workspace from `CMUX_WORKSPACE_ID` / `CMUX_REMOTE_DAEMON_SLOT`. An explicit workspace ID is a target context switch: the remote CLI resolves `~/.cmux/daemon/*/workspace-snapshot.meta.json` to find the target slot, tries the target slot's `relay_socket` if Swift is currently attached to that workspace, and falls back to mutating that target snapshot if the relay is unavailable. PTY operations use the target slot's daemon. This is what lets an orchestrator running in one remote workspace create or maintain surfaces in task workspaces while the Mac UI is attached to either workspace, detached from either workspace, or temporarily disconnected.

Terminal creation in detached mode starts a real PTY through the persistent daemon before writing the new terminal surface into the snapshot. Browser creation records the initial URL/title only.

Detached `send` / `send-key` only work for terminal or agent surfaces whose snapshot contains a `remotePTYSessionId`. They resolve the terminal surface from the snapshot and call the slot daemon directly:

```text
pty.write_session { session_id, data_base64 }
pty.send          { session_id, text }
pty.send_key      { session_id, key }
```

These RPCs require normal daemon auth and write to the PTY session input queue without creating a temporary attachment. `send-key` intentionally supports the common supervisor keys rather than a full keyboard model: `enter`, `tab`, `escape`, `backspace`, arrow keys, `home`, `end`, `delete`, `pageup`, `pagedown`, `space`, and `ctrl-<letter>` aliases including `ctrl-c`, `ctrl-d`, `ctrl-z`, and `ctrl-\`.

For supervisor scripts, prefer startup commands when creating new surfaces:

```bash
cmux new-pane --workspace current --type terminal --command '<command>'
cmux new-pane --workspace current --type browser --url '<url>'
```

Use `send` / `send-key` when the script intentionally needs to interact with an already-running terminal process.

`rename-workspace` and `rename-window` are aliases in this branch. In detached mode they update both the snapshot body title and the discovery sidecar title. `rename-tab` updates the saved pane title for terminal and browser surfaces; unsupported surface types return a clear error.

`set-status`, `clear-status`, and `list-status` keep using the Swift socket while the relay is available. When the relay is unavailable, they mutate or read `RemoteWorkspaceSnapshotV1.statusEntries` in the remote snapshot. Status entries are visible UI state, not hidden metadata: they are intended for banners/badges that should reappear when Swift attaches or reconnects.

### Attached-Only Commands

These still require an attached Swift UI:

- focus and selection commands;
- workspace/window movement;
- browser automation or navigation after browser creation;
- access to local Mac browser state such as cookies, scroll position, devtools, or WKWebView session data.

Craft should treat failures from these commands as operator-convenience failures, not supervisor-critical failures.

### Snapshot Reconciliation

Remote snapshots have a `status`:

- `detached`: workspace is not present in the Mac sidebar and is shown by Host Manager / `ssh-workspace-list-detached`;
- `live`: workspace is attached or reconnected; snapshot is a remote checkpoint and hidden from detached listings.

Detach writes a `detached` snapshot and removes the local workspace. Attach restores the workspace using the original `workspaceId`, preserving the UUID across detach/attach, then writes a `live` snapshot instead of clearing it.

When Swift reconnects to a workspace whose remote snapshot changed while detached or disconnected, the remote snapshot wins. Swift rebuilds local layout from the remote snapshot, reapplies snapshot-backed status banners, and validates PTYs during restore. Missing PTYs become lost placeholders.

### Validated E2E Behavior

The current branch was tested on `ed@tdb` with the dev build:

- created a remote workspace with multiple panes and browser/terminal surfaces;
- detached it from the Mac UI;
- ran remote `cmux` commands after forcing the Mac relay unavailable;
- set hidden metadata from the remote host;
- created a browser pane from the remote host;
- created a terminal pane from the remote host with `--command`;
- sent text and `ctrl-d` to a detached terminal PTY through the remote daemon without creating a temporary attachment;
- renamed the detached workspace and a terminal surface from the remote host;
- set/listed/cleared a detached status banner through the remote snapshot;
- reattached from the Mac;
- preserved the original workspace UUID;
- restored the remote-created terminal process and output;
- created another remote terminal after reattach.

## Craft Integration Plan

Craft should move cmux integration away from title-prefix conventions. The new cmux branch provides enough hidden metadata and detached-safe primitives for Craft to keep task workspaces controllable while the Mac UI is detached or temporarily disconnected. Visible status banners can still be used for operator feedback because they now survive detach/attach and can be set from a detached remote session.

### Workspace Identity

Use hidden metadata as the authoritative workspace identity.

Suggested keys:

```text
craft:schema-version=1
craft:project-id=<stable project slug/id>
craft:project-dir=<absolute project path>
craft:task-id=<task id>              # task workspaces only
craft:task-dir=<absolute task path>  # task workspaces only
```

Titles should be human-readable labels only. They can still include task names for the sidebar, but Craft should not use them for lookup.

Task workspace lookup:

```bash
cmux workspace lookup \
  --metadata craft:project-id="$project_id" \
  --metadata craft:task-id="$task_id" \
  --include-detached \
  --json
```

Project workspace lookup can search by `craft:project-id` and then let Craft filter out results that have `craft:task-id`. cmux does not need a dedicated `workspace.kind`.

When Craft creates a workspace, it should immediately write the identity metadata before creating secondary surfaces.

### Surface Identity

Craft should keep semantic surface references in hidden workspace metadata. cmux still owns opaque surface IDs.

Do not keep separate cmux integration paths for "task panes" and "named panes". Both are semantic surfaces:

- the main task agent is the canonical task surface, usually `craft:surface:agent`;
- plugin or auxiliary panes are additional semantic surfaces, for example `craft:surface:diffhub-review` or `craft:surface:logs`;
- task-specific helpers should be thin wrappers over the same semantic-surface provider primitives.

Suggested metadata keys:

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

Suggested JSON value:

```json
{
  "surface_id": "surface-or-uuid",
  "type": "terminal",
  "purpose": "agent",
  "title": "task-123",
  "url": null,
  "agent": "claude",
  "updated_at": "2026-05-28T00:00:00Z"
}
```

For browser surfaces, set `"type": "browser"` and include `"url"`.

Craft owns the semantic meaning and retry/ensure policy. cmux only stores the JSON string and exposes `tree` for existence checks.

### Provider Refactor

Add a small cmux provider layer around JSON commands:

```bash
_cmux_workspace_lookup_by_metadata
_cmux_metadata_get
_cmux_metadata_set_json
_cmux_metadata_clear
_cmux_tree_json
_cmux_surface_exists
_cmux_surface_from_metadata
_cmux_record_surface
_cmux_ensure_surface
_cmux_send_to_surface
_cmux_close_surface
```

Then update current Craft helpers:

- `_mux_ws_ref`: replace title-prefix matching with metadata lookup.
- `_mux_ws_window`: keep as attached-only; do not make supervisor logic depend on it.
- `_mux_surface_by_tab_title`: replace with metadata lookup plus `tree` existence validation.
- `ensure_session`: create/refresh the project workspace, then write `craft:project-id` and `craft:project-dir`.
- `ensure_task_session`: create/refresh the task workspace, then write `craft:project-id`, `craft:task-id`, and `craft:task-dir`.
- `spawn_task_pane` and `mux_spawn_named_pane`: collapse into one semantic-surface creation path. The task agent is just `craft:surface:agent`; other named panes are other `craft:surface:<name>` keys.
- `pane_is_running` and `mux_pane_exists`: collapse into one surface existence check that reads the semantic surface metadata, then validates the recorded `surface_id` against `cmux tree`.
- `kill_task_pane` and `mux_kill_named_pane`: collapse into one close path that closes the recorded surface and clears the corresponding semantic surface metadata key.
- `mux_send_to_pane`: send to the recorded terminal `surface_id`; this is detached-safe for terminal/agent surfaces only.
- status/banner helpers: keep using `set-status`, `clear-status`, and `list-status` for visible operator feedback. Do not use status entries as identity or lookup state.

The existing discoverer-specific path should become a normal agent surface with different Craft-side configuration. cmux does not need special discoverer behavior.

Known raw cmux call sites to consolidate into the provider layer on the Craft `feat/refactor` branch:

- `bin/orchestrator.sh`: first-run cmux workspace bootstrap currently calls `cmux new-workspace`, `rename-workspace`, `tree`, `rename-tab`, `send`, `send-key`, and `select-workspace` directly. Replace remote task workspace creation with `cmux ssh <canonical-host> --cwd <task-dir> --name <title> --json`; move workspace identity/surface recording through the provider; keep `select-workspace` as a relay-only UI action.
- `bin/lib/mux-cmux.sh`: this is the main cmux provider today, but it mixes provider primitives with title-prefix lookup, tab-title lookup, dashboard surface management, architect/task pane setup, and UI focus. Split this into metadata-based workspace/surface primitives plus thin compatibility wrappers.
- `plugins/craft-dashboard/dashboard/cmux.ts`: dashboard focus logic shells out to `cmux` directly and still relies on title/tab matching and status reads. Keep a dashboard-specific wrapper if useful, but route identity through metadata/tree and classify focus/select/attach failures as `cmux_ui_unavailable`.
- `plugins/craft-dashboard/scripts/set-task-state`: finds task workspaces with `cmux tree --all --json` title matching before `set-status`. Replace workspace lookup with metadata lookup; keep `set-status` for visible feedback only.
- `plugins/planning/scripts/start-discoverer`: directly creates a terminal surface, renames it, and sends a prompt. Replace the discoverer special case with the common semantic-surface path for an agent surface with different configuration.
- `plugins/buildkite-status/scripts/show-build-status` and `plugins/buildkite-status/hooks.sh`: directly search/create/rename/close browser surfaces by URL/title. Replace with semantic browser surface metadata such as `craft:surface:buildkite-status`.
- Diffhub/Devin scripts already mostly use their own surface files or higher-level helpers, but any direct cmux surface open/close path should be folded into the same semantic-surface provider instead of keeping plugin-local cmux shell logic.

### Detached-Safe Craft Operations

Craft supervisor scripts can rely on these in attached and detached remote workspaces:

- find a task workspace by metadata;
- read/write hidden metadata;
- inspect workspace tree;
- create terminal surfaces with startup commands;
- create browser surfaces with initial URLs;
- send text or common keys to known terminal/agent surfaces;
- close known surfaces.
- rename task workspaces and known terminal/browser surfaces;
- set, list, and clear visible status banners.

Craft should not require these for detached supervisor correctness:

- selecting or focusing a workspace/surface;
- moving workspaces between windows;
- browser automation after initial browser creation.

### Dashboard UI Actions

The Craft dashboard web server should ideally run on the remote host inside the orchestrator workspace. When the orchestrator workspace is attached to the Mac Swift UI, the injected remote `cmux` wrapper has a live relay/socket path back to Swift. In that state, user-facing UI actions can use normal cmux commands and should behave as they do locally:

```bash
cmux select-workspace --workspace <workspace>
cmux focus-panel --panel <surface>
cmux ssh-workspace-attach --workspace-id <workspace-id> --json
```

These actions are different from supervisor-critical detached operations. They need an attached Swift UI because their purpose is to show or focus something in the Mac app. The remote daemon can keep PTYs and snapshots alive, but it cannot focus a Mac window or create a sidebar workspace by itself.

Craft backend behavior should be:

- resolve workspace and surface identity using detached-safe metadata/tree commands;
- when a user clicks a UI action such as "show terminal", first attempt to focus/select through the relay;
- if focus/select fails because the relay or Swift socket is unavailable, optionally run `cmux ssh-workspace-attach --workspace-id <id> --json` through the same relay when enough host/slot information is available;
- after a successful attach, retry the focus/select action once;
- if attach is unavailable or fails because there is no live Swift relay, return a graceful non-fatal response such as `cmux_ui_unavailable` instead of treating it as a backend crash;
- do not retry UI focus/attach in a loop from background supervisor logic.

This means a detached task can continue to run and mutate its remote snapshot, while dashboard affordances that require the Mac UI degrade cleanly. It may be desirable for "show terminal", "open task workspace", and similar buttons to auto-attach the workspace before focusing the requested surface, because that is the interaction a user probably expects when operating from an attached dashboard.

### Example Detached-Safe Supervisor Flow

```bash
workspace_json="$(cmux workspace lookup \
  --metadata craft:project-id="$project_id" \
  --metadata craft:task-id="$task_id" \
  --include-detached \
  --json)"

workspace_id="$(printf '%s' "$workspace_json" | jq -r '.matches[0].id')"

surface_json="$(cmux new-pane \
  --workspace "$workspace_id" \
  --type terminal \
  --direction down \
  --command "$agent_command" \
  --json)"

surface_id="$(printf '%s' "$surface_json" | jq -r '.surface_id // .surface_ref')"

cmux metadata set \
  --workspace "$workspace_id" \
  craft:surface:agent \
  --value-json "{\"surface_id\":\"$surface_id\",\"type\":\"terminal\",\"purpose\":\"agent\",\"agent\":\"$agent\",\"updated_at\":\"$(date -u +%Y-%m-%dT%H:%M:%SZ)\"}"
```

When running inside the remote workspace and the Mac relay is unavailable, the same commands operate on the remote snapshot.

## TODO: Concurrency Control

Add locking or lightweight compare-and-swap semantics for remote snapshot metadata and layout mutations after the concept is stable. Concurrent remote scripts should not be able to silently overwrite each other's metadata or layout changes long term.
