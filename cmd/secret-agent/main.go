package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat/cli"
	"github.com/jtarchie/secret-agent/internal/model"
	"github.com/jtarchie/secret-agent/internal/runtime"
)

const usage = `usage: secret-agent run --model <provider/model-name> --api-key <key> <bot.yml>

examples:
  secret-agent run --model openrouter/anthropic/claude-sonnet-4-5 --api-key $OPENROUTER_API_KEY examples/hello-world.yml
  secret-agent run --model openai/gpt-4o --api-key $OPENAI_API_KEY examples/hello-world.yml
  secret-agent run --model anthropic/claude-sonnet-4-5-20250929 --api-key $ANTHROPIC_API_KEY examples/hello-world.yml
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) < 1 || args[0] != "run" {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("missing 'run' subcommand")
	}

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	modelFlag := fs.String("model", "", "provider/model-name (e.g. openrouter/anthropic/claude-sonnet-4-5)")
	keyFlag := fs.String("api-key", "", "API key for the model provider")
	baseURLFlag := fs.String("base-url", "", "override the provider's base URL (e.g. http://127.0.0.1:1234/v1 for a local OpenAI-compatible server)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	if *modelFlag == "" || *keyFlag == "" || fs.NArg() != 1 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("--model, --api-key, and a bot YAML path are all required")
	}

	path := fs.Arg(0)
	b, err := bot.Load(path)
	if err != nil {
		return err
	}

	provider, name := model.SplitModel(*modelFlag)
	llm, err := model.Resolve(provider, name, *keyFlag, *baseURLFlag)
	if err != nil {
		return err
	}

	ctx := context.Background()
	rt, err := runtime.New(ctx, b, llm)
	if err != nil {
		return err
	}

	return cli.New().Run(ctx, b.Name, rt.Send)
}
