package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type headlessSnapshot struct {
	slot     string
	root     string
	bodyPath string
	metaPath string
	body     map[string]any
	meta     workspaceSnapshotMeta
}

func runHeadlessCLICommand(commandName, method string, params map[string]any, jsonOutput bool, relayErr error) (int, bool) {
	switch method {
	case "metadata.set", "metadata.get", "metadata.list", "metadata.clear",
		"workspace.lookup", "system.tree",
		"surface.create", "pane.create", "surface.split", "surface.close":
	default:
		return 0, false
	}

	result, err := runHeadlessCLIResult(method, params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %s requires an attached cmux UI or a detached remote snapshot: %v\n", commandName, err)
		return 1, true
	}
	payload, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: failed to encode headless result: %v\n", err)
		return 1, true
	}
	if jsonOutput {
		fmt.Println(string(payload))
	} else {
		fmt.Println(defaultRelayOutput(string(payload)))
	}
	_ = relayErr
	return 0, true
}

func runHeadlessCLIResult(method string, params map[string]any) (map[string]any, error) {
	switch method {
	case "metadata.set":
		return headlessMetadataSet(params)
	case "metadata.get":
		return headlessMetadataGet(params)
	case "metadata.list":
		return headlessMetadataList(params)
	case "metadata.clear":
		return headlessMetadataClear(params)
	case "workspace.lookup":
		return headlessWorkspaceLookup(params)
	case "system.tree":
		snap, err := loadHeadlessSnapshot(params)
		if err != nil {
			return nil, err
		}
		return headlessTreePayload(snap), nil
	case "surface.create":
		return headlessCreateSurface(params, false)
	case "pane.create", "surface.split":
		return headlessCreateSurface(params, true)
	case "surface.close":
		return headlessCloseSurface(params)
	default:
		return nil, fmt.Errorf("unsupported detached command %q", method)
	}
}

func loadHeadlessSnapshot(params map[string]any) (*headlessSnapshot, error) {
	workspaceID := headlessWorkspaceID(params)
	slot := firstNonEmptyEnv("CMUX_REMOTE_DAEMON_SLOT", "CMUX_PERSISTENT_DAEMON_SLOT", "CMUX_DAEMON_SLOT")
	rootBase, err := headlessDaemonRoot()
	if err != nil {
		return nil, err
	}
	if slot == "" {
		if workspaceID == "" {
			return nil, errors.New("CMUX_REMOTE_DAEMON_SLOT or CMUX_WORKSPACE_ID is required")
		}
		slot, err = findHeadlessSlotForWorkspace(rootBase, workspaceID)
		if err != nil {
			return nil, err
		}
	}
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return nil, err
	}
	bodyPath := filepath.Join(paths.root, workspaceSnapshotBodyFile)
	metaPath := filepath.Join(paths.root, workspaceSnapshotMetaFile)
	bodyBytes, err := os.ReadFile(bodyPath)
	if err != nil {
		return nil, fmt.Errorf("read detached snapshot body: %w", err)
	}
	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		return nil, fmt.Errorf("read detached snapshot metadata: %w", err)
	}
	var meta workspaceSnapshotMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, fmt.Errorf("decode detached snapshot metadata: %w", err)
	}
	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return nil, fmt.Errorf("decode detached snapshot body: %w", err)
	}
	if workspaceID != "" {
		bodyWorkspaceID, _ := body["workspaceId"].(string)
		if !strings.EqualFold(meta.WorkspaceID, workspaceID) && !strings.EqualFold(bodyWorkspaceID, workspaceID) {
			return nil, fmt.Errorf("detached snapshot workspace mismatch: have %s, want %s", meta.WorkspaceID, workspaceID)
		}
	}
	return &headlessSnapshot{
		slot:     slot,
		root:     paths.root,
		bodyPath: bodyPath,
		metaPath: metaPath,
		body:     body,
		meta:     meta,
	}, nil
}

func storeHeadlessSnapshot(snap *headlessSnapshot) (string, error) {
	bodyBytes, err := json.Marshal(snap.body)
	if err != nil {
		return "", err
	}
	if len(bodyBytes) > workspaceSnapshotMaxBytes {
		return "", errors.New("workspace snapshot body exceeds 1 MiB")
	}
	sum := sha256.Sum256(bodyBytes)
	sumHex := hex.EncodeToString(sum[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	snap.meta.Version = 1
	if snap.meta.WorkspaceID == "" {
		if workspaceID, _ := snap.body["workspaceId"].(string); workspaceID != "" {
			snap.meta.WorkspaceID = workspaceID
		}
	}
	if snap.meta.Title == "" {
		if title, _ := snap.body["title"].(string); title != "" {
			snap.meta.Title = title
		}
	}
	if snap.meta.DetachedAt == "" {
		snap.meta.DetachedAt = now
	}
	if snap.meta.SchemaVersion <= 0 {
		snap.meta.SchemaVersion = intFromAny(snap.body["version"])
		if snap.meta.SchemaVersion <= 0 {
			snap.meta.SchemaVersion = 1
		}
	}
	snap.meta.Status = workspaceSnapshotStatusOrDetached(snap.meta.Status)
	snap.meta.UpdatedAt = now
	snap.meta.SnapshotSHA256 = sumHex
	snap.meta.BodyByteLength = len(bodyBytes)
	metaBytes, err := json.Marshal(snap.meta)
	if err != nil {
		return "", err
	}
	if len(metaBytes) > workspaceSnapshotMetaMaxBytes {
		return "", errors.New("workspace snapshot metadata exceeds 4 KiB")
	}
	return sumHex, atomicWriteWorkspaceSnapshotPair(snap.bodyPath, bodyBytes, snap.metaPath, metaBytes)
}

func headlessMetadataSet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	value := stringFromAny(params["value"])
	if value == "" {
		value = stringFromAny(params["json_value"])
	}
	if key == "" || value == "" {
		return nil, errors.New("metadata.set requires key and value")
	}
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	metadata := headlessMetadataMap(snap.body)
	metadata[key] = value
	snap.body["metadataEntries"] = metadata
	sha, err := storeHeadlessSnapshot(snap)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id":    snap.meta.WorkspaceID,
		"key":             key,
		"value":           value,
		"detached":        true,
		"snapshot_sha256": sha,
	}, nil
}

func headlessMetadataGet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("metadata.get requires key")
	}
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	metadata := headlessMetadataMap(snap.body)
	value, exists := metadata[key]
	result := map[string]any{
		"workspace_id": snap.meta.WorkspaceID,
		"key":          key,
		"exists":       exists,
		"detached":     true,
	}
	if exists {
		result["value"] = value
	}
	return result, nil
}

func headlessMetadataList(params map[string]any) (map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	prefix := stringFromAny(params["prefix"])
	metadata := headlessMetadataMap(snap.body)
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		if prefix == "" || strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	entries := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, map[string]any{"key": key, "value": metadata[key]})
	}
	return map[string]any{
		"workspace_id": snap.meta.WorkspaceID,
		"entries":      entries,
		"count":        len(entries),
		"detached":     true,
	}, nil
}

func headlessMetadataClear(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("metadata.clear requires key")
	}
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	metadata := headlessMetadataMap(snap.body)
	_, existed := metadata[key]
	delete(metadata, key)
	snap.body["metadataEntries"] = metadata
	sha, err := storeHeadlessSnapshot(snap)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id":    snap.meta.WorkspaceID,
		"key":             key,
		"cleared":         existed,
		"detached":        true,
		"snapshot_sha256": sha,
	}, nil
}

func headlessWorkspaceLookup(params map[string]any) (map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	criteria, ok := params["metadata"].(map[string]string)
	if !ok {
		raw, _ := params["metadata"].(map[string]any)
		criteria = map[string]string{}
		for key, value := range raw {
			criteria[key] = stringFromAny(value)
		}
	}
	if len(criteria) == 0 {
		return nil, errors.New("workspace.lookup requires metadata criteria")
	}
	metadata := headlessMetadataMap(snap.body)
	matches := true
	for key, value := range criteria {
		if metadata[key] != value {
			matches = false
			break
		}
	}
	result := map[string]any{
		"criteria":          criteria,
		"include_detached":  true,
		"detached_searched": true,
		"matches":           []map[string]any{},
		"count":             0,
	}
	if matches {
		item := headlessWorkspaceSummary(snap)
		item["metadata"] = metadata
		result["matches"] = []map[string]any{item}
		result["count"] = 1
	}
	return result, nil
}

func headlessCreateSurface(params map[string]any, splitPane bool) (map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	workspaceID := snap.meta.WorkspaceID
	if workspaceID == "" {
		workspaceID, _ = snap.body["workspaceId"].(string)
	}
	surfaceID := strings.ToLower(newHeadlessUUID())
	panelType := strings.ToLower(strings.TrimSpace(stringFromAny(params["type"])))
	if panelType == "" {
		panelType = "terminal"
	}
	var paneSnapshot map[string]any
	if panelType == "browser" {
		url := strings.TrimSpace(stringFromAny(params["url"]))
		if url == "" {
			url = "about:blank"
		}
		paneSnapshot = map[string]any{
			"type": "browser",
			"browser": map[string]any{
				"paneId":     surfaceID,
				"currentURL": url,
				"title":      url,
			},
		}
	} else {
		sessionID := strings.TrimSpace(stringFromAny(params["remote_pty_session_id"]))
		if sessionID == "" {
			sessionID = "ssh-" + workspaceID + "-" + surfaceID
		}
		command := headlessPTYCommand(
			snap.slot,
			workspaceID,
			surfaceID,
			stringFromAny(params["initial_command"]),
		)
		if err := headlessStartPTY(snap.slot, sessionID, surfaceID, command); err != nil {
			return nil, err
		}
		paneSnapshot = map[string]any{
			"type": "terminal",
			"terminal": map[string]any{
				"paneId":             surfaceID,
				"remotePTYSessionId": sessionID,
				"title":              "Terminal",
			},
		}
	}
	snap.body["panes"] = append(headlessPaneSnapshots(snap.body), paneSnapshot)
	paneID := surfaceID
	if splitPane {
		direction := strings.ToLower(strings.TrimSpace(stringFromAny(params["direction"])))
		if direction == "" {
			direction = "right"
		}
		if err := headlessInsertSplitPane(snap.body, surfaceID, direction); err != nil {
			return nil, err
		}
	} else {
		paneID, err = headlessAppendSurfaceToPane(snap.body, surfaceID, stringFromAny(params["pane_id"]))
		if err != nil {
			return nil, err
		}
	}
	if headlessShouldFocus(params) {
		snap.body["activePaneId"] = surfaceID
	}
	sha, err := storeHeadlessSnapshot(snap)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id":    workspaceID,
		"workspace_ref":   "workspace:" + workspaceID,
		"pane_id":         paneID,
		"pane_ref":        "pane:" + paneID,
		"surface_id":      surfaceID,
		"surface_ref":     "surface:" + surfaceID,
		"type":            panelType,
		"detached":        true,
		"snapshot_sha256": sha,
	}, nil
}

func headlessCloseSurface(params map[string]any) (map[string]any, error) {
	surfaceID := strings.ToLower(strings.TrimSpace(stringFromAny(params["surface_id"])))
	if surfaceID == "" {
		surfaceID = strings.ToLower(strings.TrimSpace(os.Getenv("CMUX_SURFACE_ID")))
	}
	if surfaceID == "" {
		return nil, errors.New("surface.close requires surface_id")
	}
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	snap.body["panes"] = removeHeadlessPaneSnapshot(snap.body, surfaceID)
	removed := headlessRemoveSurfaceFromLayout(snap.body["splitTree"], surfaceID)
	if strings.EqualFold(stringFromAny(snap.body["activePaneId"]), surfaceID) {
		snap.body["activePaneId"] = firstHeadlessSurfaceID(snap.body["splitTree"])
	}
	sha, err := storeHeadlessSnapshot(snap)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id":    snap.meta.WorkspaceID,
		"surface_id":      surfaceID,
		"closed":          removed,
		"detached":        true,
		"snapshot_sha256": sha,
	}, nil
}

func headlessTreePayload(snap *headlessSnapshot) map[string]any {
	return map[string]any{
		"active": nil,
		"caller": map[string]any{
			"workspace_id": os.Getenv("CMUX_WORKSPACE_ID"),
			"surface_id":   os.Getenv("CMUX_SURFACE_ID"),
		},
		"windows": []map[string]any{{
			"id":         "detached",
			"ref":        "window:detached",
			"index":      0,
			"selected":   false,
			"workspaces": []map[string]any{headlessWorkspaceNode(snap)},
		}},
	}
}

func headlessWorkspaceNode(snap *headlessSnapshot) map[string]any {
	node := headlessWorkspaceSummary(snap)
	node["index"] = 0
	node["selected"] = false
	node["pinned"] = false
	node["metadata"] = headlessMetadataMap(snap.body)
	node["panes"] = headlessTreePanes(snap.body)
	return node
}

func headlessWorkspaceSummary(snap *headlessSnapshot) map[string]any {
	workspaceID := snap.meta.WorkspaceID
	if workspaceID == "" {
		workspaceID, _ = snap.body["workspaceId"].(string)
	}
	title := snap.meta.Title
	if title == "" {
		title, _ = snap.body["title"].(string)
	}
	return map[string]any{
		"id":                     workspaceID,
		"ref":                    "workspace:" + workspaceID,
		"title":                  title,
		"attached":               false,
		"detached":               true,
		"persistent_daemon_slot": snap.slot,
	}
}

func headlessTreePanes(body map[string]any) []map[string]any {
	snapshots := map[string]map[string]any{}
	for _, pane := range headlessPaneSnapshots(body) {
		if id := headlessPaneSnapshotID(pane); id != "" {
			snapshots[strings.ToLower(id)] = pane
		}
	}
	var panes []map[string]any
	var visit func(node any)
	visit = func(node any) {
		m, _ := node.(map[string]any)
		switch stringFromAny(m["type"]) {
		case "split":
			split, _ := m["split"].(map[string]any)
			visit(split["first"])
			visit(split["second"])
		case "pane":
			pane, _ := m["pane"].(map[string]any)
			ids := stringSliceFromAny(pane["panelIds"])
			selected := stringFromAny(pane["selectedPanelId"])
			if selected == "" && len(ids) > 0 {
				selected = ids[0]
			}
			paneID := selected
			if paneID == "" {
				paneID = newHeadlessUUID()
			}
			surfaces := make([]map[string]any, 0, len(ids))
			for idx, id := range ids {
				surfaces = append(surfaces, headlessSurfaceNode(id, snapshots[strings.ToLower(id)], paneID, idx, strings.EqualFold(id, selected), strings.EqualFold(id, stringFromAny(body["activePaneId"]))))
			}
			panes = append(panes, map[string]any{
				"id":                  paneID,
				"ref":                 "pane:" + paneID,
				"index":               len(panes),
				"detached":            true,
				"surface_ids":         ids,
				"selected_surface_id": selected,
				"surface_count":       len(ids),
				"surfaces":            surfaces,
			})
		}
	}
	visit(body["splitTree"])
	return panes
}

func headlessSurfaceNode(surfaceID string, snapshot map[string]any, paneID string, index int, selected bool, focused bool) map[string]any {
	node := map[string]any{
		"id":               surfaceID,
		"ref":              "surface:" + surfaceID,
		"index":            index,
		"selected":         selected,
		"selected_in_pane": selected,
		"focused":          focused,
		"pane_id":          paneID,
		"pane_ref":         "pane:" + paneID,
		"index_in_pane":    index,
		"detached":         true,
	}
	switch stringFromAny(snapshot["type"]) {
	case "browser":
		browser, _ := snapshot["browser"].(map[string]any)
		node["type"] = "browser"
		node["title"] = firstNonEmptyString(stringFromAny(browser["title"]), stringFromAny(browser["currentURL"]))
		node["url"] = stringFromAny(browser["currentURL"])
	case "terminal":
		terminal, _ := snapshot["terminal"].(map[string]any)
		node["type"] = "terminal"
		node["title"] = firstNonEmptyString(stringFromAny(terminal["title"]), "Terminal")
		node["remote_pty_session_id"] = stringFromAny(terminal["remotePTYSessionId"])
		node["agent_kind"] = stringFromAny(terminal["agentKind"])
	default:
		node["type"] = "unknown"
		node["title"] = ""
	}
	return node
}

func headlessStartPTY(slot, sessionID, attachmentID, command string) error {
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return err
	}
	token, err := readPersistentDaemonTokenFile(paths.tokenFile)
	if err != nil {
		return fmt.Errorf("read persistent daemon token: %w", err)
	}
	conn, err := net.DialTimeout("unix", paths.socket, 2*time.Second)
	if err != nil {
		return fmt.Errorf("connect persistent daemon: %w", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := persistentDaemonRPC(conn, reader, persistentDaemonAuthMethod, map[string]any{"token": token}); err != nil {
		return err
	}
	_, err = persistentDaemonRPC(conn, reader, "pty.attach", map[string]any{
		"session_id":              sessionID,
		"attachment_id":           attachmentID,
		"client_attachment_token": newHeadlessUUID(),
		"cols":                    120,
		"rows":                    40,
		"command":                 command,
	})
	return err
}

func headlessShouldFocus(params map[string]any) bool {
	raw, ok := params["focus"]
	if !ok {
		return true
	}
	switch typed := raw.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "", "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		default:
			return true
		}
	default:
		return true
	}
}

func headlessPTYCommand(slot, workspaceID, surfaceID, command string) string {
	exports := []string{
		"export CMUX_WORKSPACE_ID=" + shellSingleQuote(workspaceID),
		"export CMUX_TAB_ID=" + shellSingleQuote(workspaceID),
		"export CMUX_SURFACE_ID=" + shellSingleQuote(surfaceID),
		"export CMUX_PANEL_ID=" + shellSingleQuote(surfaceID),
		"export CMUX_REMOTE_DAEMON_SLOT=" + shellSingleQuote(slot),
		`export PATH="$HOME/.cmux/bin:$PATH"`,
		`export CMUX_BUNDLED_CLI_PATH="$HOME/.cmux/bin/cmux"`,
	}
	if socketPath := strings.TrimSpace(os.Getenv("CMUX_SOCKET_PATH")); socketPath != "" {
		exports = append(exports, "export CMUX_SOCKET_PATH="+shellSingleQuote(socketPath))
	}
	prefix := strings.Join(exports, "; ")
	if strings.TrimSpace(command) == "" {
		return prefix + `; exec "${SHELL:-/bin/sh}" -l`
	}
	return prefix + "; exec /bin/sh -lc " + shellSingleQuote(command)
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func persistentDaemonRPC(conn net.Conn, reader *bufio.Reader, method string, params map[string]any) (map[string]any, error) {
	id := randomHex(8)
	payload, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return nil, err
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			continue
		}
		if resp["id"] != id {
			continue
		}
		if ok, _ := resp["ok"].(bool); !ok {
			if errObj, _ := resp["error"].(map[string]any); errObj != nil {
				return nil, fmt.Errorf("%s: %s", stringFromAny(errObj["code"]), stringFromAny(errObj["message"]))
			}
			return nil, errors.New("persistent daemon returned error")
		}
		result, _ := resp["result"].(map[string]any)
		return result, nil
	}
}

func headlessAppendSurfaceToPane(body map[string]any, surfaceID string, requestedPaneID string) (string, error) {
	var selectedPane map[string]any
	var selectedPaneID string
	active := strings.ToLower(stringFromAny(body["activePaneId"]))
	var visit func(node any)
	visit = func(node any) {
		if selectedPane != nil {
			return
		}
		m, _ := node.(map[string]any)
		switch stringFromAny(m["type"]) {
		case "split":
			split, _ := m["split"].(map[string]any)
			visit(split["first"])
			visit(split["second"])
		case "pane":
			pane, _ := m["pane"].(map[string]any)
			ids := stringSliceFromAny(pane["panelIds"])
			paneID := stringFromAny(pane["selectedPanelId"])
			if paneID == "" && len(ids) > 0 {
				paneID = ids[0]
			}
			if requestedPaneID != "" && strings.EqualFold(requestedPaneID, paneID) {
				selectedPane = pane
				selectedPaneID = paneID
				return
			}
			for _, id := range ids {
				if strings.EqualFold(id, active) {
					selectedPane = pane
					selectedPaneID = paneID
					return
				}
			}
			if selectedPane == nil && requestedPaneID == "" {
				selectedPane = pane
				selectedPaneID = paneID
			}
		}
	}
	visit(body["splitTree"])
	if selectedPane == nil {
		return "", errors.New("pane not found")
	}
	ids := append(stringSliceFromAny(selectedPane["panelIds"]), surfaceID)
	selectedPane["panelIds"] = ids
	selectedPane["selectedPanelId"] = surfaceID
	if selectedPaneID == "" {
		selectedPaneID = surfaceID
	}
	return selectedPaneID, nil
}

func headlessInsertSplitPane(body map[string]any, surfaceID, direction string) error {
	orientation := "horizontal"
	if direction == "up" || direction == "down" {
		orientation = "vertical"
	}
	newPane := map[string]any{"type": "pane", "pane": map[string]any{
		"panelIds":        []string{surfaceID},
		"selectedPanelId": surfaceID,
	}}
	current := body["splitTree"]
	insertFirst := direction == "left" || direction == "up"
	first, second := current, any(newPane)
	if insertFirst {
		first, second = newPane, current
	}
	body["splitTree"] = map[string]any{"type": "split", "split": map[string]any{
		"orientation":     orientation,
		"dividerPosition": 0.5,
		"first":           first,
		"second":          second,
	}}
	return nil
}

func headlessRemoveSurfaceFromLayout(node any, surfaceID string) bool {
	m, _ := node.(map[string]any)
	switch stringFromAny(m["type"]) {
	case "split":
		split, _ := m["split"].(map[string]any)
		left := headlessRemoveSurfaceFromLayout(split["first"], surfaceID)
		right := headlessRemoveSurfaceFromLayout(split["second"], surfaceID)
		return left || right
	case "pane":
		pane, _ := m["pane"].(map[string]any)
		ids := stringSliceFromAny(pane["panelIds"])
		filtered := ids[:0]
		removed := false
		for _, id := range ids {
			if strings.EqualFold(id, surfaceID) {
				removed = true
				continue
			}
			filtered = append(filtered, id)
		}
		pane["panelIds"] = filtered
		if strings.EqualFold(stringFromAny(pane["selectedPanelId"]), surfaceID) {
			if len(filtered) > 0 {
				pane["selectedPanelId"] = filtered[0]
			} else {
				delete(pane, "selectedPanelId")
			}
		}
		return removed
	default:
		return false
	}
}

func firstHeadlessSurfaceID(node any) string {
	m, _ := node.(map[string]any)
	switch stringFromAny(m["type"]) {
	case "split":
		split, _ := m["split"].(map[string]any)
		if id := firstHeadlessSurfaceID(split["first"]); id != "" {
			return id
		}
		return firstHeadlessSurfaceID(split["second"])
	case "pane":
		pane, _ := m["pane"].(map[string]any)
		ids := stringSliceFromAny(pane["panelIds"])
		if len(ids) > 0 {
			return ids[0]
		}
	}
	return ""
}

func removeHeadlessPaneSnapshot(body map[string]any, surfaceID string) []map[string]any {
	var out []map[string]any
	for _, pane := range headlessPaneSnapshots(body) {
		if !strings.EqualFold(headlessPaneSnapshotID(pane), surfaceID) {
			out = append(out, pane)
		}
	}
	return out
}

func headlessPaneSnapshots(body map[string]any) []map[string]any {
	if typed, ok := body["panes"].([]map[string]any); ok {
		return append([]map[string]any(nil), typed...)
	}
	raw, _ := body["panes"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func headlessPaneSnapshotID(pane map[string]any) string {
	switch stringFromAny(pane["type"]) {
	case "terminal":
		terminal, _ := pane["terminal"].(map[string]any)
		return stringFromAny(terminal["paneId"])
	case "browser":
		browser, _ := pane["browser"].(map[string]any)
		return stringFromAny(browser["paneId"])
	case "markdownViewer":
		markdown, _ := pane["markdownViewer"].(map[string]any)
		return stringFromAny(markdown["paneId"])
	default:
		return ""
	}
}

func headlessMetadataMap(body map[string]any) map[string]string {
	result := map[string]string{}
	raw, _ := body["metadataEntries"].(map[string]any)
	for key, value := range raw {
		result[key] = stringFromAny(value)
	}
	rawString, _ := body["metadataEntries"].(map[string]string)
	for key, value := range rawString {
		result[key] = value
	}
	return result
}

func headlessWorkspaceID(params map[string]any) string {
	raw := strings.TrimSpace(stringFromAny(params["workspace_id"]))
	if raw == "" || raw == "current" {
		raw = strings.TrimSpace(os.Getenv("CMUX_WORKSPACE_ID"))
	}
	raw = strings.TrimPrefix(raw, "workspace:")
	return raw
}

func findHeadlessSlotForWorkspace(rootBase, workspaceID string) (string, error) {
	entries, err := os.ReadDir(rootBase)
	if err != nil {
		return "", err
	}
	var matches []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rootBase, entry.Name(), workspaceSnapshotMetaFile))
		if err != nil {
			continue
		}
		var meta workspaceSnapshotMeta
		if json.Unmarshal(data, &meta) == nil && strings.EqualFold(meta.WorkspaceID, workspaceID) {
			matches = append(matches, entry.Name())
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no detached snapshot found for workspace %s", workspaceID)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple detached snapshots found for workspace %s; set CMUX_REMOTE_DAEMON_SLOT", workspaceID)
	}
	return matches[0], nil
}

func headlessDaemonRoot() (string, error) {
	rootBase := strings.TrimSpace(os.Getenv("CMUX_REMOTE_DAEMON_ROOT"))
	if rootBase != "" {
		return rootBase, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", errors.New("cannot resolve remote home directory")
	}
	return filepath.Join(home, ".cmux", "daemon"), nil
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if s := stringFromAny(item); s != "" {
				result = append(result, s)
			}
		}
		return result
	default:
		return nil
	}
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case float64:
		return int(typed)
	case json.Number:
		i, _ := typed.Int64()
		return int(i)
	default:
		return 0
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newHeadlessUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf(
		"%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}
