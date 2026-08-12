package tag

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const resultPrefix = "AGENT_TAG_RESULT:"

type RunRequest struct {
	Provider, Model, Executable, ExtraArgs, Command, Prompt, Root, AgentName string
	SessionID                                                                *string
	SkillPaths                                                               []string
	AllowSkillExecution                                                      bool
	SkillPermissions                                                         SkillPermissions
	OnProgress                                                               func(RunProgress)
}

type RunProgress struct {
	Text  string
	Steps []string
}

type RunResult struct {
	Code        int
	Stdout      string
	Stderr      string
	Text        string
	Steps       []string
	SessionID   *string
	Outcome     *Outcome
	Artifacts   []RunArtifact
	Observation RunObservation
}

type RunArtifact struct {
	Path  string
	Label string
}

type Outcome struct{ Status, Summary string }

type ProviderStatus struct {
	Provider  string `json:"provider"`
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

type ProviderInstallResult struct {
	Provider string `json:"provider"`
	Command  string `json:"command"`
	Output   string `json:"output,omitempty"`
}

type ProviderInstaller interface {
	Install(context.Context, string) (ProviderInstallResult, error)
}

type OSProviderInstaller struct{}

func ProviderInstallPackage(provider string) (string, bool) {
	packages := map[string]string{
		"claude": "@anthropic-ai/claude-code",
		"codex":  "@openai/codex",
		"pi":     "@earendil-works/pi-coding-agent",
	}
	value, ok := packages[provider]
	return value, ok
}

func (OSProviderInstaller) Install(ctx context.Context, provider string) (ProviderInstallResult, error) {
	packageName, ok := ProviderInstallPackage(provider)
	if !ok {
		return ProviderInstallResult{}, fmt.Errorf("unsupported provider: %s", provider)
	}
	command := "npm install -g " + packageName
	result := ProviderInstallResult{Provider: provider, Command: command}
	path, err := exec.LookPath("npm")
	if err != nil {
		return result, errors.New("未找到 npm，请先安装 Node.js 18 或更高版本")
	}
	output, err := exec.CommandContext(ctx, path, "install", "-g", packageName).CombinedOutput()
	result.Output = truncate(strings.TrimSpace(string(output)), 4000)
	if ctx.Err() != nil {
		return result, fmt.Errorf("安装 %s 超时或已取消", provider)
	}
	if err != nil {
		if result.Output == "" {
			return result, fmt.Errorf("安装 %s 失败: %w", provider, err)
		}
		return result, fmt.Errorf("安装 %s 失败：%s", provider, result.Output)
	}
	return result, nil
}

func configuredProviderExecutable(configs []ProviderConfig, provider string) string {
	for _, config := range configs {
		if config.Provider == provider && strings.TrimSpace(config.Executable) != "" {
			return strings.TrimSpace(config.Executable)
		}
	}
	return provider
}

func IsProviderInstalled(configs []ProviderConfig, provider string) bool {
	_, err := exec.LookPath(configuredProviderExecutable(configs, provider))
	return err == nil
}

func DetectProviderStatuses(ctx context.Context) []ProviderStatus {
	return DetectConfiguredProviderStatuses(ctx, nil)
}

func DetectConfiguredProviderStatuses(ctx context.Context, configs []ProviderConfig) []ProviderStatus {
	names := []string{"claude", "codex", "pi"}
	statuses := make([]ProviderStatus, len(names))
	var wg sync.WaitGroup
	for index, name := range names {
		index, name := index, name
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := ProviderStatus{Provider: name}
			executable := configuredProviderExecutable(configs, name)
			path, err := exec.LookPath(executable)
			if err != nil {
				status.Error = "未找到可执行文件"
				statuses[index] = status
				return
			}
			status.Installed = true
			status.Path = path
			commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			output, commandErr := exec.CommandContext(commandCtx, path, "--version").CombinedOutput()
			status.Version = truncate(strings.TrimSpace(string(output)), 160)
			if commandCtx.Err() == context.DeadlineExceeded {
				status.Error = "版本检查超时"
			} else if commandErr != nil {
				status.Error = "无法读取版本"
			}
			statuses[index] = status
		}()
	}
	wg.Wait()
	return statuses
}

func ClassifyProviderFailure(provider, stderr string, code int) string {
	lower := strings.ToLower(stderr)
	switch {
	case strings.Contains(lower, "executable file not found"), strings.Contains(lower, "no such file or directory"):
		return provider + " CLI 未安装或不在 PATH 中"
	case strings.Contains(lower, "not logged in"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "authentication"), strings.Contains(lower, "login required"):
		return provider + " CLI 尚未登录或登录已失效"
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
		return provider + " CLI 权限不足"
	case strings.Contains(lower, "timed out"), strings.Contains(lower, "timeout"):
		return provider + " CLI 请求超时"
	case strings.Contains(lower, "rate limit"), strings.Contains(lower, "too many requests"):
		return provider + " 服务触发限流，请稍后重试"
	case strings.TrimSpace(stderr) != "":
		return provider + " CLI 执行失败：" + truncate(strings.TrimSpace(stderr), 300)
	default:
		return fmt.Sprintf("%s CLI 执行失败（退出码 %d）", provider, code)
	}
}

type CommandResult struct {
	Code           int
	Stdout, Stderr string
}

type CommandRunner interface {
	Run(context.Context, string, []string, string, string, map[string]string) CommandResult
}

type StreamingCommandRunner interface {
	RunStream(context.Context, string, []string, string, string, map[string]string, func(string)) CommandResult
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args []string, cwd, input string, env map[string]string) CommandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			stderr.WriteString(err.Error())
		}
	}
	return CommandResult{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

func (OSRunner) RunStream(ctx context.Context, name string, args []string, cwd, input string, env map[string]string, onLine func(string)) CommandResult {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(input)
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return CommandResult{Code: 1, Stderr: err.Error()}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return CommandResult{Code: 1, Stderr: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return CommandResult{Code: 1, Stderr: err.Error()}
	}
	var stdout, stderr bytes.Buffer
	var wg sync.WaitGroup
	consume := func(scanner *bufio.Scanner, target *bytes.Buffer, callback func(string)) {
		defer wg.Done()
		scanner.Buffer(make([]byte, 64<<10), 2<<20)
		for scanner.Scan() {
			line := scanner.Text()
			target.WriteString(line)
			target.WriteByte('\n')
			if callback != nil {
				callback(line)
			}
		}
	}
	wg.Add(2)
	go consume(bufio.NewScanner(stdoutPipe), &stdout, onLine)
	go consume(bufio.NewScanner(stderrPipe), &stderr, nil)
	err = cmd.Wait()
	wg.Wait()
	code := 0
	if err != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else if ctx.Err() == nil {
			stderr.WriteString(err.Error())
		}
	}
	return CommandResult{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

type Provider interface {
	Task(context.Context, RunRequest) (RunResult, error)
	Chat(context.Context, RunRequest) (RunResult, error)
}

type ProviderDescriptor struct {
	Name              string `json:"name"`
	Label             string `json:"label"`
	SupportsTask      bool   `json:"supportsTask"`
	SupportsChat      bool   `json:"supportsChat"`
	SupportsSession   bool   `json:"supportsSession"`
	SupportsStreaming bool   `json:"supportsStreaming"`
	BuiltIn           bool   `json:"builtIn"`
}

type providerRegistration struct {
	descriptor ProviderDescriptor
	adapter    Provider
}

type Providers struct {
	registrations map[string]providerRegistration
}

func NewProviders(runner CommandRunner) *Providers {
	p := &Providers{registrations: map[string]providerRegistration{}}
	p.Register(ProviderDescriptor{Name: "claude", Label: "Claude Code", SupportsTask: true, SupportsChat: true, SupportsSession: true, SupportsStreaming: true, BuiltIn: true}, claudeAdapter{runner})
	p.Register(ProviderDescriptor{Name: "codex", Label: "Codex CLI", SupportsTask: true, SupportsChat: true, SupportsSession: true, SupportsStreaming: true, BuiltIn: true}, codexAdapter{runner})
	p.Register(ProviderDescriptor{Name: "pi", Label: "Pi Agent", SupportsTask: true, SupportsChat: true, SupportsSession: true, SupportsStreaming: true, BuiltIn: true}, piAdapter{runner})
	p.Register(ProviderDescriptor{Name: "command", Label: "Custom command", SupportsTask: true, SupportsChat: true, SupportsStreaming: true, BuiltIn: true}, commandAdapter{runner})
	return p
}

func (p *Providers) Register(descriptor ProviderDescriptor, adapter Provider) {
	name := strings.TrimSpace(descriptor.Name)
	if name == "" || adapter == nil {
		return
	}
	descriptor.Name = name
	p.registrations[name] = providerRegistration{descriptor: descriptor, adapter: adapter}
}

func (p *Providers) Descriptors() []ProviderDescriptor {
	result := make([]ProviderDescriptor, 0, len(p.registrations))
	for _, registration := range p.registrations {
		result = append(result, registration.descriptor)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (p *Providers) Supports(name string, chat bool) bool {
	registration, ok := p.registrations[name]
	if !ok {
		return false
	}
	if chat {
		return registration.descriptor.SupportsChat
	}
	return registration.descriptor.SupportsTask
}

func observeRun(req RunRequest, started time.Time, result RunResult, err error) RunResult {
	completed := time.Now().UTC()
	result.Observation.Provider = req.Provider
	result.Observation.Model = req.Model
	result.Observation.StartedAt = started.UTC().Format(time.RFC3339Nano)
	result.Observation.CompletedAt = completed.Format(time.RFC3339Nano)
	result.Observation.DurationMS = completed.Sub(started).Milliseconds()
	result.Observation.ExitCode = result.Code
	result.Observation.Usage = usageFromEvents(result.Stdout)
	if err != nil || result.Code != 0 {
		result.Observation.Status = "failed"
		result.Observation.ErrorClass = classifyFailureClass(result.Stderr, err)
	} else {
		result.Observation.Status = "completed"
	}
	return result
}

func usageFromEvents(raw string) RunUsage {
	usage := RunUsage{}
	var visit func(any)
	integer := func(value any) int64 {
		switch number := value.(type) {
		case float64:
			return int64(number)
		case json.Number:
			result, _ := number.Int64()
			return result
		default:
			return 0
		}
	}
	decimal := func(value any) float64 {
		switch number := value.(type) {
		case float64:
			return number
		case json.Number:
			result, _ := number.Float64()
			return result
		default:
			return 0
		}
	}
	visit = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			for key, child := range item {
				normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				switch normalized {
				case "input_tokens", "inputtokens":
					usage.InputTokens = max(usage.InputTokens, integer(child))
				case "output_tokens", "outputtokens":
					usage.OutputTokens = max(usage.OutputTokens, integer(child))
				case "cached_input_tokens", "cache_read_input_tokens", "cachedtokens":
					usage.CachedTokens = max(usage.CachedTokens, integer(child))
				case "total_tokens", "totaltokens":
					usage.TotalTokens = max(usage.TotalTokens, integer(child))
				case "total_cost_usd", "cost_usd", "estimated_cost_usd":
					usage.EstimatedCostUSD = max(usage.EstimatedCostUSD, decimal(child))
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		}
	}
	for _, event := range jsonEvents(raw) {
		visit(event)
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	return usage
}

func classifyFailureClass(stderr string, err error) string {
	value := strings.ToLower(stderr)
	if err != nil {
		value += " " + strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(value, "unauthorized"), strings.Contains(value, "not logged in"), strings.Contains(value, "authentication"):
		return "authentication"
	case strings.Contains(value, "rate limit"), strings.Contains(value, "too many requests"):
		return "rate_limit"
	case strings.Contains(value, "timeout"), strings.Contains(value, "deadline exceeded"):
		return "timeout"
	case strings.Contains(value, "permission denied"), strings.Contains(value, "operation not permitted"):
		return "permission"
	case strings.Contains(value, "not found"), strings.Contains(value, "no such file"):
		return "unavailable"
	case strings.TrimSpace(value) != "":
		return "provider"
	default:
		return ""
	}
}

func (p *Providers) Task(ctx context.Context, req RunRequest) (RunResult, error) {
	registration, ok := p.registrations[req.Provider]
	if !ok || !registration.descriptor.SupportsTask {
		return RunResult{}, fmt.Errorf("unknown provider: %s", req.Provider)
	}
	started := time.Now()
	result, err := registration.adapter.Task(ctx, req)
	return observeRun(req, started, result, err), err
}
func (p *Providers) Chat(ctx context.Context, req RunRequest) (RunResult, error) {
	registration, ok := p.registrations[req.Provider]
	if !ok || !registration.descriptor.SupportsChat {
		return RunResult{}, fmt.Errorf("unknown provider: %s", req.Provider)
	}
	started := time.Now()
	result, err := registration.adapter.Chat(ctx, req)
	return observeRun(req, started, result, err), err
}

func env(req RunRequest) map[string]string {
	return map[string]string{"AGENT_TAG_NAME": req.AgentName, "AGENT_TAG_ROOT": req.Root}
}
func executable(req RunRequest, fallback string) string {
	if strings.TrimSpace(req.Executable) != "" {
		return strings.TrimSpace(req.Executable)
	}
	return fallback
}
func effectivePermissions(req RunRequest) SkillPermissions {
	return normalizedSkillPermissions(req.AllowSkillExecution, req.SkillPermissions)
}
func splitArgs(value string) []string { return strings.Fields(value) }
func resultFromCommand(r CommandResult) RunResult {
	return RunResult{Code: r.Code, Stdout: r.Stdout, Stderr: r.Stderr}
}

func runCommand(ctx context.Context, runner CommandRunner, req RunRequest, name string, args []string, parse func(string) (string, []string)) CommandResult {
	streamer, ok := runner.(StreamingCommandRunner)
	if !ok || req.OnProgress == nil || parse == nil {
		return runner.Run(ctx, name, args, req.Root, req.Prompt, env(req))
	}
	var raw strings.Builder
	return streamer.RunStream(ctx, name, args, req.Root, req.Prompt, env(req), func(line string) {
		raw.WriteString(line)
		raw.WriteByte('\n')
		text, steps := parse(raw.String())
		if text != "" || len(steps) > 0 {
			req.OnProgress(RunProgress{Text: text, Steps: append([]string(nil), steps...)})
		}
	})
}

type claudeAdapter struct{ runner CommandRunner }

func (a claudeAdapter) Task(ctx context.Context, req RunRequest) (RunResult, error) {
	args := []string{"-p", "--output-format", "text", "--permission-mode", "acceptEdits"}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, splitArgs(req.ExtraArgs)...)
	r := resultFromCommand(a.runner.Run(ctx, "claude", args, req.Root, req.Prompt, env(req)))
	r.Outcome = ParseOutcome(r.Stdout)
	return r, nil
}
func (a claudeAdapter) Chat(ctx context.Context, req RunRequest) (RunResult, error) {
	permissionMode := "plan"
	permissions := effectivePermissions(req)
	if permissions.Shell || permissions.Network || permissions.Write {
		permissionMode = "acceptEdits"
	}
	args := []string{"-p", "--output-format", "stream-json", "--verbose", "--include-partial-messages", "--permission-mode", permissionMode}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	sid := req.SessionID
	if sid == nil {
		value := newUUID()
		sid = &value
		args = append(args, "--session-id", value)
	} else {
		args = append(args, "--resume", *sid)
	}
	args = append(args, splitArgs(req.ExtraArgs)...)
	cmd := runCommand(ctx, a.runner, req, executable(req, "claude"), args, ParseClaudeEvents)
	text, steps := ParseClaudeEvents(cmd.Stdout)
	r := resultFromCommand(cmd)
	r.Text = text
	r.Steps = steps
	r.SessionID = sid
	r.Artifacts = ParseClaudeArtifacts(cmd.Stdout)
	return r, nil
}

type codexAdapter struct{ runner CommandRunner }

func (a codexAdapter) Task(ctx context.Context, req RunRequest) (RunResult, error) {
	tmp, err := os.MkdirTemp("", "agent-tag-")
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(tmp)
	output := filepath.Join(tmp, "last.txt")
	args := []string{"exec", "--cd", req.Root, "--sandbox", "workspace-write", "--skip-git-repo-check", "-o", output}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, splitArgs(req.ExtraArgs)...)
	args = append(args, "-")
	cmd := a.runner.Run(ctx, "codex", args, req.Root, req.Prompt, env(req))
	final, _ := os.ReadFile(output)
	r := resultFromCommand(cmd)
	r.Text = strings.TrimSpace(string(final))
	r.Outcome = ParseOutcome(r.Text)
	return r, nil
}
func (a codexAdapter) Chat(ctx context.Context, req RunRequest) (RunResult, error) {
	tmp, err := os.MkdirTemp("", "agent-tag-chat-")
	if err != nil {
		return RunResult{}, err
	}
	defer os.RemoveAll(tmp)
	output := filepath.Join(tmp, "last.txt")
	var args []string
	if req.SessionID != nil {
		args = []string{"exec", "resume", "--skip-git-repo-check", "--json", "-o", output}
		if req.Model != "" {
			args = append(args, "--model", req.Model)
		}
		args = append(args, *req.SessionID, "-")
	} else {
		permissions := effectivePermissions(req)
		sandbox := "read-only"
		if permissions.Write {
			sandbox = "workspace-write"
		}
		args = []string{"exec", "--cd", req.Root, "--sandbox", sandbox, "--skip-git-repo-check", "--json", "-o", output}
		if permissions.Network && permissions.Write {
			args = append(args, "--config", "sandbox_workspace_write.network_access=true")
		}
		if req.Model != "" {
			args = append(args, "--model", req.Model)
		}
		args = append(args, "-")
	}
	extraArgs := splitArgs(req.ExtraArgs)
	if len(extraArgs) > 0 {
		last := args[len(args)-1]
		args = append(args[:len(args)-1], extraArgs...)
		args = append(args, last)
	}
	cmd := runCommand(ctx, a.runner, req, executable(req, "codex"), args, func(raw string) (string, []string) {
		text, steps, _ := ParseCodexEvents(raw, "")
		return text, steps
	})
	final, _ := os.ReadFile(output)
	text, steps, sid := ParseCodexEvents(cmd.Stdout, strings.TrimSpace(string(final)))
	if req.SessionID != nil {
		sid = req.SessionID
	}
	r := resultFromCommand(cmd)
	r.Text = text
	r.Steps = steps
	r.SessionID = sid
	return r, nil
}

type piAdapter struct{ runner CommandRunner }

func (a piAdapter) Task(ctx context.Context, req RunRequest) (RunResult, error) {
	args := []string{"-p", "--mode", "text", "--approve", "--name", req.AgentName}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, splitArgs(req.ExtraArgs)...)
	cmd := a.runner.Run(ctx, "pi", args, req.Root, req.Prompt, env(req))
	r := resultFromCommand(cmd)
	r.Outcome = ParseOutcome(r.Stdout)
	return r, nil
}
func (a piAdapter) Chat(ctx context.Context, req RunRequest) (RunResult, error) {
	sid := req.SessionID
	if sid == nil {
		v := newUUID()
		sid = &v
	}
	tools := "read,grep,find,ls"
	if effectivePermissions(req).Shell {
		tools += ",bash"
	}
	args := []string{"-p", "--mode", "json", "--approve", "--session-id", *sid, "--tools", tools, "--name", req.AgentName}
	for _, skillPath := range req.SkillPaths {
		args = append(args, "--skill", skillPath)
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	args = append(args, splitArgs(req.ExtraArgs)...)
	cmd := runCommand(ctx, a.runner, req, executable(req, "pi"), args, ParsePiEvents)
	text, steps := ParsePiEvents(cmd.Stdout)
	r := resultFromCommand(cmd)
	r.Text = text
	r.Steps = steps
	r.SessionID = sid
	return r, nil
}

type commandAdapter struct{ runner CommandRunner }

func (a commandAdapter) Task(ctx context.Context, req RunRequest) (RunResult, error) {
	parts := splitArgs(req.Command)
	if len(parts) == 0 {
		return RunResult{}, errors.New("custom command provider requires --command")
	}
	cmd := a.runner.Run(ctx, parts[0], append(parts[1:], splitArgs(req.ExtraArgs)...), req.Root, req.Prompt, env(req))
	r := resultFromCommand(cmd)
	r.Outcome = ParseOutcome(r.Stdout)
	return r, nil
}
func (a commandAdapter) Chat(ctx context.Context, req RunRequest) (RunResult, error) {
	parts := splitArgs(req.Command)
	if len(parts) == 0 {
		return RunResult{}, errors.New("custom command provider requires a command")
	}
	parse := func(raw string) (string, []string) { return strings.TrimSpace(raw), nil }
	cmd := runCommand(ctx, a.runner, req, parts[0], append(parts[1:], splitArgs(req.ExtraArgs)...), parse)
	result := resultFromCommand(cmd)
	result.Text = strings.TrimSpace(cmd.Stdout)
	return result, nil
}

func ParseOutcome(text string) *Outcome {
	idx := strings.LastIndex(text, resultPrefix)
	if idx < 0 {
		return nil
	}
	line := strings.Split(strings.TrimSpace(text[idx+len(resultPrefix):]), "\n")[0]
	var raw struct{ Status, Summary string }
	if json.Unmarshal([]byte(line), &raw) != nil || (raw.Status != "completed" && raw.Status != "blocked") || raw.Summary == "" {
		return nil
	}
	return &Outcome{raw.Status, raw.Summary}
}

func jsonEvents(text string) []map[string]any {
	var events []map[string]any
	s := bufio.NewScanner(strings.NewReader(text))
	for s.Scan() {
		var e map[string]any
		if json.Unmarshal(s.Bytes(), &e) == nil {
			events = append(events, e)
		}
	}
	return events
}
func short(v any) string {
	s := strings.Join(strings.Fields(fmt.Sprint(v)), " ")
	if len(s) > 160 {
		return s[:159] + "…"
	}
	return s
}
func toolStep(name string, input map[string]any) string {
	labels := map[string]string{"Read": "读取文件", "read": "读取文件", "Grep": "搜索代码", "grep": "搜索代码", "Glob": "查找文件", "find": "查找文件", "ls": "浏览目录", "Bash": "执行命令", "bash": "执行命令", "Edit": "编辑文件", "edit": "编辑文件", "Write": "写入文件", "write": "写入文件", "WebSearch": "搜索网页", "web_search": "搜索网页", "WebFetch": "读取网页"}
	label := labels[name]
	if label == "" {
		label = "调用 " + name
	}
	for _, k := range []string{"file_path", "path", "pattern", "query", "command", "cmd"} {
		if v := input[k]; v != nil {
			return label + "：" + short(v)
		}
	}
	return label
}
func addStep(steps *[]string, step string) {
	for _, existing := range *steps {
		if existing == step {
			return
		}
	}
	*steps = append(*steps, step)
}
func mapAt(v any) map[string]any { m, _ := v.(map[string]any); return m }
func sliceAt(v any) []any        { s, _ := v.([]any); return s }
func strAt(v any) string         { s, _ := v.(string); return s }
func messageText(m map[string]any) string {
	if t := strAt(m["text"]); t != "" {
		return t
	}
	var out []string
	for _, v := range sliceAt(m["content"]) {
		b := mapAt(v)
		if strAt(b["type"]) == "text" {
			out = append(out, strAt(b["text"]))
		}
	}
	return strings.Join(out, "\n")
}
func ParseClaudeEvents(raw string) (string, []string) {
	events := jsonEvents(raw)
	if len(events) == 0 {
		return strings.TrimSpace(raw), nil
	}
	var text string
	var streamedText strings.Builder
	var steps []string
	for _, e := range events {
		if strAt(e["type"]) == "stream_event" {
			event := mapAt(e["event"])
			if strAt(event["type"]) == "content_block_delta" {
				delta := mapAt(event["delta"])
				if strAt(delta["type"]) == "text_delta" {
					streamedText.WriteString(strAt(delta["text"]))
				}
			}
		}
		if strAt(e["type"]) == "assistant" {
			m := mapAt(e["message"])
			for _, v := range sliceAt(m["content"]) {
				b := mapAt(v)
				if strAt(b["type"]) == "tool_use" {
					steps = append(steps, toolStep(strAt(b["name"]), mapAt(b["input"])))
				}
				if strAt(b["type"]) == "text" && strAt(b["text"]) != "" {
					text = strAt(b["text"])
				}
			}
		}
		if strAt(e["type"]) == "result" && strAt(e["result"]) != "" {
			text = strAt(e["result"])
		}
	}
	if text == "" {
		text = streamedText.String()
	}
	return strings.TrimSpace(text), steps
}

var persistedClaudeOutputPattern = regexp.MustCompile(`(?m)Full output saved to:\s*(/[^\r\n]+)`)

func ParseClaudeArtifacts(raw string) []RunArtifact {
	result := []RunArtifact{}
	seen := map[string]bool{}
	for _, event := range jsonEvents(raw) {
		if strAt(event["type"]) != "user" {
			continue
		}
		message := mapAt(event["message"])
		for _, value := range sliceAt(message["content"]) {
			block := mapAt(value)
			if strAt(block["type"]) != "tool_result" {
				continue
			}
			for _, match := range persistedClaudeOutputPattern.FindAllStringSubmatch(strAt(block["content"]), -1) {
				if len(match) < 2 || seen[match[1]] {
					continue
				}
				seen[match[1]] = true
				result = append(result, RunArtifact{Path: match[1], Label: "Claude 工具输出"})
			}
		}
	}
	return result
}
func ParseCodexEvents(raw, final string) (string, []string, *string) {
	text := strings.TrimSpace(final)
	var steps []string
	var sid *string
	for _, e := range jsonEvents(raw) {
		typ := strAt(e["type"])
		if typ == "turn.started" {
			addStep(&steps, "开始分析问题")
		}
		if typ == "turn.completed" {
			addStep(&steps, "完成分析并组织回复")
		}
		if typ == "thread.started" {
			v := strAt(e["thread_id"])
			if v == "" {
				v = strAt(e["threadId"])
			}
			if v != "" {
				sid = &v
			}
		}
		if typ != "item.completed" {
			continue
		}
		item := mapAt(e["item"])
		switch strAt(item["type"]) {
		case "agent_message":
			if strAt(item["text"]) != "" {
				text = strAt(item["text"])
			}
		case "reasoning":
			steps = append(steps, "完成一轮分析")
		case "command_execution":
			steps = append(steps, toolStep("Bash", map[string]any{"command": item["command"]}))
		case "file_change":
			steps = append(steps, "检查文件变更")
		case "mcp_tool_call":
			steps = append(steps, toolStep(strAt(item["tool"]), mapAt(item["arguments"])))
		case "web_search":
			steps = append(steps, toolStep("web_search", map[string]any{"query": item["query"]}))
		}
	}
	return text, steps, sid
}
func ParsePiEvents(raw string) (string, []string) {
	events := jsonEvents(raw)
	if len(events) == 0 {
		return strings.TrimSpace(raw), nil
	}
	var text string
	var steps []string
	for _, e := range events {
		switch strAt(e["type"]) {
		case "agent_start":
			addStep(&steps, "开始分析问题")
		case "tool_execution_start":
			name := strAt(e["toolName"])
			if name == "" {
				name = strAt(e["tool_name"])
			}
			steps = append(steps, toolStep(name, mapAt(e["args"])))
		case "message_end":
			m := mapAt(e["message"])
			if strAt(m["role"]) == "assistant" {
				text = messageText(m)
			}
		case "agent_end":
			msgs := sliceAt(e["messages"])
			for i := len(msgs) - 1; i >= 0; i-- {
				m := mapAt(msgs[i])
				if strAt(m["role"]) == "assistant" {
					text = messageText(m)
					break
				}
			}
			addStep(&steps, "完成分析并组织回复")
		}
	}
	return strings.TrimSpace(text), steps
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func BuildTaskPrompt(agent Agent, teamName string, task Task, inbox []MailMessage, root, cli string) string {
	var msgs []string
	for _, m := range inbox {
		msgs = append(msgs, "- "+m.From+": "+m.Body)
	}
	if len(msgs) == 0 {
		msgs = []string{"- (none)"}
	}
	scopes := strings.Join(task.Scopes, ", ")
	if scopes == "" {
		scopes = "not specified; inspect before editing"
	}
	return fmt.Sprintf(`You are %s, a member of the local agent team %q.

Task %s: %s
%s

Workspace: %s
Owned file scopes: %s

Unread teammate messages:
%s

Collaboration commands:
- Send a message: %s message send --to <agent-or-all> --body <text>
- Read messages: %s inbox --agent %s
- Inspect board: %s status --json

Work only on this task. Respect other agents' file scopes. Verify your work.
Your final line MUST be exactly:
%s {"status":"completed","summary":"what changed and verification performed"}
or
%s {"status":"blocked","summary":"specific blocker"}`, agent.Name, teamName, task.ID, task.Title, task.Description, root, scopes, strings.Join(msgs, "\n"), cli, cli, agent.Name, cli, resultPrefix, resultPrefix)
}
