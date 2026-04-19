# secret-agent

A YAML-defined chat bot with pluggable transports. Ships a terminal CLI
and a Signal transport backed by [signal-cli](https://github.com/AsamK/signal-cli).

## Build

```sh
go build -o secret-agent ./cmd/secret-agent
```

## Getting Started — Signal

### 1. Install signal-cli

Requires JRE 25+.

```sh
brew install signal-cli          # macOS
# or grab a release: https://github.com/AsamK/signal-cli/releases
```

### 2. Link as a secondary device

On your phone: **Signal → Settings → Linked devices → Link new device**.
Then in a terminal:

```sh
./secret-agent signal-link --signal-state-dir ./signal-state
```

A QR code is printed in the terminal — scan it with your phone. The raw
`sgnl://...` URI is also printed above the QR as a fallback for headless
or piped setups; pass `--no-qr` to suppress the QR block entirely. The
phone number of the linked account is printed when linking completes.

The `./signal-state` directory now holds the Signal Protocol keys and
per-peer ratchet state. Back it up — wiping it means re-linking.

### 3. Run the bot

```sh
./secret-agent run \
  --transport signal \
  --signal-account +15551234567 \
  --signal-state-dir ./signal-state \
  --model anthropic/claude-sonnet-4-5-20250929 \
  --api-key "$ANTHROPIC_API_KEY" \
  examples/hello-world.yml
```

Send the linked account a Signal DM; the bot replies. Group messages are
ignored. `Ctrl-C` shuts down cleanly.

### What persists where

| Data | Location | Why |
|---|---|---|
| Signal Protocol keys, ratchet state | `--signal-state-dir` | Required by Signal; wiping forces re-link. |
| Chat history / LLM context | in-memory only | Cleared on every restart. |
| Anything else | nothing | The bot writes no logs or history. |

Each Signal contact gets its own in-memory conversation context
(keyed by ACI UUID). Contexts reset when the bot restarts.

## Getting Started — Terminal CLI

```sh
./secret-agent run \
  --model anthropic/claude-sonnet-4-5-20250929 \
  --api-key "$ANTHROPIC_API_KEY" \
  examples/hello-world.yml
```

`--transport cli` is the default. Slash commands: `/help`, `/clear`,
`/copy`, `/quit`.

### Attaching files

Reference a local file inline with `#file:<path>`. The token is replaced
with `[attached: <name>]` in the text and the file is sent alongside the
message:

```
summarize #file:./notes.md please
```

Quote paths that contain spaces: `#file:"my notes.md"`. Tools declared
with `type: attachment` (see `file_info` in [examples/hello-world.yml](examples/hello-world.yml))
receive the resolved path as an env var — the model picks the attachment
by index (`"0"`) or filename.

## Bot definition (YAML)

See [examples/](examples/) for runnable configs. Full field reference below.

### Top-level fields

| Field | Type | Required | Purpose |
|---|---|---|---|
| `name` | string | yes | Bot identifier (must match `[A-Za-z_][A-Za-z0-9_]*`). |
| `system` | string | yes | System prompt / instructions for the LLM. |
| `triggers` | []string | no | Signal-only: words that gate a response (word-boundary, case-insensitive). Required when loading >1 bot. |
| `users` | []string | no | Signal-only: E.164 allowlist. Empty = all senders. |
| `groups` | []string | no | Signal-only: group-ID allowlist. Empty = all groups. |
| `permissions` | object | no | See *Permissions*. |
| `tools` | []Tool | no | Shell/expr/JS tools the bot can call. |
| `agents` | map[string]AgentRef | no | Sub-agents callable as tools (max nesting 8, no cycles). |
| `hooks` | []Hook | no | Bot-level callbacks (before/after model, tool, agent). |
| `mcp` | []MCPServer | no | External MCP servers to expose. |
| `tests` | []TestCase | no | Declarative eval cases — see *Evaluation*. |

### Permissions

| Field | Type | Default | Purpose |
|---|---|---|---|
| `attachments` | bool | `true` | Allow inbound attachments to reach the bot. |
| `memory` | enum | `full` | `full` (per-conversation + buffering), `session` (per-conversation, no buffering), `none` (stateless per turn). |

### Tools

| Field | Type | Purpose |
|---|---|---|
| `name` | string | Unique identifier. |
| `description` | string | Shown to the model. |
| `sh` / `expr` / `js` | string | Implementation — exactly one required. |
| `params` | map[string]Param | Typed params (see below). |
| `hooks` | []Hook | Tool-scoped `before`/`after` hooks. |

Param types: `string`, `integer`, `number`, `boolean`, `attachment`. Shorthand: `string!` (required), `integer=5` (default), `boolean=true`. Enums allowed on `string` only. `required` and `default` are mutually exclusive.

### Agents

| Field | Type | Purpose |
|---|---|---|
| `file` | string | Path to sub-agent YAML (relative to parent). |
| `description` | string | Exposed to parent LLM. |
| `skip_summarization` | bool | Pass raw output back to parent. |
| `attachments` | bool | Let parent forward attachments. |

### Hooks

| Field | Type | Purpose |
|---|---|---|
| `on` | enum | Bot-level: `before_tool`, `after_tool`, `before_model`, `after_model`, `before_agent`, `after_agent`. Tool-level: `before`, `after`. |
| `tool` | string | Filter `before_tool`/`after_tool` to one tool. |
| `sh` / `expr` / `js` | string | Implementation — exactly one required. |

### MCP servers

| Field | Type | Purpose |
|---|---|---|
| `name` | string | Unique identifier (namespaces its tools). |
| `command` + `args` + `env` | | Local stdio subprocess. |
| `url` + `headers` | | Remote HTTP streamable endpoint. |
| `tool_filter` | []string | Allowlist of tool names from this server. |
| `require_confirmation` | bool | Human-in-the-loop gate per call. |

Exactly one of `command` / `url` is required.

### Tests (evaluation cases)

| Field | Type | Purpose |
|---|---|---|
| `name` | string | Unique case identifier. |
| `input` | string | User message sent as a single turn. |
| `expect.tool_calls` | []ExpectedToolCall | Must appear in this order as a subsequence of the observed trajectory. |
| `expect.final_output` | OutputMatcher | `equals` / `contains` / `not_contains` / `regex` assertions on the concatenated model text. |

`ExpectedToolCall.args` is a **subset** match — every listed key must be present with an equal value; extra actual args are ignored. Numbers compare across int/float representations (YAML `17` matches a runtime `17.0`). Run with `secret-agent eval` — see below.

## Command-line reference

### `secret-agent run <bot.yml> [bot.yml ...]`

Accepts one bot or many. With multiple bots (Signal only), the router selects one bot per incoming message based on each bot's `users:` / `groups:` scope and `triggers:`. Bots' trigger words must be globally disjoint; every multi-bot run requires ≥1 trigger on each bot. See *Multi-bot routing* below.


| Flag | Default | Purpose |
|---|---|---|
| `--model` | — | **Required.** `provider/model-name` (e.g. `anthropic/claude-sonnet-4-5-20250929`). |
| `--api-key` | — | **Required.** Model provider API key. |
| `--base-url` | — | Override provider base URL (e.g. local OpenAI-compatible server). |
| `--transport` | `cli` | `cli` or `signal`. |
| `--signal-account` | — | E.164 phone; required when `--transport=signal`. |
| `--signal-state-dir` | — | signal-cli state dir; required when `--transport=signal`. |
| `--signal-cli` | `signal-cli` | Path to the signal-cli binary. |
| `--skip-preflight` | `false` | Skip model endpoint / API key validation at startup. |
| `--verbose` | `0` | 0=info, 1/2/3=debug with signal-cli `-v` / `-vv` / `-vvv`. |

### `secret-agent eval <bot.yml>`

Runs every case in the bot's `tests:` block as a single fresh-session turn, scores each against its `expect`, and prints a PASS/FAIL summary. Exits non-zero if any case fails — suitable for CI. Each case hits the configured LLM, so an API key is required.

| Flag | Default | Purpose |
|---|---|---|
| `--model` | — | **Required.** `provider/model-name`. |
| `--api-key` | — | **Required.** Model provider API key. |
| `--base-url` | — | Override provider base URL. |
| `--skip-preflight` | `false` | Skip the startup model-endpoint check. |
| `--verbose` | `false` | Also print observed tool trajectory and final text for passing cases. |

Example `tests:` block (see [examples/hello-world.yml](examples/hello-world.yml)):

```yaml
tests:
  - name: greets_by_name
    input: "please greet Ada"
    expect:
      tool_calls:
        - { name: greet, args: { who: "Ada" } }
      final_output:
        contains: ["Ada"]
```

### `secret-agent signal-link`

| Flag | Default | Purpose |
|---|---|---|
| `--signal-state-dir` | — | **Required.** Dir to write keys/ratchet state (created `0700` if missing). |
| `--signal-device-name` | `secret-agent` | Name shown on the primary device. |
| `--signal-cli` | `signal-cli` | Path to the signal-cli binary. |
| `--no-qr` | `false` | Print only the `sgnl://` URI, suppress QR block. |
| `--verbose` | `0` | 0=info, 1=debug. |

## Interface features

### Terminal CLI

- **Slash commands:** `/help`, `/clear`, `/copy` (last reply to clipboard), `/mouse` (toggle mouse mode, disables native text selection), `/quit` / `/exit`.
- **Keybindings:** `Enter` send · `Alt+Enter` newline · `↑`/`↓` input history · `PgUp`/`PgDn` scroll · `Ctrl+U`/`Ctrl+D` half-page · `Ctrl+C` cancel turn / quit · `Esc` quit.
- **Display:** streaming chunks, glamour-rendered markdown, colored roles (user=cyan, bot=magenta, errors=red), spinner while waiting, auto-scroll on new content.
- **Attachments:** `#file:<path>` inline; quote paths with spaces. Tools with `type: attachment` receive the resolved path.
- **Conversation:** one in-memory session id `local`, cleared on exit.

### Signal

- **Message scopes:** DMs (always replied to), groups (only when a `triggers` word matches — never auto-reply), Note-to-Self (own sent echoes suppressed for 2 min).
- **Triggers:** optional per-bot allowlist; word-boundary regex, case-insensitive. Empty list = reply to every DM.
- **Buffering:** per-conversation FIFO (capacity 10) accumulates un-triggered messages and flushes them inside a `<previous_messages>` block on the next trigger. Disable with `permissions.memory: session` / `none`. Text only; never applied across group members.
- **Attachments:** inbound files downloaded by signal-cli and resolved to `<state-dir>/attachments/<id>`. Strip at transport with `permissions.attachments: false`.
- **Per-peer isolation:** one in-memory conversation per Signal contact (keyed by ACI UUID), mutex-serialized so multi-chunk replies don't interleave — two bots replying to the same peer are serialized through one stdin stream.
- **Linking:** `signal-link` prints a `sgnl://linkdevice?...` URI and an inline QR (use `--no-qr` for headless).
- **Shutdown:** SIGINT + 5 s grace lets ratchet state flush.

### Multi-bot routing

`secret-agent run` accepts several bot YAMLs and runs them behind a single Signal account. The router selects one bot per incoming message:

1. **Scope filter.** A bot is eligible if its `users:` allowlist contains the sender (or is empty) and, for group messages, its `groups:` allowlist contains the group ID (or is empty).
2. **Trigger match.** Among eligible bots, the first one whose `triggers:` matches the message text handles the turn. Unmatched messages are buffered per conversation and flushed into the first later trigger (same bot or not).
3. **No match.** The message is silently dropped — no bot replies.

Constraints enforced at load time:

- Every bot in multi-bot mode must declare ≥1 trigger.
- Trigger words must be globally disjoint across bots (case-insensitive). Load fails with an aggregated list of conflicting words.
- The CLI transport (`--transport=cli`) only accepts a single bot.

Run two bots together:

```bash
./secret-agent run --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY \
  --transport signal --signal-account +15551234567 --signal-state-dir ./signal-state \
  examples/routing/admin-bot.yml examples/routing/public-bot.yml
```

See [examples/routing/](examples/routing/) for a runnable two-bot fleet.
