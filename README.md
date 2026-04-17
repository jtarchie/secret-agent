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

## Bot definition

See [examples/hello-world.yml](examples/hello-world.yml) for the YAML
schema (name, system prompt, shell-backed tools with typed params).
