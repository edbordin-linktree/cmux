package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAllEmpty(t *testing.T) {
	root := t.TempDir()
	result, err := listWorkspaceSnapshots(root, true)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if result.Version != 1 {
		t.Fatalf("version = %d, want 1", result.Version)
	}
	if len(result.Snapshots) != 0 {
		t.Fatalf("snapshots len = %d, want 0", len(result.Snapshots))
	}
}

func TestListAllMultipleSlots(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSnapshotListSlot(t, root, "slot-c", "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e", true)
	writeWorkspaceSnapshotListSlot(t, root, "slot-a", "8c12d832-748d-463a-90e0-bcf6ff74b50f", true)
	writeWorkspaceSnapshotListSlot(t, root, "slot-b", "d7e2cf33-cecb-49cd-91e9-cf517f00a027", true)

	result, err := listWorkspaceSnapshots(root, true)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(result.Snapshots) != 3 {
		t.Fatalf("snapshots len = %d, want 3", len(result.Snapshots))
	}
	gotSlots := []string{result.Snapshots[0].Slot, result.Snapshots[1].Slot, result.Snapshots[2].Slot}
	wantSlots := []string{"slot-a", "slot-b", "slot-c"}
	for i := range wantSlots {
		if gotSlots[i] != wantSlots[i] {
			t.Fatalf("slot[%d] = %q, want %q; all=%v", i, gotSlots[i], wantSlots[i], gotSlots)
		}
	}
}

func TestListAllSkipsCorruptMeta(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSnapshotListSlot(t, root, "valid", "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e", true)
	corruptDir := filepath.Join(root, "corrupt")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatalf("mkdir corrupt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, workspaceSnapshotMetaFile), []byte("{nope"), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}

	textResult, err := listWorkspaceSnapshots(root, false)
	if err != nil {
		t.Fatalf("list text snapshots: %v", err)
	}
	if len(textResult.Snapshots) != 1 || textResult.Snapshots[0].Slot != "valid" {
		t.Fatalf("text snapshots = %+v, want only valid", textResult.Snapshots)
	}

	jsonResult, err := listWorkspaceSnapshots(root, true)
	if err != nil {
		t.Fatalf("list json snapshots: %v", err)
	}
	if len(jsonResult.Snapshots) != 2 {
		t.Fatalf("json snapshots len = %d, want 2", len(jsonResult.Snapshots))
	}
	if jsonResult.Snapshots[0].Slot != "corrupt" || jsonResult.Snapshots[0].Error == "" {
		t.Fatalf("first json snapshot should be corrupt error, got %+v", jsonResult.Snapshots[0])
	}
}

func TestListAllMissingBody(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSnapshotListSlot(t, root, "slot-a", "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e", false)

	result, err := listWorkspaceSnapshots(root, true)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(result.Snapshots) != 1 {
		t.Fatalf("snapshots len = %d, want 1", len(result.Snapshots))
	}
	if result.Snapshots[0].BodyPresent {
		t.Fatalf("body_present = true, want false")
	}
}

func TestListAllJSONOutputShape(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSnapshotListSlot(t, root, "slot-a", "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e", true)

	var out bytes.Buffer
	var stderr bytes.Buffer
	code := runWorkspaceSnapshotListAll([]string{"--root", root, "--json"}, &out, &stderr)
	if code != 0 {
		t.Fatalf("run list-all exit code = %d stderr=%s", code, stderr.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out.String())
	}
	for _, key := range []string{"version", "host_id", "scanned_at", "snapshots"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("output missing key %q: %s", key, out.String())
		}
	}
	snapshots, ok := decoded["snapshots"].([]any)
	if !ok || len(snapshots) != 1 {
		t.Fatalf("snapshots has unexpected shape: %T %v", decoded["snapshots"], decoded["snapshots"])
	}
	snapshot, ok := snapshots[0].(map[string]any)
	if !ok {
		t.Fatalf("snapshot has unexpected shape: %T", snapshots[0])
	}
	for _, key := range []string{"slot", "workspace_id", "title", "status", "detached_at", "updated_at", "schema_version", "snapshot_sha256", "body_byte_length", "body_present"} {
		if _, ok := snapshot[key]; !ok {
			t.Fatalf("snapshot missing key %q: %s", key, out.String())
		}
	}
}

func TestListAllTextOutputSkipsErrors(t *testing.T) {
	root := t.TempDir()
	writeWorkspaceSnapshotListSlot(t, root, "valid", "3f4a8d21-6a8f-4ef9-a979-7d712f2a8d9e", true)
	corruptDir := filepath.Join(root, "corrupt")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatalf("mkdir corrupt dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, workspaceSnapshotMetaFile), []byte("{nope"), 0o600); err != nil {
		t.Fatalf("write corrupt meta: %v", err)
	}

	var out bytes.Buffer
	code := runWorkspaceSnapshotListAll([]string{"--root", root}, &out, &bytes.Buffer{})
	if code != 0 {
		t.Fatalf("run list-all text exit code = %d", code)
	}
	if strings.Contains(out.String(), "corrupt") {
		t.Fatalf("text output should skip corrupt slot: %q", out.String())
	}
	if !strings.Contains(out.String(), "valid") {
		t.Fatalf("text output should include valid slot: %q", out.String())
	}
}

func writeWorkspaceSnapshotListSlot(t *testing.T, root string, slot string, workspaceID string, bodyPresent bool) {
	t.Helper()
	slotDir := filepath.Join(root, slot)
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		t.Fatalf("mkdir slot: %v", err)
	}
	body := `{"version":1}`
	meta := workspaceSnapshotMeta{
		Version:        1,
		WorkspaceID:    workspaceID,
		Title:          "training run",
		Status:         "detached",
		DetachedAt:     "2026-05-27T01:02:03Z",
		UpdatedAt:      "2026-05-27T01:02:03Z",
		SchemaVersion:  1,
		SnapshotSHA256: sha256Hex(body),
		BodyByteLength: len([]byte(body)),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotDir, workspaceSnapshotMetaFile), metaBytes, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	if bodyPresent {
		if err := os.WriteFile(filepath.Join(slotDir, workspaceSnapshotBodyFile), []byte(body), 0o600); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
}
