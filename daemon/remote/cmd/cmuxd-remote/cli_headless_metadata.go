package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

func headlessMetadataSet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	value := stringFromAny(params["value"])
	if value == "" {
		value = stringFromAny(params["json_value"])
	}
	if key == "" || value == "" {
		return nil, errors.New("metadata.set requires key and value")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		metadata := headlessMetadataMap(snap.body)
		metadata[key] = value
		snap.body["metadataEntries"] = metadata
		return map[string]any{
			"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
			"key":          key,
			"value":        value,
		}, nil
	})
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
		"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
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
		"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
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
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		metadata := headlessMetadataMap(snap.body)
		_, existed := metadata[key]
		delete(metadata, key)
		snap.body["metadataEntries"] = metadata
		return map[string]any{
			"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
			"key":          key,
			"cleared":      existed,
		}, nil
	})
}

func headlessSurfaceMetadataSet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	value := stringFromAny(params["value"])
	if value == "" {
		value = stringFromAny(params["json_value"])
	}
	if key == "" || value == "" {
		return nil, errors.New("surface.metadata.set requires surface_id, key and value")
	}
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	if surfaceID == "" {
		return nil, errors.New("surface.metadata.set requires surface_id")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		panes := headlessPaneSnapshots(snap.body)
		for i := range panes {
			if !strings.EqualFold(headlessPaneSnapshotID(panes[i]), surfaceID) {
				continue
			}
			metadata := headlessPaneMetadataMap(panes[i])
			metadata[key] = value
			headlessSetPaneMetadataMap(panes[i], metadata)
			snap.body["panes"] = panes
			return headlessSurfaceMetadataEntryPayload(snap, surfaceID, key, value, true), nil
		}
		return nil, fmt.Errorf("surface %s not found in detached snapshot", surfaceID)
	})
}

func headlessSurfaceMetadataGet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("surface.metadata.get requires key")
	}
	snap, surfaceID, pane, err := headlessSurfaceMetadataTarget(params)
	if err != nil {
		return nil, err
	}
	metadata := headlessPaneMetadataMap(pane)
	value, exists := metadata[key]
	return headlessSurfaceMetadataEntryPayload(snap, surfaceID, key, value, exists), nil
}

func headlessSurfaceMetadataList(params map[string]any) (map[string]any, error) {
	snap, surfaceID, pane, err := headlessSurfaceMetadataTarget(params)
	if err != nil {
		return nil, err
	}
	prefix := stringFromAny(params["prefix"])
	metadata := headlessPaneMetadataMap(pane)
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
		"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
		"surface_id":   canonicalHeadlessID(surfaceID),
		"surface_ref":  "surface:" + canonicalHeadlessID(surfaceID),
		"entries":      entries,
		"count":        len(entries),
		"detached":     true,
	}, nil
}

func headlessSurfaceMetadataClear(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("surface.metadata.clear requires key")
	}
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	if surfaceID == "" {
		return nil, errors.New("surface.metadata.clear requires surface_id")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		panes := headlessPaneSnapshots(snap.body)
		for i := range panes {
			if !strings.EqualFold(headlessPaneSnapshotID(panes[i]), surfaceID) {
				continue
			}
			metadata := headlessPaneMetadataMap(panes[i])
			_, existed := metadata[key]
			delete(metadata, key)
			headlessSetPaneMetadataMap(panes[i], metadata)
			snap.body["panes"] = panes
			payload := headlessSurfaceMetadataEntryPayload(snap, surfaceID, key, "", existed)
			payload["cleared"] = existed
			delete(payload, "value")
			delete(payload, "exists")
			return payload, nil
		}
		return nil, fmt.Errorf("surface %s not found in detached snapshot", surfaceID)
	})
}

func headlessSurfaceLookup(params map[string]any) (map[string]any, error) {
	criteria, ok := params["metadata"].(map[string]string)
	if !ok {
		raw, _ := params["metadata"].(map[string]any)
		criteria = map[string]string{}
		for key, value := range raw {
			criteria[key] = stringFromAny(value)
		}
	}
	if len(criteria) == 0 {
		return nil, errors.New("surface.lookup requires metadata criteria")
	}
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	matches := make([]map[string]any, 0)
	for _, pane := range headlessPaneSnapshots(snap.body) {
		metadata := headlessPaneMetadataMap(pane)
		if !headlessMetadataMatches(metadata, criteria) {
			continue
		}
		surfaceID := canonicalHeadlessID(headlessPaneSnapshotID(pane))
		matches = append(matches, map[string]any{
			"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
			"surface_id":   surfaceID,
			"surface_ref":  "surface:" + surfaceID,
			"type":         stringFromAny(pane["type"]),
			"metadata":     metadata,
			"detached":     true,
		})
	}
	result := map[string]any{
		"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
		"criteria":     criteria,
		"matches":      matches,
		"count":        len(matches),
		"detached":     true,
	}
	if len(matches) == 1 {
		result["surface"] = matches[0]
	}
	return result, nil
}

func headlessSurfaceMetadataTarget(params map[string]any) (*headlessSnapshot, string, map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, "", nil, err
	}
	surfaceID := headlessNormalizeID(stringFromAny(params["surface_id"]))
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(os.Getenv("CMUX_SURFACE_ID"))
	}
	if surfaceID == "" {
		surfaceID = headlessNormalizeID(stringFromAny(snap.body["activePaneId"]))
	}
	if surfaceID == "" {
		return nil, "", nil, errors.New("surface metadata requires surface_id or active surface")
	}
	for _, pane := range headlessPaneSnapshots(snap.body) {
		if strings.EqualFold(headlessPaneSnapshotID(pane), surfaceID) {
			return snap, surfaceID, pane, nil
		}
	}
	return nil, "", nil, fmt.Errorf("surface %s not found in detached snapshot", surfaceID)
}

func headlessSurfaceMetadataEntryPayload(snap *headlessSnapshot, surfaceID, key, value string, exists bool) map[string]any {
	payload := map[string]any{
		"workspace_id": canonicalHeadlessID(snap.meta.WorkspaceID),
		"surface_id":   canonicalHeadlessID(surfaceID),
		"surface_ref":  "surface:" + canonicalHeadlessID(surfaceID),
		"key":          key,
		"exists":       exists,
		"detached":     true,
	}
	if exists {
		payload["value"] = value
	}
	return payload
}

func headlessWorkspaceLookup(params map[string]any) (map[string]any, error) {
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

	includeDetached := boolFromAny(params["include_detached"])
	result := map[string]any{
		"criteria":          criteria,
		"include_detached":  includeDetached,
		"detached_searched": false,
		"matches":           []map[string]any{},
		"count":             0,
	}
	if !includeDetached {
		return result, nil
	}

	snapshots, loadErrs := loadAllHeadlessSnapshots()
	result["detached_searched"] = true
	if len(loadErrs) > 0 {
		result["detached_errors"] = loadErrs
	}
	matches := make([]map[string]any, 0)
	for _, snap := range snapshots {
		metadata := headlessMetadataMap(snap.body)
		if !headlessMetadataMatches(metadata, criteria) {
			continue
		}
		item := headlessWorkspaceSummary(snap)
		item["metadata"] = metadata
		matches = append(matches, item)
	}
	result["matches"] = matches
	result["count"] = len(matches)
	if len(matches) == 1 {
		result["workspace"] = matches[0]
	}
	return result, nil
}

func headlessMetadataMatches(metadata map[string]string, criteria map[string]string) bool {
	for key, value := range criteria {
		if metadata[key] != value {
			return false
		}
	}
	return true
}

func headlessStatusSet(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	value := stringFromAny(params["value"])
	if key == "" {
		return nil, errors.New("status.set requires key")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		entry := map[string]any{
			"key":       key,
			"value":     value,
			"priority":  intFromAny(params["priority"]),
			"format":    firstNonEmptyString(stringFromAny(params["format"]), "plain"),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}
		for _, field := range []string{"icon", "color", "url"} {
			if value := strings.TrimSpace(stringFromAny(params[field])); value != "" {
				entry[field] = value
			}
		}
		entries := headlessStatusEntries(snap.body)
		replaced := false
		for i, existing := range entries {
			if strings.EqualFold(stringFromAny(existing["key"]), key) {
				entries[i] = entry
				replaced = true
				break
			}
		}
		if !replaced {
			entries = append(entries, entry)
		}
		snap.body["statusEntries"] = entries
		return map[string]any{
			"key":   key,
			"value": value,
		}, nil
	})
}

func headlessStatusClear(params map[string]any) (map[string]any, error) {
	key := strings.TrimSpace(stringFromAny(params["key"]))
	if key == "" {
		return nil, errors.New("status.clear requires key")
	}
	return headlessMutate(params, func(snap *headlessSnapshot) (map[string]any, error) {
		entries := headlessStatusEntries(snap.body)
		out := entries[:0]
		cleared := false
		for _, entry := range entries {
			if strings.EqualFold(stringFromAny(entry["key"]), key) {
				cleared = true
				continue
			}
			out = append(out, entry)
		}
		if len(out) == 0 {
			delete(snap.body, "statusEntries")
		} else {
			snap.body["statusEntries"] = out
		}
		return map[string]any{
			"key":     key,
			"cleared": cleared,
		}, nil
	})
}

func headlessStatusList(params map[string]any) (map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"entries":  headlessStatusEntries(snap.body),
		"detached": true,
	}, nil
}
