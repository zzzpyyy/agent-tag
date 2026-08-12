package tag

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func gitTestCommand(t *testing.T, root string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}

func testRepository(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	root := t.TempDir()
	gitTestCommand(t, root, "init")
	gitTestCommand(t, root, "config", "user.name", "test")
	gitTestCommand(t, root, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTestCommand(t, root, "add", "README.md")
	gitTestCommand(t, root, "commit", "-m", "base")
	return root
}

func TestGitWorkspaceLifecycle(t *testing.T) {
	root := testRepository(t)
	manager := GitWorkspaceManager{Root: root}
	workspace, err := manager.Prepare(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Workspace == root || workspace.Branch != "agent-tag/run-1" {
		t.Fatalf("workspace=%+v", workspace)
	}
	if err := os.WriteFile(filepath.Join(workspace.Workspace, "result.txt"), []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err = manager.Finalize(context.Background(), workspace, "test result")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.IntegrationStatus != "ready" || workspace.HeadCommit == workspace.BaseCommit {
		t.Fatalf("finalized=%+v", workspace)
	}
	if err := manager.Integrate(context.Background(), workspace); err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(filepath.Join(root, "result.txt")); err != nil || string(contents) != "done\n" {
		t.Fatalf("integrated contents=%q err=%v", contents, err)
	}
}

func TestNonGitWorkspaceFallsBackToSharedRoot(t *testing.T) {
	root := t.TempDir()
	workspace, err := (GitWorkspaceManager{Root: root}).Prepare(context.Background(), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Workspace != root || workspace.IntegrationStatus != "shared" {
		t.Fatalf("workspace=%+v", workspace)
	}
}
