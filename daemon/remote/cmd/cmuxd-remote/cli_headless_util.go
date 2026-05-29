package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func headlessNormalizeID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, prefix := range []string{"surface:", "panel:", "pane:", "workspace:"} {
		value = strings.TrimPrefix(value, prefix)
	}
	return value
}

func canonicalHeadlessID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
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

func headlessPTYCommand(slot, workspaceID, surfaceID, command string, cwd string) string {
	exports := []string{
		"export CMUX_WORKSPACE_ID=" + shellSingleQuote(workspaceID),
		"export CMUX_TAB_ID=" + shellSingleQuote(workspaceID),
		"export CMUX_SURFACE_ID=" + shellSingleQuote(surfaceID),
		"export CMUX_PANEL_ID=" + shellSingleQuote(surfaceID),
		"export CMUX_REMOTE_DAEMON_SLOT=" + shellSingleQuote(slot),
		`export PATH="$HOME/.cmux/bin:$PATH"`,
		`export CMUX_BUNDLED_CLI_PATH="$HOME/.cmux/bin/cmux"`,
	}
	if socketPath := headlessRelaySocketForSlot(slot); socketPath != "" {
		exports = append(exports, "export CMUX_SOCKET_PATH="+shellSingleQuote(socketPath))
	}
	statements := append([]string{}, exports...)
	cwd = strings.TrimSpace(cwd)
	if cwd != "" {
		statements = append(statements, "cd -- "+shellSingleQuote(cwd))
	}
	prefix := strings.Join(statements, "; ")
	if strings.TrimSpace(command) == "" {
		return prefix + `; exec "${SHELL:-/bin/sh}" -l`
	}
	return prefix + "; exec /bin/sh -lc " + shellSingleQuote(command)
}

func headlessWorkspaceCreateCWD(params map[string]any) string {
	for _, key := range []string{"working_directory", "cwd"} {
		if value := strings.TrimSpace(stringFromAny(params[key])); value != "" {
			return resolveCLIPath(value)
		}
	}
	return ""
}

func headlessRelaySocketForSlot(slot string) string {
	paths, err := headlessPathsForSlot(slot)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(paths.root, "relay_socket"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func headlessWorkspaceDefaultTitle(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "Remote Workspace"
	}
	base := filepath.Base(cwd)
	if base == "." || base == "/" || strings.TrimSpace(base) == "" {
		return cwd
	}
	return base
}

func headlessDisplayTarget(slot string, destination string) string {
	destination = strings.TrimSpace(destination)
	if destination != "" {
		return destination + ":" + slot
	}
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		host = "remote"
	}
	return host + ":" + slot
}

func shellSingleQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(typed))
		return i
	default:
		return 0
	}
}

func boolFromAny(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
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
