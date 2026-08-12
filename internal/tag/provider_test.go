package tag

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
)

type runnerFunc func(string, []string) CommandResult

func (f runnerFunc) Run(_ context.Context, name string, args []string, _, _ string, _ map[string]string) CommandResult {
	return f(name, args)
}

type streamingRunner struct {
	lines []string
}

func (runner streamingRunner) Run(context.Context, string, []string, string, string, map[string]string) CommandResult {
	return CommandResult{Stdout: strings.Join(runner.lines, "\n") + "\n"}
}

func (runner streamingRunner) RunStream(_ context.Context, _ string, _ []string, _, _ string, _ map[string]string, onLine func(string)) CommandResult {
	for _, line := range runner.lines {
		onLine(line)
	}
	return CommandResult{Stdout: strings.Join(runner.lines, "\n") + "\n"}
}

func TestClaudeProgressEventsAreEmittedIncrementally(t *testing.T) {
	runner := streamingRunner{lines: []string{
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}`,
		`{"type":"result","result":"Hello world"}`,
	}}
	var updates []RunProgress
	result, err := NewProviders(runner).Chat(context.Background(), RunRequest{Provider: "claude", Root: t.TempDir(), AgentName: "cc", OnProgress: func(progress RunProgress) {
		updates = append(updates, progress)
	}})
	if err != nil || result.Text != "Hello world" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(updates) < 2 || updates[0].Text != "Hello" || updates[len(updates)-1].Text != "Hello world" {
		t.Fatalf("progress updates=%+v", updates)
	}
}

func TestProviderFailuresAreClassified(t *testing.T) {
	cases := map[string]string{
		"executable file not found":        "CLI 未安装",
		"Error: not logged in":             "尚未登录",
		"permission denied opening config": "权限不足",
		"request hit rate limit":           "触发限流",
	}
	for input, expected := range cases {
		if message := ClassifyProviderFailure("codex", input, 1); !strings.Contains(message, expected) {
			t.Fatalf("failure %q classified as %q", input, message)
		}
	}
}

func TestChatUsesConfiguredExecutableAndExtraArguments(t *testing.T) {
	var call []string
	runner := runnerFunc(func(name string, args []string) CommandResult {
		call = append([]string{name}, args...)
		return CommandResult{Stdout: `{"type":"result","result":"done"}` + "\n"}
	})
	result, err := NewProviders(runner).Chat(context.Background(), RunRequest{Provider: "claude", Executable: "/custom/claude", ExtraArgs: "--profile team", Root: t.TempDir(), AgentName: "cc"})
	if err != nil || result.Text != "done" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if call[0] != "/custom/claude" || !slices.Contains(call, "--profile") || !slices.Contains(call, "team") {
		t.Fatalf("configured call=%v", call)
	}
}

func TestClaudeDoesNotPassUnsupportedNameOption(t *testing.T) {
	var call []string
	runner := runnerFunc(func(name string, args []string) CommandResult {
		call = append([]string{name}, args...)
		return CommandResult{Stdout: `{"type":"result","result":"done"}` + "\n"}
	})
	_, _ = NewProviders(runner).Chat(context.Background(), RunRequest{Provider: "claude", Root: t.TempDir(), AgentName: "cc"})
	if slices.Contains(call, "--name") {
		t.Fatalf("Claude 2.x does not support --name: %v", call)
	}
}

func TestNativeSessionsAreReused(t *testing.T) {
	var calls [][]string
	runner := runnerFunc(func(name string, args []string) CommandResult {
		calls = append(calls, append([]string{name}, args...))
		if name == "codex" {
			for i, arg := range args {
				if arg == "-o" {
					_ = os.WriteFile(args[i+1], []byte("Codex reply"), 0o600)
				}
			}
			return CommandResult{Stdout: "{\"type\":\"thread.started\",\"thread_id\":\"thread-123\"}\n"}
		}
		return CommandResult{Stdout: "Claude reply"}
	})
	providers := NewProviders(runner)

	first, err := providers.Chat(context.Background(), RunRequest{Provider: "claude", Root: t.TempDir(), AgentName: "cc"})
	if err != nil || first.SessionID == nil {
		t.Fatalf("first Claude call: result=%+v err=%v", first, err)
	}
	_, _ = providers.Chat(context.Background(), RunRequest{Provider: "claude", Root: t.TempDir(), AgentName: "cc", SessionID: first.SessionID})
	if !slices.Contains(calls[1], "--resume") || slices.Contains(calls[1], "--session-id") {
		t.Fatalf("Claude resume args = %v", calls[1])
	}

	codexFirst, err := providers.Chat(context.Background(), RunRequest{Provider: "codex", Root: t.TempDir(), AgentName: "codex"})
	if err != nil || codexFirst.SessionID == nil || *codexFirst.SessionID != "thread-123" {
		t.Fatalf("first Codex call: result=%+v err=%v", codexFirst, err)
	}
	_, _ = providers.Chat(context.Background(), RunRequest{Provider: "codex", Root: t.TempDir(), AgentName: "codex", SessionID: codexFirst.SessionID})
	last := calls[len(calls)-1]
	if len(last) < 3 || last[1] != "exec" || last[2] != "resume" || !slices.Contains(last, "--skip-git-repo-check") {
		t.Fatalf("Codex resume args = %v", last)
	}
}

func TestObservableStepsExcludeHiddenReasoning(t *testing.T) {
	claude := "{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"thinking\",\"thinking\":\"private\"},{\"type\":\"tool_use\",\"name\":\"Read\",\"input\":{\"file_path\":\"src/auth.go\"}}]}}\n{\"type\":\"result\",\"result\":\"Final\"}\n"
	text, steps := ParseClaudeEvents(claude)
	if text != "Final" || len(steps) != 1 || steps[0] != "读取文件：src/auth.go" {
		t.Fatalf("Claude parse = %q, %v", text, steps)
	}
	codex := "{\"type\":\"item.completed\",\"item\":{\"type\":\"reasoning\",\"text\":\"hidden detail\"}}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Done\"}}\n"
	text, steps, _ = ParseCodexEvents(codex, "")
	if text != "Done" || len(steps) != 1 || steps[0] != "完成一轮分析" {
		t.Fatalf("Codex parse = %q, %v", text, steps)
	}
}

func TestClaudePersistedToolOutputIsExposedAsArtifact(t *testing.T) {
	raw := "{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"content\":\"<persisted-output>\\nFull output saved to: /tmp/tool-results/result.txt\\n</persisted-output>\"}]}}\n"
	artifacts := ParseClaudeArtifacts(raw)
	if len(artifacts) != 1 || artifacts[0].Path != "/tmp/tool-results/result.txt" {
		t.Fatalf("artifacts=%+v", artifacts)
	}
}

func TestNoToolEventsStillExposeSafeProgress(t *testing.T) {
	codex := "{\"type\":\"turn.started\"}\n{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"Done\"}}\n{\"type\":\"turn.completed\"}\n"
	_, codexSteps, _ := ParseCodexEvents(codex, "")
	if !slices.Equal(codexSteps, []string{"开始分析问题", "完成分析并组织回复"}) {
		t.Fatalf("Codex safe progress = %v", codexSteps)
	}

	pi := "{\"type\":\"agent_start\"}\n{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Done\"}]}}\n{\"type\":\"agent_end\",\"messages\":[]}\n"
	_, piSteps := ParsePiEvents(pi)
	if !slices.Equal(piSteps, []string{"开始分析问题", "完成分析并组织回复"}) {
		t.Fatalf("Pi safe progress = %v", piSteps)
	}
}

func TestChatExecutionPolicyAndPiSkillPaths(t *testing.T) {
	var calls [][]string
	runner := runnerFunc(func(name string, args []string) CommandResult {
		calls = append(calls, append([]string{name}, args...))
		if name == "codex" {
			for index, arg := range args {
				if arg == "-o" {
					_ = os.WriteFile(args[index+1], []byte("done"), 0o600)
				}
			}
		}
		return CommandResult{Stdout: "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}}\n"}
	})
	providers := NewProviders(runner)
	request := RunRequest{Root: t.TempDir(), AgentName: "agent", AllowSkillExecution: true, SkillPaths: []string{"/tmp/example/SKILL.md"}}
	request.Provider = "claude"
	_, _ = providers.Chat(context.Background(), request)
	request.Provider = "codex"
	_, _ = providers.Chat(context.Background(), request)
	request.Provider = "pi"
	_, _ = providers.Chat(context.Background(), request)
	if !slices.Contains(calls[0], "acceptEdits") {
		t.Fatalf("Claude args=%v", calls[0])
	}
	if !slices.Contains(calls[1], "workspace-write") || !slices.Contains(calls[1], "sandbox_workspace_write.network_access=true") {
		t.Fatalf("Codex args=%v", calls[1])
	}
	if !slices.Contains(calls[2], "read,grep,find,ls,bash") || !slices.Contains(calls[2], "--skill") || !slices.Contains(calls[2], "/tmp/example/SKILL.md") {
		t.Fatalf("Pi args=%v", calls[2])
	}
	calls = nil
	request.AllowSkillExecution = false
	request.SkillPermissions = SkillPermissions{Network: true}
	request.Provider = "codex"
	_, _ = providers.Chat(context.Background(), request)
	request.Provider = "pi"
	_, _ = providers.Chat(context.Background(), request)
	if !slices.Contains(calls[0], "read-only") || slices.Contains(calls[1], "read,grep,find,ls,bash") {
		t.Fatalf("restricted calls=%v", calls)
	}
}

func TestProviderInstallPackagesAreExplicitlyAllowlisted(t *testing.T) {
	want := map[string]string{
		"claude": "@anthropic-ai/claude-code",
		"codex":  "@openai/codex",
		"pi":     "@earendil-works/pi-coding-agent",
	}
	for provider, packageName := range want {
		if got, ok := ProviderInstallPackage(provider); !ok || got != packageName {
			t.Fatalf("ProviderInstallPackage(%q) = %q, %t", provider, got, ok)
		}
	}
	if _, ok := ProviderInstallPackage("command"); ok {
		t.Fatal("custom command provider must not be installable")
	}
}

func TestProviderRegistryAndUsageObservation(t *testing.T) {
	runner := runnerFunc(func(string, []string) CommandResult {
		return CommandResult{Stdout: "{\"type\":\"result\",\"result\":\"done\",\"usage\":{\"input_tokens\":12,\"output_tokens\":8,\"total_cost_usd\":0.004}}\n"}
	})
	providers := NewProviders(runner)
	if !providers.Supports("command", true) || len(providers.Descriptors()) != 4 {
		t.Fatalf("descriptors=%+v", providers.Descriptors())
	}
	result, err := providers.Chat(context.Background(), RunRequest{Provider: "claude", Root: t.TempDir(), AgentName: "cc"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Observation.Status != "completed" || result.Observation.Usage.TotalTokens != 20 || result.Observation.Usage.EstimatedCostUSD != 0.004 {
		t.Fatalf("observation=%+v", result.Observation)
	}
}
