<p align="center">
  <img src="assets/logo-wordmark.svg" alt="cux" width="480"/>
</p>

# cux — Claude Code account switcher with auto-resume

Run multiple Claude Code Pro/Max accounts as one. cux wraps `claude`,
listens to its `Stop` / `SessionStart` / `PostToolUseFailure` hooks,
and when an account hits its rate limit (or crosses a configurable
threshold), automatically swaps to a healthy account and continues
the same conversation with `claude --resume`. Manual `/switch` from
inside Claude is supported too. One ~5 MB Go binary — no Python, no
`jq`, no Bash version requirements.

> **Platform support today.** Linux and macOS, plus Windows under WSL
> or Git Bash. A native Windows port of the inline-switch flow is
> planned for v0.3.

## What's in v0.2

- **Auto-swap on rate limit.** When Claude Code reports a rate-limit
  error, cux waits for the current turn to flush, swaps accounts, and
  resumes the conversation with the configured prompt (default:
  `"Go continue."`). No manual intervention.
- **Threshold-based pre-emptive swap.** Configurable: e.g. swap when
  the active account passes 90% on its 5-hour window or 95% on its
  7-day window. cux polls
  `https://api.anthropic.com/api/oauth/usage` for live numbers.
- **Strategy-driven account selection.** `drain` (use one until full,
  ordered or auto-by-highest-7d), `balanced` (always pick the
  freshest), or `manual` (only swap when the user does).
- **Hook-driven shutdown.** cux only asks claude to exit *after* a
  `Stop` hook fires — i.e. only after the transcript has been flushed
  to disk. The flush race v0.1 had to caveat is gone.
- **Persistent swap history**, capped at 1000 entries, with timestamps,
  reasons, and per-account usage at swap time.
- **`cux list`** shows 5h / 7d utilization and reset time per account
  at a glance.

## Install

One-line installer (Linux / macOS / WSL / Git Bash):

```bash
curl -fsSL https://raw.githubusercontent.com/inulute/claude-switch/main/scripts/install.sh | sh
```

Or download a binary directly from the
[releases page](https://github.com/inulute/claude-switch/releases),
`chmod +x cux-<os>-<arch>`, and put it on your `PATH`.

After install, run once:

```bash
cux setup        # installs the /switch slash command + Claude Code hooks
cux add          # registers the currently logged-in account
```

Log in with another account (`claude logout && claude login`), then
`cux add` again. Repeat for each account you want to manage.

> **Restart Claude Code after `cux setup`** so it picks up the newly
> installed hooks.

## Usage

From any shell:

```bash
cux                          # launches claude under the wrapper
cux list                     # accounts with 5h / 7d utilization
cux list --refresh           # refresh usage first
cux status                   # current login and ccux state
cux history                  # recent swaps with reasons
cux config show              # current settings
cux usage refresh            # poll all accounts
cux switch <slot|email>      # manual swap (no auto-resume)
cux remove <slot|email>      # forget an account
```

From inside a Claude Code session started with `cux`:

```text
/switch                       # rotate to next account per strategy
/switch 2
/switch alt@example.com
```

The slash command writes a switch-requested signal. The wrapper
handles the rest — wait for the current turn to finish, swap, and
relaunch with `--resume`.

### Verification recipe

Run this once to confirm context is preserved end-to-end:

1. `cux add` while logged into account A.
2. `claude logout && claude login` to account B; `cux add` again.
3. `cux setup` and restart Claude Code.
4. Start `cux`. Send: *"Please remember the number 4729."*
5. Wait for claude's reply.
6. Send `/switch`.
7. After the ~2-second reconnect, ask: *"What number did I tell you?"*

If the answer is `4729`, the swap-and-resume is preserving context as
intended.

## Configuration

```bash
cux config show
cux config set thresholds.five_hour 85
cux config set strategy.kind balanced
cux config set strategy.order alice@x,bob@x      # drain mode priority
cux config set auto_message ""                    # silent resume
```

Config file: `~/.config/cux/config.json`

| Key | Default | Description |
|---|---|---|
| `thresholds.five_hour`        | `90`           | Auto-swap when 5h utilisation hits this %. `100` = reactive only. |
| `thresholds.seven_day`        | `95`           | Auto-swap when 7d utilisation hits this %. `100` = reactive only. |
| `strategy.kind`               | `drain`        | `drain` / `balanced` / `manual` |
| `strategy.order`              | `[]`           | Drain mode priority list (emails); empty = auto by highest 7d |
| `auto_switch_on_threshold`    | `true`         | Master toggle for pre-emptive threshold-driven swap |
| `auto_switch_on_rate_limit`   | `true`         | Master toggle for swap on rate-limit hook |
| `auto_resume`                 | `true`         | Pass `--resume <id>` to the relaunched claude |
| `auto_message`                | `Go continue.` | Sent as the first user turn after auto-swap; `""` = silent |
| `notify`                      | `true`         | Reserved for v0.3 desktop notifications |
| `poll_interval_seconds`       | `60`           | Reserved for v0.3 background usage monitor |

`cux config keys` lists everything above with current values and
descriptions, so you don't have to remember the exact names.

### Strategies

- **drain** — Use one account until its 7-day cap is near, then move
  on. Set `order` for explicit priority, or leave empty for
  auto-drain (highest-7d first, so the closest-to-limit account
  drains first).
- **balanced** — Always pick the account with the lowest 7-day
  utilisation (tiebreak by lowest 5h).
- **manual** — Never swap automatically. /switch and `cux switch`
  still work.

## Swap history

Every swap is logged with timestamp, trigger source (`manual`,
`threshold`, `rate-limit`, `rebalance`), from/to accounts, reason,
session id, cwd, and per-account usage at swap time:

```text
$ cux history
2026-05-01 14:12:33  alice@x → bob@x  [threshold]
    reason: 7d utilization 95% ≥ threshold 95%
    usage: alice@x 5h:34% 7d:95% → bob@x 5h:8% 7d:30%
    session: 143eec0f-277e-4ce1-95f1-58eb56331874

2026-05-01 13:55:08  bob@x → alice@x  [manual]
    reason: user requested via /switch
```

`cux history -n 5` for the last five; `cux history --json` to pipe;
`cux history --clear` to wipe.

## How it works

```
   user types     ┌────── claude (running, account A) ──────┐
   /switch ──────►│  hooks: Stop, SessionStart,             │
   or rate-limit  │         PostToolUseFailure               │
   ───────────────┴──┬───────────────────────────────────────┘
                     │ writes signal files
                     │ runtime/signals/{wrapperPID}-{name}
                     ▼
             ┌──────────────────────────────────────┐
             │  cux wrapper                         │  polls signals
             │   on rate-limit OR threshold OR      │  every 100 ms
             │   /switch:                           │
             │     wait for next Stop signal        │  ← guarantees flush
             │     ask claude to exit               │
             │     swap creds (transactional)       │
             │     append history.Entry             │
             │     relaunch claude --resume <id>    │
             │       [optional auto_message]        │
             └──────────────────────────────────────┘
```

- **Live credentials** are written wherever Claude Code itself reads:
  macOS Keychain (`Claude Code-credentials`) on Darwin,
  `~/.claude/.credentials.json` on Linux and Windows.
- **Backup credentials** (per-account stash) live in the OS keystore
  on macOS and Windows under the service `cux-backup`. On Linux they
  go to
  `~/.local/share/claude-switch/accounts/<N>-<email>/credentials.json`
  with mode 0600.
- **The oauthAccount block** inside `~/.claude/.claude.json` is the
  *only* part of that file cux ever rewrites. Themes, MCP config and
  history are untouched.
- **Atomic writes** (`tmp + fsync + rename`), file locking
  (`flock` / `LockFileEx`) on every state-modifying command.
- **Hook installation** in `~/.claude/settings.json` is upserted by
  signature — cux never modifies entries owned by other tools and
  `cux uninstall-hooks` removes only its own.
- **Process isolation**: each cux wrapper gets its own
  `CUX_WRAPPER_PID` and writes signals namespaced to that PID, so
  multiple cux sessions in different terminals never observe each
  other's hook events.

## Data layout

```
~/.local/share/claude-switch/        # ~/.claude-switch/ on macOS/Windows
├── state.json                      # account index, sequence, active slot
├── .lock                           # flock target for state mutations
├── accounts/
│   └── 01-user@example.com/
│       ├── credentials.json        # Linux only; macOS/Win uses keystore
│       └── oauth.json              # the oauthAccount block, raw JSON
└── runtime/
    ├── signals/                    # hook → wrapper signal files
    ├── usage-cache.json            # per-account 5h / 7d snapshot
    └── swap-history.json           # capped at 1000 entries

~/.config/cux/config.json           # XDG_CONFIG_HOME-aware
~/.claude/settings.json             # hooks upserted here
~/.claude/commands/switch.md        # /switch slash command
```

## Security notes

- Tokens are never logged. The structured logging path treats
  credential blobs as opaque and never crosses them into log fields;
  the helper that pulls a token out of a blob (`creds.ExtractAccessToken`)
  never surfaces it in error messages either.
- All cux-owned directories and files are 0700 / 0600. The installer
  refuses to run as root unless inside a container.
- The `/switch` slash command refuses to operate when cux is *not* the
  parent process — the `CUX_WRAPPED` env var must be set, so it
  cannot accidentally try to act on an unrelated `claude` process.
- The wrapper validates that a switch request originated in the same
  working directory before acting on it.
- The `~/.claude/settings.json` upsert only ever touches entries
  whose `command` field contains the literal string `cux ` or
  `/cux ` — every other tool's hooks are preserved.

## Building from source

```bash
git clone https://github.com/inulute/claude-switch
cd claude-switch/cux
go build -o cux ./cmd/cux
./cux help
```

Requires Go 1.21+.

## License

MIT.

---

If cux saves you time, you can support development at
[support.inulute.com](https://support.inulute.com). Entirely optional —
no nags, no telemetry, no postinstall messages. Thank you.
