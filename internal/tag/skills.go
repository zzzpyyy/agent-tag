package tag

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxManagedSkills       = 50
	maxAssignedSkills      = 12
	maxSkillContentLength  = 30000
	maxSkillDescriptionLen = 500
)

var (
	errSkillNotFound = errors.New("skill not found")
	skillNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,49}$`)
)

func normalizedSkillMode(value string) string {
	if value == "manual" {
		return "manual"
	}
	return "auto"
}

func normalizeSkillName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validateSkill(name, description, content string) error {
	if !skillNamePattern.MatchString(name) {
		return fmt.Errorf("Skill 名称需为 2-50 位小写字母、数字或横线")
	}
	if strings.TrimSpace(description) == "" || len(description) > maxSkillDescriptionLen {
		return fmt.Errorf("Skill 描述不能为空且不能超过 %d 字符", maxSkillDescriptionLen)
	}
	if strings.TrimSpace(content) == "" || len(content) > maxSkillContentLength {
		return fmt.Errorf("Skill 内容不能为空且不能超过 %d 字符", maxSkillContentLength)
	}
	return nil
}

func findOwnedSkill(state *State, id, ownerID string) *ManagedSkill {
	for index := range state.Skills {
		if state.Skills[index].ID == id && state.Skills[index].OwnerID == ownerID {
			return &state.Skills[index]
		}
	}
	return nil
}

func uniqueSkillName(state *State, ownerID, name, exceptID string) bool {
	for _, skill := range state.Skills {
		if skill.OwnerID == ownerID && skill.ID != exceptID && strings.EqualFold(skill.Name, name) {
			return false
		}
	}
	return true
}

func (s *WebServer) createSkill(w http.ResponseWriter, r *http.Request, user User) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if decode(r, &input) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	input.Name = normalizeSkillName(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Content = strings.TrimSpace(input.Content)
	if err := validateSkill(input.Name, input.Description, input.Content); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var output ManagedSkill
	err := s.store.Update(func(state *State) error {
		count := 0
		for _, skill := range state.Skills {
			if skill.OwnerID == user.ID {
				count++
			}
		}
		if count >= maxManagedSkills {
			return fmt.Errorf("每个账号最多创建 %d 个 Skill", maxManagedSkills)
		}
		if !uniqueSkillName(state, user.ID, input.Name, "") {
			return fmt.Errorf("Skill 名称已存在")
		}
		now := Now()
		output = ManagedSkill{ID: NextID(state, "skill"), OwnerID: user.ID, Name: input.Name, Description: input.Description, Content: input.Content, CreatedAt: now, UpdatedAt: now}
		state.Skills = append(state.Skills, output)
		return nil
	})
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.notify()
	jsonResponse(w, http.StatusCreated, output)
}

func (s *WebServer) updateSkill(w http.ResponseWriter, r *http.Request, id string, user User) {
	var input struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Content     string `json:"content"`
	}
	if decode(r, &input) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	input.Name = normalizeSkillName(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.Content = strings.TrimSpace(input.Content)
	if err := validateSkill(input.Name, input.Description, input.Content); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var output ManagedSkill
	err := s.store.Update(func(state *State) error {
		skill := findOwnedSkill(state, id, user.ID)
		if skill == nil {
			return errSkillNotFound
		}
		if !uniqueSkillName(state, user.ID, input.Name, id) {
			return fmt.Errorf("Skill 名称已存在")
		}
		skill.Name = input.Name
		skill.Description = input.Description
		skill.Content = input.Content
		skill.UpdatedAt = Now()
		output = *skill
		return nil
	})
	if errors.Is(err, errSkillNotFound) {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.notify()
	jsonResponse(w, http.StatusOK, output)
}

func (s *WebServer) deleteSkill(w http.ResponseWriter, id string, user User) {
	state, err := s.store.Read()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	managed := findOwnedSkill(&state, id, user.ID) != nil
	if !managed {
		if err := s.skills.DeleteImported(state, user.ID, id); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errSkillNotFound) {
				status = http.StatusNotFound
			}
			jsonResponse(w, status, map[string]string{"error": err.Error()})
			return
		}
	}
	err = s.store.Update(func(state *State) error {
		if managed {
			kept := state.Skills[:0]
			for _, skill := range state.Skills {
				if skill.ID == id && skill.OwnerID == user.ID {
					continue
				}
				kept = append(kept, skill)
			}
			state.Skills = kept
		}
		for ci := range state.Conversations {
			if state.Conversations[ci].OwnerID != user.ID {
				continue
			}
			for pi := range state.Conversations[ci].Participants {
				ids := state.Conversations[ci].Participants[pi].SkillIDs[:0]
				for _, skillID := range state.Conversations[ci].Participants[pi].SkillIDs {
					if skillID != id {
						ids = append(ids, skillID)
					}
				}
				state.Conversations[ci].Participants[pi].SkillIDs = ids
			}
		}
		return nil
	})
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.notify()
	jsonResponse(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *WebServer) skillDetail(w http.ResponseWriter, id string, user User) {
	state, err := s.store.Read()
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	skills, err := s.skills.Resolve(state, user.ID, []string{id})
	if errors.Is(err, errSkillNotFound) {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	jsonResponse(w, http.StatusOK, skills[0])
}

func (s *WebServer) importSkills(w http.ResponseWriter, r *http.Request, user User) {
	r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "ZIP 不能超过 20 MiB"})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "请选择 ZIP 文件"})
		return
	}
	defer file.Close()
	readerAt, ok := file.(io.ReaderAt)
	if !ok {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "无法读取上传文件"})
		return
	}
	if !strings.EqualFold(filepath.Ext(header.Filename), ".zip") {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "仅支持 ZIP 压缩包"})
		return
	}
	imported, err := s.skills.ImportZIP(user.ID, header.Filename, readerAt, header.Size)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.notify()
	jsonResponse(w, http.StatusCreated, map[string]any{"skills": imported})
}

func (s *WebServer) assignSkills(w http.ResponseWriter, r *http.Request, conversationID string, user User) {
	var input struct {
		Agent    string   `json:"agent"`
		SkillIDs []string `json:"skillIds"`
	}
	if decode(r, &input) != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}
	if len(input.SkillIDs) > maxAssignedSkills {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("每个 Agent 最多分配 %d 个 Skill", maxAssignedSkills)})
		return
	}
	err := s.store.Update(func(state *State) error {
		conversation := findOwnedConversation(state, conversationID, user.ID)
		if conversation == nil {
			return errConversationNotFound
		}
		if _, err := s.skills.Resolve(*state, user.ID, input.SkillIDs); err != nil {
			return err
		}
		seen := map[string]bool{}
		validated := make([]string, 0, len(input.SkillIDs))
		for _, id := range input.SkillIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			validated = append(validated, id)
		}
		for index := range conversation.Participants {
			if conversation.Participants[index].Name == input.Agent {
				conversation.Participants[index].SkillIDs = validated
				conversation.UpdatedAt = Now()
				return nil
			}
		}
		return fmt.Errorf("agent not found")
	})
	if errors.Is(err, errConversationNotFound) || errors.Is(err, errSkillNotFound) {
		jsonResponse(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.notify()
	jsonResponse(w, http.StatusOK, map[string]bool{"updated": true})
}

func (s *WebServer) skillsForParticipant(state State, conversation *Conversation, participant Participant, prompt string) ([]SkillDefinition, error) {
	assigned, err := s.skills.Resolve(state, conversation.OwnerID, participant.SkillIDs)
	if err != nil {
		return nil, err
	}
	if normalizedSkillMode(conversation.SkillMode) != "auto" {
		return assigned, nil
	}
	var recent strings.Builder
	matchedMessages := 0
	for index := len(state.ChatMessages) - 1; index >= 0 && matchedMessages < 12; index-- {
		message := state.ChatMessages[index]
		if message.ConversationID != conversation.ID {
			continue
		}
		recent.WriteString(message.Body)
		recent.WriteByte('\n')
		matchedMessages++
	}
	recent.WriteString(prompt)
	automatic, err := s.skills.AutoMatch(state, conversation.OwnerID, recent.String(), 2)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	combined := make([]SkillDefinition, 0, len(assigned)+len(automatic))
	for _, skill := range append(assigned, automatic...) {
		if seen[skill.ID] {
			continue
		}
		seen[skill.ID] = true
		combined = append(combined, skill)
	}
	return combined, nil
}

func managedSkillPrompt(skills []SkillDefinition) string {
	if len(skills) == 0 {
		return ""
	}
	var prompt strings.Builder
	prompt.WriteString("The user assigned the following managed Skills to you for this conversation. Treat each as reusable workflow instructions. Use a Skill whenever the request matches its description, and explicitly follow its procedure. If a required tool is unavailable, state that limitation and continue safely.\n")
	for _, skill := range skills {
		location := ""
		if skill.Location != "" {
			location = "\nSkill location: " + skill.Location + "\nResolve referenced scripts and resources relative to that SKILL.md."
		}
		fmt.Fprintf(&prompt, "\n--- BEGIN MANAGED SKILL: %s ---\nDescription: %s%s\n\n%s\n--- END MANAGED SKILL: %s ---\n", skill.Name, skill.Description, location, skill.Content, skill.Name)
	}
	return prompt.String()
}
