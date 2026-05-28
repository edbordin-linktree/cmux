package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func headlessCreateWorkspace(params map[string]any) (map[string]any, error) {
	workspaceID := strings.ToLower(newHeadlessUUID())
	surfaceID := strings.ToLower(newHeadlessUUID())
	slot := "ssh-" + workspaceID
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return nil, err
	}
	if err := ensurePersistentDaemonDirectory(paths); err != nil {
		return nil, err
	}

	cwd := headlessWorkspaceCreateCWD(params)
	title := strings.TrimSpace(stringFromAny(params["title"]))
	if title == "" {
		title = headlessWorkspaceDefaultTitle(cwd)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionID := "ssh-" + workspaceID + "-" + surfaceID
	command := headlessPTYCommand(
		slot,
		workspaceID,
		surfaceID,
		stringFromAny(params["initial_command"]),
		cwd,
	)
	if err := headlessStartPTYFunc(slot, sessionID, surfaceID, command); err != nil {
		return nil, err
	}

	body := map[string]any{
		"version":       1,
		"workspaceId":   workspaceID,
		"title":         title,
		"detachedAt":    now,
		"displayTarget": headlessDisplayTarget(slot, stringFromAny(params["destination"])),
		"splitTree": map[string]any{
			"type": "pane",
			"pane": map[string]any{
				"panelIds":        []string{surfaceID},
				"selectedPanelId": surfaceID,
			},
		},
		"panes": []map[string]any{{
			"type": "terminal",
			"terminal": map[string]any{
				"paneId":             surfaceID,
				"remotePTYSessionId": sessionID,
				"title":              "Terminal",
				"cwdHint":            cwd,
			},
		}},
		"activePaneId": surfaceID,
	}
	description := strings.TrimSpace(stringFromAny(params["description"]))
	if description != "" {
		body["metadataEntries"] = map[string]string{"cmux:description": description}
	}
	snap := &headlessSnapshot{
		slot:     slot,
		root:     paths.root,
		bodyPath: filepath.Join(paths.root, workspaceSnapshotBodyFile),
		metaPath: filepath.Join(paths.root, workspaceSnapshotMetaFile),
		body:     body,
		meta: workspaceSnapshotMeta{
			Version:       1,
			WorkspaceID:   workspaceID,
			Title:         title,
			Status:        "detached",
			DetachedAt:    now,
			UpdatedAt:     now,
			SchemaVersion: 1,
		},
	}
	sha, err := storeHeadlessSnapshot(snap)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workspace_id":           workspaceID,
		"workspace_ref":          "workspace:" + workspaceID,
		"surface_id":             surfaceID,
		"surface_ref":            "surface:" + surfaceID,
		"persistent_daemon_slot": slot,
		"detached":               true,
		"snapshot_sha256":        sha,
		"destination":            stringFromAny(params["destination"]),
	}, nil
}

func headlessRenameWorkspace(params map[string]any) (map[string]any, error) {
	title := strings.TrimSpace(stringFromAny(params["title"]))
	if title == "" {
		return nil, errors.New("workspace.rename requires title")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		snap.body["title"] = title
		snap.meta.Title = title
		workspaceID := snap.meta.WorkspaceID
		if workspaceID == "" {
			workspaceID = stringFromAny(snap.body["workspaceId"])
		}
		workspaceID = canonicalHeadlessID(workspaceID)
		return map[string]any{
			"workspace_id":  workspaceID,
			"workspace_ref": "workspace:" + workspaceID,
			"title":         title,
		}, nil
	})
}

func headlessTabAction(params map[string]any) (map[string]any, error) {
	action := strings.ToLower(strings.TrimSpace(stringFromAny(params["action"])))
	if action == "" {
		action = "rename"
	}
	if action != "rename" {
		return nil, fmt.Errorf("unsupported detached tab action %q", action)
	}
	return headlessRenameSurface(params)
}

func headlessRenameSurface(params map[string]any) (map[string]any, error) {
	title := strings.TrimSpace(stringFromAny(params["title"]))
	if title == "" {
		return nil, errors.New("tab.action rename requires title")
	}
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_TAB_ID"))
	}
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		if surfaceID == "" {
			surfaceID = headlessNormalizeID(stringFromAny(snap.body["activePaneId"]))
		}
		if surfaceID == "" {
			return nil, errors.New("tab.action rename requires surface_id or active surface")
		}
		panes := headlessPaneSnapshots(snap.body)
		found := false
		for _, pane := range panes {
			if !strings.EqualFold(headlessPaneSnapshotID(pane), surfaceID) {
				continue
			}
			found = true
			switch stringFromAny(pane["type"]) {
			case "terminal":
				terminal, _ := pane["terminal"].(map[string]any)
				if terminal == nil {
					terminal = map[string]any{}
					pane["terminal"] = terminal
				}
				terminal["title"] = title
			case "browser":
				browser, _ := pane["browser"].(map[string]any)
				if browser == nil {
					browser = map[string]any{}
					pane["browser"] = browser
				}
				browser["title"] = title
			default:
				return nil, fmt.Errorf("surface %s does not support detached rename", surfaceID)
			}
			break
		}
		if !found {
			return nil, fmt.Errorf("surface %s not found in detached snapshot", surfaceID)
		}
		snap.body["panes"] = panes
		workspaceID := snap.meta.WorkspaceID
		if workspaceID == "" {
			workspaceID = stringFromAny(snap.body["workspaceId"])
		}
		workspaceID = canonicalHeadlessID(workspaceID)
		surfaceID = canonicalHeadlessID(surfaceID)
		return map[string]any{
			"workspace_id":  workspaceID,
			"workspace_ref": "workspace:" + workspaceID,
			"surface_id":    surfaceID,
			"surface_ref":   "surface:" + surfaceID,
			"title":         title,
		}, nil
	})
}
