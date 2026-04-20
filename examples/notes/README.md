# notes bot

A Granola-inspired note-taking bot that saves to Apple Notes. Works over
Slack and/or Signal. The LLM auto-titles, routes to a folder (`Inbox` /
`1:1s` / `Meetings` / `Ideas` / `Journal`), cleans raw thoughts into HTML,
and extracts action items before saving.

## Flows

- **DM freeform.** DM the bot several un-triggered messages with raw thoughts.
  They buffer silently. When you send `@note save`, the bot synthesizes a note
  from the buffered messages + your save prompt and writes it to Apple Notes.
- **Channel thread save.** In any channel the bot is a member of, `@note save
  this` — the bot captures the visible context and saves.
- **Recall.** `@note find my notes about X` searches by title;
  `@note open "Q2 Planning"` reads the body back.

## One-time setup

### 1. Install `apple-notes-cli`

```sh
git clone https://github.com/angelespejo/apple-notes-cli.git
cd apple-notes-cli && chmod a+x * && ./install.sh
```

Confirm on PATH: `command -v apple-notes-cli`.

### 2. Grant Automation permission for Notes

System Settings → Privacy & Security → Automation → allow your terminal (or
whatever process ends up running `secret-agent`) to control **Notes**. First
run of `save_note` will prompt if not already granted.

### 3. Pick a transport (Slack, Signal, or both)

The shipped [config.yml](config.yml) enables both. Remove whichever stanza
you don't need — transports run in parallel and the bot replies on whichever
it received the message over.

**Slack** — create an app at api.slack.com/apps, enable **Socket Mode**, and
add these Bot Token scopes:

    app_mentions:read
    chat:write
    im:history
    im:read
    im:write
    channels:history
    groups:history

Install to workspace. Grab the Bot Token (`xoxb-…`) and an App-level Token
(`xapp-…`) with `connections:write`.

```sh
export SLACK_BOT_TOKEN=xoxb-...
export SLACK_APP_TOKEN=xapp-...
```

**Signal** — link a secondary device against your existing Signal account:

```sh
./secret-agent signal-link --signal-state-dir ./signal-state
# scan the QR block from your phone: Settings → Linked Devices → + → Scan
export SIGNAL_ACCOUNT="+15551234567"   # your E.164 number
```

The example config reads the number from `$SIGNAL_ACCOUNT` (via
`account_env:`), so the repo never commits your phone number.

## Run

```sh
./secret-agent run \
  --config examples/notes/config.yml \
  --model "$MODEL" \
  --api-key "$API_KEY"
```

Invite the bot to any channel you want it to read, or DM it directly. Over
Signal, the "bot" is just your linked number — message it from any other
Signal contact (or add it to a group). In a DM, messages without `@note`
buffer as prior context; the next message containing `@note` flushes and
processes everything.

## Evals (no Slack, no Apple Notes required)

Tool calls are captured, not executed:

```sh
./secret-agent eval \
  --model "$MODEL" \
  --api-key "$API_KEY" \
  examples/notes/bot.yml
```

## Known limits

- `apple-notes-cli add` cannot set note body, so `save_note` uses AppleScript
  via `osascript` directly to create the note and passes the model-generated
  HTML into Notes' `body` property. `apple-notes-cli add-folder` is still
  used for the idempotent folder create.
- `search_notes` iterates every note in Notes.app — slow for very large
  libraries (capped at 25 matches, early-exits once hit).
- No attachments in v1 (`permissions.attachments: false`).
- Channel thread summarization only captures messages the bot received while
  it was a member of that channel — the framework cannot retroactively fetch
  thread history.
