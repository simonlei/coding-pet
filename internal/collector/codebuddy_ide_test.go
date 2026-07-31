package collector

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// ---- mapCodeBuddyIDEStateFromDB 单元测试 ----

func TestMapCodeBuddyIDEStateFromDB_WorkingActive(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 2*60*1000, // 2 分钟前
	}
	rt := cbIDEMQRuntime{Activated: true, Paused: false}

	state, include := mapCodeBuddyIDEStateFromDB(s, rt, true, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateActive {
		t.Fatalf("expected active, got %s", state)
	}
}

func TestMapCodeBuddyIDEStateFromDB_WorkingStale(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 5*60*1000, // 5 分钟前
	}
	rt := cbIDEMQRuntime{Activated: true, Paused: false}

	state, include := mapCodeBuddyIDEStateFromDB(s, rt, true, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateWaitingForInput {
		t.Fatalf("expected waiting_for_input, got %s", state)
	}
}

func TestMapCodeBuddyIDEStateFromDB_WorkingPaused(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 1*60*1000, // 1 分钟前但被暂停
	}
	rt := cbIDEMQRuntime{Activated: true, Paused: true}

	state, include := mapCodeBuddyIDEStateFromDB(s, rt, true, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateWaitingForInput {
		t.Fatalf("expected waiting_for_input (paused), got %s", state)
	}
}

func TestMapCodeBuddyIDEStateFromDB_WorkingNotActivated(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 1*60*1000,
	}
	rt := cbIDEMQRuntime{Activated: false, Paused: false}

	state, include := mapCodeBuddyIDEStateFromDB(s, rt, true, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateWaitingForInput {
		t.Fatalf("expected waiting_for_input (not activated), got %s", state)
	}
}

func TestMapCodeBuddyIDEStateFromDB_Completed(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Completed",
		UpdatedAt: nowMs - 1*60*1000,
	}

	state, include := mapCodeBuddyIDEStateFromDB(s, cbIDEMQRuntime{}, false, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateWaitingForInput {
		t.Fatalf("expected waiting_for_input, got %s", state)
	}
}

func TestMapCodeBuddyIDEStateFromDB_StaleExcluded(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 31*60*1000, // 31 分钟前
	}

	_, include := mapCodeBuddyIDEStateFromDB(s, cbIDEMQRuntime{}, false, nowMs)
	if include {
		t.Fatal("expected excluded for stale session")
	}
}

func TestMapCodeBuddyIDEStateFromDB_NoRuntimeFallback(t *testing.T) {
	nowMs := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	s := cbIDESessionValue{
		Status:    "Working",
		UpdatedAt: nowMs - 2*60*1000, // 2 分钟，无 runtime 数据
	}

	// 无消息队列数据时，退化为 freshness 猜测
	state, include := mapCodeBuddyIDEStateFromDB(s, cbIDEMQRuntime{}, false, nowMs)
	if !include {
		t.Fatal("expected included")
	}
	if state != protocol.StateActive {
		t.Fatalf("expected active (fresh + no runtime), got %s", state)
	}

	// 5 分钟无 runtime → 猜测为 waiting
	s2 := cbIDESessionValue{Status: "Working", UpdatedAt: nowMs - 5*60*1000}
	state2, _ := mapCodeBuddyIDEStateFromDB(s2, cbIDEMQRuntime{}, false, nowMs)
	if state2 != protocol.StateWaitingForInput {
		t.Fatalf("expected waiting_for_input for stale no-runtime, got %s", state2)
	}
}

// ---- readCodeBuddyIDESessions 与 message-queue 集成测试 ----

func TestReadCodeBuddyIDESessions_SQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.vscdb")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// 创建 ItemTable
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE, value TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	// 写入一个 Working 会话
	sess := cbIDESessionValue{
		ConversationId: "conv-001",
		Cwd:            "/home/user/project",
		Title:          "Test Session",
		Status:         "Working",
		CreatedAt:      1000,
		UpdatedAt:      2000,
	}
	val, _ := json.Marshal(sess)
	_, err = db.Exec(`INSERT OR REPLACE INTO ItemTable (key, value) VALUES (?, ?)`,
		"session:conv-001", string(val))
	if err != nil {
		t.Fatal(err)
	}

	sessions, err := readCodeBuddyIDESessions(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ConversationId != "conv-001" {
		t.Fatalf("got %s", sessions[0].ConversationId)
	}
	if sessions[0].Cwd != "/home/user/project" {
		t.Fatalf("got cwd=%s", sessions[0].Cwd)
	}
}

func TestReadCodeBuddyIDESessions_SkipsDeleted(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.vscdb")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE, value TEXT)`)

	deletedAt := int64(3000)
	sess := cbIDESessionValue{
		ConversationId: "conv-002",
		Status:         "Completed",
		DeletedAt:      &deletedAt,
	}
	val, _ := json.Marshal(sess)
	_, _ = db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('session:conv-002', ?)`, string(val))

	sessions, err := readCodeBuddyIDESessions(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions (deleted), got %d", len(sessions))
	}
}

func TestReadCodeBuddyIDESessions_SkipsNonSessionKeys(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.vscdb")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE, value TEXT)`)
	_, _ = db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('some_other_key', '{"foo":"bar"}')`)

	sessions, err := readCodeBuddyIDESessions(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions, got %d", len(sessions))
	}
}

// ---- message queue 测试 ----

func TestBuildCodeBuddyIDERuntimeMap(t *testing.T) {
	dir := t.TempDir()

	mq := cbIDEMessageQueueFile{
		Version:   2,
		Conversations: map[string]cbIDEMQConversation{
			"conv-a": {
				ConversationId: "conv-a",
				Runtime: cbIDEMQRuntime{
					Activated: true,
					Paused:    false,
				},
			},
			"conv-b": {
				ConversationId: "conv-b",
				Runtime: cbIDEMQRuntime{
					Activated: false,
					Paused:    true,
				},
			},
		},
	}
	data, _ := json.Marshal(mq)
	os.WriteFile(filepath.Join(dir, "abc123.json"), data, 0644)

	// 手动设置 message queue 路径
	origMqDir := codebuddyIDEMessageQueueDir
	codebuddyIDEMessageQueueDir = func() (string, bool) { return dir, true }
	defer func() { codebuddyIDEMessageQueueDir = origMqDir }()

	runtimeMap := buildCodeBuddyIDERuntimeMap()
	if len(runtimeMap) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(runtimeMap))
	}
	if rt, ok := runtimeMap["conv-a"]; !ok || !rt.Activated || rt.Paused {
		t.Fatalf("conv-a state mismatch: %+v", rt)
	}
	if rt, ok := runtimeMap["conv-b"]; !ok || rt.Activated || !rt.Paused {
		t.Fatalf("conv-b state mismatch: %+v", rt)
	}
}

// ---- workspaceHash16 ----

func TestWorkspaceHash16(t *testing.T) {
	h := workspaceHash16("d:/work/coding-pet")
	if len(h) != 16 {
		t.Fatalf("expected 16 chars, got %d", len(h))
	}
	//  hash 应与 IDE 的 message-queue 文件名一致
	expected := "c5120ee09068571f"
	if h != expected {
		t.Fatalf("expected %s, got %s", expected, h)
	}
}

// ---- context token 读取 ----

func TestCodebuddyIDEContextTokensFromIndex(t *testing.T) {
	idx := cbIDEConversationIndex{
		Requests: []struct {
			State string `json:"state"`
			Usage *struct {
				LastTokens int64 `json:"lastTokens"`
			} `json:"usage"`
		}{
			{State: "complete", Usage: &struct{ LastTokens int64 `json:"lastTokens"` }{LastTokens: 5000}},
			{State: "running"}, // 无 usage
		},
	}

	tokens := codebuddyIDEContextTokensFromIndex(idx)
	if tokens != 5000 {
		t.Fatalf("expected 5000, got %d", tokens)
	}
}

func TestCodebuddyIDEContextTokensFromIndex_None(t *testing.T) {
	idx := cbIDEConversationIndex{
		Requests: []struct {
			State string `json:"state"`
			Usage *struct {
				LastTokens int64 `json:"lastTokens"`
			} `json:"usage"`
		}{
			{State: "running"},
		},
	}
	tokens := codebuddyIDEContextTokensFromIndex(idx)
	if tokens != 0 {
		t.Fatalf("expected 0, got %d", tokens)
	}
}

// ---- CollectCodeBuddyIDESessions 集成测试 ----

// resetCodeBuddyIDEDBForTest 重置缓存的数据库连接
func resetCodeBuddyIDEDBForTest() {
	resetCodeBuddyIDEDB()
}

func TestCollectCodeBuddyIDESessions_Integration(t *testing.T) {
	// 创建 mock SQLite
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "codebuddy-sessions.vscdb")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS ItemTable (key TEXT UNIQUE, value TEXT)`)
	if err != nil {
		t.Fatal(err)
	}

	nowMs := time.Now().UnixMilli()
	sess := cbIDESessionValue{
		ConversationId: "conv-integration-1",
		Cwd:            "/tmp/test-project",
		Title:          "Integration Test",
		Status:         "Working",
		CreatedAt:      nowMs - 60000,
		UpdatedAt:      nowMs - 30000, // 30 秒前
	}
	val, _ := json.Marshal(sess)
	_, _ = db.Exec(`INSERT INTO ItemTable (key, value) VALUES ('session:conv-integration-1', ?)`, string(val))
	db.Close()

	// 创建 mock message-queue
	mqDir := filepath.Join(dir, "mq")
	os.MkdirAll(mqDir, 0755)
	mq := cbIDEMessageQueueFile{
		Conversations: map[string]cbIDEMQConversation{
			"conv-integration-1": {
				ConversationId: "conv-integration-1",
				Runtime:        cbIDEMQRuntime{Activated: true, Paused: false},
			},
		},
	}
	mqData, _ := json.Marshal(mq)
	os.WriteFile(filepath.Join(mqDir, "abc.json"), mqData, 0644)

	// Inject paths
	origDB := codebuddyIDEDBPath
	origMq := codebuddyIDEMessageQueueDir
	codebuddyIDEDBPath = func() (string, bool) { return dbPath, true }
	codebuddyIDEMessageQueueDir = func() (string, bool) { return mqDir, true }
	defer func() {
		codebuddyIDEDBPath = origDB
		codebuddyIDEMessageQueueDir = origMq
		resetCodeBuddyIDEDBForTest()
	}()

	sessions := CollectCodeBuddyIDESessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.SessionID != "conv-integration-1" {
		t.Fatalf("got sessionID=%s", s.SessionID)
	}
	if s.Tool != protocol.ToolCodeBuddyIDE {
		t.Fatalf("expected codebuddy_ide tool, got %s", s.Tool)
	}
	if s.State != protocol.StateActive {
		t.Fatalf("expected active, got %s", s.State)
	}
	if s.CWD != "/tmp/test-project" {
		t.Fatalf("expected cwd=/tmp/test-project, got %s", s.CWD)
	}
}

// ---- readJSONFile ----

func TestReadJSONFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.json")
	os.WriteFile(path, []byte(`{"foo":"bar"}`), 0644)

	var m map[string]string
	if !readJSONFile(path, &m) {
		t.Fatal("expected success")
	}
	if m["foo"] != "bar" {
		t.Fatalf("got %s", m["foo"])
	}

	// Missing file
	if readJSONFile(filepath.Join(dir, "nope.json"), &m) {
		t.Fatal("expected false for missing file")
	}

	// Invalid JSON
	os.WriteFile(path, []byte(`not json`), 0644)
	if readJSONFile(path, &m) {
		t.Fatal("expected false for invalid JSON")
	}
}


