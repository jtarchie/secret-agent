# Embedded Built-in Sub-Agents

## Context

Today, every sub-agent in a `secret-agent` bot lives in its own YAML file referenced via `agents.<name>.file:`. For common, generic helpers (a summarizer, a translator, a code reviewer) this means every user has to author the same boilerplate child YAMLs themselves — even though the content rarely varies.

This change ships a curated set of sub-agent YAMLs *inside* the CLI binary. Users opt in per-agent by writing `builtin: <name>` in place of `file:`. Built-ins are not auto-loaded — they only resolve when explicitly referenced. The mechanism is additive: `file:` continues to work exactly as it does today, and the runtime layer (`internal/runtime`) sees no change because we still populate `AgentRef.Bot` the same way.

A new `secret-agent list-builtins` subcommand lets users discover what's available.

## Approach

### 1. Schema: add `builtin:` to `AgentRef`

[internal/bot/bot.go:199-210](../../internal/bot/bot.go#L199-L210) — add the `Builtin` field, mark `File` as `omitempty`:

```go
type AgentRef struct {
    File              string `yaml:"file,omitempty"`
    Builtin           string `yaml:"builtin,omitempty"`
    Description       string `yaml:"description"`
    SkipSummarization bool   `yaml:"skip_summarization"`
    Attachments       bool   `yaml:"attachments"`
    Bot               *Bot   `yaml:"-"`
}
```

### 2. Refactor `loadBot` → `parseBot` core

[internal/bot/bot.go:577-924](../../internal/bot/bot.go#L577-L924) — extract everything after `os.ReadFile` into:

```go
func parseBot(data []byte, label, baseDir string, visited map[string]bool, depth int) (*Bot, error)
```

- `label` is what appears in error messages and is the `visited` key (e.g. `"/abs/path/parent.yml"` for files, `"builtin:summarizer"` for built-ins). This generalizes the existing absolute-path cycle key.
- `baseDir` is the directory used to resolve a child's relative `file:` path. Pass `""` for built-ins — built-ins cannot use relative `file:` references in their (currently empty) `agents:` maps.

`Load(path)` and `loadBot(path, ...)` keep their public signatures. `loadBot` becomes: resolve abs → `os.ReadFile` → `parseBot(data, abs, filepath.Dir(abs), visited, depth)`.

### 3. Agent-resolution loop branch

[internal/bot/bot.go:897-921](../../internal/bot/bot.go#L897-L921) — replace the `file`-only logic with a mutual-exclusion check (mirroring the `sh/expr/js` pattern at [internal/bot/bot.go:567-574](../../internal/bot/bot.go#L567-L574)):

```go
ref.File = strings.TrimSpace(ref.File)
ref.Builtin = strings.TrimSpace(ref.Builtin)

set := []string{}
if ref.File != "" { set = append(set, "file") }
if ref.Builtin != "" { set = append(set, "builtin") }
switch len(set) {
case 0:
    return nil, fmt.Errorf("%s: agent %q: exactly one of file, builtin is required", label, key)
case 1:
default:
    return nil, fmt.Errorf("%s: agent %q: only one of file, builtin may be set (got %s)", label, key, strings.Join(set, ", "))
}
```

Then dispatch:

- `ref.File != ""`: existing path logic (resolve against `baseDir`, recursive `loadBot`).
- `ref.Builtin != ""`: `LookupBuiltin(ref.Builtin)` → `parseBot(data, "builtin:"+ref.Builtin, "", visited, depth+1)`. Unknown names error as `unknown builtin %q`. Depth and cycle accounting stay unified — built-ins are leaves today but might reference each other later, and the unified path is no more code than special-casing.

### 4. Embedded registry: `internal/bot/builtins.go`

New file co-located with the loader:

```go
//go:embed builtins/*.yml
var builtinFS embed.FS

type BuiltinInfo struct { Name, Description string }

func LookupBuiltin(name string) (data []byte, info BuiltinInfo, ok bool)
func ListBuiltins() ([]BuiltinInfo, error) // sorted by Name
```

Both functions hit a `sync.Once` that walks `builtins/`, reads each `.yml`, and parses it twice: once into a tiny `struct{ Name, Description string }` for the registry index, and the bytes are stored verbatim for `LookupBuiltin`. Each YAML carries a top-level `description:` field for the registry; `Bot` ignores it (yaml.v3 non-strict via [internal/bot/bot.go:602](../../internal/bot/bot.go#L602) — plain `yaml.Unmarshal`, no `KnownFields(true)`), so it costs nothing at parent-load time.

### 5. v1 built-in set: `internal/bot/builtins/*.yml`

Three leaf-only YAMLs (no tools, no model, no nested agents):

**summarizer.yml**
```yaml
name: summarizer
description: Condenses input text into a brief summary.
system: |
  Read the user's input and produce a faithful 2-3 sentence summary.
  Do not add information that isn't present in the source.
```

**translator.yml**
```yaml
name: translator
description: Translates input text into a target language.
system: |
  The user will provide text and a target language. Return only the
  translation, with no commentary.
```

**code-reviewer.yml**
```yaml
name: code-reviewer
description: Reviews a code diff for bugs and style issues.
system: |
  You receive a code diff. Identify bugs, unsafe patterns, and clear
  style issues. Be specific and concise. If the diff looks fine, say so.
```

### 6. `list-builtins` subcommand

New file [cmd/secret-agent/list_builtins.go](../../cmd/secret-agent/) with a `ListBuiltinsCmd` struct whose `Run()` calls `bot.ListBuiltins()` and prints `name — description` one per line.

Wire it into the kong CLI struct in [cmd/secret-agent/main.go](../../cmd/secret-agent/main.go) alongside the existing `run` / `eval` commands:

```go
ListBuiltins ListBuiltinsCmd `cmd:"" help:"list embedded built-in sub-agents" name:"list-builtins"`
```

### 7. Tests

Add to [internal/bot/bot_test.go](../../internal/bot/bot_test.go):

- `TestLoadAgentsBuiltin` — parent YAML with `agents.helper.builtin: summarizer`; assert `ref.Bot.Name == "summarizer"` and the parent's overrides (`description`, `attachments`) survive on the AgentRef.
- `TestLoadAgentsBuiltinUnknown` — `builtin: does-not-exist` → error contains `"unknown builtin"`.
- `TestLoadAgentsFileAndBuiltinSet` — both fields set → error contains `"only one of file, builtin"`.
- `TestLoadAgentsNeitherFileNorBuiltin` — neither set → error contains `"exactly one of file, builtin"`. (Replaces or complements the existing missing-file test depending on what message it currently asserts.)

Add an `internal/bot/builtins_test.go`:

- `TestListBuiltins` — asserts the three v1 names appear and each has a non-empty description.
- `TestLookupBuiltin` — round-trips `LookupBuiltin("summarizer")` and confirms the bytes parse to a valid `Bot`.

### 8. README

[README.md](../../README.md) — the existing "agents:" section (around line 285+, near line 244) gets:

- A `builtin:` row in the agent-fields table, noted as mutually exclusive with `file:`.
- A short paragraph pointing at `secret-agent list-builtins` for discovery, with a one-line example showing `builtin: summarizer`.

## Files to modify / create

- [internal/bot/bot.go](../../internal/bot/bot.go) — `AgentRef` field, `parseBot` extraction, agent-loop branch.
- `internal/bot/builtins.go` **(new)** — `embed.FS` + `LookupBuiltin` / `ListBuiltins`.
- `internal/bot/builtins/summarizer.yml` **(new)**
- `internal/bot/builtins/translator.yml` **(new)**
- `internal/bot/builtins/code-reviewer.yml` **(new)**
- `cmd/secret-agent/list_builtins.go` **(new)** — kong subcommand.
- [cmd/secret-agent/main.go](../../cmd/secret-agent/main.go) — wire the subcommand into the CLI struct.
- [internal/bot/bot_test.go](../../internal/bot/bot_test.go) — four new test cases.
- `internal/bot/builtins_test.go` **(new)** — registry tests.
- [README.md](../../README.md) — agents section update.

## Out of scope

- Overriding the embedded child's `system` / `tools` / `model` / etc. Only `description`, `attachments`, `skip_summarization` (the existing `AgentRef` knobs) can be customized.
- A top-level config-level `builtin_bots:` field (rejected — the per-AgentRef syntax keeps everything scoped to where it's used).
- Changes to [internal/runtime/runtime.go](../../internal/runtime/runtime.go) or [internal/tool/subagent.go](../../internal/tool/subagent.go). The `AgentRef.Bot` pointer is the contract and remains populated identically.

## Verification

1. `go build ./...` and `go test ./internal/bot/...` — unit-level coverage.
2. `./secret-agent list-builtins` — confirm the three v1 entries print with descriptions.
3. End-to-end: write a tiny parent bot YAML referencing `builtin: summarizer`, run via `./secret-agent run --config <top-config> --model <...> --api-key <...>` against the CLI transport, send a paragraph, confirm the summarizer sub-agent fires and returns a 2-3 sentence summary.
4. Negative: write a bot with `builtin: bogus` and confirm `Load()` errors with `unknown builtin "bogus"`. Write one with both `file:` and `builtin:` set and confirm the mutual-exclusion error fires.
5. Confirm an existing `file:`-based bot (e.g. [examples/hello-world.yml](../../examples/hello-world.yml)) still loads and runs unchanged.
