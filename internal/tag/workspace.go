package tag

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type TaskWorkspace struct {
	Root              string
	Workspace         string
	Branch            string
	BaseCommit        string
	HeadCommit        string
	DiffStat          string
	IntegrationStatus string
}

type WorkspaceManager interface {
	Prepare(context.Context, string) (TaskWorkspace, error)
	Finalize(context.Context, TaskWorkspace, string) (TaskWorkspace, error)
	Integrate(context.Context, TaskWorkspace) error
	Discard(context.Context, TaskWorkspace) error
}

type GitWorkspaceManager struct{ Root string }

var workspaceNamePattern = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func workspaceName(value string) string {
	value = strings.Trim(workspaceNamePattern.ReplaceAllString(value, "-"), "-")
	if value == "" {
		return "run"
	}
	return value
}

func (m GitWorkspaceManager) git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func (m GitWorkspaceManager) Prepare(ctx context.Context, runID string) (TaskWorkspace, error) {
	root, err := filepath.Abs(m.Root)
	if err != nil {
		return TaskWorkspace{}, err
	}
	if _, err := m.git(ctx, root, "rev-parse", "--show-toplevel"); err != nil {
		return TaskWorkspace{Root: root, Workspace: root, IntegrationStatus: "shared"}, nil
	}
	base, err := m.git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return TaskWorkspace{}, err
	}
	name := workspaceName(runID)
	branch := "agent-tag/" + name
	workspace := filepath.Join(TagDir(root), "worktrees", name)
	if err := os.MkdirAll(filepath.Dir(workspace), 0o700); err != nil {
		return TaskWorkspace{}, err
	}
	if _, err := m.git(ctx, root, "worktree", "add", "-b", branch, workspace, base); err != nil {
		return TaskWorkspace{}, err
	}
	return TaskWorkspace{Root: root, Workspace: workspace, Branch: branch, BaseCommit: base, HeadCommit: base, IntegrationStatus: "isolated"}, nil
}

func (m GitWorkspaceManager) Finalize(ctx context.Context, workspace TaskWorkspace, summary string) (TaskWorkspace, error) {
	if workspace.Branch == "" {
		return workspace, nil
	}
	status, err := m.git(ctx, workspace.Workspace, "status", "--porcelain")
	if err != nil {
		return workspace, err
	}
	if status != "" {
		if _, err := m.git(ctx, workspace.Workspace, "add", "--all"); err != nil {
			return workspace, err
		}
		message := "agent-tag: " + strings.TrimSpace(summary)
		if strings.TrimSpace(summary) == "" {
			message = "agent-tag: task " + filepath.Base(workspace.Workspace)
		}
		if _, err := m.git(ctx, workspace.Workspace, "-c", "user.name=agent-tag", "-c", "user.email=agent-tag@local", "commit", "-m", message); err != nil {
			return workspace, err
		}
	}
	workspace.HeadCommit, err = m.git(ctx, workspace.Workspace, "rev-parse", "HEAD")
	if err != nil {
		return workspace, err
	}
	workspace.DiffStat, _ = m.git(ctx, workspace.Workspace, "diff", "--stat", workspace.BaseCommit+".."+workspace.HeadCommit)
	if workspace.HeadCommit == workspace.BaseCommit {
		workspace.IntegrationStatus = "no_changes"
	} else {
		workspace.IntegrationStatus = "ready"
	}
	return workspace, nil
}

func (m GitWorkspaceManager) Integrate(ctx context.Context, workspace TaskWorkspace) error {
	if workspace.Branch == "" || workspace.IntegrationStatus == "shared" {
		return errors.New("task does not have an isolated git workspace")
	}
	status, err := m.git(ctx, workspace.Root, "status", "--porcelain")
	if err != nil {
		return err
	}
	lines := []string{}
	for _, line := range strings.Split(status, "\n") {
		if line != "" && !strings.HasPrefix(line, "?? .agent-tag/") {
			lines = append(lines, line)
		}
	}
	if len(lines) > 0 {
		return errors.New("主工作区存在未提交修改，暂不能合并任务分支")
	}
	if _, err := m.git(ctx, workspace.Root, "merge", "--no-ff", workspace.Branch, "-m", "agent-tag: integrate "+workspace.Branch); err != nil {
		return err
	}
	return m.remove(ctx, workspace, false)
}

func (m GitWorkspaceManager) Discard(ctx context.Context, workspace TaskWorkspace) error {
	if workspace.Branch == "" {
		return errors.New("task does not have an isolated git workspace")
	}
	return m.remove(ctx, workspace, true)
}

func (m GitWorkspaceManager) remove(ctx context.Context, workspace TaskWorkspace, force bool) error {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, workspace.Workspace)
	if _, err := m.git(ctx, workspace.Root, args...); err != nil {
		return err
	}
	_, err := m.git(ctx, workspace.Root, "branch", "-D", workspace.Branch)
	return err
}
