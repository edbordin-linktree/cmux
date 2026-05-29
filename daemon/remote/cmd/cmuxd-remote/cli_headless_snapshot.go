package main

import (
	"bufio"
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

// headlessSnapshot is the in-memory pair of body + sidecar metadata held
// while a headless handler runs under the per-slot snapshot lock.
type headlessSnapshot struct {
	slot     string
	root     string
	bodyPath string
	metaPath string
	body     map[string]any
	meta     workspaceSnapshotMeta
}

// headlessStartPTYFunc and headlessPersistentDaemonRPCFunc are var-extracted
// so tests can stub PTY launch and persistent-daemon RPC without touching the
// real socket.
var (
	headlessStartPTYFunc            = headlessStartPTY
	headlessPersistentDaemonRPCFunc = headlessPersistentDaemonRPC
)

func loadHeadlessSnapshot(params map[string]any) (*headlessSnapshot, error) {
	paths, err := resolveHeadlessSnapshotPaths(params)
	if err != nil {
		return nil, err
	}
	return loadHeadlessSnapshotAtSlot(params, paths)
}

func resolveHeadlessSnapshotPaths(params map[string]any) (persistentDaemonPaths, error) {
	workspaceID := headlessWorkspaceID(params)
	if workspaceID != "" {
		rootBase, err := headlessDaemonRoot()
		if err != nil {
			return persistentDaemonPaths{}, err
		}
		paths, resolveErr := findHeadlessPathsForWorkspace(rootBase, workspaceID)
		if resolveErr == nil {
			return paths, nil
		}
		envSlot := firstNonEmptyEnv("CMUX_REMOTE_DAEMON_SLOT", "CMUX_PERSISTENT_DAEMON_SLOT", "CMUX_DAEMON_SLOT")
		if envSlot == "" {
			return persistentDaemonPaths{}, resolveErr
		}
		return headlessPathsForSlot(envSlot)
	}
	slot, err := resolveHeadlessSnapshotSlot(params)
	if err != nil {
		return persistentDaemonPaths{}, err
	}
	return headlessPathsForSlot(slot)
}

func resolveHeadlessSnapshotSlot(params map[string]any) (string, error) {
	workspaceID := headlessWorkspaceID(params)
	envSlot := firstNonEmptyEnv("CMUX_REMOTE_DAEMON_SLOT", "CMUX_PERSISTENT_DAEMON_SLOT", "CMUX_DAEMON_SLOT")
	rootBase, err := headlessDaemonRoot()
	if err != nil {
		return "", err
	}
	slot := envSlot
	if workspaceID != "" {
		resolvedSlot, resolveErr := findHeadlessSlotForWorkspace(rootBase, workspaceID)
		if resolveErr == nil {
			slot = resolvedSlot
		} else if envSlot == "" {
			return "", resolveErr
		}
	}
	if slot == "" {
		return "", errors.New("CMUX_REMOTE_DAEMON_SLOT or CMUX_WORKSPACE_ID is required")
	}
	return slot, nil
}

func loadHeadlessSnapshotAtSlot(params map[string]any, paths persistentDaemonPaths) (*headlessSnapshot, error) {
	workspaceID := headlessWorkspaceID(params)
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
		slot:     paths.slot,
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

func loadAllHeadlessSnapshots() ([]*headlessSnapshot, []map[string]any) {
	rootBase, err := headlessDaemonRoot()
	if err != nil {
		return nil, []map[string]any{{"error": err.Error()}}
	}
	slotRoots, err := headlessSnapshotSlotRoots(rootBase)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []map[string]any{{"error": err.Error()}}
	}
	var snapshots []*headlessSnapshot
	var loadErrors []map[string]any
	for _, slotRoot := range slotRoots {
		slot := slotRoot.slot
		paths := pathsForHeadlessSlotRoot(slot, slotRoot.root)
		unlock, lockErr := lockWorkspaceSnapshot(paths.root)
		if lockErr != nil {
			loadErrors = append(loadErrors, map[string]any{"slot": slot, "error": lockErr.Error()})
			continue
		}
		bodyPath := filepath.Join(paths.root, workspaceSnapshotBodyFile)
		metaPath := filepath.Join(paths.root, workspaceSnapshotMetaFile)
		bodyBytes, bodyErr := os.ReadFile(bodyPath)
		metaBytes, metaErr := os.ReadFile(metaPath)
		if bodyErr != nil || metaErr != nil {
			unlock()
			continue
		}
		var meta workspaceSnapshotMeta
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			unlock()
			loadErrors = append(loadErrors, map[string]any{"slot": slot, "error": "decode detached snapshot metadata: " + err.Error()})
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(bodyBytes, &body); err != nil {
			unlock()
			loadErrors = append(loadErrors, map[string]any{"slot": slot, "error": "decode detached snapshot body: " + err.Error()})
			continue
		}
		unlock()
		snapshots = append(snapshots, &headlessSnapshot{
			slot:     slot,
			root:     paths.root,
			bodyPath: bodyPath,
			metaPath: metaPath,
			body:     body,
			meta:     meta,
		})
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].meta.WorkspaceID != snapshots[j].meta.WorkspaceID {
			return snapshots[i].meta.WorkspaceID < snapshots[j].meta.WorkspaceID
		}
		return snapshots[i].slot < snapshots[j].slot
	})
	return snapshots, loadErrors
}

func findHeadlessSlotForWorkspace(rootBase, workspaceID string) (string, error) {
	paths, err := findHeadlessPathsForWorkspace(rootBase, workspaceID)
	if err != nil {
		return "", err
	}
	return paths.slot, nil
}

func findHeadlessPathsForWorkspace(rootBase, workspaceID string) (persistentDaemonPaths, error) {
	slotRoots, err := headlessSnapshotSlotRoots(rootBase)
	if err != nil {
		return persistentDaemonPaths{}, err
	}
	var matches []persistentDaemonPaths
	for _, slotRoot := range slotRoots {
		data, err := os.ReadFile(filepath.Join(slotRoot.root, workspaceSnapshotMetaFile))
		if err != nil {
			continue
		}
		var meta workspaceSnapshotMeta
		if json.Unmarshal(data, &meta) == nil && strings.EqualFold(meta.WorkspaceID, workspaceID) {
			matches = append(matches, pathsForHeadlessSlotRoot(slotRoot.slot, slotRoot.root))
		}
	}
	if len(matches) == 0 {
		return persistentDaemonPaths{}, fmt.Errorf("no detached snapshot found for workspace %s", workspaceID)
	}
	if len(matches) > 1 {
		return persistentDaemonPaths{}, fmt.Errorf("multiple detached snapshots found for workspace %s; set CMUX_REMOTE_DAEMON_SLOT", workspaceID)
	}
	return matches[0], nil
}

type headlessSnapshotSlotRoot struct {
	slot string
	root string
}

func headlessSnapshotSlotRoots(rootBase string) ([]headlessSnapshotSlotRoot, error) {
	entries, err := os.ReadDir(rootBase)
	if err != nil {
		return nil, err
	}
	var roots []headlessSnapshotSlotRoot
	seen := map[string]bool{}
	add := func(slot, root string) {
		key := slot + "\x00" + root
		if seen[key] || !headlessSnapshotFilesPresent(root) {
			return
		}
		seen[key] = true
		roots = append(roots, headlessSnapshotSlotRoot{slot: slot, root: root})
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		directRoot := filepath.Join(rootBase, entry.Name())
		add(entry.Name(), directRoot)
		children, childErr := os.ReadDir(directRoot)
		if childErr != nil {
			continue
		}
		for _, child := range children {
			if child.IsDir() {
				add(child.Name(), filepath.Join(directRoot, child.Name()))
			}
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].slot != roots[j].slot {
			return roots[i].slot < roots[j].slot
		}
		return roots[i].root < roots[j].root
	})
	return roots, nil
}

func headlessSnapshotFilesPresent(root string) bool {
	if _, err := os.Stat(filepath.Join(root, workspaceSnapshotMetaFile)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(root, workspaceSnapshotBodyFile)); err == nil {
		return true
	}
	return false
}

func headlessPathsForSlot(slot string) (persistentDaemonPaths, error) {
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return persistentDaemonPaths{}, err
	}
	if headlessSnapshotFilesPresent(paths.root) {
		return paths, nil
	}
	rootBase, err := headlessDaemonRoot()
	if err != nil {
		return paths, nil
	}
	legacyRoot := filepath.Join(rootBase, slot)
	if legacyRoot != paths.root && headlessSnapshotFilesPresent(legacyRoot) {
		return pathsForHeadlessSlotRoot(slot, legacyRoot), nil
	}
	return paths, nil
}

func pathsForHeadlessSlotRoot(slot, root string) persistentDaemonPaths {
	return persistentDaemonPaths{
		slot:      slot,
		root:      root,
		socket:    persistentDaemonSocketPath(root, slot),
		tokenFile: filepath.Join(root, "auth.token"),
		logFile:   filepath.Join(root, "daemon.log"),
		lockFile:  filepath.Join(root, "daemon.lock"),
	}
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

// headlessStartPTY launches a PTY session for a detached workspace via the
// persistent daemon RPC interface.
func headlessStartPTY(slot, sessionID, attachmentID, command string) error {
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return err
	}
	paths, err = ensurePersistentDaemonDirectory(paths)
	if err != nil {
		return err
	}
	token, err := persistentDaemonToken(paths)
	if err != nil {
		return err
	}
	if err := ensurePersistentDaemonRunning(paths, token, os.Stderr); err != nil {
		return err
	}
	_, err = headlessPersistentDaemonRPC(slot, "pty.attach", map[string]any{
		"session_id":              sessionID,
		"attachment_id":           attachmentID,
		"client_attachment_token": newHeadlessUUID(),
		"cols":                    120,
		"rows":                    40,
		"command":                 command,
	})
	return err
}

// headlessPersistentDaemonRPC opens a one-shot authenticated connection to
// the per-slot persistent daemon and runs a single RPC method.
func headlessPersistentDaemonRPC(slot, method string, params map[string]any) (map[string]any, error) {
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		return nil, err
	}
	token, err := readPersistentDaemonTokenFile(paths.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read persistent daemon token: %w", err)
	}
	conn, err := net.DialTimeout("unix", paths.socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect persistent daemon: %w", err)
	}
	defer conn.Close()
	reader := bufio.NewReader(conn)
	if _, err := persistentDaemonRPC(conn, reader, persistentDaemonAuthMethod, map[string]any{"token": token}); err != nil {
		return nil, err
	}
	return persistentDaemonRPC(conn, reader, method, params)
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
