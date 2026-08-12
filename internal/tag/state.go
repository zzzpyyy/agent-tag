package tag

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const stateVersion = 2

type Team struct {
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
}

type DiscussionSettings struct {
	AutoRelay           bool             `json:"autoRelay"`
	AutoReview          bool             `json:"autoReview"`
	ReviewRounds        int              `json:"reviewRounds"`
	SkillMode           string           `json:"skillMode"`
	AllowSkillExecution bool             `json:"allowSkillExecution"`
	SkillPermissions    SkillPermissions `json:"skillPermissions"`
	TokenBudget         int64            `json:"tokenBudget,omitempty"`
	CostBudgetUSD       float64          `json:"costBudgetUsd,omitempty"`
	DefaultParticipants []Participant    `json:"defaultParticipants,omitempty"`
}

func defaultParticipants() []Participant {
	return []Participant{{Name: "cc", Provider: "claude"}, {Name: "codex", Provider: "codex", AutoDiscuss: true}}
}

type SkillPermissions struct {
	Shell   bool `json:"shell"`
	Network bool `json:"network"`
	Write   bool `json:"write"`
}

type User struct {
	ID           string             `json:"id"`
	Username     string             `json:"username"`
	PasswordHash string             `json:"passwordHash"`
	Defaults     DiscussionSettings `json:"defaults"`
	Providers    []ProviderConfig   `json:"providers"`
	CreatedAt    string             `json:"createdAt"`
}

type ProviderConfig struct {
	Provider       string `json:"provider"`
	Executable     string `json:"executable"`
	LaunchCommand  string `json:"launchCommand"`
	ExtraArgs      string `json:"extraArgs"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type LoginSession struct {
	TokenHash string `json:"tokenHash"`
	UserID    string `json:"userId"`
	ExpiresAt string `json:"expiresAt"`
	CreatedAt string `json:"createdAt"`
}

type ManagedSkill struct {
	ID          string `json:"id"`
	OwnerID     string `json:"ownerId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type Task struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Depends     []string `json:"depends"`
	Scopes      []string `json:"scopes"`
	Status      string   `json:"status"`
	Assignee    *string  `json:"assignee"`
	Attempts    int      `json:"attempts"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
	Summary     *string  `json:"summary"`
	LastError   *string  `json:"lastError"`
}

type TaskRun struct {
	ID                string         `json:"id"`
	TaskID            string         `json:"taskId"`
	Agent             string         `json:"agent"`
	Provider          string         `json:"provider"`
	StartedAt         string         `json:"startedAt"`
	CompletedAt       string         `json:"completedAt,omitempty"`
	Status            string         `json:"status"`
	ExitCode          int            `json:"exitCode"`
	Stdout            string         `json:"stdout,omitempty"`
	Stderr            string         `json:"stderr,omitempty"`
	Summary           string         `json:"summary,omitempty"`
	Observation       RunObservation `json:"observation"`
	Workspace         string         `json:"workspace,omitempty"`
	Branch            string         `json:"branch,omitempty"`
	BaseCommit        string         `json:"baseCommit,omitempty"`
	HeadCommit        string         `json:"headCommit,omitempty"`
	DiffStat          string         `json:"diffStat,omitempty"`
	IntegrationStatus string         `json:"integrationStatus,omitempty"`
}

type RunUsage struct {
	InputTokens      int64   `json:"inputTokens,omitempty"`
	OutputTokens     int64   `json:"outputTokens,omitempty"`
	CachedTokens     int64   `json:"cachedTokens,omitempty"`
	TotalTokens      int64   `json:"totalTokens,omitempty"`
	EstimatedCostUSD float64 `json:"estimatedCostUsd,omitempty"`
}

type RunObservation struct {
	Provider    string   `json:"provider,omitempty"`
	Model       string   `json:"model,omitempty"`
	StartedAt   string   `json:"startedAt,omitempty"`
	CompletedAt string   `json:"completedAt,omitempty"`
	DurationMS  int64    `json:"durationMs,omitempty"`
	ExitCode    int      `json:"exitCode,omitempty"`
	Status      string   `json:"status,omitempty"`
	ErrorClass  string   `json:"errorClass,omitempty"`
	Usage       RunUsage `json:"usage"`
}

type Agent struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Provider    string  `json:"provider"`
	Model       *string `json:"model"`
	Command     *string `json:"command"`
	Status      string  `json:"status"`
	CurrentTask *string `json:"currentTask"`
	JoinedAt    string  `json:"joinedAt"`
	HeartbeatAt string  `json:"heartbeatAt"`
}

type MailMessage struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	To        string   `json:"to"`
	Body      string   `json:"body"`
	CreatedAt string   `json:"createdAt"`
	ReadBy    []string `json:"readBy"`
}

type Participant struct {
	Name              string   `json:"name"`
	Provider          string   `json:"provider"`
	Model             *string  `json:"model"`
	AutoDiscuss       bool     `json:"autoDiscuss"`
	SessionID         *string  `json:"sessionId"`
	LastSeenMessageID *string  `json:"lastSeenMessageId"`
	SkillIDs          []string `json:"skillIds"`
	Command           *string  `json:"command,omitempty"`
}

type Conversation struct {
	ID                  string           `json:"id"`
	OwnerID             string           `json:"ownerId"`
	Title               string           `json:"title"`
	CreatedAt           string           `json:"createdAt"`
	UpdatedAt           string           `json:"updatedAt"`
	Archived            bool             `json:"archived"`
	Started             bool             `json:"started"`
	AutoRelay           bool             `json:"autoRelay"`
	AutoReview          bool             `json:"autoReview"`
	ReviewRounds        int              `json:"reviewRounds"`
	SkillMode           string           `json:"skillMode"`
	AllowSkillExecution bool             `json:"allowSkillExecution"`
	SkillPermissions    SkillPermissions `json:"skillPermissions"`
	TokenBudget         int64            `json:"tokenBudget,omitempty"`
	CostBudgetUSD       float64          `json:"costBudgetUsd,omitempty"`
	Participants        []Participant    `json:"participants"`
}

type ChatMessage struct {
	ID               string          `json:"id"`
	ConversationID   string          `json:"conversationId"`
	RoundID          string          `json:"roundId,omitempty"`
	Phase            string          `json:"phase,omitempty"`
	ReviewRound      int             `json:"reviewRound,omitempty"`
	SourceMessageIDs []string        `json:"sourceMessageIds,omitempty"`
	Author           string          `json:"author"`
	Provider         *string         `json:"provider"`
	Kind             string          `json:"kind"`
	Body             string          `json:"body"`
	Steps            []string        `json:"steps"`
	Artifacts        []ChatArtifact  `json:"artifacts,omitempty"`
	RetryAgent       string          `json:"retryAgent,omitempty"`
	Observation      *RunObservation `json:"observation,omitempty"`
	CreatedAt        string          `json:"createdAt"`
}

type ChatArtifact struct {
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
	Label     string `json:"label"`
	MediaType string `json:"mediaType,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type ArtifactRecord struct {
	ID             string `json:"id"`
	OwnerID        string `json:"ownerId"`
	ConversationID string `json:"conversationId"`
	Agent          string `json:"agent"`
	Path           string `json:"path"`
	Label          string `json:"label"`
	MediaType      string `json:"mediaType"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	CreatedAt      string `json:"createdAt"`
}

type State struct {
	Version       int                `json:"version"`
	Team          Team               `json:"team"`
	Defaults      DiscussionSettings `json:"defaults"`
	Users         []User             `json:"users"`
	LoginSessions []LoginSession     `json:"loginSessions"`
	Skills        []ManagedSkill     `json:"skills"`
	Sequence      int                `json:"sequence"`
	Tasks         []Task             `json:"tasks"`
	TaskRuns      []TaskRun          `json:"taskRuns"`
	Agents        []Agent            `json:"agents"`
	Messages      []MailMessage      `json:"messages"`
	Conversations []Conversation     `json:"conversations"`
	ChatMessages  []ChatMessage      `json:"chatMessages"`
}

type Store struct {
	Root string
	mu   sync.Mutex
}

func TagDir(root string) string    { return filepath.Join(root, ".agent-tag") }
func StatePath(root string) string { return filepath.Join(TagDir(root), "state.json") }

func FindRoot(start string) (string, error) {
	current, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(StatePath(current)); err == nil {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("no .agent-tag team found; run `agent-tag init` first")
		}
		current = parent
	}
}

func NewState(name string) State {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	permissions := SkillPermissions{Shell: true, Network: true, Write: true}
	return State{Version: stateVersion, Team: Team{Name: name, CreatedAt: now}, Defaults: DiscussionSettings{ReviewRounds: 1, SkillMode: "auto", AllowSkillExecution: true, SkillPermissions: permissions, DefaultParticipants: defaultParticipants()}, Users: []User{}, LoginSessions: []LoginSession{}, Skills: []ManagedSkill{}, Tasks: []Task{}, TaskRuns: []TaskRun{}, Agents: []Agent{}, Messages: []MailMessage{}, Conversations: []Conversation{}, ChatMessages: []ChatMessage{}}
}

func normalizedSkillPermissions(legacy bool, permissions SkillPermissions) SkillPermissions {
	if legacy && !permissions.Shell && !permissions.Network && !permissions.Write {
		return SkillPermissions{Shell: true, Network: true, Write: true}
	}
	return permissions
}

func Init(root, name string, force bool) error {
	if !force {
		if _, err := os.Stat(StatePath(root)); err == nil {
			return fmt.Errorf("team already initialized at %s", StatePath(root))
		}
	}
	if err := os.MkdirAll(TagDir(root), 0o755); err != nil {
		return err
	}
	return writeState(root, NewState(name))
}

func (s *Store) Read() (State, error) {
	return s.read(true)
}

func (s *Store) ReadMetadata() (State, error) {
	return s.read(false)
}

func (s *Store) read(loadCollections bool) (State, error) {
	raw, err := os.ReadFile(StatePath(s.Root))
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, err
	}
	if state.Version == 1 {
		state.Version = stateVersion
		state.Defaults.AllowSkillExecution = true
		for index := range state.Users {
			state.Users[index].Defaults.AllowSkillExecution = true
		}
		for conversationIndex := range state.Conversations {
			state.Conversations[conversationIndex].AllowSkillExecution = true
			for participantIndex := range state.Conversations[conversationIndex].Participants {
				state.Conversations[conversationIndex].Participants[participantIndex].SessionID = nil
			}
		}
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported state version: %d", state.Version)
	}
	if state.Tasks == nil {
		state.Tasks = []Task{}
	}
	if state.TaskRuns == nil {
		state.TaskRuns = []TaskRun{}
	}
	if state.Users == nil {
		state.Users = []User{}
	}
	if state.LoginSessions == nil {
		state.LoginSessions = []LoginSession{}
	}
	if state.Skills == nil {
		state.Skills = []ManagedSkill{}
	}
	if state.Agents == nil {
		state.Agents = []Agent{}
	}
	if state.Messages == nil {
		state.Messages = []MailMessage{}
	}
	if state.Conversations == nil {
		state.Conversations = []Conversation{}
	}
	if state.ChatMessages == nil {
		state.ChatMessages = []ChatMessage{}
	}
	if _, statErr := os.Stat(DatabasePath(s.Root)); errors.Is(statErr, os.ErrNotExist) && (len(state.ChatMessages) > 0 || len(state.TaskRuns) > 0) {
		if err := syncSQLiteCollections(s.Root, state); err != nil {
			return State{}, fmt.Errorf("migrate legacy collections: %w", err)
		}
	}
	if loadCollections {
		if _, err := loadSQLiteCollections(s.Root, &state); err != nil {
			return State{}, err
		}
	} else {
		state.ChatMessages = nil
		state.TaskRuns = nil
	}
	if state.Defaults.ReviewRounds < 1 {
		state.Defaults.ReviewRounds = 1
	}
	if state.Defaults.ReviewRounds > 5 {
		state.Defaults.ReviewRounds = 5
	}
	state.Defaults.SkillMode = normalizedSkillMode(state.Defaults.SkillMode)
	state.Defaults.SkillPermissions = normalizedSkillPermissions(state.Defaults.AllowSkillExecution, state.Defaults.SkillPermissions)
	if len(state.Defaults.DefaultParticipants) == 0 {
		state.Defaults.DefaultParticipants = defaultParticipants()
	}
	for i := range state.Users {
		if state.Users[i].Providers == nil {
			state.Users[i].Providers = []ProviderConfig{}
		}
		if state.Users[i].Defaults.ReviewRounds < 1 {
			state.Users[i].Defaults.ReviewRounds = 1
		}
		if state.Users[i].Defaults.ReviewRounds > 5 {
			state.Users[i].Defaults.ReviewRounds = 5
		}
		state.Users[i].Defaults.SkillMode = normalizedSkillMode(state.Users[i].Defaults.SkillMode)
		state.Users[i].Defaults.SkillPermissions = normalizedSkillPermissions(state.Users[i].Defaults.AllowSkillExecution, state.Users[i].Defaults.SkillPermissions)
		if len(state.Users[i].Defaults.DefaultParticipants) == 0 {
			state.Users[i].Defaults.DefaultParticipants = defaultParticipants()
		}
	}
	for ci := range state.Conversations {
		c := &state.Conversations[ci]
		for pi := range c.Participants {
			if c.Participants[pi].SkillIDs == nil {
				c.Participants[pi].SkillIDs = []string{}
			}
		}
		if c.ReviewRounds < 1 {
			c.ReviewRounds = 1
		}
		if c.ReviewRounds > 5 {
			c.ReviewRounds = 5
		}
		c.SkillMode = normalizedSkillMode(c.SkillMode)
		c.SkillPermissions = normalizedSkillPermissions(c.AllowSkillExecution, c.SkillPermissions)
		if !c.Started {
			for _, m := range state.ChatMessages {
				if m.ConversationID == c.ID && m.Kind == "agent" {
					c.Started = true
					break
				}
			}
		}
	}
	return state, nil
}

func (s *Store) Update(fn func(*State) error) error {
	return s.update(false, fn)
}

func (s *Store) UpdateMetadata(fn func(*State) error) error {
	return s.update(false, fn)
}

func (s *Store) update(_ bool, fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := acquireLock(s.Root, 10*time.Second)
	if err != nil {
		return err
	}
	defer release()
	state, err := s.ReadMetadata()
	if err != nil {
		return err
	}
	if err := fn(&state); err != nil {
		return err
	}
	if err := upsertCollections(s.Root, state.ChatMessages, state.TaskRuns); err != nil {
		return err
	}
	return writeStateMetadata(s.Root, state)
}

func writeStateMetadata(root string, state State) error {
	state.ChatMessages = nil
	state.TaskRuns = nil
	return writeState(root, state)
}

func writeState(root string, state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(TagDir(root), ".state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, StatePath(root))
}

func acquireLock(root string, timeout time.Duration) (func(), error) {
	lock := filepath.Join(TagDir(root), "lock")
	deadline := time.Now().Add(timeout)
	for {
		if err := os.Mkdir(lock, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(lock, "owner"), []byte(fmt.Sprintf("%d\n%d\n", os.Getpid(), time.Now().UnixMilli())), 0o644)
			return func() { _ = os.RemoveAll(lock) }, nil
		}
		if info, err := os.Stat(lock); err == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.RemoveAll(lock)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("timed out waiting for team state lock")
		}
		time.Sleep(40 * time.Millisecond)
	}
}

func NextID(state *State, prefix string) string {
	state.Sequence++
	return fmt.Sprintf("%s-%04d", prefix, state.Sequence)
}
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func StringPtr(value string) *string { return &value }

func TruncateForLog(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + "\n…（日志已截断）"
	}
	return value
}

func ScopesOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			if a == b || len(a) > len(b) && len(a) > len(b)+1 && a[:len(b)+1] == b+"/" || len(b) > len(a) && len(b) > len(a)+1 && b[:len(a)+1] == a+"/" {
				return true
			}
		}
	}
	return false
}
