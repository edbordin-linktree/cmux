package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// headlessTestEnv anchors a per-test daemon root and seeded snapshot. The
// caller picks a workspace+surface ID so test assertions are stable.
type headlessTestEnv struct {
	root        string
	slot        string
	slotRoot    string
	workspaceID string
	surfaceID   string
}

func setupHeadlessSnapshot(t *testing.T) *headlessTestEnv {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	// Tests may run inside an attached cmux shell that exports these vars,
	// which would otherwise leak into the handlers and skip the "missing
	// param" validation paths.
	for _, key := range []string{
		"CMUX_WORKSPACE_ID",
		"CMUX_SURFACE_ID",
		"CMUX_TAB_ID",
		"CMUX_PANEL_ID",
		"CMUX_REMOTE_DAEMON_SLOT",
		"CMUX_PERSISTENT_DAEMON_SLOT",
		"CMUX_DAEMON_SLOT",
	} {
		t.Setenv(key, "")
	}

	workspaceID := "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e"
	surfaceID := "11111111-2222-3333-4444-555555555555"
	slot := "ssh-" + workspaceID
	slotRoot := filepath.Join(root, slot)
	if err := os.MkdirAll(slotRoot, 0o700); err != nil {
		t.Fatalf("mkdir slot: %v", err)
	}

	body := map[string]any{
		"version":     1,
		"workspaceId": workspaceID,
		"title":       "training run",
		"splitTree": map[string]any{
			"type": "pane",
			"pane": map[string]any{
				"panelIds":        []any{surfaceID},
				"selectedPanelId": surfaceID,
			},
		},
		"panes": []any{map[string]any{
			"type": "terminal",
			"terminal": map[string]any{
				"paneId":             surfaceID,
				"remotePTYSessionId": "session-1",
				"title":              "Terminal",
			},
		}},
		"activePaneId": surfaceID,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotRoot, workspaceSnapshotBodyFile), bodyBytes, 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}

	meta := workspaceSnapshotMeta{
		Version:        1,
		WorkspaceID:    workspaceID,
		Title:          "training run",
		Status:         "detached",
		DetachedAt:     "2026-05-27T01:02:03Z",
		UpdatedAt:      "2026-05-27T01:02:03Z",
		SchemaVersion:  1,
		SnapshotSHA256: sha256Hex(string(bodyBytes)),
		BodyByteLength: len(bodyBytes),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotRoot, workspaceSnapshotMetaFile), metaBytes, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	return &headlessTestEnv{
		root:        root,
		slot:        slot,
		slotRoot:    slotRoot,
		workspaceID: workspaceID,
		surfaceID:   surfaceID,
	}
}

func (env *headlessTestEnv) params() map[string]any {
	return map[string]any{"workspace_id": env.workspaceID}
}

func (env *headlessTestEnv) loadBody(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(env.slotRoot, workspaceSnapshotBodyFile))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}

func (env *headlessTestEnv) loadMeta(t *testing.T) workspaceSnapshotMeta {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(env.slotRoot, workspaceSnapshotMetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta workspaceSnapshotMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	return meta
}

func stubHeadlessPTY(t *testing.T) {
	t.Helper()
	prev := headlessStartPTYFunc
	headlessStartPTYFunc = func(slot, sessionID, attachmentID, command string) error { return nil }
	t.Cleanup(func() { headlessStartPTYFunc = prev })
}

func stubHeadlessRPC(t *testing.T, response map[string]any, err error) *[]map[string]any {
	t.Helper()
	prev := headlessPersistentDaemonRPCFunc
	calls := []map[string]any{}
	headlessPersistentDaemonRPCFunc = func(slot, method string, params map[string]any) (map[string]any, error) {
		call := map[string]any{"slot": slot, "method": method, "params": params}
		calls = append(calls, call)
		return response, err
	}
	t.Cleanup(func() { headlessPersistentDaemonRPCFunc = prev })
	return &calls
}

func TestRunHeadlessCLIResultUnsupported(t *testing.T) {
	_, err := runHeadlessCLIResult("totally.unknown", map[string]any{})
	if err == nil {
		t.Fatalf("expected unsupported method error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported detached command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHeadlessSupportsMethodFromRegistry(t *testing.T) {
	for method := range headlessHandlers {
		if !headlessSupportsMethod(method) {
			t.Errorf("headlessSupportsMethod(%q) = false, want true (registry/support drift)", method)
		}
	}
	if headlessSupportsMethod("workspace.create") {
		t.Errorf("workspace.create unexpectedly reported as supported")
	}
}

func TestHeadlessMetadataLifecycle(t *testing.T) {
	env := setupHeadlessSnapshot(t)

	setResult, err := headlessMetadataSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "team",
		"value":        "trust",
	})
	if err != nil {
		t.Fatalf("metadata.set: %v", err)
	}
	if got := setResult["snapshot_sha256"]; got == "" {
		t.Fatalf("metadata.set missing snapshot_sha256: %+v", setResult)
	}

	getResult, err := headlessMetadataGet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "team",
	})
	if err != nil {
		t.Fatalf("metadata.get: %v", err)
	}
	if !reflect.DeepEqual(getResult["value"], "trust") || getResult["exists"] != true {
		t.Fatalf("metadata.get returned %+v, want value=trust exists=true", getResult)
	}

	if _, err := headlessMetadataSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "owner",
		"value":        "ed",
	}); err != nil {
		t.Fatalf("metadata.set owner: %v", err)
	}

	listResult, err := headlessMetadataList(map[string]any{"workspace_id": env.workspaceID})
	if err != nil {
		t.Fatalf("metadata.list: %v", err)
	}
	entries, _ := listResult["entries"].([]map[string]any)
	if len(entries) != 2 {
		t.Fatalf("metadata.list entries = %d, want 2 (%+v)", len(entries), listResult)
	}
	if entries[0]["key"].(string) != "owner" || entries[1]["key"].(string) != "team" {
		t.Fatalf("metadata.list keys not sorted: %+v", entries)
	}

	if _, err := headlessMetadataClear(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "team",
	}); err != nil {
		t.Fatalf("metadata.clear: %v", err)
	}
	body := env.loadBody(t)
	stored, _ := body["metadataEntries"].(map[string]any)
	if _, exists := stored["team"]; exists {
		t.Fatalf("metadata.clear did not remove team key: %+v", stored)
	}
	if stored["owner"] != "ed" {
		t.Fatalf("metadata.clear unexpectedly affected owner: %+v", stored)
	}
}

func TestHeadlessMetadataSetRequiresKeyAndValue(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	cases := []struct {
		name   string
		params map[string]any
	}{
		{"empty key", map[string]any{"workspace_id": env.workspaceID, "value": "v"}},
		{"empty value", map[string]any{"workspace_id": env.workspaceID, "key": "k"}},
		{"whitespace key", map[string]any{"workspace_id": env.workspaceID, "key": "  ", "value": "v"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := headlessMetadataSet(tc.params); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestHeadlessMetadataListPrefixFilters(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	for k, v := range map[string]string{
		"cmux:team":  "trust",
		"cmux:owner": "ed",
		"other:key":  "ignored",
	} {
		if _, err := headlessMetadataSet(map[string]any{
			"workspace_id": env.workspaceID,
			"key":          k,
			"value":        v,
		}); err != nil {
			t.Fatalf("seed metadata %s: %v", k, err)
		}
	}
	listResult, err := headlessMetadataList(map[string]any{
		"workspace_id": env.workspaceID,
		"prefix":       "cmux:",
	})
	if err != nil {
		t.Fatalf("metadata.list: %v", err)
	}
	entries, _ := listResult["entries"].([]map[string]any)
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e["key"].(string))
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"cmux:owner", "cmux:team"}) {
		t.Fatalf("prefix filter returned %v", keys)
	}
}

func TestHeadlessRenameWorkspace(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessRenameWorkspace(map[string]any{"workspace_id": env.workspaceID}); err == nil {
		t.Fatalf("expected error on missing title")
	}
	result, err := headlessRenameWorkspace(map[string]any{
		"workspace_id": env.workspaceID,
		"title":        "renamed",
	})
	if err != nil {
		t.Fatalf("workspace.rename: %v", err)
	}
	if result["title"] != "renamed" {
		t.Fatalf("result title = %v, want renamed", result["title"])
	}
	body := env.loadBody(t)
	if body["title"] != "renamed" {
		t.Fatalf("body title = %v, want renamed", body["title"])
	}
	meta := env.loadMeta(t)
	if meta.Title != "renamed" {
		t.Fatalf("meta title = %q, want renamed", meta.Title)
	}
}

func TestHeadlessTabActionRename(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	_, err := headlessTabAction(map[string]any{
		"workspace_id": env.workspaceID,
		"action":       "rename",
		"surface_id":   env.surfaceID,
		"title":        "renamed terminal",
	})
	if err != nil {
		t.Fatalf("tab.action rename: %v", err)
	}
	body := env.loadBody(t)
	panes, _ := body["panes"].([]any)
	if len(panes) != 1 {
		t.Fatalf("expected one pane, got %d", len(panes))
	}
	pane := panes[0].(map[string]any)
	terminal, _ := pane["terminal"].(map[string]any)
	if terminal["title"] != "renamed terminal" {
		t.Fatalf("terminal title = %v, want renamed terminal", terminal["title"])
	}
}

func TestHeadlessTabActionRejectsUnsupportedAction(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessTabAction(map[string]any{
		"workspace_id": env.workspaceID,
		"action":       "move",
		"surface_id":   env.surfaceID,
		"title":        "x",
	}); err == nil {
		t.Fatalf("expected error for unsupported action")
	}
}

func TestHeadlessRenameSurfaceUnknownSurface(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessRenameSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   "does-not-exist",
		"title":        "x",
	}); err == nil {
		t.Fatalf("expected error for unknown surface")
	}
}

func TestHeadlessCloseSurface(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessPTY(t)
	_, err := headlessCreateSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"type":         "terminal",
	}, false)
	if err != nil {
		t.Fatalf("surface.create: %v", err)
	}
	body := env.loadBody(t)
	panes, _ := body["panes"].([]any)
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes after create, got %d", len(panes))
	}

	closeResult, err := headlessCloseSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   env.surfaceID,
	})
	if err != nil {
		t.Fatalf("surface.close: %v", err)
	}
	if closeResult["closed"] != true {
		t.Fatalf("close result = %+v, want closed=true", closeResult)
	}
	body = env.loadBody(t)
	panes, _ = body["panes"].([]any)
	if len(panes) != 1 {
		t.Fatalf("expected 1 pane after close, got %d", len(panes))
	}
	if active, _ := body["activePaneId"].(string); strings.EqualFold(active, env.surfaceID) {
		t.Fatalf("activePaneId still points at closed surface: %v", active)
	}
}

func TestHeadlessCloseSurfaceRequiresID(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessCloseSurface(env.params()); err == nil {
		t.Fatalf("expected error when surface_id missing")
	}
}

func TestHeadlessCreateSurfaceTerminal(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessPTY(t)
	result, err := headlessCreateSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"type":         "terminal",
	}, false)
	if err != nil {
		t.Fatalf("surface.create: %v", err)
	}
	if result["type"] != "terminal" {
		t.Fatalf("type = %v, want terminal", result["type"])
	}
	surfaceID, _ := result["surface_id"].(string)
	if surfaceID == "" {
		t.Fatalf("missing surface_id: %+v", result)
	}
	body := env.loadBody(t)
	if !strings.EqualFold(stringFromAny(body["activePaneId"]), surfaceID) {
		t.Fatalf("activePaneId = %v, want %s", body["activePaneId"], surfaceID)
	}
}

func TestHeadlessCreateSurfaceBrowser(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessPTY(t) // create.browser does not call PTY but stays safe under refactor.
	result, err := headlessCreateSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"type":         "browser",
		"url":          "https://example.com",
	}, false)
	if err != nil {
		t.Fatalf("surface.create browser: %v", err)
	}
	if result["type"] != "browser" {
		t.Fatalf("type = %v, want browser", result["type"])
	}
}

func TestHeadlessSurfaceSplitInsertsPane(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessPTY(t)
	_, err := headlessCreateSurface(map[string]any{
		"workspace_id": env.workspaceID,
		"type":         "terminal",
		"direction":    "right",
	}, true)
	if err != nil {
		t.Fatalf("surface.split: %v", err)
	}
	body := env.loadBody(t)
	tree, _ := body["splitTree"].(map[string]any)
	if tree["type"] != "split" {
		t.Fatalf("splitTree root = %v, want split", tree["type"])
	}
}

func TestHeadlessSendTextStubsRPC(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	calls := stubHeadlessRPC(t, map[string]any{"session_id": "session-1", "written": 5}, nil)

	result, err := headlessSendText(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   env.surfaceID,
		"text":         "hello",
	})
	if err != nil {
		t.Fatalf("surface.send_text: %v", err)
	}
	if result["written"] != 5 {
		t.Fatalf("written = %v, want 5", result["written"])
	}
	if len(*calls) != 1 {
		t.Fatalf("expected 1 RPC call, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call["method"] != "pty.send" {
		t.Fatalf("rpc method = %v, want pty.send", call["method"])
	}
	params, _ := call["params"].(map[string]any)
	if params["text"] != "hello" || params["session_id"] != "session-1" {
		t.Fatalf("rpc params = %+v", params)
	}
}

func TestHeadlessSendTextRejectsNonStringText(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessRPC(t, map[string]any{}, nil)
	if _, err := headlessSendText(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   env.surfaceID,
		"text":         42,
	}); err == nil {
		t.Fatalf("expected error when text is not a string")
	}
}

func TestHeadlessSendKeyForwardsAndLowercases(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessRPC(t, map[string]any{"session_id": "session-1", "written": 1}, nil)
	result, err := headlessSendKey(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   env.surfaceID,
		"key":          "Enter",
	})
	if err != nil {
		t.Fatalf("surface.send_key: %v", err)
	}
	if result["key"] != "enter" {
		t.Fatalf("key = %v, want enter", result["key"])
	}
}

func TestHeadlessSendTextPropagatesRPCError(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	stubHeadlessRPC(t, nil, errors.New("boom"))
	if _, err := headlessSendText(map[string]any{
		"workspace_id": env.workspaceID,
		"surface_id":   env.surfaceID,
		"text":         "hi",
	}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected boom error, got %v", err)
	}
}

func TestHeadlessStatusLifecycle(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessStatusSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "ci",
		"value":        "running",
		"priority":     5,
	}); err != nil {
		t.Fatalf("status.set: %v", err)
	}
	if _, err := headlessStatusSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "ci",
		"value":        "passed",
	}); err != nil {
		t.Fatalf("status.set replace: %v", err)
	}
	listResult, err := headlessStatusList(env.params())
	if err != nil {
		t.Fatalf("status.list: %v", err)
	}
	entries, _ := listResult["entries"].([]map[string]any)
	if len(entries) != 1 {
		t.Fatalf("status entries len = %d, want 1 (no duplicate after replace)", len(entries))
	}
	if entries[0]["value"] != "passed" {
		t.Fatalf("status value = %v, want passed (replace must overwrite)", entries[0]["value"])
	}

	clearResult, err := headlessStatusClear(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "ci",
	})
	if err != nil {
		t.Fatalf("status.clear: %v", err)
	}
	if clearResult["cleared"] != true {
		t.Fatalf("clear result = %+v, want cleared=true", clearResult)
	}
	body := env.loadBody(t)
	if _, present := body["statusEntries"]; present {
		t.Fatalf("statusEntries should be absent after final clear: %+v", body)
	}
}

func TestHeadlessStatusSetRequiresKey(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessStatusSet(map[string]any{
		"workspace_id": env.workspaceID,
		"value":        "running",
	}); err == nil {
		t.Fatalf("expected error on missing key")
	}
}

func TestHeadlessWorkspaceLookupRequiresMetadata(t *testing.T) {
	if _, err := headlessWorkspaceLookup(map[string]any{}); err == nil {
		t.Fatalf("expected error when criteria missing")
	}
}

func TestHeadlessWorkspaceLookupFindsDetached(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessMetadataSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "cmux:task",
		"value":        "abc-123",
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}

	result, err := headlessWorkspaceLookup(map[string]any{
		"metadata":         map[string]any{"cmux:task": "abc-123"},
		"include_detached": true,
	})
	if err != nil {
		t.Fatalf("workspace.lookup: %v", err)
	}
	if result["count"] != 1 {
		t.Fatalf("count = %v, want 1 (%+v)", result["count"], result)
	}
	matches, _ := result["matches"].([]map[string]any)
	if len(matches) != 1 {
		t.Fatalf("matches len = %d, want 1", len(matches))
	}
	if matches[0]["id"] != env.workspaceID {
		t.Fatalf("match id = %v, want %s", matches[0]["id"], env.workspaceID)
	}
}

func TestHeadlessWorkspaceLookupSkipsWhenDetachedExcluded(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	if _, err := headlessMetadataSet(map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "k",
		"value":        "v",
	}); err != nil {
		t.Fatalf("seed metadata: %v", err)
	}
	result, err := headlessWorkspaceLookup(map[string]any{
		"metadata":         map[string]any{"k": "v"},
		"include_detached": false,
	})
	if err != nil {
		t.Fatalf("workspace.lookup: %v", err)
	}
	if result["detached_searched"] != false {
		t.Fatalf("detached_searched = %v, want false", result["detached_searched"])
	}
	if result["count"] != 0 {
		t.Fatalf("count = %v, want 0 when detached excluded", result["count"])
	}
}

func TestRunHeadlessCLIResultRoutesViaRegistry(t *testing.T) {
	env := setupHeadlessSnapshot(t)
	// Pick a representative method to confirm the registry actually dispatches.
	result, err := runHeadlessCLIResult("metadata.set", map[string]any{
		"workspace_id": env.workspaceID,
		"key":          "route",
		"value":        "yes",
	})
	if err != nil {
		t.Fatalf("dispatch via registry: %v", err)
	}
	if result["key"] != "route" {
		t.Fatalf("registry-dispatched handler returned %+v", result)
	}
}

func TestHeadlessFailureMessageIncludesRelayErr(t *testing.T) {
	got := headlessFailureMessage(
		"metadata.set",
		errors.New("snapshot missing"),
		errors.New("dial unix /tmp/cmux.sock: connect: connection refused"),
	)
	if !strings.Contains(got, "snapshot missing") {
		t.Errorf("missing headless reason: %q", got)
	}
	if !strings.Contains(got, "connection refused") {
		t.Errorf("relay error dropped from stderr message: %q", got)
	}
}

func TestHeadlessFailureMessageOmitsRelaySuffixWhenNil(t *testing.T) {
	got := headlessFailureMessage("metadata.set", errors.New("snapshot missing"), nil)
	if !strings.Contains(got, "snapshot missing") {
		t.Errorf("missing headless reason: %q", got)
	}
	if strings.Contains(got, "relay") {
		t.Errorf("unexpected relay suffix when relay error is nil: %q", got)
	}
}
