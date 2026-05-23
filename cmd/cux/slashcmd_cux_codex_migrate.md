---
description: Migrate the current Claude Code session to Codex without losing context
allowed-tools: Bash(cux __codex-migrate:*)
---

# /cux:codex-migrate

Exports the current Claude Code conversation to Codex's memory system, then
prints instructions for launching Codex to continue where you left off.

The full conversation is saved to `~/.codex/memories/` as a markdown file.
Codex will load it automatically and have full context from your Claude session.

```bash
cux __codex-migrate
```
