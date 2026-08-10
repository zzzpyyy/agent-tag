package tag

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillArchive(t *testing.T, files map[string]string) *bytes.Reader {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buffer.Bytes())
}

func TestSkillRegistryDiscoversLocalSkillAndReadsOnDemand(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(t.TempDir(), "skills")
	skillDir := filepath.Join(local, "evidence-review")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: evidence-review\ndescription: Verify claims against primary evidence.\n---\n\n# Procedure\nRead references/checklist.md."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := NewSkillRegistry(root, []SkillRoot{{Label: "Codex", Path: local}})
	catalog, err := registry.Catalog(State{}, "user-1", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].Name != "evidence-review" || catalog[0].Source != "Codex" || catalog[0].Content != "" {
		t.Fatalf("unexpected catalog: %+v", catalog)
	}
	resolved, err := registry.Resolve(State{}, "user-1", []string{catalog[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved) != 1 || resolved[0].Content != content || resolved[0].Editable || resolved[0].Deletable {
		t.Fatalf("unexpected resolved skill: %+v", resolved)
	}
}

func TestSkillRegistryFollowsLocalSkillDirectorySymlinks(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(t.TempDir(), "skills")
	target := filepath.Join(t.TempDir(), "linked-skill")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "SKILL.md"), []byte("---\nname: linked-skill\ndescription: Linked locally.\n---\nBody"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(local, "linked-skill")); err != nil {
		t.Fatal(err)
	}
	registry := NewSkillRegistry(root, []SkillRoot{{Label: "Claude", Path: local}})
	catalog, err := registry.Catalog(State{}, "alice", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog) != 1 || catalog[0].Name != "linked-skill" {
		t.Fatalf("symlinked skill was not discovered: %+v", catalog)
	}
}

func TestSkillRegistryParsesFoldedFrontmatterDescription(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "SKILL.md")
	content := "---\nname: folded-skill\ndescription: >-\n  First line describing the skill.\n  Second line with its trigger.\n---\nBody"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	skill, err := parseSkillFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if skill.Description != "First line describing the skill. Second line with its trigger." {
		t.Fatalf("description=%q", skill.Description)
	}
}

func TestAutoSkillScoreRequiresStrongSignalAndSupportsChineseTriggers(t *testing.T) {
	generic := SkillDefinition{Name: "generic-agent", Description: "Use when an agent works with a local project file."}
	if score := autoSkillScore(generic, "请解释这个 agent workspace"); score >= 12 {
		t.Fatalf("generic overlap scored too highly: %d", score)
	}
	triggered := SkillDefinition{Name: "datasheet", Description: "触发词:数据表/datasheet/创建数据表/查询记录。"}
	if score := autoSkillScore(triggered, "帮我查询这个数据表"); score < 12 {
		t.Fatalf("Chinese trigger was not matched: %d", score)
	}
	if score := autoSkillScore(triggered, "解读 https://ku.baidu-int.com/knowledge/a/b/c/d"); score >= 12 {
		t.Fatalf("datasheet Skill matched a normal document URL: %d", score)
	}
}

func TestSkillExecutionIsEnabledByDefault(t *testing.T) {
	state := NewState("defaults")
	if !state.Defaults.AllowSkillExecution {
		t.Fatal("new teams should allow trusted local Skills to execute by default")
	}
}

func TestVersionOneStateEnablesSkillExecutionAndResetsSessions(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(TagDir(root), 0o700); err != nil {
		t.Fatal(err)
	}
	state := NewState("migration")
	state.Version = 1
	state.Defaults.AllowSkillExecution = false
	state.Conversations = []Conversation{{ID: "conv-1", AllowSkillExecution: false, Participants: []Participant{{Name: "cc", SessionID: StringPtr("old-session")}}}}
	if err := writeState(root, state); err != nil {
		t.Fatal(err)
	}
	migrated, err := (&Store{Root: root}).Read()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != stateVersion || !migrated.Defaults.AllowSkillExecution || !migrated.Conversations[0].AllowSkillExecution || migrated.Conversations[0].Participants[0].SessionID != nil {
		t.Fatalf("migration failed: %+v", migrated)
	}
}

func TestSkillRegistryImportsCompleteZIPPerTenant(t *testing.T) {
	root := t.TempDir()
	registry := NewSkillRegistry(root, nil)
	archive := skillArchive(t, map[string]string{
		"release-check/SKILL.md":         "---\nname: release-check\ndescription: Check a release.\n---\n\nRun scripts/check.sh.",
		"release-check/scripts/check.sh": "#!/bin/sh\necho checked\n",
	})
	imported, err := registry.ImportZIP("alice", "release-check.zip", archive, archive.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].SourceType != "imported" || !imported[0].Deletable {
		t.Fatalf("unexpected import: %+v", imported)
	}
	resolved, err := registry.Resolve(State{}, "alice", []string{imported[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(filepath.Dir(resolved[0].Location), "scripts", "check.sh")
	data, err := os.ReadFile(script)
	if err != nil || !strings.Contains(string(data), "echo checked") {
		t.Fatalf("resource was not preserved: data=%q err=%v", data, err)
	}
	bob, err := registry.Catalog(State{}, "bob", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(bob) != 0 {
		t.Fatalf("bob saw alice import: %+v", bob)
	}
	if err := registry.DeleteImported(State{}, "alice", imported[0].ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := registry.Catalog(State{}, "alice", false); len(after) != 0 {
		t.Fatalf("skill remained after deletion: %+v", after)
	}
}

func TestSkillRegistryRejectsUnsafeZIP(t *testing.T) {
	root := t.TempDir()
	registry := NewSkillRegistry(root, nil)
	archive := skillArchive(t, map[string]string{
		"../escape":     "bad",
		"safe/SKILL.md": "---\nname: safe-skill\ndescription: Safe.\n---\nBody",
	})
	if _, err := registry.ImportZIP("alice", "unsafe.zip", archive, archive.Size()); err == nil || !strings.Contains(err.Error(), "不安全路径") {
		t.Fatalf("expected unsafe path error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(TagDir(root), "skills", "escape")); !os.IsNotExist(err) {
		t.Fatalf("archive escaped import root: %v", err)
	}
}

func TestSkillRegistryRejectsZIPWithoutSkillFile(t *testing.T) {
	registry := NewSkillRegistry(t.TempDir(), nil)
	archive := skillArchive(t, map[string]string{"README.md": "not a skill"})
	if _, err := registry.ImportZIP("alice", "empty.zip", archive, archive.Size()); err == nil || !strings.Contains(err.Error(), "没有找到 SKILL.md") {
		t.Fatalf("expected missing SKILL.md error, got %v", err)
	}
}
