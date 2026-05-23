// Package claudeconv reads Claude Code JSONL session files and converts the
// conversation into formats suitable for migration to other tools (e.g. Codex).
package claudeconv

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Message is one turn in a conversation.
type Message struct {
	Role string // "user" or "assistant"
	Text string
}

// line is the minimal shape of a Claude Code JSONL entry we care about.
type line struct {
	Type    string  `json:"type"`
	Message msgBody `json:"message"`
}

type msgBody struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []contentBlock
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ExtractMessages reads a Claude Code JSONL session file and returns the
// human-readable conversation turns. Tool calls and metadata are omitted.
func ExtractMessages(jsonlPath string) ([]Message, error) {
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("claudeconv: open %s: %w", jsonlPath, err)
	}
	defer f.Close()

	var msgs []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1 MiB per line
	for scanner.Scan() {
		var l line
		if err := json.Unmarshal(scanner.Bytes(), &l); err != nil {
			continue // skip unparseable lines
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		role := l.Message.Role
		if role == "" {
			role = l.Type
		}
		if role != "user" && role != "assistant" {
			continue
		}
		text := extractText(l.Message.Content)
		if text == "" {
			continue
		}
		msgs = append(msgs, Message{Role: role, Text: text})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claudeconv: scan %s: %w", jsonlPath, err)
	}
	return msgs, nil
}

// extractText pulls plain text out of a content field that may be either a
// JSON string or an array of content blocks.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Try plain string first.
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	// Try array of content blocks; concatenate text blocks.
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, strings.TrimSpace(b.Text))
		}
	}
	return strings.Join(parts, "\n")
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
