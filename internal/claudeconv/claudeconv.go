// Package claudeconv reads Claude Code JSONL session files and converts the
// conversation into formats suitable for migration to other tools (e.g. Codex).
package claudeconv

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Message is one turn in a conversation.
type Message struct {
	Role string // "user" or "assistant"
	Text string
}

// rawEntry is the minimal shape of a Claude Code JSONL entry we care about.
type rawEntry struct {
	Type    string   `json:"type"`
	Message rawMsg   `json:"message"`
}

type rawMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []rawBlock
}

// rawBlock covers text, tool_use, and tool_result content blocks.
type rawBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// tool_use fields
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result fields
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"` // string or []rawBlock
	// tool_reference (ToolSearch results)
	ToolName string `json:"tool_name"`
}

const toolOutputCap = 1500 // max chars per tool output before truncation

// ExtractMessages reads a Claude Code JSONL session file and returns the
// human-readable conversation turns. Tool calls and their outputs are inlined
// into the preceding assistant message so the full context is preserved.
func ExtractMessages(jsonlPath string) ([]Message, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("claudeconv: open %s: %w", jsonlPath, err)
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4<<20), 4<<20) // 4 MiB per line — tool results can be large
	for scanner.Scan() {
		var e rawEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		switch e.Type {
		case "assistant":
			text := buildAssistantText(e.Message.Content)
			if text != "" {
				msgs = append(msgs, Message{Role: "assistant", Text: text})
			}
		case "user":
			toolResultText, userText := splitUserContent(e.Message.Content)
			if toolResultText != "" && len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" {
				// Append tool outputs to the preceding assistant turn.
				msgs[len(msgs)-1].Text += toolResultText
			}
			if userText != "" {
				msgs = append(msgs, Message{Role: "user", Text: userText})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claudeconv: scan %s: %w", jsonlPath, err)
	}
	return msgs, nil
}

// buildAssistantText assembles an assistant turn: leading text followed by
// formatted tool_use annotations. Returns "" if there is nothing to show.
func buildAssistantText(raw json.RawMessage) string {
	blocks := parseBlocks(raw)
	if len(blocks) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(t)
			}
		case "tool_use":
			sb.WriteString(formatToolUse(b))
		}
	}
	return sb.String()
}

// splitUserContent separates a user message into tool-result text (to be
// appended to the preceding assistant turn) and actual user-typed text.
func splitUserContent(raw json.RawMessage) (toolResults, userText string) {
	// Content may be a plain string — that's always user text.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "", strings.TrimSpace(s)
	}
	blocks := parseBlocks(raw)
	var trParts, txParts []string
	for _, b := range blocks {
		switch b.Type {
		case "tool_result":
			trParts = append(trParts, formatToolResult(b))
		case "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				txParts = append(txParts, t)
			}
		}
	}
	return strings.Join(trParts, ""), strings.Join(txParts, "\n")
}

// formatToolUse renders a tool_use block as a compact annotation line.
func formatToolUse(b rawBlock) string {
	var sb strings.Builder
	sb.WriteString("\n→ ")
	switch b.Name {
	case "Bash":
		var inp struct {
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(b.Input, &inp)
		cmd := strings.TrimSpace(inp.Command)
		if len(cmd) > 200 {
			cmd = cmd[:200] + "…"
		}
		sb.WriteString("bash: ")
		sb.WriteString(cmd)
	case "Read":
		var inp struct{ FilePath string `json:"file_path"` }
		_ = json.Unmarshal(b.Input, &inp)
		sb.WriteString("read: ")
		sb.WriteString(inp.FilePath)
	case "Edit":
		var inp struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		_ = json.Unmarshal(b.Input, &inp)
		sb.WriteString("edit: ")
		sb.WriteString(inp.FilePath)
		if inp.OldString != "" {
			old := strings.TrimSpace(inp.OldString)
			if len(old) > 120 {
				old = old[:120] + "…"
			}
			fmt.Fprintf(&sb, "\n  - %s", old)
		}
		if inp.NewString != "" {
			neu := strings.TrimSpace(inp.NewString)
			if len(neu) > 120 {
				neu = neu[:120] + "…"
			}
			fmt.Fprintf(&sb, "\n  + %s", neu)
		}
	case "Write":
		var inp struct{ FilePath string `json:"file_path"` }
		_ = json.Unmarshal(b.Input, &inp)
		sb.WriteString("write: ")
		sb.WriteString(inp.FilePath)
	case "WebFetch":
		var inp struct{ URL string `json:"url"` }
		_ = json.Unmarshal(b.Input, &inp)
		sb.WriteString("fetch: ")
		sb.WriteString(inp.URL)
	default:
		// Generic: tool name + first 150 chars of input JSON
		raw := strings.TrimSpace(string(b.Input))
		if len(raw) > 150 {
			raw = raw[:150] + "…"
		}
		fmt.Fprintf(&sb, "%s: %s", b.Name, raw)
	}
	return sb.String()
}

// formatToolResult renders a tool_result block, truncating long outputs.
func formatToolResult(b rawBlock) string {
	text := extractToolResultText(b.Content)
	if text == "" {
		return ""
	}
	text = strings.TrimSpace(text)
	truncated := false
	if len(text) > toolOutputCap {
		text = text[:toolOutputCap]
		truncated = true
	}
	var sb strings.Builder
	sb.WriteString("\n  output: ")
	sb.WriteString(text)
	if truncated {
		sb.WriteString("\n  … (truncated)")
	}
	return sb.String()
}

// extractToolResultText pulls plain text from a tool_result content field,
// which may be a string, []rawBlock (text blocks), or []rawBlock (tool_reference).
func extractToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	blocks := parseBlocks(raw)
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "tool_reference":
			parts = append(parts, b.ToolName)
		}
	}
	return strings.Join(parts, "\n")
}

// parseBlocks parses a content field into rawBlock slices, returning nil on failure.
func parseBlocks(raw json.RawMessage) []rawBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []rawBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// FormatMemory renders a conversation as a Codex-compatible memory markdown
// file. The returned string is ready to be written to ~/.codex/memories/.
func FormatMemory(msgs []Message, cwd, sessionID string) string {
	var sb strings.Builder
	sb.WriteString("# Migrated Claude Code Session\n\n")
	fmt.Fprintf(&sb, "**Date:** %s  \n", time.Now().UTC().Format("2006-01-02 15:04 UTC"))
	if cwd != "" {
		fmt.Fprintf(&sb, "**Project:** %s  \n", cwd)
	}
	if sessionID != "" {
		fmt.Fprintf(&sb, "**Session ID:** %s  \n", sessionID)
	}
	sb.WriteString("\n## Conversation\n\n")

	for _, m := range msgs {
		label := "**User**"
		if m.Role == "assistant" {
			label = "**Assistant**"
		}
		fmt.Fprintf(&sb, "%s: %s\n\n", label, m.Text)
	}
	return sb.String()
}

// MemoryFileName returns a timestamped filename for the migration memory file.
func MemoryFileName() string {
	return fmt.Sprintf("claude-migration-%d.md", time.Now().UTC().Unix())
}

// SessionJSONLPath returns the expected path of a Claude Code session JSONL
// file given the project working directory and session ID.
func SessionJSONLPath(cwd, sessionID string) string {
	home, _ := os.UserHomeDir()
	encoded := strings.ReplaceAll(cwd, string(filepath.Separator), "-")
	if !strings.HasPrefix(encoded, "-") {
		encoded = "-" + encoded
	}
	return filepath.Join(home, ".claude", "projects", encoded, sessionID+".jsonl")
}

// WriteNativeCodexSession converts a Claude conversation to a native Codex
// session JSONL and writes it to ~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl.
// It also inserts a row into Codex's state_5.sqlite so `codex resume <id>` works.
// Returns the new Codex session UUID and the JSONL file path.
func WriteNativeCodexSession(msgs []Message, cwd string) (sessionID, jsonlPath string, err error) {
	sessionID = newUUIDv7()
	now := time.Now().UTC()

	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".codex", "sessions",
		now.Format("2006"), now.Format("01"), now.Format("02"))
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("claudeconv: mkdir codex sessions: %w", err)
	}

	ts := now.Format("2006-01-02T15-04-05")
	jsonlPath = filepath.Join(dir, fmt.Sprintf("rollout-%s-%s.jsonl", ts, sessionID))

	content, err := buildCodexSessionJSONL(msgs, sessionID, cwd, now)
	if err != nil {
		return "", "", err
	}

	if err = os.WriteFile(jsonlPath, content, 0o600); err != nil {
		return "", "", fmt.Errorf("claudeconv: write codex session: %w", err)
	}

	title := "Migrated from Claude Code"
	firstMsg := ""
	if len(msgs) > 0 {
		firstMsg = msgs[0].Text
		if len(firstMsg) > 200 {
			firstMsg = firstMsg[:200]
		}
	}

	dbPath := filepath.Join(home, ".codex", "state_5.sqlite")
	if _, statErr := os.Stat(dbPath); statErr == nil {
		if regErr := registerCodexThread(dbPath, sessionID, jsonlPath, cwd, title, firstMsg, now); regErr != nil {
			// Non-fatal: session file is usable even without the DB entry.
			_ = regErr
		}
	}

	return sessionID, jsonlPath, nil
}

// buildCodexSessionJSONL assembles the JSONL bytes for a native Codex session.
func buildCodexSessionJSONL(msgs []Message, sessionID, cwd string, now time.Time) ([]byte, error) {
	ts := now.Format(time.RFC3339Nano)
	turnID := newUUIDv7()
	startedAt := now.Unix()

	type event = map[string]any

	events := []event{
		{
			"timestamp": ts,
			"type":      "session_meta",
			"payload": event{
				"id":             sessionID,
				"timestamp":      ts,
				"cwd":            cwd,
				"originator":     "codex-tui",
				"cli_version":    "0.130.0",
				"source":         "cli",
				"thread_source":  "user",
				"model_provider": "openai",
				"base_instructions": event{
					"text": "You are Codex, a coding agent. This session was migrated from Claude Code.",
				},
			},
		},
		{
			"timestamp": ts,
			"type":      "event_msg",
			"payload": event{
				"type":                      "task_started",
				"turn_id":                   turnID,
				"started_at":                startedAt,
				"model_context_window":      258400,
				"collaboration_mode_kind":   "default",
			},
		},
	}

	// Emit each message pair.
	for _, m := range msgs {
		switch m.Role {
		case "user":
			events = append(events,
				event{
					"timestamp": ts,
					"type":      "response_item",
					"payload": event{
						"type": "message",
						"role": "user",
						"content": []event{
							{"type": "input_text", "text": m.Text},
						},
					},
				},
				event{
					"timestamp": ts,
					"type":      "event_msg",
					"payload": event{
						"type":    "user_message",
						"message": m.Text,
					},
				},
			)
		case "assistant":
			events = append(events,
				event{
					"timestamp": ts,
					"type":      "response_item",
					"payload": event{
						"type":  "message",
						"role":  "assistant",
						"phase": "commentary",
						"content": []event{
							{"type": "output_text", "text": m.Text},
						},
					},
				},
				event{
					"timestamp": ts,
					"type":      "event_msg",
					"payload": event{
						"type":    "agent_message",
						"message": m.Text,
					},
				},
			)
		}
	}

	events = append(events, event{
		"timestamp": ts,
		"type":      "event_msg",
		"payload": event{
			"type":         "task_complete",
			"turn_id":      turnID,
			"completed_at": startedAt + 1,
			"duration_ms":  1000,
			"interrupted":  false,
		},
	})

	var buf strings.Builder
	for _, ev := range events {
		b, err := json.Marshal(ev)
		if err != nil {
			return nil, fmt.Errorf("claudeconv: marshal event: %w", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return []byte(buf.String()), nil
}

// registerCodexThread inserts a session into Codex's state_5.sqlite via python3.
func registerCodexThread(dbPath, sessionID, rolloutPath, cwd, title, firstMsg string, now time.Time) error {
	nowS := now.Unix()
	nowMS := now.UnixMilli()

	py := fmt.Sprintf(`import sqlite3
conn = sqlite3.connect(%q)
conn.execute("""INSERT OR IGNORE INTO threads
(id,rollout_path,created_at,updated_at,source,model_provider,cwd,title,
 sandbox_policy,approval_mode,tokens_used,has_user_event,archived,cli_version,
 first_user_message,memory_mode,created_at_ms,updated_at_ms,thread_source)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)""",
(%q,%q,%d,%d,'cli','openai',%q,%q,
 '{"type":"workspace-write"}','on-request',100,1,0,'0.130.0',
 %q,'enabled',%d,%d,'user'))
conn.commit()
conn.close()
`, dbPath, sessionID, rolloutPath, nowS, nowS, cwd, title, firstMsg, nowMS, nowMS)

	if out, err := exec.Command("python3", "-c", py).CombinedOutput(); err != nil {
		return fmt.Errorf("claudeconv: sqlite insert: %w: %s", err, out)
	}
	return nil
}

// newUUIDv7 generates a time-ordered UUID version 7.
func newUUIDv7() string {
	ms := time.Now().UnixMilli()
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	// First 48 bits = millisecond timestamp
	binary.BigEndian.PutUint16(b[0:2], uint16(ms>>32))
	binary.BigEndian.PutUint32(b[2:6], uint32(ms))
	// Version 7
	b[6] = (b[6] & 0x0f) | 0x70
	// Variant 10xx
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// newUUIDv4 generates a random UUID version 4 (Claude Code's format).
func newUUIDv4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10xx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(b[0:4]),
		binary.BigEndian.Uint16(b[4:6]),
		binary.BigEndian.Uint16(b[6:8]),
		binary.BigEndian.Uint16(b[8:10]),
		b[10:16])
}

// ── Codex → Claude migration ──────────────────────────────────────────────────

// codexEntry is the minimal shape of a Codex session JSONL line.
type codexEntry struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	CWD     string `json:"cwd"`
	ID      string `json:"id"`
}

// ReadCodexSession parses a Codex session JSONL and returns the working
// directory and conversation turns (user + agent messages only).
func ReadCodexSession(jsonlPath string) (cwd string, msgs []Message, err error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return "", nil, fmt.Errorf("claudeconv: open codex session %s: %w", jsonlPath, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4<<20), 4<<20)
	for scanner.Scan() {
		var e codexEntry
		if json.Unmarshal(scanner.Bytes(), &e) != nil {
			continue
		}
		var p codexPayload
		if json.Unmarshal(e.Payload, &p) != nil {
			continue
		}
		switch e.Type {
		case "session_meta":
			if p.CWD != "" {
				cwd = p.CWD
			}
		case "event_msg":
			switch p.Type {
			case "user_message":
				if text := strings.TrimSpace(p.Message); text != "" {
					msgs = append(msgs, Message{Role: "user", Text: text})
				}
			case "agent_message":
				if text := strings.TrimSpace(p.Message); text != "" {
					msgs = append(msgs, Message{Role: "assistant", Text: text})
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, fmt.Errorf("claudeconv: scan codex session: %w", err)
	}
	return cwd, msgs, nil
}

// FindCodexSessionPath resolves a Codex session UUID to its JSONL file path.
// Queries state_5.sqlite first; falls back to a recursive filesystem search.
func FindCodexSessionPath(sessionID string) (string, error) {
	home, _ := os.UserHomeDir()
	dbPath := filepath.Join(home, ".codex", "state_5.sqlite")
	if _, err := os.Stat(dbPath); err == nil {
		py := fmt.Sprintf(`import sqlite3,sys
conn=sqlite3.connect(%q)
row=conn.execute("SELECT rollout_path FROM threads WHERE id=?",%s).fetchone()
if row: print(row[0])
`, dbPath, fmt.Sprintf("(%q,)", sessionID))
		out, err := exec.Command("python3", "-c", py).Output()
		if err == nil {
			if p := strings.TrimSpace(string(out)); p != "" {
				if _, serr := os.Stat(p); serr == nil {
					return p, nil
				}
			}
		}
	}

	// Filesystem fallback: search ~/.codex/sessions/**/*<sessionID>*.jsonl
	sessionsDir := filepath.Join(home, ".codex", "sessions")
	var found string
	_ = filepath.Walk(sessionsDir, func(path string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		if strings.Contains(filepath.Base(path), sessionID) && strings.HasSuffix(path, ".jsonl") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("claudeconv: codex session %s not found", sessionID)
}

// claudeUserEntry is the shape of a user message in a Claude JSONL.
type claudeUserEntry struct {
	ParentUUID     *string    `json:"parentUuid"`
	IsSidechain    bool       `json:"isSidechain"`
	Type           string     `json:"type"`
	Message        claudeMsg  `json:"message"`
	UUID           string     `json:"uuid"`
	Timestamp      string     `json:"timestamp"`
	PermissionMode string     `json:"permissionMode"`
	UserType       string     `json:"userType"`
	Entrypoint     string     `json:"entrypoint"`
	CWD            string     `json:"cwd"`
	SessionID      string     `json:"sessionId"`
}

type claudeMsg struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string for user, []block for assistant
}

type claudeAssistantEntry struct {
	ParentUUID  *string      `json:"parentUuid"`
	IsSidechain bool         `json:"isSidechain"`
	Type        string       `json:"type"`
	Message     claudeAstMsg `json:"message"`
	UUID        string       `json:"uuid"`
	Timestamp   string       `json:"timestamp"`
	CWD         string       `json:"cwd"`
	SessionID   string       `json:"sessionId"`
}

type claudeAstMsg struct {
	Model      string             `json:"model"`
	ID         string             `json:"id"`
	MsgType    string             `json:"type"`
	Role       string             `json:"role"`
	Content    []claudeTextBlock   `json:"content"`
	StopReason string             `json:"stop_reason"`
}

type claudeTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// WriteNativeClaudeSession writes a conversation as a native Claude Code
// session JSONL that can be resumed with `claude --resume <sessionID>`.
// The file is written to ~/.claude/projects/<encoded-cwd>/<sessionID>.jsonl.
func WriteNativeClaudeSession(msgs []Message, cwd string) (sessionID, jsonlPath string, err error) {
	sessionID = newUUIDv4()
	now := time.Now().UTC()
	ts := now.Format(time.RFC3339Nano)

	home, _ := os.UserHomeDir()
	encoded := strings.ReplaceAll(cwd, string(filepath.Separator), "-")
	if !strings.HasPrefix(encoded, "-") {
		encoded = "-" + encoded
	}
	dir := filepath.Join(home, ".claude", "projects", encoded)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("claudeconv: mkdir claude projects: %w", err)
	}
	jsonlPath = filepath.Join(dir, sessionID+".jsonl")

	var lines [][]byte

	// Preamble
	perm := map[string]any{
		"type":           "permission-mode",
		"permissionMode": "default",
		"sessionId":      sessionID,
	}
	permB, _ := json.Marshal(perm)
	lines = append(lines, permB)

	snapID := newUUIDv4()
	snap := map[string]any{
		"type":      "file-history-snapshot",
		"messageId": snapID,
		"snapshot": map[string]any{
			"messageId":          snapID,
			"trackedFileBackups": map[string]any{},
			"timestamp":          ts,
		},
		"isSnapshotUpdate": false,
	}
	snapB, _ := json.Marshal(snap)
	lines = append(lines, snapB)

	// Conversation turns
	var prevUUID *string
	for _, m := range msgs {
		msgUUID := newUUIDv4()
		switch m.Role {
		case "user":
			entry := claudeUserEntry{
				ParentUUID:     prevUUID,
				IsSidechain:    false,
				Type:           "user",
				Message:        claudeMsg{Role: "user", Content: m.Text},
				UUID:           msgUUID,
				Timestamp:      ts,
				PermissionMode: "default",
				UserType:       "external",
				Entrypoint:     "cli",
				CWD:            cwd,
				SessionID:      sessionID,
			}
			b, _ := json.Marshal(entry)
			lines = append(lines, b)
		case "assistant":
			entry := claudeAssistantEntry{
				ParentUUID:  prevUUID,
				IsSidechain: false,
				Type:        "assistant",
				Message: claudeAstMsg{
					Model:      "codex-migration",
					ID:         "msg_" + strings.ReplaceAll(msgUUID, "-", "")[:24],
					MsgType:    "message",
					Role:       "assistant",
					Content:    []claudeTextBlock{{Type: "text", Text: m.Text}},
					StopReason: "end_turn",
				},
				UUID:      msgUUID,
				Timestamp: ts,
				CWD:       cwd,
				SessionID: sessionID,
			}
			b, _ := json.Marshal(entry)
			lines = append(lines, b)
		default:
			continue
		}
		u := msgUUID
		prevUUID = &u
	}

	content := []byte(strings.Join(func() []string {
		ss := make([]string, len(lines))
		for i, l := range lines {
			ss[i] = string(l)
		}
		return ss
	}(), "\n") + "\n")

	if err = os.WriteFile(jsonlPath, content, 0o600); err != nil {
		return "", "", fmt.Errorf("claudeconv: write claude session: %w", err)
	}
	return sessionID, jsonlPath, nil
}
