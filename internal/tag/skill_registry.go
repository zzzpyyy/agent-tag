package tag

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	skillDomainPattern   = regexp.MustCompile(`[a-z0-9][a-z0-9.-]+\.[a-z]{2,}`)
	skillTokenPattern    = regexp.MustCompile(`[a-z0-9][a-z0-9_.-]{2,}`)
	skillQuotedPattern   = regexp.MustCompile(`["']([^"']{2,80})["']`)
	skillCJKQuotePattern = regexp.MustCompile(`“([^”]{2,80})”`)
	skillTriggerPattern  = regexp.MustCompile(`(触发词|关键词)[:：]([^。；\n]+)`)
)

const (
	maxSkillArchiveFiles = 2000
	maxSkillArchiveBytes = 50 << 20
	maxSkillFileBytes    = 256 << 10
)

type SkillRoot struct {
	Label string
	Path  string
}

type SkillDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content,omitempty"`
	Source      string `json:"source"`
	SourceType  string `json:"sourceType"`
	Location    string `json:"location,omitempty"`
	Editable    bool   `json:"editable"`
	Deletable   bool   `json:"deletable"`
	skillFile   string
}

type SkillRegistry struct {
	root       string
	localRoots []SkillRoot
}

func DefaultSkillRoots(workspace string) []SkillRoot {
	home, _ := os.UserHomeDir()
	return []SkillRoot{
		{Label: "工作区", Path: filepath.Join(workspace, ".agents", "skills")},
		{Label: "工作区", Path: filepath.Join(workspace, ".codex", "skills")},
		{Label: "工作区", Path: filepath.Join(workspace, ".claude", "skills")},
		{Label: "工作区", Path: filepath.Join(workspace, ".pi", "agent", "skills")},
		{Label: "工作区", Path: filepath.Join(workspace, ".pi", "skills")},
		{Label: "Agents", Path: filepath.Join(home, ".agents", "skills")},
		{Label: "Codex", Path: filepath.Join(home, ".codex", "skills")},
		{Label: "Claude", Path: filepath.Join(home, ".claude", "skills")},
		{Label: "Pi", Path: filepath.Join(home, ".pi", "agent", "skills")},
		{Label: "Pi", Path: filepath.Join(home, ".pi", "skills")},
	}
}

func NewSkillRegistry(root string, localRoots []SkillRoot) *SkillRegistry {
	return &SkillRegistry{root: root, localRoots: append([]SkillRoot(nil), localRoots...)}
}

func managedDefinition(skill ManagedSkill, includeContent bool) SkillDefinition {
	content := ""
	if includeContent {
		content = skill.Content
	}
	return SkillDefinition{ID: skill.ID, Name: skill.Name, Description: skill.Description, Content: content, Source: "托管", SourceType: "managed", Editable: true, Deletable: true}
}

func stableSkillID(sourceType, path string) string {
	digest := sha256.Sum256([]byte(sourceType + "\x00" + path))
	return sourceType + "-" + hex.EncodeToString(digest[:8])
}

func trimYAMLValue(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func parseSkillFile(path string) (SkillDefinition, error) {
	file, err := os.Open(path)
	if err != nil {
		return SkillDefinition{}, err
	}
	defer file.Close()
	limited := io.LimitReader(file, maxSkillFileBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return SkillDefinition{}, err
	}
	if len(raw) > maxSkillFileBytes {
		return SkillDefinition{}, fmt.Errorf("SKILL.md 超过 %d KiB", maxSkillFileBytes>>10)
	}
	content := strings.TrimSpace(string(raw))
	if content == "" {
		return SkillDefinition{}, errors.New("SKILL.md 为空")
	}
	name := filepath.Base(filepath.Dir(path))
	description := "本地 Skill"
	if strings.HasPrefix(content, "---\n") {
		frontmatter := strings.SplitN(strings.TrimPrefix(content, "---\n"), "\n---", 2)[0]
		lines := strings.Split(frontmatter, "\n")
		for index := 0; index < len(lines); index++ {
			line := lines[index]
			key, value, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			switch strings.TrimSpace(key) {
			case "name":
				if parsed := trimYAMLValue(value); parsed != "" {
					name = parsed
				}
			case "description":
				parsed := trimYAMLValue(value)
				if strings.HasPrefix(parsed, ">") || strings.HasPrefix(parsed, "|") {
					literal := strings.HasPrefix(parsed, "|")
					parts := []string{}
					for index+1 < len(lines) {
						next := lines[index+1]
						if strings.TrimSpace(next) != "" && next[0] != ' ' && next[0] != '\t' {
							break
						}
						index++
						if text := strings.TrimSpace(next); text != "" {
							parts = append(parts, text)
						}
					}
					separator := " "
					if literal {
						separator = "\n"
					}
					parsed = strings.Join(parts, separator)
				}
				if parsed != "" {
					description = parsed
				}
			}
		}
	}
	return SkillDefinition{Name: name, Description: description, Content: content, Location: path, skillFile: path}, nil
}

func scanSkillRoot(root SkillRoot, sourceType string, includeContent bool) ([]SkillDefinition, error) {
	info, err := os.Stat(root.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SkillDefinition{}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return []SkillDefinition{}, nil
	}
	result := []SkillDefinition{}
	seenDirectories := map[string]bool{}
	seenFiles := map[string]bool{}
	var visit func(string, int)
	visit = func(directory string, depth int) {
		if depth > 4 {
			return
		}
		realDirectory, realErr := filepath.EvalSymlinks(directory)
		if realErr != nil {
			return
		}
		realDirectory, _ = filepath.Abs(realDirectory)
		if seenDirectories[realDirectory] {
			return
		}
		seenDirectories[realDirectory] = true
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			return
		}
		for _, entry := range entries {
			path := filepath.Join(directory, entry.Name())
			if entry.Name() == "SKILL.md" {
				realFile, fileErr := filepath.EvalSymlinks(path)
				if fileErr != nil || seenFiles[realFile] {
					continue
				}
				seenFiles[realFile] = true
				skill, parseErr := parseSkillFile(realFile)
				if parseErr != nil {
					continue
				}
				skill.ID = stableSkillID(sourceType, realFile)
				skill.Source = root.Label
				skill.SourceType = sourceType
				skill.Editable = false
				skill.Deletable = sourceType == "imported"
				if !includeContent {
					skill.Content = ""
				}
				result = append(result, skill)
				continue
			}
			if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == ".venv" {
				continue
			}
			entryInfo, infoErr := os.Stat(path)
			if infoErr == nil && entryInfo.IsDir() {
				visit(path, depth+1)
			}
		}
	}
	visit(root.Path, 0)
	return result, nil
}

func (r *SkillRegistry) importedRoot(ownerID string) string {
	return filepath.Join(TagDir(r.root), "skills", ownerID)
}

func (r *SkillRegistry) Catalog(state State, ownerID string, includeContent bool) ([]SkillDefinition, error) {
	result := []SkillDefinition{}
	for _, skill := range state.Skills {
		if skill.OwnerID == ownerID {
			result = append(result, managedDefinition(skill, includeContent))
		}
	}
	seenPaths := map[string]bool{}
	for _, root := range r.localRoots {
		absolute, _ := filepath.Abs(root.Path)
		canonical, canonicalErr := filepath.EvalSymlinks(absolute)
		if canonicalErr != nil {
			canonical = absolute
		}
		if seenPaths[canonical] {
			continue
		}
		seenPaths[canonical] = true
		skills, err := scanSkillRoot(SkillRoot{Label: root.Label, Path: absolute}, "local", includeContent)
		if err != nil {
			return nil, err
		}
		result = append(result, skills...)
	}
	imports, err := scanSkillRoot(SkillRoot{Label: "已导入", Path: r.importedRoot(ownerID)}, "imported", includeContent)
	if err != nil {
		return nil, err
	}
	result = append(result, imports...)
	unique := make([]SkillDefinition, 0, len(result))
	seenIDs := map[string]bool{}
	for _, skill := range result {
		if seenIDs[skill.ID] {
			continue
		}
		seenIDs[skill.ID] = true
		unique = append(unique, skill)
	}
	result = unique
	sort.Slice(result, func(i, j int) bool {
		if strings.EqualFold(result[i].Name, result[j].Name) {
			return result[i].Source < result[j].Source
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (r *SkillRegistry) Resolve(state State, ownerID string, ids []string) ([]SkillDefinition, error) {
	catalog, err := r.Catalog(state, ownerID, true)
	if err != nil {
		return nil, err
	}
	byID := map[string]SkillDefinition{}
	for _, skill := range catalog {
		byID[skill.ID] = skill
	}
	result := make([]SkillDefinition, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		skill, ok := byID[id]
		if !ok {
			return nil, errSkillNotFound
		}
		seen[id] = true
		result = append(result, skill)
	}
	return result, nil
}

func autoSkillScore(skill SkillDefinition, prompt string) int {
	prompt = strings.ToLower(prompt)
	searchable := strings.ToLower(skill.Name + " " + skill.Description)
	if (strings.Contains(searchable, "datasheet") || strings.Contains(searchable, "数据表")) &&
		!strings.Contains(prompt, "datasheet") && !strings.Contains(prompt, "数据表") && !strings.Contains(prompt, "tb=") && !strings.Contains(prompt, "type=dst") {
		return 0
	}
	score := 0
	name := strings.ToLower(skill.Name)
	if len(name) >= 3 && strings.Contains(prompt, name) {
		score += 120
	}
	for _, domain := range skillDomainPattern.FindAllString(searchable, -1) {
		if strings.Contains(prompt, domain) {
			score += 100
		}
	}
	for _, match := range skillQuotedPattern.FindAllStringSubmatch(skill.Description, -1) {
		if len(match) > 1 && strings.Contains(prompt, strings.ToLower(match[1])) {
			score += 45
		}
	}
	for _, match := range skillCJKQuotePattern.FindAllStringSubmatch(skill.Description, -1) {
		if len(match) > 1 && strings.Contains(prompt, strings.ToLower(match[1])) {
			score += 45
		}
	}
	for _, match := range skillTriggerPattern.FindAllStringSubmatch(skill.Description, -1) {
		if len(match) < 3 {
			continue
		}
		keywords := strings.FieldsFunc(match[2], func(r rune) bool {
			return strings.ContainsRune("/、,，| \t\"'“”", r)
		})
		for _, keyword := range keywords {
			keyword = strings.ToLower(strings.TrimSpace(keyword))
			length := len([]rune(keyword))
			if length >= 2 && length <= 30 && strings.Contains(prompt, keyword) {
				score += 45
			}
		}
	}
	ignored := map[string]bool{"skill": true, "use": true, "when": true, "with": true, "from": true, "this": true, "that": true, "user": true, "users": true, "agent": true, "local": true, "file": true, "code": true, "open": true, "current": true, "project": true}
	seen := map[string]bool{}
	for _, token := range skillTokenPattern.FindAllString(searchable, -1) {
		if seen[token] || ignored[token] || len(token) < 4 || strings.Contains(token, ".") {
			continue
		}
		seen[token] = true
		if strings.Contains(prompt, token) {
			score += 6
		}
	}
	if (strings.Contains(prompt, "读取") || strings.Contains(prompt, "查看") || strings.Contains(prompt, "解读")) &&
		(strings.Contains(skill.Description, "读取") || strings.Contains(skill.Description, "查询") || strings.Contains(skill.Description, "抓取") || strings.Contains(skill.Description, "文档")) {
		score += 8
	}
	return score
}

func skillSourcePriority(sourceType string) int {
	switch sourceType {
	case "imported":
		return 3
	case "managed":
		return 2
	default:
		return 1
	}
}

func (r *SkillRegistry) AutoMatch(state State, ownerID, prompt string, limit int) ([]SkillDefinition, error) {
	catalog, err := r.Catalog(state, ownerID, true)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		skill SkillDefinition
		score int
	}
	candidates := []candidate{}
	for _, skill := range catalog {
		if score := autoSkillScore(skill, prompt); score >= 12 {
			candidates = append(candidates, candidate{skill: skill, score: score})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return skillSourcePriority(candidates[i].skill.SourceType) > skillSourcePriority(candidates[j].skill.SourceType)
	})
	if limit < 1 {
		limit = 1
	}
	result := make([]SkillDefinition, 0, limit)
	seenNames := map[string]bool{}
	for _, candidate := range candidates {
		name := strings.ToLower(candidate.skill.Name)
		if seenNames[name] {
			continue
		}
		seenNames[name] = true
		result = append(result, candidate.skill)
		if len(result) == limit {
			break
		}
	}
	return result, nil
}

func safeArchivePath(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("ZIP 包含不安全路径：%s", name)
	}
	return clean, nil
}

func (r *SkillRegistry) ImportZIP(ownerID, filename string, reader io.ReaderAt, size int64) ([]SkillDefinition, error) {
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, fmt.Errorf("无法读取 ZIP：%w", err)
	}
	if len(archive.File) == 0 || len(archive.File) > maxSkillArchiveFiles {
		return nil, fmt.Errorf("ZIP 文件数量必须在 1-%d 之间", maxSkillArchiveFiles)
	}
	base := r.importedRoot(ownerID)
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(base, ".import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	var total uint64
	for _, entry := range archive.File {
		name, pathErr := safeArchivePath(entry.Name)
		if pathErr != nil {
			return nil, pathErr
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("ZIP 不允许符号链接：%s", entry.Name)
		}
		total += entry.UncompressedSize64
		if total > maxSkillArchiveBytes {
			return nil, fmt.Errorf("ZIP 解压后不能超过 %d MiB", maxSkillArchiveBytes>>20)
		}
		target := filepath.Join(temporary, name)
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
			continue
		}
		if !entry.Mode().IsRegular() {
			return nil, fmt.Errorf("ZIP 包含不支持的文件类型：%s", entry.Name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		source, err := entry.Open()
		if err != nil {
			return nil, err
		}
		mode := entry.Mode().Perm()
		if mode == 0 {
			mode = 0o600
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err == nil {
			_, err = io.Copy(destination, io.LimitReader(source, int64(entry.UncompressedSize64)+1))
		}
		closeErr := source.Close()
		if destination != nil {
			if destinationErr := destination.Close(); err == nil {
				err = destinationErr
			}
		}
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
	}
	preview, err := scanSkillRoot(SkillRoot{Label: "已导入", Path: temporary}, "imported", false)
	if err != nil || len(preview) == 0 {
		if err == nil {
			err = errors.New("ZIP 中没有找到 SKILL.md")
		}
		return nil, err
	}
	packageName := normalizeSkillName(strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename)))
	if !skillNamePattern.MatchString(packageName) {
		packageName = "skill-package"
	}
	suffix, err := randomToken(5)
	if err != nil {
		return nil, err
	}
	finalPath := filepath.Join(base, packageName+"-"+strings.ToLower(suffix))
	if err := os.Rename(temporary, finalPath); err != nil {
		return nil, err
	}
	return scanSkillRoot(SkillRoot{Label: "已导入", Path: finalPath}, "imported", false)
}

func (r *SkillRegistry) DeleteImported(state State, ownerID, id string) error {
	skills, err := r.Catalog(state, ownerID, true)
	if err != nil {
		return err
	}
	for _, skill := range skills {
		if skill.ID != id {
			continue
		}
		if skill.SourceType != "imported" {
			return errors.New("本机 Skill 为只读，不能删除")
		}
		base, _ := filepath.Abs(r.importedRoot(ownerID))
		if canonicalBase, canonicalErr := filepath.EvalSymlinks(base); canonicalErr == nil {
			base = canonicalBase
		}
		target, _ := filepath.Abs(filepath.Dir(skill.skillFile))
		if target == base || !strings.HasPrefix(target, base+string(filepath.Separator)) {
			return errors.New("Skill 路径无效")
		}
		return os.RemoveAll(target)
	}
	return errSkillNotFound
}
