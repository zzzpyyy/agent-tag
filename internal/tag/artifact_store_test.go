package tag

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"database/sql"
)

func TestArtifactStoreIsOwnerScoped(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "test", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	path := filepath.Join(TagDir(root), "artifact.txt")
	if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := ArtifactRecord{ID: "artifact-1", OwnerID: "user-1", ConversationID: "conv-1", Agent: "codex", Path: path, Label: "result", MediaType: "text/plain", Size: 8, SHA256: "digest", CreatedAt: Now()}
	if err := store.SaveArtifact(record); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Artifact("user-2", record.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-owner read err=%v", err)
	}
	artifacts, err := store.Artifacts("user-1", 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].Path != path {
		t.Fatalf("artifacts=%+v err=%v", artifacts, err)
	}
}
