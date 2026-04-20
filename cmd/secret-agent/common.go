package main

import (
	"log/slog"
	"os"
)

// ModelFlags is embedded by subcommands that need to resolve an LLM endpoint.
type ModelFlags struct {
	Model         string `help:"provider/model-name (e.g. openrouter/anthropic/claude-sonnet-4-5)"                                     required:""`
	APIKey        string `help:"API key for the model provider"                                                                        name:"api-key"        required:""`
	BaseURL       string `help:"override the provider's base URL (e.g. http://127.0.0.1:1234/v1 for a local OpenAI-compatible server)" name:"base-url"`
	SkipPreflight bool   `help:"skip the startup check that verifies the model endpoint is reachable and the API key is valid"         name:"skip-preflight"`
}

// newLogger builds a text slog.Logger on stderr. verbose>=1 enables Debug.
func newLogger(verbose int) *slog.Logger {
	level := slog.LevelInfo
	if verbose >= 1 {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
