# secret-agent

A YAML-defined chat bot with pluggable transports. Ships a terminal CLI,
a Signal transport backed by [signal-cli](https://github.com/AsamK/signal-cli),
and a Slack transport using Socket Mode. Multiple transports can run
concurrently in one process.

## Installation

```bash
brew tap jtarchie/secret-agent https://github.com/jtarchie/secret-agent
brew install secret-agent
```

Or download a pre-built binary from the
[releases page](https://github.com/jtarchie/secret-agent/releases).

## Build from source

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

Create a `config.yml` (see [examples/config.yml](examples/config.yml)):

```yaml
bots:
  - examples/hello-world.yml
transports:
  - type: signal
    account: "+15551234567"
    state_dir: ./signal-state
```

```sh
./secret-agent run \
  --config config.yml \
  --model anthropic/claude-sonnet-4-5-20250929 \
  --api-key "$ANTHROPIC_API_KEY"
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

`cli-config.yml`:

```yaml
bots:
  - examples/hello-world.yml
transports:
  - type: cli
```

```sh
./secret-agent run \
  --config cli-config.yml \
  --model anthropic/claude-sonnet-4-5-20250929 \
  --api-key "$ANTHROPIC_API_KEY"
```

The CLI transport owns stdin/stdout and therefore can't be combined with
other transports. Slash commands: `/help`, `/clear`, `/copy`, `/quit`.

## Getting Started — Slack

Socket Mode: a persistent WebSocket opened with an app-level token, so no
public ingress is required.

### 1. Create a Slack app

1. https://api.slack.com/apps → **Create New App** → **From scratch**.
2. **Socket Mode** → enable; generate an app-level token (scope
   `connections:write`). This is your `SLACK_APP_TOKEN` (`xapp-…`).
3. **OAuth & Permissions** → add bot scopes: `app_mentions:read`,
   `channels:history`, `chat:write`, `files:read`, `groups:history`,
   `im:history`, `mpim:history`, `users:read`. Install to a workspace →
   copy the bot token (`xoxb-…`) as `SLACK_BOT_TOKEN`.
4. **Event Subscriptions** (enabled by Socket Mode): subscribe to
   `message.im`, `message.channels`, `message.groups`, `message.mpim`.
5. Invite the bot to a test channel and note its channel ID
   (`CXXXXXXX`) and your user ID (`UXXXXXXX`).

### 2. Scope the bot

Add `slack_users:` and/or `slack_channels:` alongside the existing
`users:` / `groups:` fields in a bot YAML. Leave empty to allow all.

### 3. Run

```yaml
bots:
  - examples/hello-world.yml
transports:
  - type: slack
    bot_token_env: SLACK_BOT_TOKEN
    app_token_env: SLACK_APP_TOKEN
```

```sh
SLACK_BOT_TOKEN=xoxb-… SLACK_APP_TOKEN=xapp-… \
  ./secret-agent run --config config.yml --model … --api-key …
```

Replies go back on the transport that received the prompt. Threaded DMs
and channel messages each get their own conversation memory.

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
| `model` | string | no | Per-bot model override (`provider/model-name`). Unset = global `--model`. Also honored on sub-agent YAMLs. |
| `api_key_env` | string | no | Env var name holding the API key for this bot. Unset = global `--api-key`. Never inline the key. |
| `base_url` | string | no | Per-bot base URL. Unset = derived from the provider prefix (for `anthropic|openai|openrouter|ollama`) or the global `--base-url` when the provider matches. |

Per-bot model fields are all optional and fall back field-by-field to the CLI flags, so a YAML that sets only `model:` keeps the global API key and base URL. Example — a config with one global-model bot and one local-Ollama bot:

```yaml
# bots/triage.yml — inherits the global --model
name: triage
system: "Route the user to the right expert."
```

```yaml
# bots/local.yml — pins its own model + key + base URL
name: local
system: "You answer offline, from a local model."
model: ollama/llama3
api_key_env: OLLAMA_API_KEY   # env-var indirection, same pattern as Slack tokens
base_url: http://localhost:11434/v1
```

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

### `secret-agent run --config <path>`

Bots and transports are declared in a YAML config file. See
[examples/config.yml](examples/config.yml). With multiple bots, the router
selects one bot per incoming message based on each bot's transport-specific
scope (`users:`/`groups:` for Signal, `slack_users:`/`slack_channels:` for
Slack) and `triggers:`. Multiple transports run concurrently in separate
goroutines; a reply always returns on the transport that received the
prompt.

| Flag | Default | Purpose |
|---|---|---|
| `--model` | — | **Required.** `provider/model-name` (e.g. `anthropic/claude-sonnet-4-5-20250929`). Fallback when a bot does not set its own `model:`. |
| `--api-key` | — | **Required.** Model provider API key. Fallback when a bot does not set its own `api_key_env:`. |
| `--config` | — | **Required.** Path to the run config file (bots + transports). |
| `--base-url` | — | Override provider base URL (e.g. local OpenAI-compatible server). Fallback when a bot does not set its own `base_url:`. |
| `--skip-preflight` | `false` | Skip model endpoint / API key validation at startup (applied to every unique per-bot endpoint). |
| `--mcp-preflight-timeout` | `5s` | Per-server timeout for the startup MCP tool-listing probe; `0` disables the deadline. |
| `--verbose` | `0` | 0=info, 1/2/3=debug with signal-cli `-v` / `-vv` / `-vvv`. |

### Config file

```yaml
bots:
  - examples/hello-world.yml           # relative paths resolve to the config dir
transports:
  - type: signal
    account: "+15551234567"            # or account_env: SIGNAL_ACCOUNT (mutually exclusive)
    state_dir: ./signal-state
    command: signal-cli                # optional; defaults to "signal-cli"
  - type: slack
    bot_token_env: SLACK_BOT_TOKEN     # env var names, never inline secrets
    app_token_env: SLACK_APP_TOKEN
  # - type: cli                         # exclusive with other transports
```

### `secret-agent eval <bot.yml>`

Runs every case in the bot's `tests:` block as a single fresh-session turn, scores each against its `expect`, and prints a PASS/FAIL summary. Exits non-zero if any case fails — suitable for CI. Each case hits the configured LLM, so an API key is required.

| Flag | Default | Purpose |
|---|---|---|
| `--model` | — | **Required.** `provider/model-name`. Fallback when the bot does not set its own `model:`. |
| `--api-key` | — | **Required.** Model provider API key. Fallback when the bot does not set its own `api_key_env:`. |
| `--base-url` | — | Override provider base URL. Fallback when the bot does not set its own `base_url:`. |
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
- The CLI transport cannot be combined with other transports (it owns stdin/stdout).

Run two bots together (config-driven):

```yaml
bots:
  - examples/routing/admin-bot.yml
  - examples/routing/public-bot.yml
transports:
  - type: signal
    account: "+15551234567"
    state_dir: ./signal-state
```

```bash
./secret-agent run --config routing.yml \
  --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY
```

See [examples/routing/](examples/routing/) for a runnable two-bot fleet.
