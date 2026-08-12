package tag

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

func DatabasePath(root string) string { return filepath.Join(TagDir(root), "data.sqlite") }

func openDatabase(root string) (*sql.DB, error) {
	database, err := sql.Open("sqlite3", DatabasePath(root)+"?_busy_timeout=10000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS chat_messages (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, created_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS chat_messages_conversation_created ON chat_messages(conversation_id, created_at, id)`,
		`CREATE TABLE IF NOT EXISTS task_runs (id TEXT PRIMARY KEY, task_id TEXT NOT NULL, started_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS task_runs_task_started ON task_runs(task_id, started_at, id)`,
		`CREATE TABLE IF NOT EXISTS audit_events (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, conversation_id TEXT, actor TEXT NOT NULL, action TEXT NOT NULL, details TEXT NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS audit_events_owner_created ON audit_events(owner_id, created_at DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS artifacts (id TEXT PRIMARY KEY, owner_id TEXT NOT NULL, conversation_id TEXT NOT NULL, created_at TEXT NOT NULL, payload BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS artifacts_owner_created ON artifacts(owner_id, created_at DESC, id DESC)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	return database, nil
}

func upsertChatMessage(root string, message ChatMessage) error {
	database, err := openDatabase(root)
	if err != nil {
		return err
	}
	defer database.Close()
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO chat_messages(id,conversation_id,created_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET conversation_id=excluded.conversation_id,created_at=excluded.created_at,payload=excluded.payload`, message.ID, message.ConversationID, message.CreatedAt, payload)
	return err
}

func upsertTaskRun(root string, run TaskRun) error {
	database, err := openDatabase(root)
	if err != nil {
		return err
	}
	defer database.Close()
	payload, err := json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO task_runs(id,task_id,started_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET task_id=excluded.task_id,started_at=excluded.started_at,payload=excluded.payload`, run.ID, run.TaskID, run.StartedAt, payload)
	return err
}

func upsertCollections(root string, messages []ChatMessage, runs []TaskRun) error {
	if len(messages) == 0 && len(runs) == 0 {
		return nil
	}
	database, err := openDatabase(root)
	if err != nil {
		return err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return err
	}
	rollback := func(cause error) error { _ = transaction.Rollback(); return cause }
	for _, message := range messages {
		payload, err := json.Marshal(message)
		if err != nil {
			return rollback(err)
		}
		if _, err := transaction.Exec(`INSERT INTO chat_messages(id,conversation_id,created_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET conversation_id=excluded.conversation_id,created_at=excluded.created_at,payload=excluded.payload`, message.ID, message.ConversationID, message.CreatedAt, payload); err != nil {
			return rollback(err)
		}
	}
	for _, run := range runs {
		payload, err := json.Marshal(run)
		if err != nil {
			return rollback(err)
		}
		if _, err := transaction.Exec(`INSERT INTO task_runs(id,task_id,started_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET task_id=excluded.task_id,started_at=excluded.started_at,payload=excluded.payload`, run.ID, run.TaskID, run.StartedAt, payload); err != nil {
			return rollback(err)
		}
	}
	return transaction.Commit()
}

func (s *Store) AppendChatMessage(message ChatMessage) error {
	return upsertChatMessage(s.Root, message)
}
func (s *Store) AppendTaskRun(run TaskRun) error { return upsertTaskRun(s.Root, run) }

func (s *Store) UpdateTaskRun(id string, update func(*TaskRun)) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	var payload []byte
	if err := database.QueryRow(`SELECT payload FROM task_runs WHERE id=?`, id).Scan(&payload); err != nil {
		return err
	}
	var run TaskRun
	if err := json.Unmarshal(payload, &run); err != nil {
		return err
	}
	update(&run)
	payload, err = json.Marshal(run)
	if err != nil {
		return err
	}
	_, err = database.Exec(`UPDATE task_runs SET task_id=?,started_at=?,payload=? WHERE id=?`, run.TaskID, run.StartedAt, payload, id)
	return err
}

func (s *Store) DeleteConversationMessages(conversationID string) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.Exec(`DELETE FROM chat_messages WHERE conversation_id=?`, conversationID)
	return err
}

func (s *Store) DeleteTaskRuns(taskID string) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.Exec(`DELETE FROM task_runs WHERE task_id=?`, taskID)
	return err
}

func (s *Store) ConversationMessages(conversationID string) ([]ChatMessage, error) {
	database, err := openDatabase(s.Root)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT payload FROM chat_messages WHERE conversation_id=? ORDER BY created_at,id`, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ChatMessage{}
	for rows.Next() {
		var payload []byte
		var message ChatMessage
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &message) != nil {
			return nil, fmt.Errorf("invalid chat message row")
		}
		result = append(result, message)
	}
	return result, rows.Err()
}

func (s *Store) RecentTaskRuns(limit int) ([]TaskRun, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	database, err := openDatabase(s.Root)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT payload FROM task_runs ORDER BY started_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TaskRun{}
	for rows.Next() {
		var payload []byte
		var run TaskRun
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &run) != nil {
			return nil, fmt.Errorf("invalid task run row")
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) TaskRunsForTask(taskID string) ([]TaskRun, error) {
	database, err := openDatabase(s.Root)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT payload FROM task_runs WHERE task_id=? ORDER BY started_at DESC,id DESC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []TaskRun{}
	for rows.Next() {
		var payload []byte
		var run TaskRun
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &run) != nil {
			return nil, fmt.Errorf("invalid task run row")
		}
		result = append(result, run)
	}
	return result, rows.Err()
}

func (s *Store) SaveArtifact(record ArtifactRecord) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO artifacts(id,owner_id,conversation_id,created_at,payload) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload`, record.ID, record.OwnerID, record.ConversationID, record.CreatedAt, payload)
	return err
}

func (s *Store) Artifacts(ownerID string, limit int) ([]ArtifactRecord, error) {
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	database, err := openDatabase(s.Root)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT payload FROM artifacts WHERE owner_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ArtifactRecord{}
	for rows.Next() {
		var payload []byte
		var record ArtifactRecord
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &record) != nil {
			return nil, fmt.Errorf("invalid artifact row")
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func (s *Store) Artifact(ownerID, id string) (ArtifactRecord, error) {
	database, err := openDatabase(s.Root)
	if err != nil {
		return ArtifactRecord{}, err
	}
	defer database.Close()
	var payload []byte
	if err := database.QueryRow(`SELECT payload FROM artifacts WHERE owner_id=? AND id=?`, ownerID, id).Scan(&payload); err != nil {
		return ArtifactRecord{}, err
	}
	var record ArtifactRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return ArtifactRecord{}, err
	}
	return record, nil
}

func (s *Store) DeleteArtifact(ownerID, id string) (ArtifactRecord, error) {
	record, err := s.Artifact(ownerID, id)
	if err != nil {
		return ArtifactRecord{}, err
	}
	database, err := openDatabase(s.Root)
	if err != nil {
		return ArtifactRecord{}, err
	}
	defer database.Close()
	_, err = database.Exec(`DELETE FROM artifacts WHERE owner_id=? AND id=?`, ownerID, id)
	return record, err
}

func (s *Store) DeleteConversationArtifacts(ownerID, conversationID string) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	_, err = database.Exec(`DELETE FROM artifacts WHERE owner_id=? AND conversation_id=?`, ownerID, conversationID)
	return err
}

type AuditEvent struct {
	ID             string `json:"id"`
	OwnerID        string `json:"ownerId"`
	ConversationID string `json:"conversationId,omitempty"`
	Actor          string `json:"actor"`
	Action         string `json:"action"`
	Details        string `json:"details"`
	CreatedAt      string `json:"createdAt"`
}

func (s *Store) AppendAudit(ownerID, conversationID, actor, action, details string) error {
	database, err := openDatabase(s.Root)
	if err != nil {
		return err
	}
	defer database.Close()
	token, err := randomToken(8)
	if err != nil {
		return err
	}
	_, err = database.Exec(`INSERT INTO audit_events(id,owner_id,conversation_id,actor,action,details,created_at) VALUES(?,?,?,?,?,?,?)`, "audit-"+token, ownerID, conversationID, actor, action, TruncateForLog(details, 4000), Now())
	return err
}

func (s *Store) RecentAudit(ownerID string, limit int) ([]AuditEvent, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	database, err := openDatabase(s.Root)
	if err != nil {
		return nil, err
	}
	defer database.Close()
	rows, err := database.Query(`SELECT id,owner_id,conversation_id,actor,action,details,created_at FROM audit_events WHERE owner_id=? ORDER BY created_at DESC,id DESC LIMIT ?`, ownerID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []AuditEvent{}
	for rows.Next() {
		var event AuditEvent
		if err := rows.Scan(&event.ID, &event.OwnerID, &event.ConversationID, &event.Actor, &event.Action, &event.Details, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func loadSQLiteCollections(root string, state *State) (bool, error) {
	if _, err := os.Stat(DatabasePath(root)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	database, err := openDatabase(root)
	if err != nil {
		return false, err
	}
	defer database.Close()
	messages := []ChatMessage{}
	rows, err := database.Query(`SELECT payload FROM chat_messages ORDER BY created_at, id`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var payload []byte
		var message ChatMessage
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &message) != nil {
			_ = rows.Close()
			return false, fmt.Errorf("invalid chat message row")
		}
		messages = append(messages, message)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	runs := []TaskRun{}
	rows, err = database.Query(`SELECT payload FROM task_runs ORDER BY started_at, id`)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var payload []byte
		var run TaskRun
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &run) != nil {
			_ = rows.Close()
			return false, fmt.Errorf("invalid task run row")
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	state.ChatMessages = messages
	state.TaskRuns = runs
	return true, nil
}

func syncSQLiteCollections(root string, state State) error {
	database, err := openDatabase(root)
	if err != nil {
		return err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return err
	}
	rollback := func(cause error) error { _ = transaction.Rollback(); return cause }
	messageIDs := map[string]bool{}
	for _, message := range state.ChatMessages {
		payload, err := json.Marshal(message)
		if err != nil {
			return rollback(err)
		}
		messageIDs[message.ID] = true
		if _, err := transaction.Exec(`INSERT INTO chat_messages(id,conversation_id,created_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET conversation_id=excluded.conversation_id,created_at=excluded.created_at,payload=excluded.payload WHERE payload<>excluded.payload`, message.ID, message.ConversationID, message.CreatedAt, payload); err != nil {
			return rollback(err)
		}
	}
	if err := deleteMissingRows(transaction, "chat_messages", messageIDs); err != nil {
		return rollback(err)
	}
	runIDs := map[string]bool{}
	for _, run := range state.TaskRuns {
		payload, err := json.Marshal(run)
		if err != nil {
			return rollback(err)
		}
		runIDs[run.ID] = true
		if _, err := transaction.Exec(`INSERT INTO task_runs(id,task_id,started_at,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET task_id=excluded.task_id,started_at=excluded.started_at,payload=excluded.payload WHERE payload<>excluded.payload`, run.ID, run.TaskID, run.StartedAt, payload); err != nil {
			return rollback(err)
		}
	}
	if err := deleteMissingRows(transaction, "task_runs", runIDs); err != nil {
		return rollback(err)
	}
	return transaction.Commit()
}

func deleteMissingRows(transaction *sql.Tx, table string, keep map[string]bool) error {
	rows, err := transaction.Query(`SELECT id FROM ` + table)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if !keep[id] {
			ids = append(ids, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := transaction.Exec(`DELETE FROM `+table+` WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func messagePage(root, conversationID, before string, limit int) ([]ChatMessage, bool, error) {
	database, err := openDatabase(root)
	if err != nil {
		return nil, false, err
	}
	defer database.Close()
	query := `SELECT payload FROM chat_messages WHERE conversation_id=?`
	arguments := []any{conversationID}
	if before != "" {
		query += ` AND created_at < (SELECT created_at FROM chat_messages WHERE id=?)`
		arguments = append(arguments, before)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	arguments = append(arguments, limit+1)
	rows, err := database.Query(query, arguments...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	messages := []ChatMessage{}
	for rows.Next() {
		var payload []byte
		var message ChatMessage
		if err := rows.Scan(&payload); err != nil || json.Unmarshal(payload, &message) != nil {
			return nil, false, fmt.Errorf("invalid chat message row")
		}
		messages = append(messages, message)
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, hasMore, rows.Err()
}
