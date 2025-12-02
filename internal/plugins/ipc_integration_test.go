package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestPluginIPC_Ack(t *testing.T) {
	// pick a free TCP port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	addr := l.Addr().(*net.TCPAddr)
	port := addr.Port
	l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// path to the test plugin from this package (internal/plugins)
	cmd := exec.CommandContext(ctx, "go", "run", "../../scripts/test-plugin")
	cmd.Env = append(os.Environ(), "PLUGIN_PORT="+fmt.Sprintf("%d", port))
	// forward plugin output to test logs to aid debugging
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start plugin failed: %v", err)
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	// wait for plugin to listen (simple backoff)
	var conn net.Conn
	var dErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, dErr = net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", port)), 300*time.Millisecond)
		if dErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if dErr != nil {
		t.Fatalf("dial plugin failed: %v", dErr)
	}
	defer conn.Close()

	// send event
	event := map[string]interface{}{"type": "event", "name": "sale.completed", "payload": map[string]interface{}{"id": "test"}}
	b, _ := json.Marshal(event)
	b = append(b, '\n')
	conn.Write(b)

	// read ack
	rd := make([]byte, 512)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := conn.Read(rd)
	if err != nil {
		t.Fatalf("read ack failed: %v", err)
	}
	var ack map[string]interface{}
	if err := json.Unmarshal(rd[:n], &ack); err != nil {
		t.Fatalf("invalid ack json: %v", err)
	}
	if status, _ := ack["status"].(string); status != "ok" {
		t.Fatalf("unexpected ack status: %v", ack)
	}
}
