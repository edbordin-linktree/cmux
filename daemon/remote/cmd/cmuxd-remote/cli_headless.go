package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// headlessHandler runs a detached-workspace JSON-RPC method against the local
// snapshot files instead of going through the relay socket. Each entry in
// headlessHandlers represents a method that supports the no-relay fallback.
type headlessHandler func(params map[string]any) (map[string]any, error)

// headlessHandlers is the single source of truth for which RPC methods have
// headless implementations. headlessSupportsMethod and runHeadlessCLIResult
// both read from it, so adding a method here is enough to wire the fallback.
var headlessHandlers = map[string]headlessHandler{
	"metadata.set":           headlessMetadataSet,
	"metadata.get":           headlessMetadataGet,
	"metadata.list":          headlessMetadataList,
	"metadata.clear":         headlessMetadataClear,
	"surface.metadata.set":   headlessSurfaceMetadataSet,
	"surface.metadata.get":   headlessSurfaceMetadataGet,
	"surface.metadata.list":  headlessSurfaceMetadataList,
	"surface.metadata.clear": headlessSurfaceMetadataClear,
	"surface.lookup":         headlessSurfaceLookup,
	"workspace.lookup":       headlessWorkspaceLookup,
	"system.tree":            headlessSystemTree,
	"surface.create":         func(p map[string]any) (map[string]any, error) { return headlessCreateSurface(p, false) },
	"pane.create":            func(p map[string]any) (map[string]any, error) { return headlessCreateSurface(p, true) },
	"surface.split":          func(p map[string]any) (map[string]any, error) { return headlessCreateSurface(p, true) },
	"surface.close":          headlessCloseSurface,
	"surface.send_text":      headlessSendText,
	"surface.send_key":       headlessSendKey,
	"workspace.rename":       headlessRenameWorkspace,
	"tab.action":             headlessTabAction,
	"status.set":             headlessStatusSet,
	"status.clear":           headlessStatusClear,
	"status.list":            headlessStatusList,
}

func runHeadlessCLICommand(commandName, method string, params map[string]any, jsonOutput bool, relayErr error) (int, bool) {
	if !headlessSupportsMethod(method) {
		return 0, false
	}

	result, err := runHeadlessCLIResult(method, params)
	if err != nil {
		fmt.Fprintln(os.Stderr, headlessFailureMessage(commandName, err, relayErr))
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
	return 0, true
}

// headlessFailureMessage formats the stderr message printed when the headless
// fallback also fails. relayErr is the relay-side failure that triggered the
// fallback (or a synthetic "target workspace is detached" when the caller
// forced the headless path); including it tells the user why the fallback
// was attempted at all, instead of leaving them staring at only the headless
// reason.
func headlessFailureMessage(commandName string, headlessErr, relayErr error) string {
	msg := fmt.Sprintf("cmux: %s requires an attached cmux UI or a detached remote snapshot: %v", commandName, headlessErr)
	if relayErr != nil {
		msg += fmt.Sprintf(" (relay path also failed: %v)", relayErr)
	}
	return msg
}

func headlessSupportsMethod(method string) bool {
	_, ok := headlessHandlers[method]
	return ok
}

func runHeadlessCLIResult(method string, params map[string]any) (map[string]any, error) {
	handler, ok := headlessHandlers[method]
	if !ok {
		return nil, fmt.Errorf("unsupported detached command %q", method)
	}
	return handler(params)
}

func headlessSystemTree(params map[string]any) (map[string]any, error) {
	snap, err := loadHeadlessSnapshot(params)
	if err != nil {
		return nil, err
	}
	return headlessTreePayload(snap), nil
}

// withHeadlessSnapshot acquires the per-slot snapshot lock and loads the
// snapshot into memory before running fn against it. Use this for reads; for
// mutations that need to write back, use headlessMutate instead.
func withHeadlessSnapshot(params map[string]any, fn func(*headlessSnapshot) (map[string]any, error)) (map[string]any, error) {
	paths, err := resolveHeadlessSnapshotPaths(params)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = withWorkspaceSnapshotLock(paths.root, func() error {
		snap, err := loadHeadlessSnapshotAtSlot(params, paths)
		if err != nil {
			return err
		}
		result, err = fn(snap)
		return err
	})
	return result, err
}

// headlessMutate runs fn inside the snapshot lock, then stores the snapshot
// and merges the snapshot bookkeeping (snapshot_sha256, detached) into the
// returned payload. Handlers should populate their own fields and leave the
// store/sha plumbing to the wrapper.
func headlessMutate(params map[string]any, fn func(*headlessSnapshot) (map[string]any, error)) (map[string]any, error) {
	return withHeadlessSnapshot(params, func(snap *headlessSnapshot) (map[string]any, error) {
		result, err := fn(snap)
		if err != nil {
			return nil, err
		}
		sha, err := storeHeadlessSnapshot(snap)
		if err != nil {
			return nil, err
		}
		if result == nil {
			result = map[string]any{}
		}
		if _, exists := result["snapshot_sha256"]; !exists {
			result["snapshot_sha256"] = sha
		}
		if _, exists := result["detached"]; !exists {
			result["detached"] = true
		}
		return result, nil
	})
}
