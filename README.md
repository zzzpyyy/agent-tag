# agent-tag

`agent-tag` 是一个以 Go 为协调层的本地多 Agent 工具。它通过公开 CLI 驱动 Claude Code、Codex CLI 和 Pi，并提供共享任务板、邮箱与 Web 群聊；不依赖任何产品的私有 Team 协议。

前端仍使用浏览器原生 HTML/CSS/JavaScript，后端、CLI、状态存储、Agent 调度、会话管理和 SSE 推送均由 Go 实现。现有 `.agent-tag/state.json` 格式保持兼容。

## 构建与启动

需要 Go 1.23 或更高版本，并安装至少一个 Agent CLI：`claude`、`codex` 或 `pi`。

```bash
make build
./bin/agent-tag init --name demo   # 已初始化的项目可跳过
./bin/agent-tag web
```

打开 `http://127.0.0.1:4317`，先注册或登录本地账号。升级前已有的未归属对话会由第一个注册账号接管。不同账号只能看到自己的对话、消息、Agent 会话和设置；登录态保存在 HttpOnly Cookie 中，密码使用带随机盐的 PBKDF2-SHA256 哈希保存。

对话支持重命名、归档、恢复和永久删除；永久删除会同时清理该对话的消息与共享产物。群成员可以修改名称、Provider、模型和自动参与策略，也可以单独重置原生 CLI 会话或从对话中移除。为避免运行中状态错乱，这些操作会在当前回复或评议进行时被拒绝。

首次消息需要 `@cc`、`@codex`、其他成员名或 `@all`；输入区提供可直接点击的 `@all`。新建对话时可以选择本次参与的 Agent，也可以把该组合保存为账号之后新对话的默认模板。任一 Agent 成功回复后可开启“并行抢答”，后续普通消息无需继续显示 `@`。

每轮回答结束后可以手动选择“综合这些观点”或“互相评议”。互评完成后按钮会再次出现，可以继续下一轮。全局设置为当前账号的新会话提供默认值；会话设置只覆盖当前对话。两处都可以配置并行抢答、自动互相评议和 1–5 轮评议；每一轮只读取紧邻的上一轮观点，停止按钮会取消整个自动循环。

每个 Web 对话中的每个成员都有独立的原生 CLI 会话：

- Claude Code：首次使用 `--session-id`，后续使用 `--resume`；
- Codex：保存 `thread.started` 返回的线程 ID，后续使用 `codex exec resume`；
- Pi：持续复用同一个 `--session-id`。

Agent 回复支持 Markdown。折叠的“思考与执行”区域只展示文件读取、搜索、命令和工具调用等可观察事件，不保存或展示模型的隐藏思维链。

CLI 运行期间，Web 页面会通过 SSE 实时显示已产生的回答文本和工具步骤。并行抢答或互评时，每个运行中的 Agent 都有独立的“停止此 Agent”操作；单独停止不会取消同轮其他 Agent，输入框中的总停止按钮仍会取消整轮。

设置页会检测 Claude Code、Codex CLI 和 Pi 的安装路径与版本。安装属于宿主机操作，不设置应用内“管理员”角色：是否可安装由进程的操作系统/npm 权限决定，宿主机运维可设置 `AGENT_TAG_DISABLE_PROVIDER_INSTALL=1` 完全关闭安装入口。用户取消、安装失败或仍未配置到可执行路径时，该 Agent 会标记为不可用，服务端也会阻止它参与对话。常见的未安装、未登录、权限不足、超时和限流错误会被归类为可操作的提示。

每个账号可为三类 Provider 配置 CLI 路径、可选启动命令、附加参数和 10–3600 秒调用超时，配置会在下一次 Web Agent 调用时生效。启动命令通过用户的登录 shell 执行，因此可填写 `cccc` 这类 alias；它的优先级高于 CLI 路径。侧边栏的“任务看板”展示共享团队任务与 Worker 在线状态，支持创建带依赖和文件作用域的任务、重试失败任务，以及安全删除未运行且未被依赖的任务。

任务看板还支持编辑未运行任务、检测循环依赖、取消运行中的任务，以及查看最近一次运行的 stdout/stderr 日志。Git 项目中的每次任务运行会创建 `.agent-tag/worktrees/<run-id>` 和独立 `agent-tag/<run-id>` 分支；Worker 完成后自动固化改动，页面显示 diff 摘要，并提供显式“合并”或“丢弃分支”闭环。合并前主工作区必须干净。非 Git 目录继续使用共享根目录。

聊天消息和任务运行记录使用 `.agent-tag/data.sqlite` 持久化，首次读取时会从旧 `state.json` 自动迁移；其余元数据继续保留兼容的 JSON 格式。运行路径不再读取并重写全量集合，而是使用按会话分页查询、按任务查询及事务批量 upsert。SQLite 使用 WAL 和完整同步。Web 首屏每个对话只返回最近 100 条消息，更早记录可按页加载。

每次 Provider 调用都会记录模型、开始/完成时间、耗时、退出码、错误类别、Token 使用量和 Provider 返回的估算成本。会话可设置累计 Token/美元预算，达到阈值后新的 Agent 调用会被阻止。若 CLI 不返回 usage/cost，相应数值保持为 0，不会伪造费用。

共享产物具有独立 SQLite 索引、租户所有权、媒体类型、大小和 SHA-256；Agent 运行期间新增或修改的文档、图片、表格和压缩包会由所有 Provider 统一发现并登记，侧边栏“产物中心”支持浏览、下载和删除，消息中的产物可直接下载。设置页仍可创建最多保留 10 份的一致性备份，并清理已删除对话遗留的孤立目录。

Skill 执行权限可按全局默认或单个会话分别控制 Shell、网络和工作区写入；权限变化会重建该会话的 Agent Session。Codex 使用 `read-only` 或 `workspace-write` 最小沙箱，不再使用 `danger-full-access`；Pi 仅在 Shell 开启时暴露 `bash`。Claude 原生权限模式粒度较粗，因此同时注入明确的权限边界。权限修改、Skill 加载、Agent 成功和失败都会写入 SQLite 审计表，并可在设置页按当前账号查看。

## Skills 管理

登录后打开侧边栏的 **Skills** Tab，可以使用三类来源，并在同一页面按当前会话分别分配给 Claude Code、Codex CLI 和 Pi：

- **本机 Skill（只读）**：自动发现工作区及用户目录中的 `.agents/skills`、`.codex/skills`、`.claude/skills`、`.pi/agent/skills` 和 `.pi/skills`；
- **ZIP 导入**：上传包含一个或多个 `SKILL.md` 的完整 Skill 包，保留其 `scripts/`、`references/`、`assets/` 等相对资源；
- **托管 Skill**：直接在页面创建、编辑纯文本 `SKILL.md` 指令。

ZIP 导入内容保存在 `.agent-tag/skills/<账号 ID>/`，仅导入账号可见和删除。导入器会拒绝目录穿越、符号链接、特殊文件、过多文件及超量解压。本机 Skill 属于当前机器的只读能力目录，因此同一台机器上的登录账号均可发现，但不能从页面修改或删除。

服务端会按账号、会话和 Agent 解析分配，并在每次 CLI 调用时注入完整指令及 `SKILL.md` 的绝对位置，因此：

- 不同账号看不到彼此创建或导入的 Skill；
- 同一会话中的不同 Agent 可以使用不同 Skill；
- Claude、Codex 和 Pi 可以使用同一份 Skill；
- 更新 Skill 后，下一轮回复自动使用新内容；
- 消息的“思考与执行”区域会显示本轮加载的 Skill。
- Claude 将过大的工具输出保存为文件时，Agent Tag 会把该文件复制到租户会话的 `.agent-tag/artifacts/` 并登记到产物中心；后续 Agent 会通过受控索引解析本地路径并优先读取。

设置页提供两种 Skill 选择方式，并同时支持全局默认和会话级覆盖。自动匹配只读取最近 3 条用户消息，不会让系统提示、错误信息或 Agent 回复中的通用词造成误命中：

- **固定绑定**：只加载 Skills 页面为该 Agent 勾选的项目；
- **自动匹配（默认）**：根据当前问题及最近会话中的 Skill 名称、描述、域名和触发词选择最多 3 个 Skill，同时保留手工固定项；升级前未记录该字段的会话也按自动匹配处理。

Skill 权限按 Shell、网络与工作区写入分别控制。Claude 使用其原生权限模式，Codex 在 `read-only` 与 `workspace-write` 间切换，Pi 只在 Shell 开启时暴露 `bash`；权限变化会重建 Agent 原生 Session。

本机和 ZIP Skill 的关联文件会原样保留，Agent 可按 `SKILL.md` 所在目录解析相对路径；脚本执行、MCP 和其他工具仍受对应 CLI 自身的权限与沙箱限制，不会因分配 Skill 而被隐式授权。托管 Skill 继续适合无需附属文件的纯指令工作流。

## 任务协作

```bash
./bin/agent-tag task add "Implement API" \
  --description "Add endpoints and tests" \
  --scopes internal/api,test/api

# 在不同终端启动，可随时加入或退出
./bin/agent-tag worker --name claude-api --provider claude --model sonnet
./bin/agent-tag worker --name codex-ui --provider codex
./bin/agent-tag worker --name pi-reviewer --provider pi

./bin/agent-tag status
./bin/agent-tag message send --to all --body "完成前运行完整测试"
```

使用 `--once` 让 worker 最多领取一个任务。只有 Agent 输出合法的 `AGENT_TAG_RESULT` 行时任务才会完成，否则任务会进入 `blocked`，可用以下命令重试：

```bash
./bin/agent-tag task retry task-0001
```

## 接入更多 Agent

任务 worker 的稳定扩展点是通用命令适配器：

```bash
./bin/agent-tag worker --name local-agent --provider command \
  --command "my-agent --headless"
```

命令在该任务的隔离工作区运行（非 Git 项目为团队根目录），完整任务通过 stdin 传入，并可读取：

- `AGENT_TAG_NAME`：当前 Agent 名称；
- `AGENT_TAG_ROOT`：团队工作区绝对路径。

成功时最终输出：

```text
AGENT_TAG_RESULT: {"status":"completed","summary":"implemented X; tests pass"}
```

阻塞时最终输出：

```text
AGENT_TAG_RESULT: {"status":"blocked","summary":"specific blocker"}
```

Provider 通过能力描述符注册，声明 task/chat/session/streaming 能力。通用 `command` Provider 已同时支持任务和 Web 对话；新增 Provider 只需实现 `Provider` 接口并向注册表登记，Web API 与 Agent 选择器会从注册表发现它。

## 验证与安全

```bash
make test
go vet ./...
```

Claude 任务 worker 使用 `acceptEdits`，Codex 使用 `workspace-write`；Web 对话默认只读，Skill 权限可显式提升。状态元数据通过目录锁和原子重命名写入，高频消息与运行记录由 SQLite 增量维护。任务级 Git worktree 是编辑隔离的主要边界，`--scopes` 仍用于调度冲突检测。
