package main

import (
	"context"
	"testing"
	"time"

	"agent-tag/internal/tag"
)

func TestWorkerObservesCancellationAndPersistsRunLog(t *testing.T) {
	root := t.TempDir()
	if err := tag.Init(root, "cancel-worker", false); err != nil {
		t.Fatal(err)
	}
	store := &tag.Store{Root: root}
	if err := store.Update(func(state *tag.State) error {
		assignee := "worker"
		state.Tasks = append(state.Tasks, tag.Task{ID: "task-1", Title: "cancel", Status: "cancel_requested", Assignee: &assignee, CreatedAt: tag.Now(), UpdatedAt: tag.Now()})
		state.Agents = append(state.Agents, tag.Agent{Name: assignee, Status: "online", HeartbeatAt: tag.Now()})
		state.TaskRuns = append(state.TaskRuns, tag.TaskRun{ID: "run-1", TaskID: "task-1", Agent: assignee, Status: "in_progress", StartedAt: tag.Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go heartbeat(ctx, store, "worker", "task-1", cancel, done)
	select {
	case <-ctx.Done():
	case <-time.After(3 * time.Second):
		close(done)
		t.Fatal("worker did not observe cancellation request")
	}
	close(done)
	result := tag.RunResult{Code: -1, Stdout: "partial output", Stderr: "signal: killed"}
	if err := finish(store, "worker", "task-1", "run-1", "blocked", "", "cancelled", result); err != nil {
		t.Fatal(err)
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[0].Status != "canceled" || state.TaskRuns[0].Status != "canceled" || state.TaskRuns[0].Stdout != "partial output" {
		t.Fatalf("cancel result task=%+v run=%+v", state.Tasks[0], state.TaskRuns[0])
	}
}
