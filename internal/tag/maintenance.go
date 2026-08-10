package tag

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type MaintenanceStatus struct {
	StateBytes    int64    `json:"stateBytes"`
	DatabaseBytes int64    `json:"databaseBytes"`
	ArtifactBytes int64    `json:"artifactBytes"`
	Backups       []string `json:"backups"`
}

func directoryBytes(root string) int64 {
	var total int64
	_ = filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (s *Store) MaintenanceStatus() MaintenanceStatus {
	status := MaintenanceStatus{ArtifactBytes: directoryBytes(filepath.Join(TagDir(s.Root), "artifacts")), Backups: []string{}}
	if info, err := os.Stat(StatePath(s.Root)); err == nil {
		status.StateBytes = info.Size()
	}
	if info, err := os.Stat(DatabasePath(s.Root)); err == nil {
		status.DatabaseBytes = info.Size()
	}
	entries, _ := os.ReadDir(filepath.Join(TagDir(s.Root), "backups"))
	for _, entry := range entries {
		if entry.IsDir() {
			status.Backups = append(status.Backups, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(status.Backups)))
	return status
}

func copyRegularFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func (s *Store) CreateBackup() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireLock(s.Root, 10*time.Second)
	if err != nil {
		return "", err
	}
	defer release()
	database, err := openDatabase(s.Root)
	if err != nil {
		return "", err
	}
	_, _ = database.Exec(`PRAGMA wal_checkpoint(FULL)`)
	_ = database.Close()
	name := time.Now().UTC().Format("20060102T150405.000000000Z")
	directory := filepath.Join(TagDir(s.Root), "backups", name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := copyRegularFile(StatePath(s.Root), filepath.Join(directory, "state.json")); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	if err := copyRegularFile(DatabasePath(s.Root), filepath.Join(directory, "data.sqlite")); err != nil {
		_ = os.RemoveAll(directory)
		return "", err
	}
	entries, _ := os.ReadDir(filepath.Join(TagDir(s.Root), "backups"))
	names := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > 10 {
		for _, old := range names[10:] {
			_ = os.RemoveAll(filepath.Join(TagDir(s.Root), "backups", old))
		}
	}
	return name, nil
}

func (s *Store) CleanupOrphanArtifacts() (int, error) {
	state, err := s.Read()
	if err != nil {
		return 0, err
	}
	valid := map[string]bool{}
	for _, conversation := range state.Conversations {
		valid[filepath.Join(conversation.OwnerID, conversation.ID)] = true
	}
	root := filepath.Join(TagDir(s.Root), "artifacts")
	owners, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, owner := range owners {
		if !owner.IsDir() {
			continue
		}
		conversations, _ := os.ReadDir(filepath.Join(root, owner.Name()))
		for _, conversation := range conversations {
			if conversation.IsDir() && !valid[filepath.Join(owner.Name(), conversation.Name())] {
				if err := os.RemoveAll(filepath.Join(root, owner.Name(), conversation.Name())); err != nil {
					return removed, fmt.Errorf("cleanup artifact: %w", err)
				}
				removed++
			}
		}
	}
	return removed, nil
}
