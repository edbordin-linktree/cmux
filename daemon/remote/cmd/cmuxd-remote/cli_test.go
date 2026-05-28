package main

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = original
	}()

	fn()

	if err := writer.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close stdout reader: %v", err)
	}
	return string(output)
}

func makeShortUnixSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cmuxd-")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "cmux.sock")
}

// startMockSocket creates a Unix socket that accepts one connection,
// reads a line, and responds with the given canned response.
func startMockSocket(t *testing.T, response string) string {
	t.Helper()
	sockPath := makeShortUnixSocketPath(t)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			_ = n // consume request
			conn.Write([]byte(response + "\n"))
			conn.Close()
		}
	}()

	return sockPath
}

// startMockV2Socket creates a Unix socket that echoes the received request's method
// back as a successful JSON-RPC response with the method name in the result.
func startMockV2Socket(t *testing.T) string {
	t.Helper()
	sockPath := makeShortUnixSocketPath(t)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			if n > 0 {
				var req map[string]any
				if err := json.Unmarshal(buf[:n], &req); err == nil {
					resp := map[string]any{
						"id":     req["id"],
						"ok":     true,
						"result": map[string]any{"method": req["method"], "params": req["params"]},
					}
					payload, _ := json.Marshal(resp)
					conn.Write(append(payload, '\n'))
				} else {
					conn.Write([]byte(`{"ok":false,"error":{"code":"parse","message":"bad json"}}` + "\n"))
				}
			}
			conn.Close()
		}
	}()

	return sockPath
}

func startMockV2SocketWithRequestCapture(t *testing.T) (string, <-chan map[string]any) {
	t.Helper()
	sockPath := makeShortUnixSocketPath(t)
	requests := make(chan map[string]any, 8)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				if n == 0 {
					return
				}
				var req map[string]any
				if err := json.Unmarshal(buf[:n], &req); err != nil {
					_, _ = conn.Write([]byte(`{"ok":false,"error":{"code":"parse","message":"bad json"}}` + "\n"))
					return
				}
				requests <- req
				resp := map[string]any{
					"id":     req["id"],
					"ok":     true,
					"result": map[string]any{"method": req["method"], "params": req["params"]},
				}
				payload, _ := json.Marshal(resp)
				_, _ = conn.Write(append(payload, '\n'))
			}(conn)
		}
	}()

	return sockPath, requests
}

func startMockV2ErrorSocket(t *testing.T, code string, message string) string {
	t.Helper()
	sockPath := makeShortUnixSocketPath(t)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				_, _ = conn.Read(buf)
				resp := map[string]any{
					"ok": false,
					"error": map[string]any{
						"code":    code,
						"message": message,
					},
				}
				payload, _ := json.Marshal(resp)
				_, _ = conn.Write(append(payload, '\n'))
			}(conn)
		}
	}()

	return sockPath
}

func startMockV2TCPSocketWithResult(t *testing.T, result any) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on TCP: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				if n == 0 {
					return
				}
				var req map[string]any
				if err := json.Unmarshal(buf[:n], &req); err != nil {
					_, _ = conn.Write([]byte(`{"ok":false,"error":{"code":"parse","message":"bad json"}}` + "\n"))
					return
				}
				resp := map[string]any{
					"id":     req["id"],
					"ok":     true,
					"result": result,
				}
				payload, _ := json.Marshal(resp)
				_, _ = conn.Write(append(payload, '\n'))
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// startMockTCPSocket creates a TCP listener that responds with a canned response.
func startMockTCPSocket(t *testing.T, response string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on TCP: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 4096)
			n, _ := conn.Read(buf)
			_ = n
			conn.Write([]byte(response + "\n"))
			conn.Close()
		}
	}()

	return ln.Addr().String()
}

func startMockAuthenticatedTCPSocket(t *testing.T, relayID, relayToken, response string) string {
	t.Helper()
	relayTokenBytes := mustHex(t, relayToken)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on TCP: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				nonce := "testnonce"
				challenge, _ := json.Marshal(map[string]any{
					"protocol": "cmux-relay-auth",
					"version":  1,
					"relay_id": relayID,
					"nonce":    nonce,
				})
				_, _ = conn.Write(append(challenge, '\n'))

				reader := bufio.NewReader(conn)
				line, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				var authResp map[string]any
				if err := json.Unmarshal([]byte(line), &authResp); err != nil {
					_, _ = conn.Write([]byte(`{"ok":false}` + "\n"))
					return
				}
				macHex, _ := authResp["mac"].(string)
				receivedMAC, err := hex.DecodeString(macHex)
				if err != nil {
					_, _ = conn.Write([]byte(`{"ok":false}` + "\n"))
					return
				}

				h := hmac.New(sha256.New, relayTokenBytes)
				_, _ = io.WriteString(h, fmt.Sprintf("relay_id=%s\nnonce=%s\nversion=%d", relayID, nonce, 1))
				expectedMAC := h.Sum(nil)
				if !hmac.Equal(receivedMAC, expectedMAC) {
					_, _ = conn.Write([]byte(`{"ok":false}` + "\n"))
					return
				}

				_, _ = conn.Write([]byte(`{"ok":true}` + "\n"))
				buf := make([]byte, 4096)
				n, _ := conn.Read(buf)
				_, _ = conn.Write([]byte(response))
				if n > 0 && !strings.HasSuffix(response, "\n") {
					_, _ = conn.Write([]byte("\n"))
				}
			}(conn)
		}
	}()

	return ln.Addr().String()
}

func mustHex(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}
	return data
}

func TestDialSocketRefreshesToUpdatedTCPAddressWithoutPolling(t *testing.T) {
	staleListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen stale: %v", err)
	}
	staleAddr := staleListener.Addr().String()
	staleListener.Close()

	readyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ready: %v", err)
	}
	defer readyListener.Close()

	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		conn, acceptErr := readyListener.Accept()
		if acceptErr != nil {
			return
		}
		conn.Close()
	}()

	refreshCalls := 0
	start := time.Now()
	conn, err := dialSocket(staleAddr, func() string {
		refreshCalls++
		return readyListener.Addr().String()
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("dialSocket should refresh to updated address, got: %v", err)
	}
	conn.Close()
	<-accepted
	if refreshCalls != 1 {
		t.Fatalf("refreshAddr should be called once, got %d", refreshCalls)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("dialSocket should fail over without polling, took %v", elapsed)
	}
}

func TestDialSocketFailsFastWhenTCPAddressStaysStale(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	refreshCalls := 0
	start := time.Now()
	_, err = dialSocket(addr, func() string {
		refreshCalls++
		return addr
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("dialSocket should fail when the relay address stays stale")
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshAddr should be called once on stale TCP failure, got %d", refreshCalls)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("dialSocket should fail fast without polling, took %v", elapsed)
	}
}

func TestCLIPingV1(t *testing.T) {
	sockPath := startMockSocket(t, "pong")
	code := runCLI([]string{"--socket", sockPath, "ping"})
	if code != 0 {
		t.Fatalf("ping should return 0, got %d", code)
	}
}

func TestCLIPingV1OverTCP(t *testing.T) {
	addr := startMockTCPSocket(t, "pong")
	code := runCLI([]string{"--socket", addr, "ping"})
	if code != 0 {
		t.Fatalf("ping over TCP should return 0, got %d", code)
	}
}

func TestCLIPingV1OverAuthenticatedTCPWithEnv(t *testing.T) {
	relayID := "relay-1"
	relayToken := strings.Repeat("a1", 32)
	addr := startMockAuthenticatedTCPSocket(t, relayID, relayToken, "pong")
	t.Setenv("CMUX_RELAY_ID", relayID)
	t.Setenv("CMUX_RELAY_TOKEN", relayToken)

	code := runCLI([]string{"--socket", addr, "ping"})
	if code != 0 {
		t.Fatalf("ping over authenticated TCP should return 0, got %d", code)
	}
}

func TestCLIPingV1OverAuthenticatedTCPWithRelayFile(t *testing.T) {
	relayID := "relay-2"
	relayToken := strings.Repeat("b2", 32)
	addr := startMockAuthenticatedTCPSocket(t, relayID, relayToken, "pong")
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CMUX_RELAY_ID", "")
	t.Setenv("CMUX_RELAY_TOKEN", "")
	relayDir := filepath.Join(home, ".cmux", "relay")
	if err := os.MkdirAll(relayDir, 0o700); err != nil {
		t.Fatalf("mkdir relay dir: %v", err)
	}
	authPayload, _ := json.Marshal(relayAuthState{RelayID: relayID, RelayToken: relayToken})
	if err := os.WriteFile(filepath.Join(relayDir, port+".auth"), authPayload, 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	code := runCLI([]string{"--socket", addr, "ping"})
	if code != 0 {
		t.Fatalf("ping over authenticated TCP file relay should return 0, got %d", code)
	}
}

func TestDialSocketDetection(t *testing.T) {
	// Unix socket paths should attempt Unix dial
	for _, path := range []string{"/tmp/cmux-nonexistent-test-99999.sock", "/var/run/cmux-nonexistent.sock"} {
		conn, err := dialSocket(path, nil)
		if conn != nil {
			conn.Close()
		}
		// We expect a connection error (not found), not a panic
		if err == nil {
			t.Fatalf("dialSocket(%q) should fail for non-existent path", path)
		}
	}

	// TCP addresses should attempt TCP dial
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			conn.Close()
		}
	}()

	conn, err := dialSocket(ln.Addr().String(), nil)
	if err != nil {
		t.Fatalf("dialSocket(%q) should succeed for TCP: %v", ln.Addr().String(), err)
	}
	conn.Close()
}

func TestCLINewWindowV1(t *testing.T) {
	sockPath := startMockSocket(t, "OK window_id=abc123")
	code := runCLI([]string{"--socket", sockPath, "new-window"})
	if code != 0 {
		t.Fatalf("new-window should return 0, got %d", code)
	}
}

func TestSocketRoundTripReadsFullMultilineV1Response(t *testing.T) {
	addr := startMockTCPSocket(t, "window:alpha\nwindow:beta\nwindow:gamma")
	resp, err := socketRoundTrip(addr, "list_windows", nil)
	if err != nil {
		t.Fatalf("socketRoundTrip should succeed, got error: %v", err)
	}
	want := "window:alpha\nwindow:beta\nwindow:gamma"
	if resp != want {
		t.Fatalf("socketRoundTrip truncated v1 response: got %q want %q", resp, want)
	}
}

func TestCLICloseWindowV1(t *testing.T) {
	// Verify that the flag value is appended to the v1 command
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cmux.sock")

	receivedCh := make(chan string, 1)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		receivedCh <- strings.TrimSpace(string(buf[:n]))
		conn.Write([]byte("OK\n"))
		conn.Close()
	}()

	code := runCLI([]string{"--socket", sockPath, "close-window", "--window", "win-42"})
	if code != 0 {
		t.Fatalf("close-window should return 0, got %d", code)
	}
	select {
	case received := <-receivedCh:
		if received != "close_window win-42" {
			t.Fatalf("expected 'close_window win-42', got %q", received)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close-window payload")
	}
}

func TestCLIListWorkspacesV2(t *testing.T) {
	sockPath := startMockV2Socket(t)
	code := runCLI([]string{"--socket", sockPath, "--json", "list-workspaces"})
	if code != 0 {
		t.Fatalf("list-workspaces should return 0, got %d", code)
	}
}

func TestCLIListWorkspacesV2DefaultOutputShowsResult(t *testing.T) {
	sockPath := startMockV2TCPSocketWithResult(t, map[string]any{"method": "workspace.list", "params": map[string]any{}})
	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "list-workspaces"})
		if code != 0 {
			t.Fatalf("list-workspaces should return 0, got %d", code)
		}
	})
	if !strings.Contains(output, "\"method\": \"workspace.list\"") {
		t.Fatalf("expected default output to include result payload, got %q", output)
	}
}

func TestCLINotifyDefaultOutputPrintsOKForEmptyResult(t *testing.T) {
	sockPath := startMockV2TCPSocketWithResult(t, map[string]any{})
	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "notify", "--body", "hi"})
		if code != 0 {
			t.Fatalf("notify should return 0, got %d", code)
		}
	})
	if strings.TrimSpace(output) != "OK" {
		t.Fatalf("expected empty-result command to print OK, got %q", output)
	}
}

func TestCLIRPCPassthrough(t *testing.T) {
	sockPath := startMockV2Socket(t)
	code := runCLI([]string{"--socket", sockPath, "rpc", "system.capabilities"})
	if code != 0 {
		t.Fatalf("rpc should return 0, got %d", code)
	}
}

func TestCLIRPCWithParams(t *testing.T) {
	sockPath := startMockV2Socket(t)
	code := runCLI([]string{"--socket", sockPath, "rpc", "workspace.create", `{"title":"test"}`})
	if code != 0 {
		t.Fatalf("rpc with params should return 0, got %d", code)
	}
}

func TestCLIUnknownCommand(t *testing.T) {
	code := runCLI([]string{"--socket", "/dev/null", "does-not-exist"})
	if code != 2 {
		t.Fatalf("unknown command should return 2, got %d", code)
	}
}

func TestCLINoSocket(t *testing.T) {
	// Without CMUX_SOCKET_PATH set, should fail
	os.Unsetenv("CMUX_SOCKET_PATH")
	code := runCLI([]string{"ping"})
	if code != 1 {
		t.Fatalf("missing socket should return 1, got %d", code)
	}
}

func TestCLISocketEnvVar(t *testing.T) {
	sockPath := startMockSocket(t, "pong")
	os.Setenv("CMUX_SOCKET_PATH", sockPath)
	defer os.Unsetenv("CMUX_SOCKET_PATH")

	code := runCLI([]string{"ping"})
	if code != 0 {
		t.Fatalf("ping with env socket should return 0, got %d", code)
	}
}

func TestCLIV2FlagMapping(t *testing.T) {
	// Verify that --workspace gets mapped to workspace_id in params
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cmux.sock")

	receivedParamsCh := make(chan map[string]any, 1)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		var req map[string]any
		json.Unmarshal(buf[:n], &req)
		receivedParams, _ := req["params"].(map[string]any)
		receivedParamsCh <- receivedParams
		resp := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
		payload, _ := json.Marshal(resp)
		conn.Write(append(payload, '\n'))
		conn.Close()
	}()

	code := runCLI([]string{"--socket", sockPath, "--json", "close-workspace", "--workspace", "ws-abc"})
	if code != 0 {
		t.Fatalf("close-workspace should return 0, got %d", code)
	}
	select {
	case receivedParams := <-receivedParamsCh:
		if receivedParams["workspace_id"] != "ws-abc" {
			t.Fatalf("expected workspace_id=ws-abc, got %v", receivedParams)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close-workspace payload")
	}
}

func TestCLINewWorkspaceAcceptsCWDAndTrailingJSON(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	sockPath, requests := startMockV2SocketWithRequestCapture(t)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "new-workspace", "--cwd", ".", "--name", "Task Workspace", "--json"})
		if code != 0 {
			t.Fatalf("new-workspace should return 0, got %d", code)
		}
	})
	if !strings.Contains(output, `"method":"workspace.create"`) {
		t.Fatalf("expected JSON relay output, got %q", output)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "workspace.create" {
			t.Fatalf("expected workspace.create, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["cwd"]; got != cwd {
			t.Fatalf("expected resolved cwd %q, got %v", cwd, got)
		}
		if got := params["title"]; got != "Task Workspace" {
			t.Fatalf("expected title, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new-workspace payload")
	}
}

func TestCLINewSurfaceAcceptsTrailingJSON(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "new-surface", "--type", "browser", "--url", "https://example.com", "--json"})
		if code != 0 {
			t.Fatalf("new-surface should return 0, got %d", code)
		}
	})
	if !strings.Contains(output, `"method":"surface.create"`) {
		t.Fatalf("expected JSON relay output, got %q", output)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "surface.create" {
			t.Fatalf("expected surface.create, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new-surface payload")
	}
}

func TestCLISendPositionalMapsToTextAndUnescapes(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "cmux-cli-send-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "cmux.sock")

	receivedParamsCh := make(chan map[string]any, 1)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		var req map[string]any
		_ = json.Unmarshal(buf[:n], &req)
		receivedParams, _ := req["params"].(map[string]any)
		receivedParamsCh <- receivedParams
		resp := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
		payload, _ := json.Marshal(resp)
		_, _ = conn.Write(append(payload, '\n'))
	}()

	code := runCLI([]string{"--socket", sockPath, "--json", "send", "--surface", "surface-a", "--", "echo hello\\n"})
	if code != 0 {
		t.Fatalf("send should return 0, got %d", code)
	}
	select {
	case receivedParams := <-receivedParamsCh:
		if receivedParams["surface_id"] != "surface-a" {
			t.Fatalf("expected surface_id=surface-a, got %v", receivedParams)
		}
		if receivedParams["text"] != "echo hello\n" {
			t.Fatalf("expected unescaped text, got %#v", receivedParams["text"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for send payload")
	}
}

func TestBusyboxArgv0Detection(t *testing.T) {
	// Verify that when argv[0] base is "cmux", we enter CLI mode
	base := filepath.Base("cmux")
	if base != "cmux" {
		t.Fatalf("expected base 'cmux', got %q", base)
	}
	base2 := filepath.Base("/home/user/.cmux/bin/cmux")
	if base2 != "cmux" {
		t.Fatalf("expected base 'cmux', got %q", base2)
	}
	base3 := filepath.Base("cmuxd-remote")
	if base3 == "cmux" {
		t.Fatalf("cmuxd-remote should not match cmux")
	}
}

func TestCLIBrowserSubcommand(t *testing.T) {
	sockPath := startMockV2Socket(t)
	code := runCLI([]string{"--socket", sockPath, "--json", "browser", "open", "--url", "https://example.com"})
	if code != 0 {
		t.Fatalf("browser open should return 0, got %d", code)
	}
}

func TestCLINewPaneDefaultsDirectionAndForwardsExtraFlags(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"new-pane",
		"--workspace", "ws-1",
		"--type", "browser",
		"--url", "https://example.com",
	})
	if code != 0 {
		t.Fatalf("new-pane should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "pane.create" {
			t.Fatalf("expected pane.create, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "ws-1" {
			t.Fatalf("expected workspace_id ws-1, got %v", got)
		}
		if got := params["direction"]; got != "right" {
			t.Fatalf("expected default direction right, got %v", got)
		}
		if got := params["type"]; got != "browser" {
			t.Fatalf("expected type browser, got %v", got)
		}
		if got := params["url"]; got != "https://example.com" {
			t.Fatalf("expected url to be forwarded, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new-pane request")
	}
}

func TestCLINewPaneExplicitOtherWorkspaceDoesNotForwardCallerSurfaceEnv(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_WORKSPACE_ID", "caller-ws")
	t.Setenv("CMUX_SURFACE_ID", "caller-surface")

	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"new-pane",
		"--workspace", "target-ws",
		"--type", "browser",
		"--url", "https://example.com",
	})
	if code != 0 {
		t.Fatalf("new-pane should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "target-ws" {
			t.Fatalf("workspace_id = %v, want target-ws", got)
		}
		if got := params["surface_id"]; got != nil {
			t.Fatalf("surface_id should not default from caller env for other workspace, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new-pane request")
	}
}

func TestCLINewPaneExplicitCurrentWorkspaceForwardsCallerSurfaceEnv(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_WORKSPACE_ID", "caller-ws")
	t.Setenv("CMUX_SURFACE_ID", "caller-surface")

	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"new-pane",
		"--workspace", "current",
		"--type", "browser",
		"--url", "https://example.com",
	})
	if code != 0 {
		t.Fatalf("new-pane should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "caller-surface" {
			t.Fatalf("surface_id = %v, want caller-surface", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for new-pane request")
	}
}

func TestCLIListPanelsUsesSurfaceList(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	code := runCLI([]string{"--socket", sockPath, "--json", "list-panels", "--workspace", "ws-1"})
	if code != 0 {
		t.Fatalf("list-panels should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "surface.list" {
			t.Fatalf("expected surface.list, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "ws-1" {
			t.Fatalf("expected workspace_id ws-1, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for list-panels request")
	}
}

func TestCLIFocusPanelUsesSurfaceFocus(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	code := runCLI([]string{"--socket", sockPath, "--json", "focus-panel", "--workspace", "ws-1", "--panel", "surface-1"})
	if code != 0 {
		t.Fatalf("focus-panel should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "surface.focus" {
			t.Fatalf("expected surface.focus, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "ws-1" {
			t.Fatalf("expected workspace_id ws-1, got %v", got)
		}
		if got := params["surface_id"]; got != "surface-1" {
			t.Fatalf("expected surface_id surface-1, got %v", got)
		}
		if _, ok := params["panel_id"]; ok {
			t.Fatalf("did not expect panel_id in params: %v", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for focus-panel request")
	}
}

func TestCLIFocusSurfaceUsesSurfaceFocus(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	code := runCLI([]string{"--socket", sockPath, "--json", "focus-surface", "--workspace", "ws-1", "surface-1"})
	if code != 0 {
		t.Fatalf("focus-surface should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "surface.focus" {
			t.Fatalf("expected surface.focus, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "ws-1" {
			t.Fatalf("expected workspace_id ws-1, got %v", got)
		}
		if got := params["surface_id"]; got != "surface-1" {
			t.Fatalf("expected surface_id surface-1, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for focus-surface request")
	}
}

func TestCLIBrowserOpenUsesOpenSplitAndWorkspaceEnv(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_WORKSPACE_ID", "env-ws")
	code := runCLI([]string{"--socket", sockPath, "--json", "browser", "open", "https://example.com"})
	if code != 0 {
		t.Fatalf("browser open should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.open_split" {
			t.Fatalf("expected browser.open_split, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != "env-ws" {
			t.Fatalf("expected workspace_id env-ws, got %v", got)
		}
		if got := params["url"]; got != "https://example.com" {
			t.Fatalf("expected positional url to be forwarded, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser open request")
	}
}

func TestCLIBrowserGetURLUsesCurrentMethodAndSurfaceEnv(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{"--socket", sockPath, "--json", "browser", "get-url"})
	if code != 0 {
		t.Fatalf("browser get-url should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.url.get" {
			t.Fatalf("expected browser.url.get, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser get-url request")
	}
}

func TestCLIBrowserSnapshotUsesSurfaceEnvAndForwardsOptions(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"browser", "snapshot",
		"--selector", "main",
		"--max-depth", "4",
	})
	if code != 0 {
		t.Fatalf("browser snapshot should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.snapshot" {
			t.Fatalf("expected browser.snapshot, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
		if got := params["selector"]; got != "main" {
			t.Fatalf("expected selector main, got %v", got)
		}
		if got := params["max_depth"]; got != "4" {
			t.Fatalf("expected max_depth 4, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser snapshot request")
	}
}

func TestCLIBrowserWaitUsesSurfaceEnvAndForwardsOptions(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"browser", "wait",
		"--timeout-ms", "1500",
		"--url-contains", "/cloud",
		"--load-state", "networkidle",
	})
	if code != 0 {
		t.Fatalf("browser wait should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.wait" {
			t.Fatalf("expected browser.wait, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
		if got := params["timeout_ms"]; got != "1500" {
			t.Fatalf("expected timeout_ms 1500, got %v", got)
		}
		if got := params["url_contains"]; got != "/cloud" {
			t.Fatalf("expected url_contains /cloud, got %v", got)
		}
		if got := params["load_state"]; got != "networkidle" {
			t.Fatalf("expected load_state networkidle, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser wait request")
	}
}

func TestCLIBrowserAutomationPositionals(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"browser", "fill",
		"input[name=email]",
		"dev@example.com",
	})
	if code != 0 {
		t.Fatalf("browser fill should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.fill" {
			t.Fatalf("expected browser.fill, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
		if got := params["selector"]; got != "input[name=email]" {
			t.Fatalf("expected selector, got %v", got)
		}
		if got := params["text"]; got != "dev@example.com" {
			t.Fatalf("expected text, got %v", got)
		}
		if _, ok := params["value"]; ok {
			t.Fatalf("browser.fill should not send value param: %#v", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser fill request")
	}
}

func TestCLIBrowserSelectDoesNotMirrorValueToText(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"browser", "select",
		"select[name=plan]",
		"free",
	})
	if code != 0 {
		t.Fatalf("browser select should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.select" {
			t.Fatalf("expected browser.select, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
		if got := params["selector"]; got != "select[name=plan]" {
			t.Fatalf("expected selector, got %v", got)
		}
		if got := params["value"]; got != "free" {
			t.Fatalf("expected value, got %v", got)
		}
		if _, ok := params["text"]; ok {
			t.Fatalf("browser.select should not send text param: %#v", params)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser select request")
	}
}

func TestCLIBrowserEvalUsesPositionalScript(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	t.Setenv("CMUX_SURFACE_ID", "env-sf")
	code := runCLI([]string{
		"--socket", sockPath, "--json",
		"browser", "eval",
		"document.title",
	})
	if code != 0 {
		t.Fatalf("browser eval should return 0, got %d", code)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "browser.eval" {
			t.Fatalf("expected browser.eval, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["surface_id"]; got != "env-sf" {
			t.Fatalf("expected surface_id env-sf, got %v", got)
		}
		if got := params["script"]; got != "document.title" {
			t.Fatalf("expected script, got %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for browser eval request")
	}
}

func TestCLINoArgs(t *testing.T) {
	code := runCLI([]string{})
	if code != 2 {
		t.Fatalf("no args should return 2, got %d", code)
	}
}

func TestParseFlagsRejectsMissingFlagValue(t *testing.T) {
	_, err := parseFlags(
		[]string{"--timeout-ms"},
		[]string{"timeout-ms", "url-contains"},
	)
	if err == nil {
		t.Fatal("parseFlags should reject missing flag values")
	}
	if got, want := err.Error(), "flag --timeout-ms requires a value"; got != want {
		t.Fatalf("unexpected parseFlags error %q, want %q", got, want)
	}
}

func TestParseFlagsAllowsSingleDashFlagValue(t *testing.T) {
	parsed, err := parseFlags(
		[]string{"--text", "-n", "--command", "-lc echo hi"},
		[]string{"text", "command"},
	)
	if err != nil {
		t.Fatalf("parseFlags should allow single-dash values: %v", err)
	}
	if got := parsed.flags["text"]; got != "-n" {
		t.Fatalf("expected text -n, got %q", got)
	}
	if got := parsed.flags["command"]; got != "-lc echo hi" {
		t.Fatalf("expected command -lc echo hi, got %q", got)
	}
}

func TestParseFlagsAllowsDoubleDashFlagValue(t *testing.T) {
	parsed, err := parseFlags(
		[]string{"--text", "--some-content", "--body", "--flag-like text"},
		[]string{"text", "body"},
	)
	if err != nil {
		t.Fatalf("parseFlags should allow double-dash values: %v", err)
	}
	if got := parsed.flags["text"]; got != "--some-content" {
		t.Fatalf("expected text --some-content, got %q", got)
	}
	if got := parsed.flags["body"]; got != "--flag-like text" {
		t.Fatalf("expected body --flag-like text, got %q", got)
	}
}

func TestCLIHelpFlag(t *testing.T) {
	code := runCLI([]string{"--help"})
	if code != 0 {
		t.Fatalf("--help should return 0, got %d", code)
	}
}

func TestCLIHelpCommand(t *testing.T) {
	code := runCLI([]string{"help"})
	if code != 0 {
		t.Fatalf("help should return 0, got %d", code)
	}
}

func TestFlagToParamKey(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"workspace", "workspace_id"},
		{"surface", "surface_id"},
		{"panel", "panel_id"},
		{"pane", "pane_id"},
		{"window", "window_id"},
		{"command", "initial_command"},
		{"name", "title"},
		{"cwd", "cwd"},
		{"working-directory", "working_directory"},
		{"title", "title"},
		{"url", "url"},
		{"direction", "direction"},
	}
	for _, tc := range tests {
		got := flagToParamKey(tc.input)
		if got != tc.expected {
			t.Errorf("flagToParamKey(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

func TestParseFlags(t *testing.T) {
	args := []string{"positional-cmd", "--workspace", "ws-1", "--surface", "sf-2", "--unknown", "val"}
	_, err := parseFlags(args, []string{"workspace", "surface"})
	if err == nil {
		t.Fatal("parseFlags should reject unknown flags")
	}
}

func TestParseFlagsCollectsKnownFlagsAndPositionalArgs(t *testing.T) {
	args := []string{"positional-cmd", "--workspace", "ws-1", "--surface", "sf-2"}
	result, err := parseFlags(args, []string{"workspace", "surface"})
	if err != nil {
		t.Fatalf("parseFlags should succeed for known flags: %v", err)
	}
	if result.flags["workspace"] != "ws-1" {
		t.Errorf("expected workspace=ws-1, got %q", result.flags["workspace"])
	}
	if result.flags["surface"] != "sf-2" {
		t.Errorf("expected surface=sf-2, got %q", result.flags["surface"])
	}
	if len(result.positional) == 0 || result.positional[0] != "positional-cmd" {
		t.Errorf("expected first positional=positional-cmd, got %v", result.positional)
	}
}

func TestCLIEnvVarDefaults(t *testing.T) {
	// Test that CMUX_WORKSPACE_ID and CMUX_SURFACE_ID are used as defaults
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "cmux.sock")

	receivedParamsCh := make(chan map[string]any, 1)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		var req map[string]any
		json.Unmarshal(buf[:n], &req)
		receivedParams, _ := req["params"].(map[string]any)
		receivedParamsCh <- receivedParams
		resp := map[string]any{"id": req["id"], "ok": true, "result": map[string]any{}}
		payload, _ := json.Marshal(resp)
		conn.Write(append(payload, '\n'))
		conn.Close()
	}()

	os.Setenv("CMUX_WORKSPACE_ID", "env-ws-id")
	os.Setenv("CMUX_SURFACE_ID", "env-sf-id")
	defer os.Unsetenv("CMUX_WORKSPACE_ID")
	defer os.Unsetenv("CMUX_SURFACE_ID")

	code := runCLI([]string{"--socket", sockPath, "--json", "close-surface"})
	if code != 0 {
		t.Fatalf("close-surface should return 0, got %d", code)
	}
	select {
	case receivedParams := <-receivedParamsCh:
		if receivedParams["workspace_id"] != "env-ws-id" {
			t.Errorf("expected workspace_id from env, got %v", receivedParams["workspace_id"])
		}
		if receivedParams["surface_id"] != "env-sf-id" {
			t.Errorf("expected surface_id from env, got %v", receivedParams["surface_id"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for close-surface payload")
	}
}

func TestCLIHeadlessMetadataFallbackMutatesSnapshot(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "metadata", "set", "--workspace", "current", "craft:task-id", "task-2"})
		if code != 0 {
			t.Fatalf("metadata set returned %d", code)
		}
	})
	if !strings.Contains(output, `"snapshot_sha256"`) {
		t.Fatalf("metadata set output missing snapshot hash: %s", output)
	}

	body := readHeadlessCLITestBody(t, root, slot)
	metadata, _ := body["metadataEntries"].(map[string]any)
	if got := metadata["craft:task-id"]; got != "task-2" {
		t.Fatalf("metadata value = %v, want task-2", got)
	}
}

func TestCLIHeadlessNoSocketUsesRemoteContext(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CMUX_SOCKET_PATH", "")
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--json", "metadata", "set", "--workspace", "current", "craft:task-id", "task-no-socket"})
		if code != 0 {
			t.Fatalf("metadata set returned %d", code)
		}
	})
	if !strings.Contains(output, `"snapshot_sha256"`) {
		t.Fatalf("metadata set output missing snapshot hash: %s", output)
	}
	body := readHeadlessCLITestBody(t, root, slot)
	metadata := headlessMetadataMap(body)
	if got := metadata["craft:task-id"]; got != "task-no-socket" {
		t.Fatalf("metadata value = %v, want task-no-socket", got)
	}
}

func TestCLINoSocketWithoutRemoteContextStillFails(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CMUX_SOCKET_PATH", "")
	t.Setenv("CMUX_WORKSPACE_ID", "")
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", "")

	code := runCLI([]string{"--json", "metadata", "list"})
	if code == 0 {
		t.Fatal("metadata list without socket or remote context should fail")
	}
}

func TestCLIReachableSocketServerErrorDoesNotHeadlessFallback(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)
	sockPath := startMockV2ErrorSocket(t, "invalid_state", "swift handled but rejected")

	code := runCLI([]string{"--socket", sockPath, "--json", "metadata", "set", "--workspace", "current", "craft:task-id", "should-not-write"})
	if code == 0 {
		t.Fatal("metadata set should fail on reachable Swift server error")
	}
	body := readHeadlessCLITestBody(t, root, slot)
	metadata := headlessMetadataMap(body)
	if got := metadata["craft:task-id"]; got != "task-1" {
		t.Fatalf("metadata changed via fallback to %q, want original task-1", got)
	}
}

func TestCLIHeadlessWorkspaceLookupWithoutIncludeDetachedReturnsEmpty(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "workspace", "lookup", "--metadata", "craft:task-id=task-1"})
		if code != 0 {
			t.Fatalf("workspace lookup returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result["count"] != float64(0) {
		t.Fatalf("lookup count = %v, want 0", result["count"])
	}
	if result["detached_searched"] != false {
		t.Fatalf("detached_searched = %v, want false", result["detached_searched"])
	}
}

func TestCLIHeadlessWorkspaceLookupIncludeDetachedScansAllSnapshots(t *testing.T) {
	root, callerWorkspaceID, callerSlot := writeHeadlessCLITestSnapshot(t)
	targetWorkspaceID := "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	targetWorkspaceIDLower := strings.ToLower(targetWorkspaceID)
	targetSlot := "slot-b"
	targetSurfaceID := "22222222-2222-4222-8222-222222222222"
	writeHeadlessCLITestSnapshotAtWithMetadata(t, root, targetWorkspaceID, targetSlot, "Target Workspace", targetSurfaceID, map[string]string{
		"craft:project-id": "craft-remote",
		"craft:task-id":    "target-task",
	})
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", callerWorkspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", callerSlot)

	output := captureStdout(t, func() {
		code := runCLI([]string{
			"--socket", filepath.Join(t.TempDir(), "missing.sock"),
			"--json",
			"workspace", "lookup",
			"--include-detached",
			"--metadata", "craft:project-id=craft-remote",
			"--metadata", "craft:task-id=target-task",
		})
		if code != 0 {
			t.Fatalf("workspace lookup returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result["count"] != float64(1) {
		t.Fatalf("lookup count = %v, want 1", result["count"])
	}
	matches, _ := result["matches"].([]any)
	if len(matches) != 1 {
		t.Fatalf("matches len = %d, want 1", len(matches))
	}
	match, _ := matches[0].(map[string]any)
	if got := match["id"]; got != targetWorkspaceIDLower {
		t.Fatalf("match id = %v, want %s", got, targetWorkspaceIDLower)
	}
	if got := match["workspace_id"]; got != targetWorkspaceIDLower {
		t.Fatalf("match workspace_id = %v, want %s", got, targetWorkspaceIDLower)
	}
	callerBody := readHeadlessCLITestBody(t, root, callerSlot)
	if got := headlessMetadataMap(callerBody)["craft:task-id"]; got != "task-1" {
		t.Fatalf("caller metadata changed to %q", got)
	}
}

func TestCLISSHRoutesToSwiftWhenRelayAvailable(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "--json", "ssh", "localhost", "--cwd", "/tmp/project", "--name", "Attached Task", "--", "printf", "hello world"})
		if code != 0 {
			t.Fatalf("ssh returned %d", code)
		}
	})
	if !strings.Contains(output, `"method":"workspace.remote.ssh_create"`) {
		t.Fatalf("ssh output should come from Swift relay: %s", output)
	}
	select {
	case req := <-requests:
		if got := req["method"]; got != "workspace.remote.ssh_create" {
			t.Fatalf("expected workspace.remote.ssh_create, got %v", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["destination"]; got != "localhost" {
			t.Fatalf("destination = %v, want localhost", got)
		}
		if got := params["cwd"]; got != "/tmp/project" {
			t.Fatalf("cwd = %v, want /tmp/project", got)
		}
		if got := params["title"]; got != "Attached Task" {
			t.Fatalf("title = %v, want Attached Task", got)
		}
		if got := params["initial_command"]; got != "printf 'hello world'" {
			t.Fatalf("initial_command = %v, want printf 'hello world'", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ssh create request")
	}
}

func TestCLISSHSingleCommandArgIsNotShellQuoted(t *testing.T) {
	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	command := "printf remote_wrapper_command_ok >/tmp/cmux-remote-wrapper-command-marker; exec bash -l"
	_ = captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "--json", "ssh", "localhost", "--cwd", "/tmp/project", "--name", "Attached Task", "--", command})
		if code != 0 {
			t.Fatalf("ssh returned %d", code)
		}
	})
	select {
	case req := <-requests:
		params, _ := req["params"].(map[string]any)
		if got := params["initial_command"]; got != command {
			t.Fatalf("initial_command = %q, want %q", got, command)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ssh create request")
	}
}

func TestCLISSHDetachedFlagBypassesSwiftRelay(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	sockPath, requests := startMockV2SocketWithRequestCapture(t)

	oldStartPTY := headlessStartPTYFunc
	var startedSlot string
	headlessStartPTYFunc = func(slot, sessionID, attachmentID, command string) error {
		startedSlot = slot
		return nil
	}
	t.Cleanup(func() { headlessStartPTYFunc = oldStartPTY })

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "--json", "ssh", "--detached", "localhost", "--cwd", cwd, "--name", "Forced Detached Task"})
		if code != 0 {
			t.Fatalf("ssh returned %d", code)
		}
	})
	select {
	case req := <-requests:
		t.Fatalf("ssh --detached should bypass Swift relay, got request %#v", req)
	case <-time.After(100 * time.Millisecond):
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if got, _ := result["detached"].(bool); !got {
		t.Fatalf("detached = %v, want true", result["detached"])
	}
	slot := stringFromAny(result["persistent_daemon_slot"])
	if slot == "" || startedSlot != slot {
		t.Fatalf("slot result=%q started=%q", slot, startedSlot)
	}
	body := readHeadlessCLITestBody(t, root, slot)
	if got := stringFromAny(body["title"]); got != "Forced Detached Task" {
		t.Fatalf("title = %q, want Forced Detached Task", got)
	}
}

func TestCLIHeadlessSSHSameHostFallbackCreatesDetachedSnapshot(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	missingSocket := filepath.Join(t.TempDir(), "missing.sock")

	oldStartPTY := headlessStartPTYFunc
	var startedSlot, startedSession, startedAttachment, startedCommand string
	headlessStartPTYFunc = func(slot, sessionID, attachmentID, command string) error {
		startedSlot = slot
		startedSession = sessionID
		startedAttachment = attachmentID
		startedCommand = command
		return nil
	}
	t.Cleanup(func() { headlessStartPTYFunc = oldStartPTY })

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", missingSocket, "--json", "ssh", "localhost", "--cwd", cwd, "--name", "Detached Task", "--", "printf hi"})
		if code != 0 {
			t.Fatalf("ssh returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	workspaceID := stringFromAny(result["workspace_id"])
	slot := stringFromAny(result["persistent_daemon_slot"])
	surfaceID := stringFromAny(result["surface_id"])
	if workspaceID == "" || slot == "" || surfaceID == "" {
		t.Fatalf("missing identifiers in result: %#v", result)
	}
	if startedSlot != slot {
		t.Fatalf("started slot = %q, want %q", startedSlot, slot)
	}
	if startedSession == "" || startedAttachment != surfaceID {
		t.Fatalf("started PTY session=%q attachment=%q surface=%q", startedSession, startedAttachment, surfaceID)
	}
	if !strings.Contains(startedCommand, "cd -- "+shellSingleQuote(cwd)) {
		t.Fatalf("started command missing cwd cd: %s", startedCommand)
	}
	if !strings.Contains(startedCommand, "printf hi") {
		t.Fatalf("started command missing initial command: %s", startedCommand)
	}

	body := readHeadlessCLITestBody(t, root, slot)
	if got := stringFromAny(body["workspaceId"]); got != workspaceID {
		t.Fatalf("workspaceId = %q, want %q", got, workspaceID)
	}
	if got := stringFromAny(body["title"]); got != "Detached Task" {
		t.Fatalf("title = %q, want Detached Task", got)
	}
	panes := headlessPaneSnapshots(body)
	if len(panes) != 1 {
		t.Fatalf("pane snapshots = %d, want 1", len(panes))
	}
	terminal, _ := panes[0]["terminal"].(map[string]any)
	if got := stringFromAny(terminal["cwdHint"]); got != cwd {
		t.Fatalf("cwdHint = %q, want %q", got, cwd)
	}
	metaBytes, err := os.ReadFile(filepath.Join(root, slot, workspaceSnapshotMetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta workspaceSnapshotMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Status != "detached" {
		t.Fatalf("meta status = %q, want detached", meta.Status)
	}
}

func TestCLIHeadlessSSHIgnoresImplicitSocketAddrInRemoteContext(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	home := t.TempDir()
	cmuxDir := filepath.Join(home, ".cmux")
	if err := os.MkdirAll(cmuxDir, 0o700); err != nil {
		t.Fatalf("mkdir cmux dir: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CMUX_SOCKET_PATH", "")
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", "caller-workspace")
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", "ssh-caller")

	sockPath, requests := startMockV2SocketWithRequestCapture(t)
	if err := os.WriteFile(filepath.Join(cmuxDir, "socket_addr"), []byte(sockPath), 0o600); err != nil {
		t.Fatalf("write socket_addr: %v", err)
	}

	oldStartPTY := headlessStartPTYFunc
	var startedSlot string
	headlessStartPTYFunc = func(slot, sessionID, attachmentID, command string) error {
		startedSlot = slot
		return nil
	}
	t.Cleanup(func() { headlessStartPTYFunc = oldStartPTY })

	output := captureStdout(t, func() {
		code := runCLI([]string{"--json", "ssh", "localhost", "--cwd", cwd, "--name", "No Socket Detached Task"})
		if code != 0 {
			t.Fatalf("ssh returned %d", code)
		}
	})
	select {
	case req := <-requests:
		t.Fatalf("ssh without explicit socket should not borrow implicit socket_addr in remote context, got request %#v", req)
	case <-time.After(100 * time.Millisecond):
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	slot := stringFromAny(result["persistent_daemon_slot"])
	if slot == "" || startedSlot != slot {
		t.Fatalf("slot result=%q started=%q", slot, startedSlot)
	}
	if got, _ := result["detached"].(bool); !got {
		t.Fatalf("detached = %v, want true", result["detached"])
	}
	var meta workspaceSnapshotMeta
	metaBytes, err := os.ReadFile(filepath.Join(root, slot, workspaceSnapshotMetaFile))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Status != "detached" {
		t.Fatalf("meta status = %q, want detached", meta.Status)
	}
}

func TestCLIHeadlessNewWorkspaceDoesNotCreateDetachedSnapshot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "new-workspace", "--cwd", root, "--name", "Wrong Primitive"})
	if code == 0 {
		t.Fatal("new-workspace should not fall back to detached snapshot creation")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("new-workspace created daemon entries: %v", entries)
	}
}

func TestCLIHeadlessNewPaneBrowserFallbackMutatesSnapshot(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "new-pane", "--workspace", "current", "--type", "browser", "--url", "https://example.com", "--direction", "right"})
		if code != 0 {
			t.Fatalf("new-pane returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if result["type"] != "browser" {
		t.Fatalf("created type = %v, want browser", result["type"])
	}
	body := readHeadlessCLITestBody(t, root, slot)
	if stringFromAny(body["activePaneId"]) == "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("activePaneId was not updated")
	}
	if layout, _ := body["splitTree"].(map[string]any); stringFromAny(layout["type"]) != "split" {
		t.Fatalf("splitTree type = %v, want split", layout["type"])
	}
	if got := len(headlessPaneSnapshots(body)); got != 2 {
		t.Fatalf("pane snapshots = %d, want 2", got)
	}
}

func TestCLIHeadlessExplicitWorkspaceTargetsOtherSnapshot(t *testing.T) {
	root, callerWorkspaceID, callerSlot := writeHeadlessCLITestSnapshot(t)
	targetWorkspaceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	targetSlot := "slot-b"
	targetSurfaceID := "22222222-2222-4222-8222-222222222222"
	writeHeadlessCLITestSnapshotAt(t, root, targetWorkspaceID, targetSlot, "Target Workspace", targetSurfaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", callerWorkspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", callerSlot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "metadata", "set", "--workspace", targetWorkspaceID, "craft:task-id", "target-task"})
		if code != 0 {
			t.Fatalf("metadata set returned %d", code)
		}
	})
	if !strings.Contains(output, targetWorkspaceID) {
		t.Fatalf("metadata set output should reference target workspace: %s", output)
	}

	callerBody := readHeadlessCLITestBody(t, root, callerSlot)
	callerMetadata := headlessMetadataMap(callerBody)
	if got := callerMetadata["craft:task-id"]; got != "task-1" {
		t.Fatalf("caller metadata changed to %q, want original task-1", got)
	}
	targetBody := readHeadlessCLITestBody(t, root, targetSlot)
	targetMetadata := headlessMetadataMap(targetBody)
	if got := targetMetadata["craft:task-id"]; got != "target-task" {
		t.Fatalf("target metadata = %q, want target-task", got)
	}
}

func TestCLIHeadlessMetadataSetWaitsForSnapshotLock(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)
	paths, err := persistentDaemonPathsForSlot(slot)
	if err != nil {
		t.Fatalf("persistent daemon paths: %v", err)
	}
	unlock, err := lockWorkspaceSnapshot(paths.root)
	if err != nil {
		t.Fatalf("lock snapshot: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := runHeadlessCLIResult("metadata.set", map[string]any{
			"workspace_id": workspaceID,
			"key":          "craft:locked-write",
			"value":        "after-lock",
		})
		done <- err
	}()

	select {
	case err := <-done:
		unlock()
		t.Fatalf("metadata.set completed while snapshot lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("metadata.set after unlock: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("metadata.set did not complete after snapshot lock was released")
	}
	body := readHeadlessCLITestBody(t, root, slot)
	metadata := headlessMetadataMap(body)
	if got := metadata["craft:locked-write"]; got != "after-lock" {
		t.Fatalf("metadata value = %q, want after-lock", got)
	}
}

func TestCLIExplicitWorkspaceUsesTargetRelayBeforeHeadlessFallback(t *testing.T) {
	root, callerWorkspaceID, callerSlot := writeHeadlessCLITestSnapshot(t)
	targetWorkspaceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	targetSlot := "slot-b"
	targetSurfaceID := "22222222-2222-4222-8222-222222222222"
	writeHeadlessCLITestSnapshotAt(t, root, targetWorkspaceID, targetSlot, "Target Workspace", targetSurfaceID)
	targetSocket, requests := startMockV2SocketWithRequestCapture(t)
	if err := os.WriteFile(filepath.Join(root, targetSlot, "relay_socket"), []byte(targetSocket), 0o600); err != nil {
		t.Fatalf("write relay socket: %v", err)
	}
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", callerWorkspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", callerSlot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "new-surface", "--workspace", targetWorkspaceID, "--type", "browser", "--url", "https://example.com"})
		if code != 0 {
			t.Fatalf("new-surface returned %d", code)
		}
	})
	if !strings.Contains(output, `"method":"surface.create"`) {
		t.Fatalf("expected target relay JSON output, got %s", output)
	}

	select {
	case req := <-requests:
		if got := req["method"]; got != "surface.create" {
			t.Fatalf("method = %v, want surface.create", got)
		}
		params, _ := req["params"].(map[string]any)
		if got := params["workspace_id"]; got != targetWorkspaceID {
			t.Fatalf("workspace_id = %v, want %s", got, targetWorkspaceID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for target relay request")
	}

	callerBody := readHeadlessCLITestBody(t, root, callerSlot)
	targetBody := readHeadlessCLITestBody(t, root, targetSlot)
	if got := len(headlessPaneSnapshots(callerBody)); got != 1 {
		t.Fatalf("caller pane snapshots = %d, want unchanged 1", got)
	}
	if got := len(headlessPaneSnapshots(targetBody)); got != 1 {
		t.Fatalf("target pane snapshots = %d, want unchanged 1 because relay handled it", got)
	}
}

func TestCLIHeadlessNewSurfaceExplicitWorkspaceTargetsOtherSnapshot(t *testing.T) {
	root, callerWorkspaceID, callerSlot := writeHeadlessCLITestSnapshot(t)
	targetWorkspaceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	targetSlot := "slot-b"
	targetSurfaceID := "22222222-2222-4222-8222-222222222222"
	writeHeadlessCLITestSnapshotAt(t, root, targetWorkspaceID, targetSlot, "Target Workspace", targetSurfaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", callerWorkspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", callerSlot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "new-surface", "--workspace", targetWorkspaceID, "--type", "browser", "--url", "https://example.com"})
		if code != 0 {
			t.Fatalf("new-surface returned %d", code)
		}
	})
	if !strings.Contains(output, targetWorkspaceID) {
		t.Fatalf("new-surface output should reference target workspace: %s", output)
	}

	callerBody := readHeadlessCLITestBody(t, root, callerSlot)
	if got := len(headlessPaneSnapshots(callerBody)); got != 1 {
		t.Fatalf("caller pane snapshots = %d, want unchanged 1", got)
	}
	targetBody := readHeadlessCLITestBody(t, root, targetSlot)
	if got := len(headlessPaneSnapshots(targetBody)); got != 2 {
		t.Fatalf("target pane snapshots = %d, want 2", got)
	}
}

func TestCLIAttachedCallerExplicitDetachedWorkspaceUsesHeadless(t *testing.T) {
	root, callerWorkspaceID, callerSlot := writeHeadlessCLITestSnapshot(t)
	targetWorkspaceID := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	targetSlot := "slot-b"
	targetSurfaceID := "22222222-2222-4222-8222-222222222222"
	writeHeadlessCLITestSnapshotAt(t, root, targetWorkspaceID, targetSlot, "Target Workspace", targetSurfaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", callerWorkspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", callerSlot)
	callerSocket, requests := startMockV2SocketWithRequestCapture(t)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", callerSocket, "--json", "new-surface", "--workspace", targetWorkspaceID, "--type", "browser", "--url", "https://example.com"})
		if code != 0 {
			t.Fatalf("new-surface returned %d", code)
		}
	})
	select {
	case req := <-requests:
		t.Fatalf("explicit detached target should bypass caller Swift relay, got request %#v", req)
	case <-time.After(100 * time.Millisecond):
	}
	if !strings.Contains(output, targetWorkspaceID) {
		t.Fatalf("new-surface output should reference target workspace: %s", output)
	}

	callerBody := readHeadlessCLITestBody(t, root, callerSlot)
	if got := len(headlessPaneSnapshots(callerBody)); got != 1 {
		t.Fatalf("caller pane snapshots = %d, want unchanged 1", got)
	}
	targetBody := readHeadlessCLITestBody(t, root, targetSlot)
	if got := len(headlessPaneSnapshots(targetBody)); got != 2 {
		t.Fatalf("target pane snapshots = %d, want 2", got)
	}
}

func TestCLIHeadlessNewPaneAcceptsFocusFalse(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "new-pane", "--workspace", "current", "--type", "browser", "--url", "https://example.com", "--direction", "right", "--focus", "false"})
		if code != 0 {
			t.Fatalf("new-pane returned %d", code)
		}
	})
	if !strings.Contains(output, `"detached":true`) {
		t.Fatalf("new-pane output did not look detached: %s", output)
	}
	body := readHeadlessCLITestBody(t, root, slot)
	if got := stringFromAny(body["activePaneId"]); got != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("activePaneId = %q, want original pane when --focus false", got)
	}
}

func TestCLIHeadlessRenameWorkspaceAndTabFallbackMutatesSnapshot(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)
	t.Setenv("CMUX_SURFACE_ID", "11111111-1111-4111-8111-111111111111")

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "rename-workspace", "Detached Renamed"})
		if code != 0 {
			t.Fatalf("rename-workspace returned %d", code)
		}
	})
	if !strings.Contains(output, `"title":"Detached Renamed"`) {
		t.Fatalf("rename-workspace output = %s", output)
	}
	body := readHeadlessCLITestBody(t, root, slot)
	if got := stringFromAny(body["title"]); got != "Detached Renamed" {
		t.Fatalf("workspace title = %q, want Detached Renamed", got)
	}

	output = captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "rename-tab", "--surface", "surface:11111111-1111-4111-8111-111111111111", "Agent Surface"})
		if code != 0 {
			t.Fatalf("rename-tab returned %d", code)
		}
	})
	if !strings.Contains(output, `"title":"Agent Surface"`) {
		t.Fatalf("rename-tab output = %s", output)
	}
	body = readHeadlessCLITestBody(t, root, slot)
	panes := headlessPaneSnapshots(body)
	terminal, _ := panes[0]["terminal"].(map[string]any)
	if got := stringFromAny(terminal["title"]); got != "Agent Surface" {
		t.Fatalf("terminal title = %q, want Agent Surface", got)
	}
}

func TestCLIHeadlessStatusFallbackMutatesSnapshot(t *testing.T) {
	root, workspaceID, slot := writeHeadlessCLITestSnapshot(t)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", workspaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "set-status", "build", "compiling", "--icon", "hammer", "--color", "#ff9500", "--priority", "80"})
		if code != 0 {
			t.Fatalf("set-status returned %d", code)
		}
	})
	if !strings.Contains(output, `"snapshot_sha256"`) {
		t.Fatalf("set-status output missing snapshot hash: %s", output)
	}
	body := readHeadlessCLITestBody(t, root, slot)
	entries := headlessStatusEntries(body)
	if len(entries) != 1 {
		t.Fatalf("status entries = %v, want one", entries)
	}
	if got := stringFromAny(entries[0]["key"]); got != "build" {
		t.Fatalf("status key = %q, want build", got)
	}
	if got := stringFromAny(entries[0]["value"]); got != "compiling" {
		t.Fatalf("status value = %q, want compiling", got)
	}
	if got := intFromAny(entries[0]["priority"]); got != 80 {
		t.Fatalf("status priority = %d, want 80", got)
	}

	output = captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "list-status"})
		if code != 0 {
			t.Fatalf("list-status returned %d", code)
		}
	})
	if !strings.Contains(output, `"key":"build"`) {
		t.Fatalf("list-status output = %s", output)
	}

	output = captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "clear-status", "build"})
		if code != 0 {
			t.Fatalf("clear-status returned %d", code)
		}
	})
	if !strings.Contains(output, `"cleared":true`) {
		t.Fatalf("clear-status output = %s", output)
	}
	body = readHeadlessCLITestBody(t, root, slot)
	if got := len(headlessStatusEntries(body)); got != 0 {
		t.Fatalf("status entries after clear = %d, want 0", got)
	}
}

func TestCLIListStatusJSONPrintsRelayV2Response(t *testing.T) {
	response := `{"id":"test","ok":true,"result":{"entries":[{"key":"task_state","value":"running","icon":"bolt.fill","color":"#4C8DFF","priority":100,"format":"plain"},{"key":"review","value":"needs input","priority":0,"format":"markdown"}],"count":2}}`
	sockPath := startMockSocket(t, response)
	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", sockPath, "list-status", "--workspace", "workspace:1", "--json"})
		if code != 0 {
			t.Fatalf("list-status returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	entries, _ := result["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries = %v, want 2", result["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if got := first["key"]; got != "task_state" {
		t.Fatalf("first key = %v, want task_state", got)
	}
	if got := first["value"]; got != "running" {
		t.Fatalf("first value = %v, want running", got)
	}
	if got := first["icon"]; got != "bolt.fill" {
		t.Fatalf("first icon = %v, want bolt.fill", got)
	}
	if got := intFromAny(first["priority"]); got != 100 {
		t.Fatalf("first priority = %v, want 100", first["priority"])
	}
	second, _ := entries[1].(map[string]any)
	if got := second["value"]; got != "needs input" {
		t.Fatalf("second value = %v, want needs input", got)
	}
	if got := second["format"]; got != "markdown" {
		t.Fatalf("second format = %v, want markdown", got)
	}
}

func TestCLICommandJSONFlagDoesNotConsumePassthroughArgs(t *testing.T) {
	args, found := extractCommandJSONFlag([]string{"ed@tdb", "--json", "--", "printf", "--json"})
	if !found {
		t.Fatal("expected command-level --json to be found")
	}
	want := []string{"ed@tdb", "--", "printf", "--json"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestCLIHeadlessTreeCanonicalizesUppercaseSnapshotIDs(t *testing.T) {
	root := t.TempDir()
	workspaceID := "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
	surfaceID := "BBBBBBBB-BBBB-4BBB-8BBB-BBBBBBBBBBBB"
	slot := "slot-upper"
	writeHeadlessCLITestSnapshotAt(t, root, workspaceID, slot, "Uppercase IDs", surfaceID)
	t.Setenv("CMUX_REMOTE_DAEMON_ROOT", root)
	t.Setenv("CMUX_WORKSPACE_ID", strings.ToLower(workspaceID))
	t.Setenv("CMUX_REMOTE_DAEMON_SLOT", slot)

	output := captureStdout(t, func() {
		code := runCLI([]string{"--socket", filepath.Join(t.TempDir(), "missing.sock"), "--json", "tree", "--workspace", strings.ToLower(workspaceID)})
		if code != 0 {
			t.Fatalf("tree returned %d", code)
		}
	})
	var result map[string]any
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	windows, _ := result["windows"].([]any)
	window, _ := windows[0].(map[string]any)
	workspaces, _ := window["workspaces"].([]any)
	workspace, _ := workspaces[0].(map[string]any)
	if got := workspace["id"]; got != strings.ToLower(workspaceID) {
		t.Fatalf("workspace id = %v, want lowercase", got)
	}
	panes, _ := workspace["panes"].([]any)
	pane, _ := panes[0].(map[string]any)
	surfaces, _ := pane["surfaces"].([]any)
	surface, _ := surfaces[0].(map[string]any)
	if got := surface["id"]; got != strings.ToLower(surfaceID) {
		t.Fatalf("surface id = %v, want lowercase", got)
	}
	if got := surface["ref"]; got != "surface:"+strings.ToLower(surfaceID) {
		t.Fatalf("surface ref = %v, want lowercase prefixed ref", got)
	}
}

func writeHeadlessCLITestSnapshot(t *testing.T) (root string, workspaceID string, slot string) {
	t.Helper()
	root = t.TempDir()
	workspaceID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	slot = "slot-a"
	writeHeadlessCLITestSnapshotAt(t, root, workspaceID, slot, "Detached Test", "11111111-1111-4111-8111-111111111111")
	return root, workspaceID, slot
}

func writeHeadlessCLITestSnapshotAt(t *testing.T, root string, workspaceID string, slot string, title string, surfaceID string) {
	writeHeadlessCLITestSnapshotAtWithMetadata(t, root, workspaceID, slot, title, surfaceID, map[string]string{
		"craft:task-id": "task-1",
	})
}

func writeHeadlessCLITestSnapshotAtWithMetadata(t *testing.T, root string, workspaceID string, slot string, title string, surfaceID string, metadata map[string]string) {
	t.Helper()
	slotDir := filepath.Join(root, slot)
	if err := os.MkdirAll(slotDir, 0o700); err != nil {
		t.Fatalf("mkdir slot: %v", err)
	}
	body := map[string]any{
		"version":       1,
		"workspaceId":   workspaceID,
		"title":         title,
		"detachedAt":    "2026-05-28T00:00:00Z",
		"displayTarget": "test:" + slot,
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
				"remotePTYSessionId": "sess-existing",
				"title":              "Terminal",
			},
		}},
		"activePaneId":    surfaceID,
		"metadataEntries": metadata,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	sum := sha256.Sum256(bodyBytes)
	meta := workspaceSnapshotMeta{
		Version:        1,
		WorkspaceID:    workspaceID,
		Title:          title,
		Status:         "detached",
		DetachedAt:     "2026-05-28T00:00:00Z",
		SchemaVersion:  1,
		SnapshotSHA256: hex.EncodeToString(sum[:]),
		BodyByteLength: len(bodyBytes),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal meta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotDir, workspaceSnapshotBodyFile), bodyBytes, 0o600); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := os.WriteFile(filepath.Join(slotDir, workspaceSnapshotMetaFile), metaBytes, 0o600); err != nil {
		t.Fatalf("write meta: %v", err)
	}
}

func readHeadlessCLITestBody(t *testing.T, root string, slot string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, slot, workspaceSnapshotBodyFile))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return body
}
