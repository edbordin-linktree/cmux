package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func headlessCreateSurface(params map[string]any, splitPane bool) (map[string]any, error) {
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
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
				headlessWorkspaceCreateCWD(params),
			)
			if err := headlessStartPTYFunc(snap.slot, sessionID, surfaceID, command); err != nil {
				return nil, err
			}
			paneSnapshot = map[string]any{
				"type": "terminal",
				"terminal": map[string]any{
					"paneId":             surfaceID,
					"remotePTYSessionId": sessionID,
					"title":              "Terminal",
					"cwdHint":            headlessWorkspaceCreateCWD(params),
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
			var err error
			paneID, err = headlessAppendSurfaceToPane(snap.body, surfaceID, stringFromAny(params["pane_id"]))
			if err != nil {
				return nil, err
			}
		}
		if headlessShouldFocus(params) {
			snap.body["activePaneId"] = surfaceID
		}
		return map[string]any{
			"workspace_id":  workspaceID,
			"workspace_ref": "workspace:" + workspaceID,
			"pane_id":       paneID,
			"pane_ref":      "pane:" + paneID,
			"surface_id":    surfaceID,
			"surface_ref":   "surface:" + surfaceID,
			"type":          panelType,
		}, nil
	})
}

func headlessCloseSurface(params map[string]any) (map[string]any, error) {
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	if surfaceID == "" {
		return nil, errors.New("surface.close requires surface_id")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		snap.body["panes"] = removeHeadlessPaneSnapshot(snap.body, surfaceID)
		removed := headlessRemoveSurfaceFromLayout(snap.body["splitTree"], surfaceID)
		if strings.EqualFold(stringFromAny(snap.body["activePaneId"]), surfaceID) {
			snap.body["activePaneId"] = firstHeadlessSurfaceID(snap.body["splitTree"])
		}
		return map[string]any{
			"workspace_id": snap.meta.WorkspaceID,
			"surface_id":   surfaceID,
			"closed":       removed,
		}, nil
	})
}

func headlessSendText(params map[string]any) (map[string]any, error) {
	snap, surfaceID, sessionID, err := headlessTargetTerminal(params)
	if err != nil {
		return nil, err
	}
	text, ok := params["text"].(string)
	if !ok {
		return nil, errors.New("surface.send_text requires text")
	}
	result, err := headlessPersistentDaemonRPCFunc(snap.slot, "pty.send", map[string]any{
		"session_id": sessionID,
		"text":       text,
	})
	if err != nil {
		return nil, err
	}
	return headlessSendResultPayload(snap, surfaceID, result), nil
}

func headlessSendKey(params map[string]any) (map[string]any, error) {
	snap, surfaceID, sessionID, err := headlessTargetTerminal(params)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("surface.send_key requires key")
	}
	result, err := headlessPersistentDaemonRPCFunc(snap.slot, "pty.send_key", map[string]any{
		"session_id": sessionID,
		"key":        key,
	})
	if err != nil {
		return nil, err
	}
	payload := headlessSendResultPayload(snap, surfaceID, result)
	payload["key"] = strings.ToLower(key)
	return payload, nil
}

func headlessTargetTerminal(params map[string]any) (*headlessSnapshot, string, string, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, "", "", err
	}
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(stringFromAny(snap.body["activePaneId"]))
	}
	if surfaceID == "" {
		return nil, "", "", errors.New("surface send requires surface_id or active terminal surface")
	}
	for _, pane := range headlessPaneSnapshots(snap.body) {
		if !strings.EqualFold(headlessPaneSnapshotID(pane), surfaceID) {
			continue
		}
		if stringFromAny(pane["type"]) != "terminal" {
			return nil, "", "", fmt.Errorf("surface %s is not a terminal", surfaceID)
		}
		terminal, _ := pane["terminal"].(map[string]any)
		sessionID := strings.TrimSpace(stringFromAny(terminal["remotePTYSessionId"]))
		if sessionID == "" {
			return nil, "", "", fmt.Errorf("terminal surface %s has no remotePTYSessionId", surfaceID)
		}
		return snap, surfaceID, sessionID, nil
	}
	return nil, "", "", fmt.Errorf("surface %s not found in detached snapshot", surfaceID)
}

func headlessSendResultPayload(snap *headlessSnapshot, surfaceID string, result map[string]any) map[string]any {
	workspaceID := snap.meta.WorkspaceID
	if workspaceID == "" {
		workspaceID = stringFromAny(snap.body["workspaceId"])
	}
	written := intFromAny(result["written"])
	return map[string]any{
		"workspace_id":  workspaceID,
		"workspace_ref": "workspace:" + workspaceID,
		"surface_id":    surfaceID,
		"surface_ref":   "surface:" + surfaceID,
		"session_id":    stringFromAny(result["session_id"]),
		"written":       written,
		"detached":      true,
	}
}
