package bugbot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BrokkAi/acp-go/runner"
)

// Speak JSON on stdio so these tests exercise the published runner and wire
// schema through the same process boundary as an installed ACP agent.
func TestACPAgentHelper(t *testing.T) {
	scenario := os.Getenv("BUG_BOT_TEST_ACP")
	if scenario == "" {
		return
	}
	if scenario == "terminal-command" {
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	read := func() map[string]any {
		var message map[string]any
		if err := decoder.Decode(&message); err != nil {
			os.Exit(2)
		}
		return message
	}
	send := func(message map[string]any) {
		message["jsonrpc"] = "2.0"
		if err := encoder.Encode(message); err != nil {
			os.Exit(2)
		}
	}
	request := func(method string) map[string]any {
		message := read()
		if message["method"] != method {
			t.Fatalf("method = %v, want %s", message["method"], method)
		}
		return message
	}
	reply := func(message map[string]any, result any) { send(map[string]any{"id": message["id"], "result": result}) }
	init := request("initialize")
	info := init["params"].(map[string]any)["clientInfo"].(map[string]any)
	if info["name"] != "bug-bot" {
		t.Fatalf("client info = %v", info)
	}
	reply(init, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}})
	session := request("session/new")
	cwd, _ := os.Getwd()
	// macOS temporary paths may resolve through /var's symlink.
	gotCwd, _ := filepath.EvalSymlinks(session["params"].(map[string]any)["cwd"].(string))
	if gotCwd != cwd {
		t.Fatalf("session cwd = %q, process cwd = %q", gotCwd, cwd)
	}
	options := []map[string]any{
		{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": "small", "options": []map[string]string{{"value": "small", "name": "Small"}, {"value": "large", "name": "Large"}}},
		{"id": "reasoning_effort", "name": "Effort", "category": "thought_level", "type": "select", "currentValue": "low", "options": []map[string]string{{"value": "low", "name": "Low"}, {"value": "high", "name": "High"}}},
	}
	if scenario == "uncategorized-effort" {
		options[1]["id"] = "thought_level"
		delete(options[1], "category")
	}
	reply(session, map[string]any{"sessionId": "test-session", "configOptions": options, "modes": map[string]any{"currentModeId": "plan", "availableModes": []map[string]string{{"id": "plan", "name": "Plan"}, {"id": "code", "name": "Code"}}}})
	mode := request("session/set_mode")
	if mode["params"].(map[string]any)["modeId"] != "code" {
		t.Fatal("wrong mode")
	}
	reply(mode, map[string]any{})
	for i, value := range []string{"large", "high"} {
		selection := request("session/set_config_option")
		params := selection["params"].(map[string]any)
		if params["configId"] != options[i]["id"] || params["value"] != value {
			t.Fatalf("selection = %v", params)
		}
		if scenario == "reject" {
			send(map[string]any{"id": selection["id"], "error": map[string]any{"code": -32602, "message": "model unavailable"}})
			// Any prompt after a rejected setting is a test failure.
			request("never prompt after failed setup")
			os.Exit(2)
		}
		options[i]["currentValue"] = value
		reply(selection, map[string]any{"configOptions": options})
	}
	prompt := request("session/prompt")
	params := prompt["params"].(map[string]any)
	if params["sessionId"] != "test-session" || params["prompt"].([]any)[0].(map[string]any)["text"] != "investigate" {
		t.Fatalf("prompt = %v", params)
	}
	if scenario == "cancel" {
		if err := os.WriteFile("prompt-started", []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		// Stay alive until the runner cancels and terminates the process.
		for {
			read()
		}
	}
	if scenario == "disconnect-terminal" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		send(map[string]any{"id": "create", "method": "terminal/create", "params": map[string]any{
			"sessionId": "test-session", "command": executable, "args": []string{"-test.run=^TestACPAgentHelper$"},
			"env": []map[string]string{{"name": "BUG_BOT_TEST_ACP", "value": "terminal-command"}},
		}})
		created := read()
		if created["error"] != nil || created["result"] == nil {
			t.Fatalf("terminal creation = %v", created)
		}
		id := created["result"].(map[string]any)["terminalId"]
		send(map[string]any{"id": "wait", "method": "terminal/wait_for_exit", "params": map[string]any{"sessionId": "test-session", "terminalId": id}})
		// Close the transport with an unanswered prompt and a terminal handler
		// waiting on a command that outlives the parent's test deadline.
		os.Exit(0)
	}
	send(map[string]any{"id": "permission", "method": "session/request_permission", "params": map[string]any{"sessionId": "test-session", "toolCall": map[string]any{"toolCallId": "tool", "title": "Inspect source"}, "options": []map[string]string{{"optionId": "deny", "name": "Deny", "kind": "reject_once"}, {"optionId": "allow", "name": "Allow", "kind": "allow_once"}}}})
	permission := read()
	outcome := permission["result"].(map[string]any)["outcome"].(map[string]any)
	if outcome["outcome"] != "selected" || outcome["optionId"] != "allow" {
		t.Fatalf("permission = %v", permission)
	}
	for _, chunk := range []string{"no ", "findings"} {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "test-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": chunk}}}})
	}
	reply(prompt, map[string]any{"stopReason": "end_turn"})
	for {
		read()
	}
}

func testACPProcess(t *testing.T, scenario string) agentProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return agentProcess{config: Config{Directory: t.TempDir(), StateDirectory: t.TempDir(), Agent: AgentConfig{
		Command: []string{executable, "-test.run=^TestACPAgentHelper$"}, Environment: map[string]string{"BUG_BOT_TEST_ACP": scenario}, Mode: "code", Model: "large", Effort: "high",
	}}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestAgentProcessACP(t *testing.T) {
	for _, scenario := range []string{"success", "reject", "cancel", "uncategorized-effort", "disconnect-terminal"} {
		t.Run(scenario, func(t *testing.T) {
			process := testACPProcess(t, scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			type result struct {
				text string
				err  error
			}
			done := make(chan result, 1)
			go func() { text, err := process.Execute(ctx, "investigate"); done <- result{text, err} }()
			if scenario == "cancel" {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
			waitPrompt:
				for {
					select {
					case <-ticker.C:
						if _, err := os.Stat(filepath.Join(process.config.Directory, "prompt-started")); err == nil {
							break waitPrompt
						}
					case result := <-done:
						t.Fatalf("agent exited before prompt: %v", result.err)
					case <-ctx.Done():
						t.Fatal("agent never reached prompt")
					}
				}
				cancel()
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("runner did not terminate")
			}
			var setup *runner.SetupError
			switch scenario {
			case "success", "uncategorized-effort":
				if got.err != nil || got.text != "no findings" {
					t.Fatalf("Execute = %q, %v", got.text, got.err)
				}
			case "reject":
				if !errors.As(got.err, &setup) || !strings.Contains(got.err.Error(), "model unavailable") || got.text != "" {
					t.Fatalf("setup result = %+v", got)
				}
			case "cancel":
				if !errors.Is(got.err, context.Canceled) || errors.As(got.err, &setup) {
					t.Fatalf("cancel error = %v", got.err)
				}
			case "disconnect-terminal":
				if !errors.Is(got.err, io.EOF) || ctx.Err() != nil || got.text != "" {
					t.Fatalf("disconnect result = %+v, context error = %v", got, ctx.Err())
				}
			}
			files, err := filepath.Glob(filepath.Join(process.config.StateDirectory, "sessions", "*.jsonl"))
			if err != nil || len(files) != 1 {
				t.Fatalf("transcripts = %v, %v", files, err)
			}
			data, err := os.ReadFile(files[0])
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if !json.Valid([]byte(line)) {
					t.Fatalf("invalid transcript line: %s", line)
				}
			}
			if !strings.Contains(string(data), "session_end") {
				t.Fatal("missing session end")
			}
			if scenario == "success" && (!strings.Contains(string(data), "investigate") || !strings.Contains(string(data), "findings")) {
				t.Fatal("missing prompt or answer in transcript")
			}
			if scenario == "reject" && strings.Contains(string(data), `"prompt":`) {
				t.Fatal("prompt recorded after rejected setup")
			}
		})
	}
}

func TestAgentProcessStartupFailure(t *testing.T) {
	process := testACPProcess(t, "success")
	process.config.Agent.Command = []string{filepath.Join(t.TempDir(), "missing-agent")}
	_, err := process.Execute(context.Background(), "investigate")
	var setup *runner.SetupError
	if !errors.As(err, &setup) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup error = %v", err)
	}
}
