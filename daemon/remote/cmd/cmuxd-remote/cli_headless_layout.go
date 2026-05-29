package main

import (
	"errors"
	"os"
	"sort"
	"strings"
)

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
	workspaceID = canonicalHeadlessID(workspaceID)
	title := snap.meta.Title
	if title == "" {
		title, _ = snap.body["title"].(string)
	}
	return map[string]any{
		"id":                     workspaceID,
		"ref":                    "workspace:" + workspaceID,
		"workspace_id":           workspaceID,
		"workspace_ref":          "workspace:" + workspaceID,
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
	surfaceID = canonicalHeadlessID(surfaceID)
	paneID = canonicalHeadlessID(paneID)
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
		"metadata":         headlessPaneMetadataMap(snapshot),
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

func headlessStatusEntries(body map[string]any) []map[string]any {
	if typed, ok := body["statusEntries"].([]map[string]any); ok {
		return append([]map[string]any(nil), typed...)
	}
	raw, _ := body["statusEntries"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return stringFromAny(out[i]["key"]) < stringFromAny(out[j]["key"])
	})
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

func headlessPaneSnapshotPayload(pane map[string]any) map[string]any {
	switch stringFromAny(pane["type"]) {
	case "terminal":
		payload, _ := pane["terminal"].(map[string]any)
		return payload
	case "browser":
		payload, _ := pane["browser"].(map[string]any)
		return payload
	case "markdownViewer":
		payload, _ := pane["markdownViewer"].(map[string]any)
		return payload
	default:
		return nil
	}
}

func headlessPaneMetadataMap(pane map[string]any) map[string]string {
	result := map[string]string{}
	payload := headlessPaneSnapshotPayload(pane)
	if payload == nil {
		return result
	}
	raw, _ := payload["metadataEntries"].(map[string]any)
	for key, value := range raw {
		result[key] = stringFromAny(value)
	}
	rawString, _ := payload["metadataEntries"].(map[string]string)
	for key, value := range rawString {
		result[key] = value
	}
	return result
}

func headlessSetPaneMetadataMap(pane map[string]any, metadata map[string]string) {
	payload := headlessPaneSnapshotPayload(pane)
	if payload == nil {
		return
	}
	if len(metadata) == 0 {
		delete(payload, "metadataEntries")
		return
	}
	payload["metadataEntries"] = metadata
}
