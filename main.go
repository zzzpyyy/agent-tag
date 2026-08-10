package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"agent-tag/internal/tag"
)

//go:embed web/*
var webFiles embed.FS

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-tag:", err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		help()
		return nil
	}
	switch args[0] {
	case "init":
		return initCmd(args[1:])
	case "task":
		if len(args) < 2 {
			return errors.New("use task add or task retry")
		}
		if args[1] == "add" {
			return taskAdd(args[2:])
		}
		if args[1] == "retry" {
			return taskRetry(args[2:])
		}
	case "status":
		return statusCmd(args[1:])
	case "message":
		if len(args) > 1 && args[1] == "send" {
			return messageSend(args[2:])
		}
	case "inbox":
		return inboxCmd(args[1:])
	case "worker", "join":
		return workerCmd(args[1:])
	case "web":
		return webCmd(args[1:])
	}
	return errors.New("unknown command; run agent-tag --help")
}
func help() {
	fmt.Print(`agent-tag — provider-neutral local agent teams (Go)

Usage:
  agent-tag init [--name TEAM]
  agent-tag task add TITLE [--description TEXT] [--depends ID,ID] [--scopes PATH,PATH]
  agent-tag task retry TASK_ID
  agent-tag worker --name NAME --provider claude|codex|pi|command [--model MODEL] [--once]
  agent-tag message send --from NAME --to NAME|all --body TEXT
  agent-tag inbox --agent NAME [--peek]
  agent-tag status [--json]
  agent-tag web [--port 4317] [--host 127.0.0.1]
`)
}
func rootStore() (*tag.Store, error) {
	cwd, _ := os.Getwd()
	root, err := tag.FindRoot(cwd)
	if err != nil {
		return nil, err
	}
	return &tag.Store{Root: root}, nil
}
func initCmd(args []string) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	name := f.String("name", "", "team name")
	force := f.Bool("force", false, "overwrite")
	if err := f.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	if *name == "" {
		*name = filepath.Base(cwd)
	}
	if err := tag.Init(cwd, *name, *force); err != nil {
		return err
	}
	fmt.Printf("Initialized team %s at %s\n", *name, tag.StatePath(cwd))
	return nil
}

func popOptions(args []string) (map[string]string, []string, error) {
	opts := map[string]string{}
	var rest []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "--") {
			key := args[i]
			if key == "--once" || key == "--peek" || key == "--json" {
				opts[key] = "true"
				continue
			}
			if i+1 >= len(args) {
				return nil, nil, fmt.Errorf("%s requires a value", key)
			}
			i++
			opts[key] = args[i]
		} else {
			rest = append(rest, args[i])
		}
	}
	return opts, rest, nil
}
func csv(s string) []string {
	if s == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	out := []string{}
	for _, p := range parts {
		if v := strings.Trim(strings.TrimSpace(p), "./"); v != "" {
			out = append(out, v)
		}
	}
	return out
}
func taskAdd(args []string) error {
	opts, rest, err := popOptions(args)
	if err != nil {
		return err
	}
	title := strings.TrimSpace(strings.Join(rest, " "))
	if title == "" {
		return errors.New("task title is required")
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	var task tag.Task
	err = store.Update(func(st *tag.State) error {
		deps := csv(opts["--depends"])
		for _, id := range deps {
			found := false
			for _, t := range st.Tasks {
				if t.ID == id {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("unknown dependency: %s", id)
			}
		}
		now := tag.Now()
		task = tag.Task{ID: tag.NextID(st, "task"), Title: title, Description: opts["--description"], Depends: deps, Scopes: csv(opts["--scopes"]), Status: "pending", CreatedAt: now, UpdatedAt: now}
		st.Tasks = append(st.Tasks, task)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("%s created: %s\n", task.ID, task.Title)
	return nil
}
func taskRetry(args []string) error {
	if len(args) == 0 {
		return errors.New("task id is required")
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	return store.Update(func(st *tag.State) error {
		for i := range st.Tasks {
			if st.Tasks[i].ID == args[0] {
				if st.Tasks[i].Status == "in_progress" {
					return errors.New("task is still in progress")
				}
				st.Tasks[i].Status = "pending"
				st.Tasks[i].Assignee = nil
				st.Tasks[i].UpdatedAt = tag.Now()
				fmt.Println(args[0], "requeued")
				return nil
			}
		}
		return errors.New("unknown task")
	})
}
func statusCmd(args []string) error {
	opts, _, _ := popOptions(args)
	store, err := rootStore()
	if err != nil {
		return err
	}
	st, err := store.Read()
	if err != nil {
		return err
	}
	if opts["--json"] == "true" {
		raw, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(raw))
		return nil
	}
	fmt.Println("Team:", st.Team.Name, "\n\nAgents:")
	if len(st.Agents) == 0 {
		fmt.Println("  (none)")
	}
	for _, a := range st.Agents {
		task := "-"
		if a.CurrentTask != nil {
			task = *a.CurrentTask
		}
		fmt.Printf("  %-18s %-8s %-8s %s\n", a.Name, a.Provider, a.Status, task)
	}
	fmt.Println("\nTasks:")
	if len(st.Tasks) == 0 {
		fmt.Println("  (none)")
	}
	for _, t := range st.Tasks {
		who := "-"
		if t.Assignee != nil {
			who = *t.Assignee
		}
		fmt.Printf("  %s  %-11s %-18s %s\n", t.ID, t.Status, who, t.Title)
	}
	return nil
}
func messageSend(args []string) error {
	opts, _, err := popOptions(args)
	if err != nil {
		return err
	}
	to, body := opts["--to"], opts["--body"]
	from := opts["--from"]
	if from == "" {
		from = os.Getenv("AGENT_TAG_NAME")
		if from == "" {
			from = "user"
		}
	}
	if to == "" || body == "" {
		return errors.New("--to and --body are required")
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	var id string
	err = store.Update(func(st *tag.State) error {
		if to != "all" {
			found := false
			for _, a := range st.Agents {
				if a.Name == to {
					found = true
				}
			}
			if !found {
				return errors.New("unknown recipient")
			}
		}
		id = tag.NextID(st, "msg")
		st.Messages = append(st.Messages, tag.MailMessage{ID: id, From: from, To: to, Body: body, CreatedAt: tag.Now(), ReadBy: []string{}})
		return nil
	})
	if err == nil {
		fmt.Printf("%s sent to %s\n", id, to)
	}
	return err
}
func inboxCmd(args []string) error {
	opts, _, err := popOptions(args)
	if err != nil {
		return err
	}
	name := opts["--agent"]
	if name == "" {
		name = os.Getenv("AGENT_TAG_NAME")
	}
	if name == "" {
		return errors.New("--agent is required")
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	var msgs []tag.MailMessage
	err = store.Update(func(st *tag.State) error {
		for i := range st.Messages {
			m := &st.Messages[i]
			read := false
			for _, n := range m.ReadBy {
				if n == name {
					read = true
				}
			}
			if (m.To == name || m.To == "all") && !read {
				msgs = append(msgs, *m)
				if opts["--peek"] != "true" {
					m.ReadBy = append(m.ReadBy, name)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(msgs) == 0 {
		fmt.Println("No unread messages.")
	}
	for _, m := range msgs {
		fmt.Printf("%s  %s -> %s: %s\n", m.ID, m.From, m.To, m.Body)
	}
	return nil
}

func workerCmd(args []string) error {
	opts, _, err := popOptions(args)
	if err != nil {
		return err
	}
	name, provider := opts["--name"], opts["--provider"]
	if name == "" {
		return errors.New("--name is required")
	}
	if provider == "" {
		provider = "claude"
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	lease := 30 * time.Second
	once := opts["--once"] == "true"
	providers := tag.NewProviders(tag.OSRunner{})
	var agent tag.Agent
	teamName := "team"
	err = store.Update(func(st *tag.State) error {
		teamName = st.Team.Name
		now := tag.Now()
		idx := -1
		for i := range st.Agents {
			if st.Agents[i].Name == name {
				idx = i
			}
		}
		if idx < 0 {
			agent = tag.Agent{ID: tag.NextID(st, "agent"), Name: name, JoinedAt: now}
			st.Agents = append(st.Agents, agent)
			idx = len(st.Agents) - 1
		}
		a := &st.Agents[idx]
		a.Provider = provider
		a.Status = "online"
		a.HeartbeatAt = now
		if v := opts["--model"]; v != "" {
			a.Model = &v
		}
		if v := opts["--command"]; v != "" {
			a.Command = &v
		}
		agent = *a
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("[%s] joined using %s\n", name, provider)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer store.Update(func(st *tag.State) error {
		for i := range st.Agents {
			if st.Agents[i].Name == name {
				st.Agents[i].Status = "offline"
				st.Agents[i].CurrentTask = nil
				st.Agents[i].HeartbeatAt = tag.Now()
			}
		}
		return nil
	})
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		task, err := claim(store, name, lease)
		if err != nil {
			return err
		}
		if task == nil {
			if once {
				return nil
			}
			time.Sleep(2 * time.Second)
			continue
		}
		inbox := takeInbox(store, name)
		model, command := "", ""
		if agent.Model != nil {
			model = *agent.Model
		}
		if agent.Command != nil {
			command = *agent.Command
		}
		prompt := tag.BuildTaskPrompt(agent, teamName, *task, inbox, store.Root, os.Args[0])
		var runID string
		_ = store.Update(func(st *tag.State) error {
			runID = tag.NextID(st, "run")
			st.TaskRuns = append(st.TaskRuns, tag.TaskRun{ID: runID, TaskID: task.ID, Agent: name, Provider: provider, StartedAt: tag.Now(), Status: "in_progress"})
			return nil
		})
		taskCtx, taskCancel := context.WithCancel(ctx)
		heartbeatDone := make(chan struct{})
		go heartbeat(taskCtx, store, name, task.ID, taskCancel, heartbeatDone)
		result, runErr := providers.Task(taskCtx, tag.RunRequest{Provider: provider, Model: model, ExtraArgs: opts["--extra-args"], Command: command, Prompt: prompt, Root: store.Root, AgentName: name})
		close(heartbeatDone)
		taskCancel()
		status, summary, lastErr := "blocked", "", fmt.Sprintf("provider exit=%d; missing valid result marker", result.Code)
		if runErr != nil {
			lastErr = runErr.Error()
		} else if result.Code == 0 && result.Outcome != nil {
			status = result.Outcome.Status
			summary = result.Outcome.Summary
			lastErr = ""
		}
		_ = finish(store, name, task.ID, runID, status, summary, lastErr, result)
		fmt.Printf("[%s] %s -> %s\n", name, task.ID, status)
		if once {
			return nil
		}
	}
}

func heartbeat(ctx context.Context, store *tag.Store, name, taskID string, cancel context.CancelFunc, done <-chan struct{}) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-ticker.C:
			_ = store.Update(func(st *tag.State) error {
				for _, task := range st.Tasks {
					if task.ID == taskID && task.Status == "cancel_requested" {
						cancel()
					}
				}
				for i := range st.Agents {
					if st.Agents[i].Name == name {
						st.Agents[i].HeartbeatAt = tag.Now()
					}
				}
				return nil
			})
		}
	}
}
func claim(store *tag.Store, name string, lease time.Duration) (*tag.Task, error) {
	var claimed *tag.Task
	err := store.Update(func(st *tag.State) error {
		now := time.Now()
		stale := map[string]bool{}
		for i := range st.Agents {
			a := &st.Agents[i]
			t, _ := time.Parse(time.RFC3339Nano, a.HeartbeatAt)
			if a.Status == "online" && now.Sub(t) > lease {
				a.Status = "offline"
				stale[a.Name] = true
			}
			if a.Name == name {
				a.HeartbeatAt = tag.Now()
			}
		}
		for i := range st.Tasks {
			t := &st.Tasks[i]
			if (t.Status == "in_progress" || t.Status == "cancel_requested") && t.Assignee != nil && stale[*t.Assignee] {
				if t.Status == "cancel_requested" {
					t.Status = "canceled"
				} else {
					t.Status = "pending"
				}
				t.Assignee = nil
			}
		}
		done := map[string]bool{}
		var active []tag.Task
		for _, t := range st.Tasks {
			if t.Status == "completed" {
				done[t.ID] = true
			}
			if t.Status == "in_progress" || t.Status == "cancel_requested" {
				active = append(active, t)
			}
		}
		for i := range st.Tasks {
			t := &st.Tasks[i]
			if t.Status != "pending" {
				continue
			}
			ok := true
			for _, d := range t.Depends {
				if !done[d] {
					ok = false
				}
			}
			for _, a := range active {
				if tag.ScopesOverlap(t.Scopes, a.Scopes) {
					ok = false
				}
			}
			if ok {
				t.Status = "in_progress"
				t.Assignee = tag.StringPtr(name)
				t.Attempts++
				t.UpdatedAt = tag.Now()
				for j := range st.Agents {
					if st.Agents[j].Name == name {
						st.Agents[j].CurrentTask = &t.ID
					}
				}
				copy := *t
				claimed = &copy
				break
			}
		}
		return nil
	})
	return claimed, err
}
func takeInbox(store *tag.Store, name string) []tag.MailMessage {
	var out []tag.MailMessage
	_ = store.Update(func(st *tag.State) error {
		for i := range st.Messages {
			m := &st.Messages[i]
			read := false
			for _, n := range m.ReadBy {
				if n == name {
					read = true
				}
			}
			if (m.To == name || m.To == "all") && !read {
				out = append(out, *m)
				m.ReadBy = append(m.ReadBy, name)
			}
		}
		return nil
	})
	return out
}
func finish(store *tag.Store, name, id, runID, status, summary, lastErr string, result tag.RunResult) error {
	return store.Update(func(st *tag.State) error {
		for i := range st.Tasks {
			if st.Tasks[i].ID == id {
				if st.Tasks[i].Status == "cancel_requested" {
					status = "canceled"
					summary = "任务已由用户取消"
					lastErr = ""
				}
				st.Tasks[i].Status = status
				st.Tasks[i].UpdatedAt = tag.Now()
				if summary != "" {
					st.Tasks[i].Summary = tag.StringPtr(summary)
				}
				if lastErr != "" {
					st.Tasks[i].LastError = tag.StringPtr(lastErr)
				} else {
					st.Tasks[i].LastError = nil
				}
			}
		}
		for index := range st.TaskRuns {
			run := &st.TaskRuns[index]
			if run.ID == runID {
				run.CompletedAt = tag.Now()
				run.Status = status
				run.ExitCode = result.Code
				run.Stdout = tag.TruncateForLog(result.Stdout, 20000)
				run.Stderr = tag.TruncateForLog(result.Stderr, 20000)
				run.Summary = summary
			}
		}
		for i := range st.Agents {
			if st.Agents[i].Name == name {
				st.Agents[i].CurrentTask = nil
				st.Agents[i].HeartbeatAt = tag.Now()
			}
		}
		return nil
	})
}

func webCmd(args []string) error {
	f := flag.NewFlagSet("web", flag.ContinueOnError)
	host := f.String("host", "127.0.0.1", "")
	port := f.Int("port", 4317, "")
	if err := f.Parse(args); err != nil {
		return err
	}
	store, err := rootStore()
	if err != nil {
		return err
	}
	if err := store.Update(func(*tag.State) error { return nil }); err != nil {
		return err
	}
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return err
	}
	registry := tag.NewSkillRegistry(store.Root, tag.DefaultSkillRoots(store.Root))
	server := &http.Server{Addr: fmt.Sprintf("%s:%d", *host, *port), Handler: tag.NewWebServerWithSkillRegistry(store, tag.NewProviders(tag.OSRunner{}), assets, registry).Handler(), ReadHeaderTimeout: 5 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Printf("agent-tag web is ready at http://%s:%d\n", *host, *port)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
