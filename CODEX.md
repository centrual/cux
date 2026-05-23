# Codex Integration Plan

## Overview

Extend cux to manage Codex accounts alongside Claude Code accounts, and support migrating
an active Claude Code session to Codex without losing conversation context.

## User-facing features

```
cux codex add               # register the currently-authenticated Codex account
cux codex list              # list managed Codex accounts
cux codex remove <slot|id>  # forget a Codex account
cux codex migrate           # export current Claude session → Codex memories + launch codex
```

From inside a Claude Code session:
```
/cux:codex-migrate          # same as cux codex migrate, works in-session
```

Auto-overflow: when all Claude accounts are rate-limited/exhausted, cux automatically
migrates context to a registered Codex account and relaunches as `codex`.

## How context migration works

Claude Code stores sessions as JSONL at:
  `~/.claude/projects/<encoded-cwd>/<session-id>.jsonl`

Migration reads that file, extracts user/assistant message pairs, and writes a markdown
memory file to `~/.codex/memories/claude-migration-<timestamp>.md`. Codex loads files
from that directory as persistent memory, so the full conversation is available without
being re-sent as a large prompt. Codex is then launched with a brief handoff message.

## Architecture

### Data model change

`internal/store/store.go` — add `Tool` field to `Account`:
```go
Tool string `json:"tool,omitempty"` // "" or "claude" = Claude Code; "codex" = Codex
```
Backward compatible. Add `(a Account) IsCodex() bool`.

### New packages

**`internal/codexcreds/codexcreds.go`**
Mirrors `internal/creds` for Codex's `~/.codex/auth.json`.
- `ReadLive() (blob, accountID string, err error)`
- `BackupCodexAuth(slot int, id, blob string) error`
- `ReadBackup(slot int, id string) (string, error)`
- `RestoreLive(slot int, id string) error`
- `DeleteBackup(slot int, id string) error`

auth.json shape: `{ "auth_mode", "tokens": { "account_id", "access_token", "refresh_token", "id_token" }, "last_refresh" }`

**`internal/claudeconv/claudeconv.go`**
Reads a Claude JSONL session and extracts conversation for migration.
- `ExtractMessages(jsonlPath string) ([]Message, error)` — keeps only text content from user/assistant roles
- `FormatMemory(msgs []Message, cwd, sessionID string) string` — renders markdown memory file

### Path helpers

`internal/paths/paths.go` — add:
- `CodexAuthFile() string`   → `~/.codex/auth.json`
- `CodexMemoriesDir() string` → `~/.codex/memories/`

### Switcher additions

`internal/switcher/switcher.go`:
- `AddCodexCurrent(preferredSlot int, alias string) (store.Account, error)`
- `MigrateToCodex(cwd, sessionID string) (memoryFile string, err error)`

### CLI

`cmd/cux/main.go` — new `codex` subcommand group + `__codex-migrate` internal command.

### Slash command

`slashcmd/codex-migrate.md` → installed to `~/.claude/commands/cux/codex-migrate.md`

`cmd/cux/setup.go` — include `codex-migrate.md` in the install list.

### Wrapper auto-overflow

`internal/wrapper/wrapper.go` — when no eligible Claude account is found, check for
Codex accounts, call `MigrateToCodex`, and relaunch as `codex` instead of `claude --resume`.

## Files touched

| File | Change |
|------|--------|
| `internal/store/store.go` | `Tool` field + `IsCodex()` |
| `internal/paths/paths.go` | `CodexAuthFile()`, `CodexMemoriesDir()` |
| `internal/codexcreds/codexcreds.go` | **new** |
| `internal/claudeconv/claudeconv.go` | **new** |
| `internal/switcher/switcher.go` | `AddCodexCurrent()`, `MigrateToCodex()` |
| `cmd/cux/main.go` | `cux codex` subcommands + `__codex-migrate` |
| `cmd/cux/setup.go` | install `codex-migrate.md` |
| `slashcmd/codex-migrate.md` | **new** |
| `internal/wrapper/wrapper.go` | Codex overflow in relaunch path |

## Verification

1. `go build ./cmd/cux` — no errors
2. `cux codex add` → shows in `cux codex list`
3. `cux codex migrate` → writes `~/.codex/memories/claude-migration-*.md`, launches Codex
4. `/cux:codex-migrate` inside a session → same result
5. With all Claude accounts exhausted, wrapper auto-launches `codex` with memory written
6. Existing Claude switching unaffected
