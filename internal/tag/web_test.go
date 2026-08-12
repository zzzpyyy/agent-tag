package tag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

func TestArtifactCenterDownloadAndOwnerIsolation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "web", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, fstest.MapFS{"index.html": {Data: []byte("ok")}}).Handler())
	defer server.Close()
	owner := authenticatedClient(t, server.URL, "owner")
	state, err := store.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(TagDir(root), "result.txt")
	if err := os.WriteFile(path, []byte("downloadable"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := ArtifactRecord{ID: "artifact-download", OwnerID: state.Users[0].ID, ConversationID: "conv-1", Agent: "codex", Path: path, Label: "result", MediaType: "text/plain", Size: 12, SHA256: "digest", CreatedAt: Now()}
	if err := store.SaveArtifact(record); err != nil {
		t.Fatal(err)
	}
	response, err := owner.Get(server.URL + "/api/artifacts/artifact-download/content")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != "downloadable" {
		t.Fatalf("download status=%d body=%q", response.StatusCode, body)
	}
	other := authenticatedClient(t, server.URL, "other")
	response, err = other.Get(server.URL + "/api/artifacts/artifact-download/content")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-owner status=%d", response.StatusCode)
	}
}

type fakeChat struct {
	mu    sync.Mutex
	calls []RunRequest
}

type fakeProviderInstaller struct {
	installed *bool
	calls     []string
}

func (f *fakeProviderInstaller) Install(_ context.Context, provider string) (ProviderInstallResult, error) {
	f.calls = append(f.calls, provider)
	*f.installed = true
	return ProviderInstallResult{Provider: provider, Command: "npm install -g fake-" + provider}, nil
}

type artifactChat struct {
	mu       sync.Mutex
	calls    []RunRequest
	artifact string
}

func (f *artifactChat) Chat(_ context.Context, request RunRequest) (RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, request)
	result := RunResult{Text: request.Provider + " reply"}
	if len(f.calls) == 1 {
		result.Artifacts = []RunArtifact{{Path: f.artifact, Label: "知识库正文"}}
	}
	return result, nil
}

type blockingChat struct {
	started chan struct{}
	stopped chan struct{}
}

type raceChat struct {
	started chan RunRequest
	release chan struct{}
}

type individuallyCancelableChat struct {
	started chan string
	release chan struct{}
}

func (chat *individuallyCancelableChat) Chat(ctx context.Context, request RunRequest) (RunResult, error) {
	if request.OnProgress != nil {
		request.OnProgress(RunProgress{Text: request.Provider + " partial", Steps: []string{"读取文件：README.md"}})
	}
	chat.started <- request.AgentName
	if request.AgentName == "cc" {
		<-ctx.Done()
		return RunResult{}, ctx.Err()
	}
	select {
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case <-chat.release:
		session := request.Provider + "-session"
		return RunResult{Text: request.Provider + " complete", SessionID: &session}, nil
	}
}

func (f *raceChat) Chat(_ context.Context, req RunRequest) (RunResult, error) {
	f.started <- req
	<-f.release
	delays := map[string]time.Duration{"codex": 0, "pi": 30 * time.Millisecond, "claude": 60 * time.Millisecond}
	time.Sleep(delays[req.Provider])
	sid := req.Provider + "-session"
	return RunResult{Text: req.Provider + " raced", SessionID: &sid}, nil
}

func (f *blockingChat) Chat(ctx context.Context, _ RunRequest) (RunResult, error) {
	f.started <- struct{}{}
	<-ctx.Done()
	f.stopped <- struct{}{}
	return RunResult{}, ctx.Err()
}

func (f *fakeChat) Chat(_ context.Context, req RunRequest) (RunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	sid := req.SessionID
	if sid == nil {
		v := req.Provider + "-native-session"
		sid = &v
	}
	return RunResult{Text: req.Provider + " reply", Steps: []string{"读取文件：README.md"}, SessionID: sid}, nil
}

func requestJSON(t *testing.T, client *http.Client, method, url string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postJSON(t *testing.T, client *http.Client, url string, body any) *http.Response {
	return requestJSON(t, client, http.MethodPost, url, body)
}

func authenticatedClient(t *testing.T, serverURL string, usernames ...string) *http.Client {
	t.Helper()
	username := "tester"
	if len(usernames) > 0 {
		username = usernames[0]
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	response := postJSON(t, client, serverURL+"/api/auth/register", map[string]string{"username": username, "password": "password-123"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("register status = %d", response.StatusCode)
	}
	return client
}

func postSkillZIP(t *testing.T, client *http.Client, url string, archive *bytes.Reader) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "imported-skill.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := archive.WriteTo(part); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url, &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestImportedSkillAPIIsTenantScopedAndAssignable(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "imported-skills", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	registry := NewSkillRegistry(root, nil)
	server := httptest.NewServer(NewWebServerWithSkillRegistry(store, &fakeChat{}, assets, registry).Handler())
	defer server.Close()
	alice := authenticatedClient(t, server.URL, "alice-import")
	bob := authenticatedClient(t, server.URL, "bob-import")
	archive := skillArchive(t, map[string]string{"package/SKILL.md": "---\nname: imported-skill\ndescription: Imported through the API.\n---\nUse this procedure."})
	response := postSkillZIP(t, alice, server.URL+"/api/skills/import", archive)
	if response.StatusCode != http.StatusCreated {
		defer response.Body.Close()
		var failure map[string]any
		_ = json.NewDecoder(response.Body).Decode(&failure)
		t.Fatalf("import status=%d body=%v", response.StatusCode, failure)
	}
	var result struct {
		Skills []SkillDefinition `json:"skills"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(result.Skills) != 1 {
		t.Fatalf("imported skills=%+v", result.Skills)
	}
	id := result.Skills[0].ID
	detail := requestJSON(t, alice, http.MethodGet, server.URL+"/api/skills/"+id, nil)
	if detail.StatusCode != http.StatusOK {
		t.Fatalf("alice detail status=%d", detail.StatusCode)
	}
	_ = detail.Body.Close()
	forbidden := requestJSON(t, bob, http.MethodGet, server.URL+"/api/skills/"+id, nil)
	if forbidden.StatusCode != http.StatusNotFound {
		t.Fatalf("bob detail status=%d", forbidden.StatusCode)
	}
	_ = forbidden.Body.Close()
	created := postJSON(t, alice, server.URL+"/api/conversations", map[string]string{"title": "imported skill assignment"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	assigned := requestJSON(t, alice, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID+"/skills", map[string]any{"agent": "cc", "skillIds": []string{id}})
	if assigned.StatusCode != http.StatusOK {
		t.Fatalf("assign status=%d", assigned.StatusCode)
	}
	_ = assigned.Body.Close()
}

func TestTenantAuthenticationAndRecordIsolation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "tenants", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, assets).Handler())
	defer server.Close()

	unauthenticated := requestJSON(t, server.Client(), http.MethodGet, server.URL+"/api/state", nil)
	_ = unauthenticated.Body.Close()
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous state status = %d", unauthenticated.StatusCode)
	}

	alice := authenticatedClient(t, server.URL, "alice")
	bob := authenticatedClient(t, server.URL, "bob")
	created := postJSON(t, alice, server.URL+"/api/conversations", map[string]string{"title": "Alice private discussion"})
	var aliceConversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&aliceConversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	if err := store.Update(func(state *State) error {
		state.ChatMessages = append(state.ChatMessages, ChatMessage{ID: NextID(state, "chat"), ConversationID: aliceConversation.ID, Author: "你", Kind: "user", Body: "private message", CreatedAt: Now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	response := requestJSON(t, bob, http.MethodGet, server.URL+"/api/state", nil)
	var bobState struct {
		Defaults      DiscussionSettings `json:"defaults"`
		Conversations []Conversation     `json:"conversations"`
		ChatMessages  []ChatMessage      `json:"chatMessages"`
	}
	if err := json.NewDecoder(response.Body).Decode(&bobState); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if len(bobState.Conversations) != 0 || len(bobState.ChatMessages) != 0 {
		t.Fatalf("bob saw alice records: conversations=%d messages=%d", len(bobState.Conversations), len(bobState.ChatMessages))
	}

	forbidden := requestJSON(t, bob, http.MethodPatch, server.URL+"/api/conversations/"+aliceConversation.ID+"/settings", map[string]any{"autoReview": true})
	_ = forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant settings status = %d", forbidden.StatusCode)
	}

	aliceDefaults := requestJSON(t, alice, http.MethodPatch, server.URL+"/api/settings", map[string]any{"autoReview": true, "reviewRounds": 4})
	_ = aliceDefaults.Body.Close()
	response = requestJSON(t, bob, http.MethodGet, server.URL+"/api/state", nil)
	if err := json.NewDecoder(response.Body).Decode(&bobState); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if bobState.Defaults.AutoReview || bobState.Defaults.ReviewRounds != 1 {
		t.Fatalf("bob defaults changed with alice: %+v", bobState.Defaults)
	}
}

func TestManagedSkillIsTenantScopedAndInjectedForEveryProvider(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "managed-skills", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	alice := authenticatedClient(t, server.URL, "alice")
	bob := authenticatedClient(t, server.URL, "bob")

	createdSkill := postJSON(t, alice, server.URL+"/api/skills", map[string]string{"name": "evidence-review", "description": "Use when reviewing technical claims.", "content": "Check each claim against concrete evidence and label assumptions."})
	var skill ManagedSkill
	if err := json.NewDecoder(createdSkill.Body).Decode(&skill); err != nil {
		t.Fatal(err)
	}
	_ = createdSkill.Body.Close()
	createdConversation := postJSON(t, alice, server.URL+"/api/conversations", map[string]string{"title": "skill discussion"})
	var conversation Conversation
	if err := json.NewDecoder(createdConversation.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = createdConversation.Body.Close()
	if err := store.Update(func(state *State) error {
		current := findOwnedConversation(state, conversation.ID, conversation.OwnerID)
		current.Started = true
		current.AutoRelay = true
		current.Participants = append(current.Participants, Participant{Name: "pi", Provider: "pi", AutoDiscuss: true, SkillIDs: []string{}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{"cc", "codex", "pi"} {
		assigned := requestJSON(t, alice, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID+"/skills", map[string]any{"agent": agent, "skillIds": []string{skill.ID}})
		_ = assigned.Body.Close()
		if assigned.StatusCode != http.StatusOK {
			t.Fatalf("assign %s status = %d", agent, assigned.StatusCode)
		}
	}
	posted := postJSON(t, alice, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "review this claim"})
	_ = posted.Body.Close()
	state := waitForMessages(t, store, 4)
	waitInactive(t, web, conversation.ID)
	runner.mu.Lock()
	calls := append([]RunRequest(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) != 3 {
		t.Fatalf("provider calls = %d", len(calls))
	}
	for _, call := range calls {
		if !strings.Contains(call.Prompt, "BEGIN MANAGED SKILL: evidence-review") || !strings.Contains(call.Prompt, "Check each claim against concrete evidence") {
			t.Fatalf("%s did not receive skill: %s", call.Provider, call.Prompt)
		}
	}
	loaded := 0
	for _, message := range state.ChatMessages {
		if message.Kind == "agent" && len(message.Steps) > 0 && message.Steps[0] == "加载 Skill：evidence-review" {
			loaded++
		}
	}
	if loaded != 3 {
		t.Fatalf("skill loading steps = %d", loaded)
	}

	bobStateResponse := requestJSON(t, bob, http.MethodGet, server.URL+"/api/state", nil)
	var bobState struct {
		Skills []ManagedSkill `json:"skills"`
	}
	if err := json.NewDecoder(bobStateResponse.Body).Decode(&bobState); err != nil {
		t.Fatal(err)
	}
	_ = bobStateResponse.Body.Close()
	if len(bobState.Skills) != 0 {
		t.Fatalf("bob saw alice skills: %+v", bobState.Skills)
	}
	forbidden := requestJSON(t, bob, http.MethodDelete, server.URL+"/api/skills/"+skill.ID, nil)
	_ = forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status = %d", forbidden.StatusCode)
	}
}

func waitForMessages(t *testing.T, store *Store, count int) State {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := store.Read()
		if err == nil && len(state.ChatMessages) >= count {
			return state
		}
		time.Sleep(10 * time.Millisecond)
	}
	state, _ := store.Read()
	t.Fatalf("timed out: got %d messages, want %d", len(state.ChatMessages), count)
	return State{}
}

func waitInactive(t *testing.T, web *WebServer, conversationID string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		web.mu.Lock()
		_, active := web.active[conversationID]
		web.mu.Unlock()
		if !active {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("turn stayed active")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWebFirstMentionAutoRelayAndSessionReuse(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "web", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)

	resp := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "design"})
	var conv Conversation
	_ = json.NewDecoder(resp.Body).Decode(&conv)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	rejected := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "hello"})
	_ = rejected.Body.Close()
	if rejected.StatusCode != http.StatusBadRequest {
		t.Fatalf("first message status = %d", rejected.StatusCode)
	}

	first := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "@cc first"})
	_ = first.Body.Close()
	waitForMessages(t, store, 2)
	waitInactive(t, web, conv.ID)
	second := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "@cc second"})
	_ = second.Body.Close()
	state := waitForMessages(t, store, 4)

	runner.mu.Lock()
	calls := append([]RunRequest(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) != 2 || calls[0].SessionID != nil || calls[1].SessionID == nil || *calls[1].SessionID != "claude-native-session" {
		t.Fatalf("session calls = %+v", calls)
	}
	if state.ChatMessages[0].Body != "first" || state.ChatMessages[2].Body != "second" {
		t.Fatalf("visible bodies = %q, %q", state.ChatMessages[0].Body, state.ChatMessages[2].Body)
	}
	if err := store.Update(func(st *State) error {
		findConversation(st, conv.ID).AutoRelay = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	third := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "ordinary follow-up"})
	_ = third.Body.Close()
	waitForMessages(t, store, 7)
	runner.mu.Lock()
	calls = append([]RunRequest(nil), runner.calls...)
	runner.mu.Unlock()
	if len(calls) != 4 {
		t.Fatalf("auto relay calls = %+v", calls)
	}
	thirdCalls := append([]RunRequest(nil), calls[2:]...)
	sort.Slice(thirdCalls, func(i, j int) bool { return thirdCalls[i].Provider < thirdCalls[j].Provider })
	if thirdCalls[0].Provider != "claude" || thirdCalls[1].Provider != "codex" {
		t.Fatalf("auto relay providers = %+v", thirdCalls)
	}
	if strings.Contains(thirdCalls[1].Prompt, "ordinary follow-up\n\ncc:") {
		t.Fatalf("Codex received a same-round Claude reply: %s", thirdCalls[1].Prompt)
	}
	waitInactive(t, web, conv.ID)
	review := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/review", map[string]string{"mode": "synthesize", "agent": "codex"})
	_ = review.Body.Close()
	if review.StatusCode != http.StatusAccepted {
		t.Fatalf("synthesis status = %d", review.StatusCode)
	}
	state = waitForMessages(t, store, 8)
	last := state.ChatMessages[len(state.ChatMessages)-1]
	if last.Phase != "synthesis" || last.RoundID == "" || len(last.SourceMessageIDs) != 2 {
		t.Fatalf("synthesis metadata = %+v", last)
	}
	runner.mu.Lock()
	synthesisPrompt := runner.calls[len(runner.calls)-1].Prompt
	runner.mu.Unlock()
	if !strings.Contains(synthesisPrompt, "[cc]\nclaude reply") || !strings.Contains(synthesisPrompt, "[codex]\ncodex reply") {
		t.Fatalf("synthesis prompt missing views: %s", synthesisPrompt)
	}
	waitInactive(t, web, conv.ID)
}

func TestStartedMessageWithoutTargetIsRejected(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "target-required", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, &fakeChat{}, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "routing"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	if err := store.Update(func(state *State) error {
		findConversation(state, conversation.ID).Started = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "no target"})
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", response.StatusCode)
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ChatMessages) != 0 {
		t.Fatalf("targetless message was persisted: %+v", state.ChatMessages)
	}
}

func TestAgentToolArtifactIsCopiedAndSharedWithNextAgent(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "shared-artifact", false); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(source, []byte("shared knowledge document body"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &artifactChat{artifact: source}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "artifact"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	first := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@cc fetch"})
	_ = first.Body.Close()
	waitForMessages(t, store, 2)
	waitInactive(t, web, conversation.ID)
	second := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@codex interpret it"})
	_ = second.Body.Close()
	waitForMessages(t, store, 4)
	waitInactive(t, web, conversation.ID)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[1].Prompt, ".agent-tag/artifacts") {
		t.Fatalf("shared artifact was missing from next prompt: %+v", runner.calls)
	}
}

func TestAutoSkillModeMatchesLocalSkillAndInjectsIt(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "auto-skill", false); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "skills", "ku-doc-fetcher")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	skillBody := "---\nname: ku-doc-fetcher\ndescription: Use for ku.baidu-int.com knowledge document URLs.\n---\nRun scripts/fetch.py for the supplied URL."
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	registry := NewSkillRegistry(root, []SkillRoot{{Label: "Local", Path: filepath.Dir(local)}})
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServerWithSkillRegistry(store, runner, assets, registry)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "auto skill"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	settings := requestJSON(t, client, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID+"/settings", map[string]any{"skillMode": "auto"})
	_ = settings.Body.Close()
	if settings.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d", settings.StatusCode)
	}
	response := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@cc 解读 https://ku.baidu-int.com/knowledge/a/b/c/d"})
	_ = response.Body.Close()
	waitForMessages(t, store, 2)
	waitInactive(t, web, conversation.ID)
	followUp := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@cc 重新获取"})
	_ = followUp.Body.Close()
	waitForMessages(t, store, 4)
	waitInactive(t, web, conversation.ID)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 2 || !strings.Contains(runner.calls[0].Prompt, "BEGIN MANAGED SKILL: ku-doc-fetcher") || !strings.Contains(runner.calls[1].Prompt, "BEGIN MANAGED SKILL: ku-doc-fetcher") {
		t.Fatalf("auto-matched skill was not injected: %+v", runner.calls)
	}
}

func TestAutoSkillModeDoesNotMatchGeneratedSystemPrompt(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "auto-skill", false); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(t.TempDir(), "skills", "false-positive")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	skillBody := "---\nname: false-positive\ndescription: Use when the user explicitly says “skill” or asks to configure a browser.\n---\nMust not load for a greeting."
	if err := os.WriteFile(filepath.Join(local, "SKILL.md"), []byte(skillBody), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	registry := NewSkillRegistry(root, []SkillRoot{{Label: "Local", Path: filepath.Dir(local)}})
	web := NewWebServerWithSkillRegistry(store, runner, fstest.MapFS{"index.html": {Data: []byte("ok")}}, registry)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "greeting"})
	var conversation Conversation
	_ = json.NewDecoder(created.Body).Decode(&conversation)
	_ = created.Body.Close()
	response := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@codex 你好"})
	_ = response.Body.Close()
	waitForMessages(t, store, 2)
	waitInactive(t, web, conversation.ID)
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || strings.Contains(runner.calls[0].Prompt, "BEGIN MANAGED SKILL") {
		t.Fatalf("generated prompt caused a false positive: %+v", runner.calls)
	}
}

func TestPeerReviewStartsAllAgentsWithPeerViews(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "review", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	var convID string
	if err := store.Update(func(st *State) error {
		now := Now()
		convID = NextID(st, "conv")
		st.Conversations = append(st.Conversations, Conversation{ID: convID, Title: "review", Started: true, AutoRelay: true, CreatedAt: now, UpdatedAt: now, Participants: []Participant{{Name: "cc", Provider: "claude"}, {Name: "codex", Provider: "codex", AutoDiscuss: true}, {Name: "pi", Provider: "pi", AutoDiscuss: true}}})
		roundID := NextID(st, "chat")
		st.ChatMessages = append(st.ChatMessages, ChatMessage{ID: roundID, ConversationID: convID, RoundID: roundID, Phase: "query", Author: "你", Kind: "user", Body: "Which design?", CreatedAt: now})
		for _, author := range []string{"cc", "codex", "pi"} {
			provider := map[string]string{"cc": "claude", "codex": "codex", "pi": "pi"}[author]
			st.ChatMessages = append(st.ChatMessages, ChatMessage{ID: NextID(st, "chat"), ConversationID: convID, RoundID: roundID, Phase: "primary", Author: author, Provider: &provider, Kind: "agent", Body: author + " view", CreatedAt: now})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := &raceChat{started: make(chan RunRequest, 3), release: make(chan struct{})}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	response := postJSON(t, client, server.URL+"/api/conversations/"+convID+"/review", map[string]string{"mode": "peer"})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("peer review status = %d", response.StatusCode)
	}
	for i := 0; i < 3; i++ {
		select {
		case req := <-runner.started:
			if strings.Contains(req.Prompt, "["+req.AgentName+"]") {
				t.Fatalf("%s received its own view: %s", req.AgentName, req.Prompt)
			}
		case <-time.After(time.Second):
			t.Fatalf("only %d peer reviewers started", i)
		}
	}
	close(runner.release)
	state := waitForMessages(t, store, 7)
	for _, m := range state.ChatMessages[4:] {
		if m.Phase != "review" || m.ReviewRound != 1 || len(m.SourceMessageIDs) != 3 {
			t.Fatalf("peer review metadata = %+v", m)
		}
	}
	waitInactive(t, web, convID)
	second := postJSON(t, client, server.URL+"/api/conversations/"+convID+"/review", map[string]string{"mode": "peer"})
	_ = second.Body.Close()
	if second.StatusCode != http.StatusAccepted {
		t.Fatalf("second peer review status = %d", second.StatusCode)
	}
	state = waitForMessages(t, store, 10)
	for _, m := range state.ChatMessages[7:] {
		if m.Phase != "review" || m.ReviewRound != 2 || len(m.SourceMessageIDs) != 3 {
			t.Fatalf("second peer review metadata = %+v", m)
		}
	}
	waitInactive(t, web, convID)
}

func TestAutomaticPeerReviewRunsConfiguredRounds(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "auto-review", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "auto"})
	var conv Conversation
	_ = json.NewDecoder(created.Body).Decode(&conv)
	_ = created.Body.Close()
	if err := store.Update(func(st *State) error {
		c := findConversation(st, conv.ID)
		c.Started = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	settings := requestJSON(t, client, http.MethodPatch, server.URL+"/api/conversations/"+conv.ID+"/settings", map[string]any{"autoRelay": true, "autoReview": true, "reviewRounds": 2})
	_ = settings.Body.Close()
	if settings.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", settings.StatusCode)
	}
	posted := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "compare designs"})
	_ = posted.Body.Close()
	state := waitForMessages(t, store, 7)
	counts := map[int]int{}
	for _, message := range state.ChatMessages {
		if message.Phase == "review" {
			counts[message.ReviewRound]++
		}
	}
	if counts[1] != 2 || counts[2] != 2 {
		t.Fatalf("automatic review rounds = %v", counts)
	}
	waitInactive(t, web, conv.ID)
}

func TestDiscussionSettingsAreIsolatedPerConversation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "conversation-settings", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, assets).Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)

	create := func(title string) Conversation {
		response := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": title})
		defer response.Body.Close()
		var conversation Conversation
		if err := json.NewDecoder(response.Body).Decode(&conversation); err != nil {
			t.Fatal(err)
		}
		return conversation
	}
	first := create("first")
	second := create("second")
	if err := store.Update(func(state *State) error {
		conversation := findConversation(state, first.ID)
		conversation.AllowSkillExecution = false
		conversation.Participants[0].SessionID = StringPtr("old-session")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, client, http.MethodPatch, server.URL+"/api/conversations/"+first.ID+"/settings", map[string]any{"autoReview": true, "reviewRounds": 4, "skillMode": "auto", "allowSkillExecution": true})
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status = %d", response.StatusCode)
	}

	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	gotFirst := findConversation(&state, first.ID)
	gotSecond := findConversation(&state, second.ID)
	if !gotFirst.AutoReview || gotFirst.ReviewRounds != 4 || gotFirst.SkillMode != "auto" || !gotFirst.AllowSkillExecution || gotFirst.Participants[0].SessionID != nil {
		t.Fatalf("first settings = %+v", gotFirst)
	}
	if gotSecond.AutoReview || gotSecond.ReviewRounds != 1 || gotSecond.SkillMode != "auto" || !gotSecond.AllowSkillExecution {
		t.Fatalf("second settings changed = %+v", gotSecond)
	}
}

func TestGlobalSettingsBecomeDefaultsForNewConversations(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "global-settings", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, assets).Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)

	updated := requestJSON(t, client, http.MethodPatch, server.URL+"/api/settings", map[string]any{"autoRelay": true, "autoReview": true, "reviewRounds": 3, "skillMode": "auto", "allowSkillExecution": true})
	_ = updated.Body.Close()
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("global settings status = %d", updated.StatusCode)
	}
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "inherits defaults"})
	defer created.Body.Close()
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	if !conversation.AutoRelay || !conversation.AutoReview || conversation.ReviewRounds != 3 || conversation.SkillMode != "auto" || !conversation.AllowSkillExecution {
		t.Fatalf("conversation defaults = %+v", conversation)
	}
}

func TestNewConversationCanChooseAndPersistDefaultAgents(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "defaults", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, fstest.MapFS{"index.html": {Data: []byte("ok")}}).Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	chosen := []Participant{{Name: "pi", Provider: "pi", AutoDiscuss: true}}
	response := postJSON(t, client, server.URL+"/api/conversations", map[string]any{"title": "first", "participants": chosen, "saveAsDefault": true})
	var first Conversation
	_ = json.NewDecoder(response.Body).Decode(&first)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated || len(first.Participants) != 1 || first.Participants[0].Provider != "pi" {
		t.Fatalf("first=%+v status=%d", first, response.StatusCode)
	}
	response = postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "second"})
	var second Conversation
	_ = json.NewDecoder(response.Body).Decode(&second)
	_ = response.Body.Close()
	if len(second.Participants) != 1 || second.Participants[0].Provider != "pi" {
		t.Fatalf("saved defaults were not reused: %+v", second.Participants)
	}
}

func TestFirstMessageAllTargetsEveryParticipant(t *testing.T) {
	conversation := Conversation{Participants: []Participant{{Name: "cc", Provider: "claude"}, {Name: "codex", Provider: "codex"}, {Name: "pi", Provider: "pi"}}}
	targets := responseTargets(&conversation, "@all 大家好")
	if len(targets) != 3 {
		t.Fatalf("@all targets=%+v", targets)
	}
}

func TestFirstMessageAutomaticallyNamesUntitledConversation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "titles", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	web := NewWebServer(store, &fakeChat{}, fstest.MapFS{"index.html": {Data: []byte("ok")}})
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	response := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": ""})
	var conversation Conversation
	_ = json.NewDecoder(response.Body).Decode(&conversation)
	_ = response.Body.Close()
	if conversation.Title != "新对话" {
		t.Fatalf("initial title=%q", conversation.Title)
	}
	response = postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@codex 评审登录模块方案"})
	_ = response.Body.Close()
	waitForMessages(t, store, 2)
	waitInactive(t, web, conversation.ID)
	state, err := store.ReadMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if got := findConversation(&state, conversation.ID).Title; got != "评审登录模块方案" {
		t.Fatalf("automatic title=%q", got)
	}
}

func TestAutoRelayAgentsRaceInsteadOfWaitingInOrder(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "race", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &raceChat{started: make(chan RunRequest, 3), release: make(chan struct{})}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)
	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "race"})
	var conv Conversation
	_ = json.NewDecoder(created.Body).Decode(&conv)
	_ = created.Body.Close()
	if err := store.Update(func(st *State) error {
		c := findConversation(st, conv.ID)
		c.Started = true
		c.AutoRelay = true
		c.Participants = append(c.Participants, Participant{Name: "pi", Provider: "pi", AutoDiscuss: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	posted := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "race now"})
	_ = posted.Body.Close()
	providers := []string{}
	for len(providers) < 3 {
		select {
		case req := <-runner.started:
			providers = append(providers, req.Provider)
			if strings.Contains(req.Prompt, " raced") {
				t.Fatalf("same-round reply leaked into prompt: %s", req.Prompt)
			}
		case <-time.After(time.Second):
			t.Fatalf("only %d providers started before release; calls were sequential", len(providers))
		}
	}
	sort.Strings(providers)
	if strings.Join(providers, ",") != "claude,codex,pi" {
		t.Fatalf("racing providers = %v", providers)
	}
	close(runner.release)
	state := waitForMessages(t, store, 4)
	authors := []string{state.ChatMessages[1].Author, state.ChatMessages[2].Author, state.ChatMessages[3].Author}
	if strings.Join(authors, ",") != "codex,pi,cc" {
		t.Fatalf("completion order = %v", authors)
	}
	deadline := time.Now().Add(time.Second)
	for {
		web.mu.Lock()
		_, active := web.active[conv.ID]
		web.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("race turn stayed active after all replies")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWebCanCancelBeforeResending(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "cancel", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &blockingChat{started: make(chan struct{}, 2), stopped: make(chan struct{}, 2)}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL)

	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "cancel"})
	var conv Conversation
	_ = json.NewDecoder(created.Body).Decode(&conv)
	_ = created.Body.Close()
	first := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "@cc typo"})
	_ = first.Body.Close()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("provider did not start")
	}

	conflict := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "corrected"})
	_ = conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("concurrent message status = %d", conflict.StatusCode)
	}
	cancelled := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/cancel", map[string]any{})
	_ = cancelled.Body.Close()
	if cancelled.StatusCode != http.StatusAccepted {
		t.Fatalf("cancel status = %d", cancelled.StatusCode)
	}
	select {
	case <-runner.stopped:
	case <-time.After(time.Second):
		t.Fatal("provider process was not cancelled")
	}
	state := waitForMessages(t, store, 2)
	if state.ChatMessages[1].Kind != "system" || state.ChatMessages[1].Body != "已终止本轮回复，可以修改后重新发送。" {
		t.Fatalf("cancel message = %+v", state.ChatMessages[1])
	}
	deadline := time.Now().Add(time.Second)
	for {
		web.mu.Lock()
		_, active := web.active[conv.ID]
		web.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn stayed active after cancellation")
		}
		time.Sleep(5 * time.Millisecond)
	}

	retry := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/messages", map[string]string{"body": "@cc corrected"})
	_ = retry.Body.Close()
	if retry.StatusCode != http.StatusCreated {
		t.Fatalf("retry status = %d", retry.StatusCode)
	}
	<-runner.started
	cleanup := postJSON(t, client, server.URL+"/api/conversations/"+conv.ID+"/cancel", map[string]any{})
	_ = cleanup.Body.Close()
	<-runner.stopped
	deadline = time.Now().Add(time.Second)
	for {
		web.mu.Lock()
		_, active := web.active[conv.ID]
		web.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry turn stayed active after cleanup")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConversationLifecycleAndParticipantManagementAreTenantScoped(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "lifecycle", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, assets).Handler())
	defer server.Close()
	alice := authenticatedClient(t, server.URL, "lifecycle-alice")
	bob := authenticatedClient(t, server.URL, "lifecycle-bob")

	created := postJSON(t, alice, server.URL+"/api/conversations", map[string]string{"title": "original"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()

	updated := requestJSON(t, alice, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID, map[string]any{"title": "renamed", "archived": true})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("update conversation status=%d", updated.StatusCode)
	}
	var renamed Conversation
	if err := json.NewDecoder(updated.Body).Decode(&renamed); err != nil {
		t.Fatal(err)
	}
	_ = updated.Body.Close()
	if renamed.Title != "renamed" || !renamed.Archived {
		t.Fatalf("updated conversation=%+v", renamed)
	}

	forbidden := requestJSON(t, bob, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID, map[string]string{"title": "stolen"})
	_ = forbidden.Body.Close()
	if forbidden.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-tenant conversation update status=%d", forbidden.StatusCode)
	}

	participant := requestJSON(t, alice, http.MethodPatch, server.URL+"/api/conversations/"+conversation.ID+"/participants/codex", map[string]any{"name": "reviewer", "provider": "pi", "model": "custom-model", "autoDiscuss": false})
	if participant.StatusCode != http.StatusOK {
		t.Fatalf("update participant status=%d", participant.StatusCode)
	}
	var reviewer Participant
	if err := json.NewDecoder(participant.Body).Decode(&reviewer); err != nil {
		t.Fatal(err)
	}
	_ = participant.Body.Close()
	if reviewer.Name != "reviewer" || reviewer.Provider != "pi" || reviewer.Model == nil || *reviewer.Model != "custom-model" || reviewer.AutoDiscuss {
		t.Fatalf("updated participant=%+v", reviewer)
	}

	if err := store.Update(func(state *State) error {
		current := findConversation(state, conversation.ID)
		session := "native-session"
		current.Participants[1].SessionID = &session
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reset := postJSON(t, alice, server.URL+"/api/conversations/"+conversation.ID+"/participants/reviewer/reset-session", map[string]any{})
	_ = reset.Body.Close()
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset participant status=%d", reset.StatusCode)
	}

	removed := requestJSON(t, alice, http.MethodDelete, server.URL+"/api/conversations/"+conversation.ID+"/participants/reviewer", map[string]any{})
	_ = removed.Body.Close()
	if removed.StatusCode != http.StatusOK {
		t.Fatalf("remove participant status=%d", removed.StatusCode)
	}

	if err := os.MkdirAll(filepath.Join(TagDir(root), "artifacts", renamed.OwnerID, conversation.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	deleted := requestJSON(t, alice, http.MethodDelete, server.URL+"/api/conversations/"+conversation.ID, map[string]any{})
	_ = deleted.Body.Close()
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete conversation status=%d", deleted.StatusCode)
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if findConversation(&state, conversation.ID) != nil {
		t.Fatal("deleted conversation remains in state")
	}
	if _, err := os.Stat(filepath.Join(TagDir(root), "artifacts", renamed.OwnerID, conversation.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("artifact directory still exists: %v", err)
	}
}

func TestLiveProgressAndIndividualAgentCancellation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "streaming", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &individuallyCancelableChat{started: make(chan string, 2), release: make(chan struct{})}
	assets := fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})
	web := NewWebServer(store, runner, assets)
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL, "stream-user")

	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "stream"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	started := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@all stream this"})
	_ = started.Body.Close()
	if started.StatusCode != http.StatusCreated {
		t.Fatalf("start status=%d", started.StatusCode)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-runner.started:
		case <-time.After(time.Second):
			t.Fatal("agents did not start")
		}
	}

	stateResponse := requestJSON(t, client, http.MethodGet, server.URL+"/api/state", nil)
	var apiState struct {
		LiveReplies []LiveReply `json:"liveReplies"`
	}
	if err := json.NewDecoder(stateResponse.Body).Decode(&apiState); err != nil {
		t.Fatal(err)
	}
	_ = stateResponse.Body.Close()
	if len(apiState.LiveReplies) != 2 {
		t.Fatalf("live replies=%+v", apiState.LiveReplies)
	}
	for _, reply := range apiState.LiveReplies {
		if !strings.Contains(reply.Text, "partial") || len(reply.Steps) != 1 {
			t.Fatalf("live reply=%+v", reply)
		}
	}

	cancelled := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/participants/cc/cancel", map[string]any{})
	_ = cancelled.Body.Close()
	if cancelled.StatusCode != http.StatusAccepted {
		t.Fatalf("individual cancel status=%d", cancelled.StatusCode)
	}
	close(runner.release)
	deadline := time.Now().Add(time.Second)
	for {
		web.mu.Lock()
		_, active := web.active[conversation.ID]
		web.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("turn remained active")
		}
		time.Sleep(5 * time.Millisecond)
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	foundCodex := false
	for _, message := range state.ChatMessages {
		if message.Kind == "agent" && message.Author == "codex" && message.Body == "codex complete" {
			foundCodex = true
		}
		if message.Kind == "system" && message.Body == "已终止本轮回复，可以修改后重新发送。" {
			t.Fatal("individual cancellation was treated as whole-turn cancellation")
		}
	}
	if !foundCodex {
		t.Fatalf("remaining agent did not complete: %+v", state.ChatMessages)
	}
}

func TestWebTaskBoardLifecycleAndProviderConfig(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "web-tasks", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	server := httptest.NewServer(NewWebServer(store, &fakeChat{}, fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}})).Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL, "task-user")

	configured := requestJSON(t, client, http.MethodPatch, server.URL+"/api/providers/codex", map[string]any{"executable": "/custom/codex", "launchCommand": "my-codex", "extraArgs": "--profile team", "timeoutSeconds": 45})
	if configured.StatusCode != http.StatusOK {
		t.Fatalf("provider config status=%d", configured.StatusCode)
	}
	var providerConfigResponse ProviderConfig
	if err := json.NewDecoder(configured.Body).Decode(&providerConfigResponse); err != nil || providerConfigResponse.LaunchCommand != "my-codex" {
		t.Fatalf("provider config=%+v err=%v", providerConfigResponse, err)
	}
	_ = configured.Body.Close()
	firstResponse := postJSON(t, client, server.URL+"/api/tasks", map[string]any{"title": "API", "scopes": []string{"internal/api"}})
	var first Task
	if err := json.NewDecoder(firstResponse.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	_ = firstResponse.Body.Close()
	secondResponse := postJSON(t, client, server.URL+"/api/tasks", map[string]any{"title": "Tests", "depends": []string{first.ID}, "scopes": []string{"test/api"}})
	var second Task
	if err := json.NewDecoder(secondResponse.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	_ = secondResponse.Body.Close()
	blockedDelete := requestJSON(t, client, http.MethodDelete, server.URL+"/api/tasks/"+first.ID, map[string]any{})
	_ = blockedDelete.Body.Close()
	if blockedDelete.StatusCode != http.StatusBadRequest {
		t.Fatalf("dependency delete status=%d", blockedDelete.StatusCode)
	}
	if err := store.Update(func(state *State) error {
		state.Tasks[1].Status = "blocked"
		state.Tasks[1].LastError = StringPtr("failed")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	retried := requestJSON(t, client, http.MethodPatch, server.URL+"/api/tasks/"+second.ID, map[string]any{"retry": true})
	_ = retried.Body.Close()
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("retry status=%d", retried.StatusCode)
	}
	state, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[1].Status != "pending" || state.Tasks[1].LastError != nil || len(state.Users[0].Providers) != 1 {
		t.Fatalf("state after web updates=%+v user=%+v", state.Tasks[1], state.Users[0])
	}
}

func TestProviderInstallationRequiresExplicitEndpointAndEnablesProvider(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "provider-install", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	web := NewWebServer(store, &fakeChat{}, fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}}))
	installed := false
	installer := &fakeProviderInstaller{installed: &installed}
	web.installer = installer
	web.providerInstalled = func(_ []ProviderConfig, provider string) bool { return provider == "codex" && installed }
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL, "provider-installer")

	response := postJSON(t, client, server.URL+"/api/providers/codex/install", map[string]any{})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("install status=%d", response.StatusCode)
	}
	if !installed || len(installer.calls) != 1 || installer.calls[0] != "codex" {
		t.Fatalf("installation not recorded: installed=%t calls=%v", installed, installer.calls)
	}

	unknown := postJSON(t, client, server.URL+"/api/providers/unknown/install", map[string]any{})
	defer unknown.Body.Close()
	if unknown.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown provider install status=%d", unknown.StatusCode)
	}
}

func TestUnavailableProviderCannotParticipateInConversation(t *testing.T) {
	root := t.TempDir()
	if err := Init(root, "provider-unavailable", false); err != nil {
		t.Fatal(err)
	}
	store := &Store{Root: root}
	runner := &fakeChat{}
	web := NewWebServer(store, runner, fs.FS(fstest.MapFS{"index.html": {Data: []byte("ok")}}))
	web.enforceProviderAvailability = true
	web.providerInstalled = func(_ []ProviderConfig, _ string) bool { return false }
	server := httptest.NewServer(web.Handler())
	defer server.Close()
	client := authenticatedClient(t, server.URL, "provider-unavailable")

	created := postJSON(t, client, server.URL+"/api/conversations", map[string]string{"title": "unavailable"})
	var conversation Conversation
	if err := json.NewDecoder(created.Body).Decode(&conversation); err != nil {
		t.Fatal(err)
	}
	_ = created.Body.Close()
	message := postJSON(t, client, server.URL+"/api/conversations/"+conversation.ID+"/messages", map[string]string{"body": "@codex hello"})
	_ = message.Body.Close()
	if message.StatusCode != http.StatusCreated {
		t.Fatalf("message status=%d", message.StatusCode)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := store.Read()
		if err != nil {
			t.Fatal(err)
		}
		blocked := false
		for _, chat := range state.ChatMessages {
			if chat.ConversationID == conversation.ID && chat.Kind == "system" && strings.Contains(chat.Body, "本机未安装") {
				blocked = true
			}
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("unavailable provider was not blocked")
		}
		time.Sleep(10 * time.Millisecond)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 0 {
		t.Fatalf("unavailable provider was invoked: %d calls", len(runner.calls))
	}
}
