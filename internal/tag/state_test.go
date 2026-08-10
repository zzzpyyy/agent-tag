package tag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentStateUpdatesDoNotLoseData(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "test", false); err != nil {
		t.Fatal(err)
	}
	const count = 24
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			store := &Store{Root: root}
			errs <- store.Update(func(st *State) error {
				st.Tasks = append(st.Tasks, Task{ID: NextID(st, "task"), Title: fmt.Sprint(i), Depends: []string{}, Scopes: []string{}})
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := (&Store{Root: root}).Read()
	if err != nil || len(state.Tasks) != count || state.Sequence != count {
		t.Fatalf("state tasks=%d sequence=%d err=%v", len(state.Tasks), state.Sequence, err)
	}
}

func TestSQLiteCollectionsPaginationBackupAndArtifactCleanup(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "sqlite", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	if err := store.Update(func(state *State) error {
		state.Users = append(state.Users, User{ID: "user-1", Username: "user", Providers: []ProviderConfig{}})
		state.Conversations = append(state.Conversations, Conversation{ID: "conv-1", OwnerID: "user-1", Title: "history", CreatedAt: Now(), UpdatedAt: Now(), Participants: []Participant{}})
		for index := 0; index < 150; index++ {
			state.ChatMessages = append(state.ChatMessages, ChatMessage{ID: fmt.Sprintf("chat-%04d", index), ConversationID: "conv-1", Author: "you", Kind: "user", Body: fmt.Sprintf("message-%03d", index), CreatedAt: fmt.Sprintf("2026-01-01T00:00:%02d.%03dZ", index/100, index)})
		}
		state.TaskRuns = append(state.TaskRuns, TaskRun{ID: "run-1", TaskID: "task-1", StartedAt: Now(), Status: "completed", Stdout: "run output"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DatabasePath(root)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(StatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "message-149") || strings.Contains(string(raw), "run output") {
		t.Fatal("high-volume records remain embedded in state.json")
	}
	state, err := store.Read()
	if err != nil || len(state.ChatMessages) != 150 || len(state.TaskRuns) != 1 {
		t.Fatalf("sqlite reload messages=%d runs=%d err=%v", len(state.ChatMessages), len(state.TaskRuns), err)
	}
	page, more, err := messagePage(root, "conv-1", "", 100)
	if err != nil || len(page) != 100 || !more || page[0].Body != "message-050" {
		t.Fatalf("first page len=%d more=%v first=%+v err=%v", len(page), more, page[0], err)
	}
	older, more, err := messagePage(root, "conv-1", page[0].ID, 100)
	if err != nil || len(older) != 50 || more || older[0].Body != "message-000" {
		t.Fatalf("older page len=%d more=%v err=%v", len(older), more, err)
	}
	backup, err := store.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.json", "data.sqlite"} {
		if _, err := os.Stat(filepath.Join(TagDir(root), "backups", backup, name)); err != nil {
			t.Fatal(err)
		}
	}
	orphan := filepath.Join(TagDir(root), "artifacts", "user-1", "missing-conversation")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	removed, err := store.CleanupOrphanArtifacts()
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed=%d err=%v", removed, err)
	}
	if err := store.AppendAudit("user-1", "conv-1", "tester", "skills_loaded", "skills=example shell=true network=false write=false"); err != nil {
		t.Fatal(err)
	}
	events, err := store.RecentAudit("user-1", 10)
	if err != nil || len(events) != 1 || events[0].Action != "skills_loaded" {
		t.Fatalf("audit events=%+v err=%v", events, err)
	}
}

func TestScopesOverlap(t *testing.T) {
	if !ScopesOverlap([]string{"internal/tag"}, []string{"internal/tag/web.go"}) {
		t.Fatal("parent and child scopes should overlap")
	}
	if ScopesOverlap([]string{"web"}, []string{"internal"}) {
		t.Fatal("unrelated scopes should not overlap")
	}
}
