package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type relayAuthState struct {
	RelayID    string `json:"relay_id"`
	RelayToken string `json:"relay_token"`
}

type socketServerError struct {
	code    string
	message string
}

func (err *socketServerError) Error() string {
	return fmt.Sprintf("server error [%s]: %s", err.code, err.message)
}

func isSocketServerError(err error) bool {
	var serverErr *socketServerError
	return errors.As(err, &serverErr)
}

func shouldTryHeadlessFallback(err error) bool {
	return err != nil && !isSocketServerError(err)
}

// protocolVersion indicates whether a command uses the v1 text or v2 JSON-RPC protocol.
type protocolVersion int

const (
	protoV1 protocolVersion = iota
	protoV2
)

// commandSpec describes a single CLI command and how to relay it.
type commandSpec struct {
	name     string          // CLI command name (e.g. "ping", "new-window")
	proto    protocolVersion // v1 text or v2 JSON-RPC
	v1Cmd    string          // v1: literal command string sent over the socket
	v2Method string          // v2: JSON-RPC method name
	// flagKeys lists parameter keys this command accepts.
	// They are extracted from --key flags and added to params.
	flagKeys []string
	// noParams means the command takes no parameters at all.
	noParams bool
	// paramKeyOverrides remaps specific flags for compatibility aliases.
	paramKeyOverrides map[string]string
	// defaultParams are applied before flags/env fallbacks.
	defaultParams map[string]any
}

type browserCommandSpec struct {
	method                string
	flagKeys              []string
	allowPositionalURL    bool
	allowPositionalScript bool
	allowPositionalKey    bool
	allowPositionalQuery  bool
	allowPositionalValue  bool
	useWorkspaceEnv       bool
	useSurfaceEnv         bool
}

var commands = []commandSpec{
	// V1 text protocol commands
	{name: "ping", proto: protoV1, v1Cmd: "ping", noParams: true},
	{name: "new-window", proto: protoV1, v1Cmd: "new_window", noParams: true},
	{name: "current-window", proto: protoV1, v1Cmd: "current_window", noParams: true},
	{name: "close-window", proto: protoV1, v1Cmd: "close_window", flagKeys: []string{"window"}},
	{name: "focus-window", proto: protoV1, v1Cmd: "focus_window", flagKeys: []string{"window"}},
	{name: "list-windows", proto: protoV1, v1Cmd: "list_windows", noParams: true},

	// V2 JSON-RPC commands
	{name: "capabilities", proto: protoV2, v2Method: "system.capabilities", noParams: true},
	{name: "tree", proto: protoV2, v2Method: "system.tree", flagKeys: []string{"workspace"}},
	{name: "list-workspaces", proto: protoV2, v2Method: "workspace.list", noParams: true},
	{name: "new-workspace", proto: protoV2, v2Method: "workspace.create", flagKeys: []string{"command", "cwd", "working-directory", "name", "description", "focus"}},
	{name: "close-workspace", proto: protoV2, v2Method: "workspace.close", flagKeys: []string{"workspace"}},
	{name: "select-workspace", proto: protoV2, v2Method: "workspace.select", flagKeys: []string{"workspace"}},
	{name: "current-workspace", proto: protoV2, v2Method: "workspace.current", noParams: true},
	{name: "rename-workspace", proto: protoV2, v2Method: "workspace.rename", flagKeys: []string{"workspace", "title"}},
	{name: "rename-window", proto: protoV2, v2Method: "workspace.rename", flagKeys: []string{"workspace", "title"}},
	{name: "list-panels", proto: protoV2, v2Method: "surface.list", flagKeys: []string{"workspace"}},
	{name: "focus-panel", proto: protoV2, v2Method: "surface.focus", flagKeys: []string{"panel", "workspace"}, paramKeyOverrides: map[string]string{"panel": "surface_id"}},
	{name: "focus-surface", proto: protoV2, v2Method: "surface.focus", flagKeys: []string{"surface", "workspace"}},
	{name: "list-panes", proto: protoV2, v2Method: "pane.list", flagKeys: []string{"workspace"}},
	{name: "list-pane-surfaces", proto: protoV2, v2Method: "pane.surfaces", flagKeys: []string{"pane"}},
	{name: "new-pane", proto: protoV2, v2Method: "pane.create", flagKeys: []string{"workspace", "surface", "direction", "type", "url", "command", "focus"}, defaultParams: map[string]any{"direction": "right"}},
	{name: "new-surface", proto: protoV2, v2Method: "surface.create", flagKeys: []string{"workspace", "pane", "type", "url", "command", "focus"}},
	{name: "new-split", proto: protoV2, v2Method: "surface.split", flagKeys: []string{"workspace", "surface", "direction", "type", "url", "command", "focus"}},
	{name: "close-surface", proto: protoV2, v2Method: "surface.close", flagKeys: []string{"workspace", "surface"}},
	{name: "send", proto: protoV2, v2Method: "surface.send_text", flagKeys: []string{"workspace", "surface", "text"}},
	{name: "send-key", proto: protoV2, v2Method: "surface.send_key", flagKeys: []string{"workspace", "surface", "key"}},
	{name: "rename-tab", proto: protoV2, v2Method: "tab.action", flagKeys: []string{"workspace", "surface", "tab", "title"}, paramKeyOverrides: map[string]string{"tab": "surface_id"}, defaultParams: map[string]any{"action": "rename"}},
	{name: "notify", proto: protoV2, v2Method: "notification.create", flagKeys: []string{"title", "body", "workspace"}},
	{name: "refresh-surfaces", proto: protoV2, v2Method: "surface.refresh", noParams: true},
}

var browserCommands = map[string]browserCommandSpec{
	"open":       {method: "browser.open_split", flagKeys: []string{"url", "workspace", "surface"}, allowPositionalURL: true, useWorkspaceEnv: true},
	"open-split": {method: "browser.open_split", flagKeys: []string{"url", "workspace", "surface"}, allowPositionalURL: true, useWorkspaceEnv: true},
	"new":        {method: "browser.open_split", flagKeys: []string{"url", "workspace", "surface"}, allowPositionalURL: true, useWorkspaceEnv: true},
	"navigate":   {method: "browser.navigate", flagKeys: []string{"url", "surface"}, allowPositionalURL: true, useSurfaceEnv: true},
	"goto":       {method: "browser.navigate", flagKeys: []string{"url", "surface"}, allowPositionalURL: true, useSurfaceEnv: true},
	"back":       {method: "browser.back", flagKeys: []string{"surface"}, useSurfaceEnv: true},
	"forward":    {method: "browser.forward", flagKeys: []string{"surface"}, useSurfaceEnv: true},
	"reload":     {method: "browser.reload", flagKeys: []string{"surface"}, useSurfaceEnv: true},
	"get-url":    {method: "browser.url.get", flagKeys: []string{"surface"}, useSurfaceEnv: true},
	"url":        {method: "browser.url.get", flagKeys: []string{"surface"}, useSurfaceEnv: true},
	"snapshot":   {method: "browser.snapshot", flagKeys: []string{"surface", "selector", "max-depth"}, useSurfaceEnv: true},
	"eval":       {method: "browser.eval", flagKeys: []string{"surface", "script"}, allowPositionalScript: true, useSurfaceEnv: true},
	"wait":       {method: "browser.wait", flagKeys: []string{"surface", "selector", "text", "url-contains", "load-state", "function", "timeout-ms"}, useSurfaceEnv: true},
	"click":      {method: "browser.click", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"dblclick":   {method: "browser.dblclick", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"hover":      {method: "browser.hover", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"focus":      {method: "browser.focus", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"check":      {method: "browser.check", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"uncheck":    {method: "browser.uncheck", flagKeys: []string{"surface", "selector"}, allowPositionalQuery: true, useSurfaceEnv: true},
	"type":       {method: "browser.type", flagKeys: []string{"surface", "selector", "text"}, allowPositionalValue: true, useSurfaceEnv: true},
	"fill":       {method: "browser.fill", flagKeys: []string{"surface", "selector", "text"}, allowPositionalValue: true, useSurfaceEnv: true},
	"press":      {method: "browser.press", flagKeys: []string{"surface", "key"}, allowPositionalKey: true, useSurfaceEnv: true},
	"key":        {method: "browser.press", flagKeys: []string{"surface", "key"}, allowPositionalKey: true, useSurfaceEnv: true},
	"keydown":    {method: "browser.keydown", flagKeys: []string{"surface", "key"}, allowPositionalKey: true, useSurfaceEnv: true},
	"keyup":      {method: "browser.keyup", flagKeys: []string{"surface", "key"}, allowPositionalKey: true, useSurfaceEnv: true},
	"select":     {method: "browser.select", flagKeys: []string{"surface", "selector", "value"}, allowPositionalValue: true, useSurfaceEnv: true},
	"screenshot": {method: "browser.screenshot", flagKeys: []string{"surface"}, useSurfaceEnv: true},
}

var commandIndex map[string]*commandSpec

func init() {
	commandIndex = make(map[string]*commandSpec, len(commands))
	for i := range commands {
		commandIndex[commands[i].name] = &commands[i]
	}
}

func hasHeadlessRemoteContext() bool {
	return strings.TrimSpace(os.Getenv("CMUX_WORKSPACE_ID")) != "" ||
		strings.TrimSpace(os.Getenv("CMUX_REMOTE_DAEMON_SLOT")) != "" ||
		strings.TrimSpace(os.Getenv("CMUX_PERSISTENT_DAEMON_SLOT")) != "" ||
		strings.TrimSpace(os.Getenv("CMUX_DAEMON_SLOT")) != ""
}

func commandMayUseHeadlessNoSocket(cmdName string) bool {
	switch cmdName {
	case "metadata", "workspace", "tree", "set-status", "clear-status", "list-status":
		return true
	}
	if spec := commandIndex[cmdName]; spec != nil && spec.proto == protoV2 {
		return headlessSupportsMethod(spec.v2Method)
	}
	return false
}

func extractCommandJSONFlag(args []string) ([]string, bool) {
	out := make([]string, 0, len(args))
	found := false
	passthrough := false
	for _, arg := range args {
		if passthrough {
			out = append(out, arg)
			continue
		}
		if arg == "--" {
			passthrough = true
			out = append(out, arg)
			continue
		}
		if arg == "--json" {
			found = true
			continue
		}
		out = append(out, arg)
	}
	return out, found
}

// runCLI is the entry point for the "cli" subcommand (or busybox "cmux" invocation).
func runCLI(args []string) int {
	socketPath := os.Getenv("CMUX_SOCKET_PATH")

	// Parse global flags
	var jsonOutput bool
	var remaining []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "cmux: --socket requires a path")
				return 2
			}
			socketPath = args[i+1]
			i++
		case "--json":
			jsonOutput = true
		case "--help", "-h":
			cliUsage()
			return 0
		default:
			remaining = append(remaining, args[i:]...)
			goto doneFlags
		}
	}
doneFlags:

	if len(remaining) == 0 {
		cliUsage()
		return 2
	}
	cmdName := remaining[0]
	cmdArgs := remaining[1:]
	if cmdName == "help" {
		cliUsage()
		return 0
	}
	var commandJSON bool
	cmdArgs, commandJSON = extractCommandJSONFlag(cmdArgs)
	if commandJSON {
		jsonOutput = true
	}

	// refreshAddr is set when the address came from socket_addr file (not env/flag),
	// allowing one stale-address refresh if another workspace has replaced socket_addr.
	var refreshAddr func() string
	if socketPath == "" && !shouldSkipImplicitSocketAddr(cmdName) {
		socketPath = readSocketAddrFile()
		refreshAddr = readSocketAddrFile
	}
	if socketPath == "" {
		if cmdName == "ssh" {
			return runSSHRelay("", cmdArgs, jsonOutput, nil)
		}
		if !hasHeadlessRemoteContext() || !commandMayUseHeadlessNoSocket(cmdName) {
			fmt.Fprintln(os.Stderr, "cmux: CMUX_SOCKET_PATH not set and --socket not provided")
			return 1
		}
	}
	if cmdName == "ssh" {
		return runSSHRelay(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}

	// Special case: "rpc" passthrough
	if cmdName == "rpc" {
		return runRPC(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}

	// Browser subcommand delegation
	if cmdName == "browser" {
		return runBrowserRelay(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}
	if cmdName == "tree" {
		return runTreeRelay(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}
	if cmdName == "metadata" {
		return runMetadataRelay(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}
	if cmdName == "workspace" {
		return runWorkspaceRelay(socketPath, cmdArgs, jsonOutput, refreshAddr)
	}
	if cmdName == "set-status" || cmdName == "clear-status" || cmdName == "list-status" {
		return runStatusRelay(socketPath, cmdName, cmdArgs, jsonOutput, refreshAddr)
	}

	// Agent launch commands
	if cmdName == "claude-teams" {
		return runClaudeTeamsRelay(socketPath, cmdArgs, refreshAddr)
	}
	if cmdName == "omo" {
		return runOMORelay(socketPath, cmdArgs, refreshAddr)
	}
	if cmdName == "omx" {
		return runOMXRelay(socketPath, cmdArgs, refreshAddr)
	}
	if cmdName == "omc" {
		return runOMCRelay(socketPath, cmdArgs, refreshAddr)
	}

	// Tmux compatibility layer (used by agent shims)
	if cmdName == "__tmux-compat" {
		return runTmuxCompat(socketPath, cmdArgs, refreshAddr)
	}

	spec, ok := commandIndex[cmdName]
	if !ok {
		fmt.Fprintf(os.Stderr, "cmux: unknown command %q\n", cmdName)
		return 2
	}

	switch spec.proto {
	case protoV1:
		return execV1(socketPath, spec, cmdArgs, refreshAddr)
	case protoV2:
		return execV2(socketPath, spec, cmdArgs, jsonOutput, refreshAddr)
	default:
		fmt.Fprintf(os.Stderr, "cmux: internal error: unknown protocol for %q\n", cmdName)
		return 1
	}
}

func shouldSkipImplicitSocketAddr(cmdName string) bool {
	if !hasHeadlessRemoteContext() {
		return false
	}
	if cmdName == "ssh" {
		return true
	}
	return commandMayUseHeadlessNoSocket(cmdName)
}

// execV1 sends a v1 text command over the socket.
func execV1(socketPath string, spec *commandSpec, args []string, refreshAddr func() string) int {
	cmd := spec.v1Cmd

	if !spec.noParams {
		parsed, err := parseFlags(args, spec.flagKeys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
			return 2
		}
		for _, key := range spec.flagKeys {
			if val, ok := parsed.flags[key]; ok {
				cmd += " " + val
			}
		}
	}

	resp, err := socketRoundTrip(socketPath, cmd, refreshAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	fmt.Print(resp)
	if !strings.HasSuffix(resp, "\n") {
		fmt.Println()
	}
	return 0
}

// execV2 sends a v2 JSON-RPC request over the socket.
func execV2(socketPath string, spec *commandSpec, args []string, jsonOutput bool, refreshAddr func() string) int {
	params := make(map[string]any, len(spec.defaultParams))
	for key, value := range spec.defaultParams {
		params[key] = value
	}
	forceHeadless := false

	if !spec.noParams {
		parsed, err := parseFlags(args, spec.flagKeys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
			return 2
		}
		if _, ok := parsed.flags["json"]; ok {
			jsonOutput = true
		}
		// Map flag keys to JSON param keys (e.g. "workspace" → "workspace_id" where appropriate)
		for _, key := range spec.flagKeys {
			if val, ok := parsed.flags[key]; ok {
				paramKey := flagToParamKey(key)
				if override, ok := spec.paramKeyOverrides[key]; ok {
					paramKey = override
				}
				if key == "cwd" || key == "working-directory" {
					val = resolveCLIPath(val)
				}
				params[paramKey] = val
			}
		}

		switch spec.name {
		case "send":
			if _, ok := params["text"]; !ok && len(parsed.positional) > 0 {
				params["text"] = strings.Join(parsed.positional, " ")
			}
			if text, ok := params["text"].(string); ok {
				params["text"] = unescapeCLIText(text)
			}
		case "send-key":
			if _, ok := params["key"]; !ok && len(parsed.positional) > 0 {
				params["key"] = parsed.positional[0]
			}
		case "focus-surface":
			if _, ok := params["surface_id"]; !ok && len(parsed.positional) > 0 {
				params["surface_id"] = parsed.positional[0]
			}
		case "rename-workspace", "rename-window", "rename-tab":
			if _, ok := params["title"]; !ok && len(parsed.positional) > 0 {
				params["title"] = strings.Join(parsed.positional, " ")
			}
		default:
			// First positional arg is used as initial_command if --command wasn't given.
			if _, ok := params["initial_command"]; !ok && len(parsed.positional) > 0 {
				params["initial_command"] = parsed.positional[0]
			}
		}

		applyWorkspaceEnvFallback(params)
		applySurfaceEnvFallback(params, parsed.flags["workspace"])

		if workspaceArg := parsed.flags["workspace"]; workspaceArg != "" {
			route := routeForExplicitWorkspaceArg(workspaceArg, socketPath)
			socketPath = route.socketPath
			forceHeadless = route.forceHeadless
		}
	}

	if forceHeadless {
		if code, handled := runHeadlessCLICommand(spec.name, spec.v2Method, params, jsonOutput, errors.New("target workspace is detached")); handled {
			return code
		}
		fmt.Fprintf(os.Stderr, "cmux: %s cannot operate on detached workspace\n", spec.name)
		return 1
	}

	resp, err := socketRoundTripV2(socketPath, spec.v2Method, params, refreshAddr)
	if err != nil {
		if shouldTryHeadlessFallback(err) {
			if code, handled := runHeadlessCLICommand(spec.name, spec.v2Method, params, jsonOutput, err); handled {
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}

	if jsonOutput {
		fmt.Println(resp)
	} else {
		fmt.Println(defaultRelayOutput(resp))
	}
	return 0
}

func runStatusRelay(socketPath string, cmdName string, args []string, jsonOutput bool, refreshAddr func() string) int {
	var method string
	var socketCommand string
	var params map[string]any
	var err error
	switch cmdName {
	case "set-status":
		method = "status.set"
		socketCommand, params, err = parseSetStatusCommand(args)
	case "clear-status":
		method = "status.clear"
		socketCommand, params, err = parseClearStatusCommand(args)
	case "list-status":
		method = "status.list"
		socketCommand, params, err = parseListStatusCommand(args)
	default:
		err = fmt.Errorf("unsupported status command %q", cmdName)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 2
	}
	if targetSocketPath := stringFromAny(params["_target_socket_path"]); targetSocketPath != "" {
		socketPath = targetSocketPath
		delete(params, "_target_socket_path")
	}
	forceHeadless := boolFromAny(params["_target_headless"])
	delete(params, "_target_headless")
	if forceHeadless {
		if code, handled := runHeadlessCLICommand(cmdName, method, params, jsonOutput, errors.New("target workspace is detached")); handled {
			return code
		}
		fmt.Fprintf(os.Stderr, "cmux: %s cannot operate on detached workspace\n", cmdName)
		return 1
	}
	_ = socketCommand // Kept for parser parity with the legacy v1 command shape.
	resp, err := socketRoundTripV2(socketPath, method, params, refreshAddr)
	if err != nil {
		if shouldTryHeadlessFallback(err) {
			if code, handled := runHeadlessCLICommand(cmdName, method, params, jsonOutput, err); handled {
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	if jsonOutput {
		fmt.Println(resp)
		return 0
	}
	fmt.Println(statusRelayTextOutput(cmdName, resp))
	return 0
}

func statusRelayTextOutput(cmdName string, resp string) string {
	if cmdName != "list-status" {
		return "OK"
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(resp), &payload); err != nil {
		return defaultRelayOutput(resp)
	}
	rawEntries, _ := payload["entries"].([]any)
	if len(rawEntries) == 0 {
		return "No status entries"
	}
	lines := make([]string, 0, len(rawEntries))
	for _, rawEntry := range rawEntries {
		entry, _ := rawEntry.(map[string]any)
		if entry == nil {
			continue
		}
		key := stringFromAny(entry["key"])
		value := stringFromAny(entry["value"])
		if key == "" {
			continue
		}
		line := key + "=" + value
		for _, option := range []string{"icon", "color", "url"} {
			if value := stringFromAny(entry[option]); value != "" {
				line += " " + option + "=" + value
			}
		}
		if priority := intFromAny(entry["priority"]); priority != 0 {
			line += fmt.Sprintf(" priority=%d", priority)
		}
		if format := stringFromAny(entry["format"]); format != "" && format != "plain" {
			line += " format=" + format
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "No status entries"
	}
	return strings.Join(lines, "\n")
}

type remoteSSHCLIOptions struct {
	destination    string
	name           string
	cwd            string
	port           string
	identity       string
	noFocus        bool
	detached       bool
	sshOptions     []string
	extraArguments []string
	jsonOutput     bool
}

func runSSHRelay(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	options, err := parseRemoteSSHCLIOptions(args, jsonOutput)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 2
	}
	params := remoteSSHWorkspaceParams(options)
	if strings.TrimSpace(socketPath) != "" && !options.detached {
		resp, err := socketRoundTripV2(socketPath, "workspace.remote.ssh_create", params, refreshAddr)
		if err == nil {
			if options.jsonOutput {
				fmt.Println(resp)
			} else {
				fmt.Println(defaultRelayOutput(resp))
			}
			return 0
		}
		if !shouldTryHeadlessFallback(err) {
			fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
			return 1
		}
	}
	if !remoteSSHDestinationIsSameHost(options.destination) {
		fmt.Fprintf(
			os.Stderr,
			"cmux: remote cmux ssh currently supports same-host workspace creation only; run cmux ssh from the Mac for %q\n",
			options.destination,
		)
		return 2
	}

	result, err := headlessCreateWorkspace(params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: ssh same-host detached workspace creation failed: %v\n", err)
		return 1
	}
	payload, err := json.Marshal(result)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: failed to encode ssh result: %v\n", err)
		return 1
	}
	if options.jsonOutput {
		fmt.Println(string(payload))
	} else {
		fmt.Println(defaultRelayOutput(string(payload)))
	}
	return 0
}

func remoteSSHWorkspaceParams(options remoteSSHCLIOptions) map[string]any {
	cwd := strings.TrimSpace(options.cwd)
	if cwd == "" {
		if current, err := os.Getwd(); err == nil {
			cwd = current
		}
	}
	command := remoteSSHShellJoin(options.extraArguments)
	params := map[string]any{
		"destination":     options.destination,
		"cwd":             cwd,
		"initial_command": command,
		"focus":           !options.noFocus,
		"same_host":       remoteSSHDestinationIsSameHost(options.destination),
	}
	if strings.TrimSpace(options.name) != "" {
		params["title"] = strings.TrimSpace(options.name)
	}
	if strings.TrimSpace(options.port) != "" {
		params["port"] = strings.TrimSpace(options.port)
	}
	if strings.TrimSpace(options.identity) != "" {
		params["identity_file"] = strings.TrimSpace(options.identity)
	}
	if len(options.sshOptions) > 0 {
		params["ssh_options"] = options.sshOptions
	}
	return params
}

func parseRemoteSSHCLIOptions(args []string, jsonOutput bool) (remoteSSHCLIOptions, error) {
	var options remoteSSHCLIOptions
	options.jsonOutput = jsonOutput
	passthrough := false
	for i := 0; i < len(args); {
		arg := args[i]
		if passthrough {
			options.extraArguments = append(options.extraArguments, arg)
			i++
			continue
		}
		switch arg {
		case "--":
			passthrough = true
			i++
		case "--json":
			options.jsonOutput = true
			i++
		case "--port":
			value, next, err := remoteSSHFlagValue(args, i, arg)
			if err != nil {
				return options, err
			}
			options.port = value
			i = next
		case "--identity":
			value, next, err := remoteSSHFlagValue(args, i, arg)
			if err != nil {
				return options, err
			}
			options.identity = value
			i = next
		case "--name":
			value, next, err := remoteSSHFlagValue(args, i, arg)
			if err != nil {
				return options, err
			}
			options.name = value
			i = next
		case "--cwd", "--working-directory":
			value, next, err := remoteSSHFlagValue(args, i, arg)
			if err != nil {
				return options, err
			}
			options.cwd = resolveCLIPath(value)
			i = next
		case "--no-focus":
			options.noFocus = true
			i++
		case "--detached":
			options.detached = true
			i++
		case "--ssh-option":
			value, next, err := remoteSSHFlagValue(args, i, arg)
			if err != nil {
				return options, err
			}
			value = strings.TrimSpace(value)
			if value != "" {
				options.sshOptions = append(options.sshOptions, value)
			}
			i = next
		default:
			if strings.HasPrefix(arg, "--") {
				return options, fmt.Errorf("ssh: unknown flag %q", arg)
			}
			if options.destination == "" {
				if strings.HasPrefix(arg, "-") {
					return options, errors.New("ssh: destination must be <user@host>")
				}
				options.destination = arg
			} else {
				options.extraArguments = append(options.extraArguments, arg)
			}
			i++
		}
	}
	if strings.TrimSpace(options.destination) == "" {
		return options, errors.New("ssh requires a destination (example: cmux ssh user@host)")
	}
	return options, nil
}

func remoteSSHFlagValue(args []string, index int, flag string) (string, int, error) {
	if index+1 >= len(args) {
		return "", index, fmt.Errorf("ssh: %s requires a value", flag)
	}
	return args[index+1], index + 2, nil
}

func remoteSSHShellJoin(args []string) string {
	if len(args) == 0 {
		return ""
	}
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, remoteSSHShellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func remoteSSHShellQuote(value string) string {
	if value == "" {
		return "''"
	}
	safe := true
	for _, r := range value {
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			strings.ContainsRune("@%_+=:,./-", r) {
			continue
		}
		safe = false
		break
	}
	if safe {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func remoteSSHDestinationIsSameHost(destination string) bool {
	host := remoteSSHHostPart(destination)
	if host == "" {
		return false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	candidates := map[string]bool{}
	addHostCandidate := func(value string) {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		if value == "" {
			return
		}
		candidates[value] = true
		if dot := strings.IndexByte(value, '.'); dot > 0 {
			candidates[value[:dot]] = true
		}
	}
	if local, err := os.Hostname(); err == nil {
		addHostCandidate(local)
	}
	addHostCandidate(os.Getenv("HOSTNAME"))
	addHostCandidate(os.Getenv("HOST"))
	return candidates[host]
}

func remoteSSHHostPart(destination string) string {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return ""
	}
	if at := strings.LastIndex(destination, "@"); at >= 0 {
		destination = destination[at+1:]
	}
	if strings.HasPrefix(destination, "[") {
		if end := strings.Index(destination, "]"); end > 0 {
			return destination[1:end]
		}
	}
	if colon := strings.LastIndex(destination, ":"); colon > 0 && !strings.Contains(destination[colon+1:], "/") {
		possiblePort := destination[colon+1:]
		allDigits := possiblePort != ""
		for _, r := range possiblePort {
			if r < '0' || r > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			destination = destination[:colon]
		}
	}
	return strings.TrimSpace(destination)
}

func parseSetStatusCommand(args []string) (string, map[string]any, error) {
	parsed, err := parseFlags(args, []string{"workspace", "icon", "color", "url", "priority", "format"})
	if err != nil {
		return "", nil, err
	}
	if len(parsed.positional) < 2 {
		return "", nil, errors.New("set-status requires <key> <value>")
	}
	params := map[string]any{
		"key":   parsed.positional[0],
		"value": strings.Join(parsed.positional[1:], " "),
	}
	parts := []string{"set_status", shellQuoteForSocket(params["key"].(string)), shellQuoteForSocket(params["value"].(string))}
	for _, flag := range []string{"icon", "color", "url", "priority", "format"} {
		if value, ok := parsed.flags[flag]; ok {
			params[flag] = value
			parts = append(parts, fmt.Sprintf("--%s=%s", flag, shellQuoteForSocket(value)))
		}
	}
	if workspace, ok := parsed.flags["workspace"]; ok {
		params["workspace_id"] = workspace
		parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		route := routeForExplicitWorkspaceArg(workspace, "")
		if route.forceHeadless {
			params["_target_headless"] = true
		} else if route.socketPath != "" {
			params["_target_socket_path"] = route.socketPath
		}
	} else {
		applyWorkspaceEnvFallback(params)
		if workspace := stringFromAny(params["workspace_id"]); workspace != "" {
			parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		}
	}
	return strings.Join(parts, " "), params, nil
}

func parseClearStatusCommand(args []string) (string, map[string]any, error) {
	parsed, err := parseFlags(args, []string{"workspace"})
	if err != nil {
		return "", nil, err
	}
	if len(parsed.positional) != 1 {
		return "", nil, errors.New("clear-status requires <key>")
	}
	params := map[string]any{"key": parsed.positional[0]}
	parts := []string{"clear_status", shellQuoteForSocket(parsed.positional[0])}
	if workspace, ok := parsed.flags["workspace"]; ok {
		params["workspace_id"] = workspace
		parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		route := routeForExplicitWorkspaceArg(workspace, "")
		if route.forceHeadless {
			params["_target_headless"] = true
		} else if route.socketPath != "" {
			params["_target_socket_path"] = route.socketPath
		}
	} else {
		applyWorkspaceEnvFallback(params)
		if workspace := stringFromAny(params["workspace_id"]); workspace != "" {
			parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		}
	}
	return strings.Join(parts, " "), params, nil
}

func parseListStatusCommand(args []string) (string, map[string]any, error) {
	parsed, err := parseFlags(args, []string{"workspace"})
	if err != nil {
		return "", nil, err
	}
	if len(parsed.positional) > 0 {
		return "", nil, errors.New("list-status does not accept positional arguments")
	}
	params := map[string]any{}
	parts := []string{"list_status"}
	if workspace, ok := parsed.flags["workspace"]; ok {
		params["workspace_id"] = workspace
		parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		route := routeForExplicitWorkspaceArg(workspace, "")
		if route.forceHeadless {
			params["_target_headless"] = true
		} else if route.socketPath != "" {
			params["_target_socket_path"] = route.socketPath
		}
	} else {
		applyWorkspaceEnvFallback(params)
		if workspace := stringFromAny(params["workspace_id"]); workspace != "" {
			parts = append(parts, "--tab="+shellQuoteForSocket(workspace))
		}
	}
	return strings.Join(parts, " "), params, nil
}

func shellQuoteForSocket(value string) string {
	if value == "" {
		return "''"
	}
	if !strings.ContainsAny(value, " \t\n\r'\"\\$`!|&;()<>*?[]{}") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func runTreeRelay(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	args, trailingJSON := stripStandaloneFlag(args, "--json")
	jsonOutput = jsonOutput || trailingJSON
	parsed, err := parseFlags(args, []string{"workspace"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux tree: %v\n", err)
		return 2
	}
	if len(parsed.positional) > 0 {
		fmt.Fprintf(os.Stderr, "cmux tree: unexpected argument %q\n", parsed.positional[0])
		return 2
	}
	params := map[string]any{}
	if workspace, ok := parsed.flags["workspace"]; ok {
		params["workspace_id"] = workspace
		route := routeForExplicitWorkspaceArg(workspace, socketPath)
		socketPath = route.socketPath
		if route.forceHeadless {
			if code, handled := runHeadlessCLICommand("tree", "system.tree", params, jsonOutput, errors.New("target workspace is detached")); handled {
				return code
			}
			fmt.Fprintln(os.Stderr, "cmux: tree cannot operate on detached workspace")
			return 1
		}
	}
	applyWorkspaceEnvFallback(params)
	resp, err := socketRoundTripV2(socketPath, "system.tree", params, refreshAddr)
	if err != nil {
		if shouldTryHeadlessFallback(err) {
			if code, handled := runHeadlessCLICommand("tree", "system.tree", params, jsonOutput, err); handled {
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	if jsonOutput {
		fmt.Println(resp)
	} else {
		fmt.Println(defaultRelayOutput(resp))
	}
	return 0
}

func runMetadataRelay(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	args, trailingJSON := stripStandaloneFlag(args, "--json")
	jsonOutput = jsonOutput || trailingJSON
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "cmux metadata: requires a subcommand (set, get, list, clear)")
		return 2
	}
	sub := args[0]
	parsed, err := parseFlags(args[1:], []string{"workspace", "prefix", "value", "value-json"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux metadata: %v\n", err)
		return 2
	}
	params := map[string]any{}
	forceHeadless := false
	if workspace, ok := parsed.flags["workspace"]; ok {
		params["workspace_id"] = workspace
		route := routeForExplicitWorkspaceArg(workspace, socketPath)
		socketPath = route.socketPath
		forceHeadless = route.forceHeadless
	}
	applyWorkspaceEnvFallback(params)

	var method string
	switch sub {
	case "set":
		method = "metadata.set"
		if len(parsed.positional) == 0 {
			fmt.Fprintln(os.Stderr, "cmux metadata set: requires <key> <value>")
			return 2
		}
		params["key"] = parsed.positional[0]
		if value, ok := parsed.flags["value-json"]; ok {
			params["json_value"] = value
		} else if value, ok := parsed.flags["value"]; ok {
			params["value"] = value
		} else if len(parsed.positional) > 1 {
			params["value"] = strings.Join(parsed.positional[1:], " ")
		} else {
			fmt.Fprintln(os.Stderr, "cmux metadata set: requires a value")
			return 2
		}
	case "get":
		method = "metadata.get"
		if len(parsed.positional) != 1 {
			fmt.Fprintln(os.Stderr, "cmux metadata get: requires <key>")
			return 2
		}
		params["key"] = parsed.positional[0]
	case "list":
		method = "metadata.list"
		if prefix, ok := parsed.flags["prefix"]; ok {
			params["prefix"] = prefix
		}
	case "clear":
		method = "metadata.clear"
		if len(parsed.positional) != 1 {
			fmt.Fprintln(os.Stderr, "cmux metadata clear: requires <key>")
			return 2
		}
		params["key"] = parsed.positional[0]
	default:
		fmt.Fprintf(os.Stderr, "cmux metadata: unknown subcommand %q\n", sub)
		return 2
	}

	if forceHeadless {
		if code, handled := runHeadlessCLICommand("metadata "+sub, method, params, jsonOutput, errors.New("target workspace is detached")); handled {
			return code
		}
		fmt.Fprintf(os.Stderr, "cmux: metadata %s cannot operate on detached workspace\n", sub)
		return 1
	}

	resp, err := socketRoundTripV2(socketPath, method, params, refreshAddr)
	if err != nil {
		if shouldTryHeadlessFallback(err) {
			if code, handled := runHeadlessCLICommand("metadata "+sub, method, params, jsonOutput, err); handled {
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	if jsonOutput {
		fmt.Println(resp)
	} else {
		fmt.Println(defaultRelayOutput(resp))
	}
	return 0
}

func runWorkspaceRelay(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	if len(args) == 0 || args[0] != "lookup" {
		fmt.Fprintln(os.Stderr, "cmux workspace: supported subcommand: lookup")
		return 2
	}
	args, trailingJSON := stripStandaloneFlag(args, "--json")
	jsonOutput = jsonOutput || trailingJSON
	criteria := map[string]string{}
	includeDetached := false
	remaining := args[1:]
	for i := 0; i < len(remaining); i++ {
		switch remaining[i] {
		case "--metadata":
			if i+1 >= len(remaining) {
				fmt.Fprintln(os.Stderr, "cmux workspace lookup: --metadata requires key=value")
				return 2
			}
			parts := strings.SplitN(remaining[i+1], "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
				fmt.Fprintln(os.Stderr, "cmux workspace lookup: --metadata requires key=value")
				return 2
			}
			criteria[strings.TrimSpace(parts[0])] = parts[1]
			i++
		case "--include-detached":
			includeDetached = true
		default:
			fmt.Fprintf(os.Stderr, "cmux workspace lookup: unknown argument %q\n", remaining[i])
			return 2
		}
	}
	if len(criteria) == 0 {
		fmt.Fprintln(os.Stderr, "cmux workspace lookup: requires at least one --metadata key=value")
		return 2
	}
	params := map[string]any{"metadata": criteria}
	if includeDetached {
		params["include_detached"] = true
	}
	resp, err := socketRoundTripV2(socketPath, "workspace.lookup", params, refreshAddr)
	if err != nil {
		if shouldTryHeadlessFallback(err) {
			if code, handled := runHeadlessCLICommand("workspace lookup", "workspace.lookup", params, jsonOutput, err); handled {
				return code
			}
		}
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	if jsonOutput {
		fmt.Println(resp)
	} else {
		fmt.Println(defaultRelayOutput(resp))
	}
	return 0
}

func stripStandaloneFlag(args []string, flag string) ([]string, bool) {
	var stripped []string
	found := false
	for _, arg := range args {
		if arg == flag {
			found = true
			continue
		}
		stripped = append(stripped, arg)
	}
	return stripped, found
}

// runRPC sends an arbitrary JSON-RPC method with optional JSON params.
func runRPC(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "cmux rpc: requires a method name")
		return 2
	}
	method := args[0]
	var params map[string]any
	if len(args) > 1 {
		if err := json.Unmarshal([]byte(args[1]), &params); err != nil {
			fmt.Fprintf(os.Stderr, "cmux rpc: invalid JSON params: %v\n", err)
			return 2
		}
	}

	resp, err := socketRoundTripV2(socketPath, method, params, refreshAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	fmt.Println(resp)
	return 0
}

// runBrowserRelay handles "cmux browser <subcommand>" by mapping to browser.* v2 methods.
func runBrowserRelay(socketPath string, args []string, jsonOutput bool, refreshAddr func() string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "cmux browser: requires a subcommand (%s)\n", browserSubcommandHint())
		return 2
	}

	sub := args[0]
	subArgs := args[1:]

	spec, ok := browserCommands[sub]
	if !ok {
		fmt.Fprintf(os.Stderr, "cmux browser: unknown subcommand %q\n", sub)
		return 2
	}

	params := make(map[string]any)
	parsed, err := parseFlags(subArgs, spec.flagKeys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux browser: %v\n", err)
		return 2
	}
	if _, ok := parsed.flags["json"]; ok {
		jsonOutput = true
	}
	for _, key := range spec.flagKeys {
		if val, ok := parsed.flags[key]; ok {
			paramKey := flagToParamKey(key)
			params[paramKey] = val
		}
	}
	if spec.allowPositionalURL {
		if _, ok := params["url"]; !ok && len(parsed.positional) > 0 {
			params["url"] = strings.Join(parsed.positional, " ")
		}
	}
	if spec.allowPositionalScript {
		if _, ok := params["script"]; !ok && len(parsed.positional) > 0 {
			params["script"] = strings.Join(parsed.positional, " ")
		}
	}
	if spec.allowPositionalKey {
		if _, ok := params["key"]; !ok && len(parsed.positional) > 0 {
			params["key"] = strings.Join(parsed.positional, " ")
		}
	}
	if spec.allowPositionalQuery {
		if _, ok := params["selector"]; !ok && len(parsed.positional) > 0 {
			params["selector"] = strings.Join(parsed.positional, " ")
		}
	}
	if spec.allowPositionalValue {
		applyBrowserValuePositionals(
			params,
			parsed.positional,
			browserSpecSupportsParam(spec, "value"),
			browserSpecSupportsParam(spec, "text"),
		)
	}
	if spec.useWorkspaceEnv {
		applyWorkspaceEnvFallback(params)
	}
	if spec.useSurfaceEnv {
		applySurfaceEnvFallback(params, parsed.flags["workspace"])
	}
	forceHeadless := false
	if workspaceArg := parsed.flags["workspace"]; workspaceArg != "" {
		route := routeForExplicitWorkspaceArg(workspaceArg, socketPath)
		socketPath = route.socketPath
		forceHeadless = route.forceHeadless
	}
	if forceHeadless {
		if code, handled := runHeadlessCLICommand("browser "+sub, spec.method, params, jsonOutput, errors.New("target workspace is detached")); handled {
			return code
		}
		fmt.Fprintf(os.Stderr, "cmux: browser %s cannot operate on detached workspace\n", sub)
		return 1
	}

	resp, err := socketRoundTripV2(socketPath, spec.method, params, refreshAddr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cmux: %v\n", err)
		return 1
	}
	if jsonOutput {
		fmt.Println(resp)
	} else {
		fmt.Println(defaultRelayOutput(resp))
	}
	return 0
}

func browserSubcommandHint() string {
	names := make([]string, 0, len(browserCommands))
	for name := range browserCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func browserSpecSupportsParam(spec browserCommandSpec, paramKey string) bool {
	for _, key := range spec.flagKeys {
		if flagToParamKey(key) == paramKey {
			return true
		}
	}
	return false
}

func applyBrowserValuePositionals(params map[string]any, positionals []string, allowValue bool, allowText bool) {
	if len(positionals) == 0 {
		return
	}
	if _, ok := params["selector"]; !ok {
		params["selector"] = positionals[0]
		positionals = positionals[1:]
	}
	joined := strings.Join(positionals, " ")
	if allowValue {
		if _, ok := params["value"]; !ok {
			if joined != "" {
				params["value"] = joined
			} else if text, ok := params["text"]; ok {
				params["value"] = text
			}
		}
	}
	if allowText {
		if _, ok := params["text"]; !ok {
			if joined != "" {
				params["text"] = joined
			} else if value, ok := params["value"]; ok {
				params["text"] = value
			}
		}
	}
}

func applyWorkspaceEnvFallback(params map[string]any) {
	if _, ok := params["workspace_id"]; ok {
		return
	}
	if envWs := os.Getenv("CMUX_WORKSPACE_ID"); envWs != "" {
		params["workspace_id"] = envWs
	}
}

type explicitWorkspaceRoute struct {
	socketPath    string
	forceHeadless bool
}

func routeForExplicitWorkspaceArg(workspaceArg string, fallback string) explicitWorkspaceRoute {
	workspaceID := headlessNormalizeID(workspaceArg)
	if workspaceID == "" || workspaceID == "current" {
		return explicitWorkspaceRoute{socketPath: fallback}
	}
	rootBase, err := headlessDaemonRoot()
	if err != nil {
		return explicitWorkspaceRoute{socketPath: fallback}
	}
	slot, err := findHeadlessSlotForWorkspace(rootBase, workspaceID)
	if err != nil {
		return explicitWorkspaceRoute{socketPath: fallback}
	}
	socketPath := headlessRelaySocketForSlot(slot)
	if socketPath == "" {
		return explicitWorkspaceRoute{socketPath: fallback, forceHeadless: true}
	}
	return explicitWorkspaceRoute{socketPath: socketPath}
}

func socketPathForExplicitWorkspaceArg(workspaceArg string, fallback string) string {
	return routeForExplicitWorkspaceArg(workspaceArg, fallback).socketPath
}

func applySurfaceEnvFallback(params map[string]any, workspaceArg string) {
	if _, ok := params["surface_id"]; ok {
		return
	}
	if !workspaceArgAllowsCallerSurfaceEnv(workspaceArg) {
		return
	}
	if envSf := os.Getenv("CMUX_SURFACE_ID"); envSf != "" {
		params["surface_id"] = envSf
	}
}

func workspaceArgAllowsCallerSurfaceEnv(workspaceArg string) bool {
	workspaceArg = headlessNormalizeID(workspaceArg)
	if workspaceArg == "" || workspaceArg == "current" {
		return true
	}
	envWorkspace := headlessNormalizeID(os.Getenv("CMUX_WORKSPACE_ID"))
	return envWorkspace != "" && strings.EqualFold(workspaceArg, envWorkspace)
}

func defaultRelayOutput(resp string) string {
	var result any
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		trimmed := strings.TrimSpace(resp)
		if trimmed == "" {
			return "OK"
		}
		return trimmed
	}

	if relayResultIsEmpty(result) {
		return "OK"
	}

	switch typed := result.(type) {
	case string:
		return typed
	default:
		encoded, err := json.MarshalIndent(typed, "", "  ")
		if err != nil {
			return "OK"
		}
		return string(encoded)
	}
}

func relayResultIsEmpty(result any) bool {
	switch typed := result.(type) {
	case nil:
		return true
	case map[string]any:
		return len(typed) == 0
	case []any:
		return len(typed) == 0
	case string:
		return typed == ""
	default:
		return false
	}
}

func unescapeCLIText(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var out strings.Builder
	out.Grow(len(text))
	escaped := false
	for _, r := range text {
		if !escaped {
			if r == '\\' {
				escaped = true
				continue
			}
			out.WriteRune(r)
			continue
		}
		switch r {
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case 'e':
			out.WriteByte(0x1b)
		case '\\':
			out.WriteByte('\\')
		default:
			out.WriteByte('\\')
			out.WriteRune(r)
		}
		escaped = false
	}
	if escaped {
		out.WriteByte('\\')
	}
	return out.String()
}

// flagToParamKey maps a CLI flag name to its JSON-RPC param key.
func flagToParamKey(key string) string {
	switch key {
	case "workspace":
		return "workspace_id"
	case "surface":
		return "surface_id"
	case "panel":
		return "panel_id"
	case "pane":
		return "pane_id"
	case "tab":
		return "surface_id"
	case "window":
		return "window_id"
	case "command":
		return "initial_command"
	case "name":
		return "title"
	case "cwd":
		return "cwd"
	case "working-directory":
		return "working_directory"
	case "max-depth":
		return "max_depth"
	case "timeout-ms":
		return "timeout_ms"
	case "url-contains":
		return "url_contains"
	case "load-state":
		return "load_state"
	default:
		return key
	}
}

// parsedFlags holds the results of flag parsing.
type parsedFlags struct {
	flags      map[string]string // --key value pairs
	positional []string          // non-flag arguments
}

// parseFlags extracts --key value pairs from args for the given allowed keys.
// Non-flag arguments are collected in positional.
func parseFlags(args []string, keys []string) (parsedFlags, error) {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}

	result := parsedFlags{flags: make(map[string]string)}
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			result.positional = append(result.positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(args[i], "--") {
			result.positional = append(result.positional, args[i])
			continue
		}
		key := strings.TrimPrefix(args[i], "--")
		if key == "json" {
			result.flags[key] = "true"
			continue
		}
		if !allowed[key] {
			return parsedFlags{}, fmt.Errorf("unknown flag --%s", key)
		}
		if i+1 >= len(args) {
			return parsedFlags{}, fmt.Errorf("flag --%s requires a value", key)
		}
		result.flags[key] = args[i+1]
		i++
	}
	return result, nil
}

func resolveCLIPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "~") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if trimmed == "~" {
				trimmed = home
			} else if strings.HasPrefix(trimmed, "~/") {
				trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
			}
		}
	}
	if filepath.IsAbs(trimmed) {
		if clean, err := filepath.Abs(trimmed); err == nil {
			return clean
		}
		return filepath.Clean(trimmed)
	}
	if abs, err := filepath.Abs(trimmed); err == nil {
		return abs
	}
	return filepath.Clean(trimmed)
}

// readSocketAddrFile reads the socket address from ~/.cmux/socket_addr as a fallback
// when CMUX_SOCKET_PATH is not set. Written by the cmux app after the relay establishes.
func readSocketAddrFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".cmux", "socket_addr"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readRelayAuthFile(socketPath string) *relayAuthState {
	if strings.Contains(socketPath, ":") && !strings.HasPrefix(socketPath, "/") {
		_, port, err := net.SplitHostPort(socketPath)
		if err != nil || port == "" {
			return nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		data, err := os.ReadFile(filepath.Join(home, ".cmux", "relay", port+".auth"))
		if err != nil {
			return nil
		}
		var state relayAuthState
		if err := json.Unmarshal(data, &state); err != nil {
			return nil
		}
		if state.RelayID == "" || state.RelayToken == "" {
			return nil
		}
		return &state
	}
	return nil
}

func currentRelayAuth(socketPath string) *relayAuthState {
	relayID := strings.TrimSpace(os.Getenv("CMUX_RELAY_ID"))
	relayToken := strings.TrimSpace(os.Getenv("CMUX_RELAY_TOKEN"))
	if relayID != "" && relayToken != "" {
		return &relayAuthState{RelayID: relayID, RelayToken: relayToken}
	}
	return readRelayAuthFile(socketPath)
}

// dialSocket connects to the cmux socket. If addr contains a colon and doesn't
// start with '/', it's treated as a TCP address (host:port); otherwise Unix socket.
// For TCP connections, refreshAddr is used only to recover from a stale socket_addr
// rewrite, not to poll for relay readiness.
func dialSocket(addr string, refreshAddr func() string) (net.Conn, error) {
	if strings.Contains(addr, ":") && !strings.HasPrefix(addr, "/") {
		conn, connectedAddr, err := dialTCP(addr)
		if err != nil && refreshAddr != nil && isConnectionRefused(err) {
			if refreshedAddr := strings.TrimSpace(refreshAddr()); refreshedAddr != "" && refreshedAddr != addr {
				addr = refreshedAddr
				conn, connectedAddr, err = dialTCP(addr)
			}
		}
		if err != nil {
			return nil, err
		}
		if auth := currentRelayAuth(connectedAddr); auth != nil {
			if err := authenticateRelayConn(conn, auth); err != nil {
				conn.Close()
				return nil, err
			}
		}
		return conn, nil
	}
	return net.Dial("unix", addr)
}

func dialTCP(addr string) (net.Conn, string, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, addr, err
	}
	setTCPNoDelay(conn)
	return conn, addr, nil
}

func isConnectionRefused(err error) bool {
	if opErr, ok := err.(*net.OpError); ok {
		return strings.Contains(opErr.Err.Error(), "connection refused")
	}
	return strings.Contains(err.Error(), "connection refused")
}

func authenticateRelayConn(conn net.Conn, auth *relayAuthState) error {
	reader := bufio.NewReader(conn)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var challenge struct {
		Protocol string `json:"protocol"`
		Version  int    `json:"version"`
		RelayID  string `json:"relay_id"`
		Nonce    string `json:"nonce"`
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read relay auth challenge: %w", err)
	}
	if err := json.Unmarshal([]byte(line), &challenge); err != nil {
		return fmt.Errorf("invalid relay auth challenge")
	}
	if challenge.Protocol != "cmux-relay-auth" || challenge.Version != 1 || challenge.RelayID != auth.RelayID || challenge.Nonce == "" {
		return fmt.Errorf("relay auth challenge mismatch")
	}

	tokenBytes, err := hex.DecodeString(auth.RelayToken)
	if err != nil {
		return fmt.Errorf("invalid relay auth token")
	}
	mac := computeRelayMAC(tokenBytes, auth.RelayID, challenge.Nonce, challenge.Version)
	payload, err := json.Marshal(map[string]any{
		"relay_id": auth.RelayID,
		"mac":      hex.EncodeToString(mac),
	})
	if err != nil {
		return fmt.Errorf("failed to encode relay auth response: %w", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("failed to send relay auth response: %w", err)
	}

	line, err = reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("failed to read relay auth result: %w", err)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		return fmt.Errorf("invalid relay auth result")
	}
	if !result.OK {
		return fmt.Errorf("relay auth rejected")
	}
	_ = conn.SetDeadline(time.Time{})
	return nil
}

func computeRelayMAC(token []byte, relayID, nonce string, version int) []byte {
	mac := hmac.New(sha256.New, token)
	_, _ = io.WriteString(mac, fmt.Sprintf("relay_id=%s\nnonce=%s\nversion=%d", relayID, nonce, version))
	return mac.Sum(nil)
}

// socketRoundTrip sends a raw text line and reads a raw text response (v1).
func socketRoundTrip(socketPath, command string, refreshAddr func() string) (string, error) {
	conn, err := dialSocket(socketPath, refreshAddr)
	if err != nil {
		return "", fmt.Errorf("failed to connect to %s: %w", socketPath, err)
	}
	defer conn.Close()

	if _, err := fmt.Fprintf(conn, "%s\n", command); err != nil {
		return "", fmt.Errorf("failed to send command: %w", err)
	}

	// V1 handlers may return multiple lines (e.g. list_windows). Read until
	// the stream goes idle briefly after seeing at least one newline.
	reader := bufio.NewReader(conn)
	var response strings.Builder
	sawNewline := false

	for {
		readTimeout := 15 * time.Second
		if sawNewline {
			readTimeout = 120 * time.Millisecond
		}
		_ = conn.SetReadDeadline(time.Now().Add(readTimeout))

		chunk, err := reader.ReadString('\n')
		if chunk != "" {
			response.WriteString(chunk)
			if strings.Contains(chunk, "\n") {
				sawNewline = true
			}
		}

		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				if sawNewline {
					break
				}
				return "", fmt.Errorf("failed to read response: timeout waiting for response")
			}
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("failed to read response: %w", err)
		}
	}

	return strings.TrimRight(response.String(), "\n"), nil
}

// socketRoundTripV2 sends a JSON-RPC request and returns the result JSON.
func socketRoundTripV2(socketPath, method string, params map[string]any, refreshAddr func() string) (string, error) {
	conn, err := dialSocket(socketPath, refreshAddr)
	if err != nil {
		return "", fmt.Errorf("failed to connect to %s: %w", socketPath, err)
	}
	defer conn.Close()

	id := randomHex(8)
	req := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		req["params"] = params
	} else {
		req["params"] = map[string]any{}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return "", fmt.Errorf("failed to send request: %w", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Parse the response to check for errors
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return strings.TrimRight(line, "\n"), nil
	}

	if ok, _ := resp["ok"].(bool); !ok {
		if errObj, _ := resp["error"].(map[string]any); errObj != nil {
			code, _ := errObj["code"].(string)
			msg, _ := errObj["message"].(string)
			return "", &socketServerError{code: code, message: msg}
		}
		return "", &socketServerError{code: "error", message: "server returned error response"}
	}

	// Return the result portion as JSON
	if result, ok := resp["result"]; ok {
		resultJSON, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("failed to marshal result: %w", err)
		}
		return string(resultJSON), nil
	}

	return "{}", nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func cliUsage() {
	fmt.Fprintln(os.Stderr, "Usage: cmux [--socket <path>] [--json] <command> [args...]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  ping                     Check connectivity")
	fmt.Fprintln(os.Stderr, "  capabilities              List server capabilities")
	fmt.Fprintln(os.Stderr, "  list-workspaces           List all workspaces")
	fmt.Fprintln(os.Stderr, "  new-window                Create a new window")
	fmt.Fprintln(os.Stderr, "  new-workspace             Create a new workspace")
	fmt.Fprintln(os.Stderr, "  ssh <host>                Create an SSH workspace; use --detached to force same-host detached creation")
	fmt.Fprintln(os.Stderr, "  new-surface               Create a new surface")
	fmt.Fprintln(os.Stderr, "  new-split                 Split an existing surface")
	fmt.Fprintln(os.Stderr, "  close-surface             Close a surface")
	fmt.Fprintln(os.Stderr, "  close-workspace           Close a workspace")
	fmt.Fprintln(os.Stderr, "  select-workspace          Select a workspace")
	fmt.Fprintln(os.Stderr, "  focus-surface             Focus a surface")
	fmt.Fprintln(os.Stderr, "  send                      Send text to a surface")
	fmt.Fprintln(os.Stderr, "  send-key                  Send a key to a surface")
	fmt.Fprintln(os.Stderr, "  set-status/list-status    Manage workspace status entries; list-status supports --json")
	fmt.Fprintln(os.Stderr, "  notify                    Create a notification")
	fmt.Fprintln(os.Stderr, "  browser <sub>             Browser commands through the local cmux browser relay")
	fmt.Fprintln(os.Stderr, "  claude-teams [args...]     Launch Claude Code in teammate mode")
	fmt.Fprintln(os.Stderr, "  omo [args...]              Launch OpenCode with cmux integration")
	fmt.Fprintln(os.Stderr, "  omx [args...]              Launch Oh My Codex with cmux integration")
	fmt.Fprintln(os.Stderr, "  omc [args...]              Launch Oh My Claude Code with cmux integration")
	fmt.Fprintln(os.Stderr, "  rpc <method> [json-params] Send arbitrary JSON-RPC")
}
