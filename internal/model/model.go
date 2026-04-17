// Package model resolves a provider/model string to an ADK LLM.
package model

import (
	"fmt"
	"strings"

	genaianthropic "github.com/achetronic/adk-utils-go/genai/anthropic"
	genaiopenai "github.com/achetronic/adk-utils-go/genai/openai"
	adkmodel "google.golang.org/adk/model"
)

// DefaultBaseURLs maps OpenAI-compatible providers to their API endpoints.
var DefaultBaseURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"openrouter": "https://openrouter.ai/api/v1",
	"ollama":     "http://localhost:11434/v1",
}

// SplitModel parses "provider/model-name" into (provider, model-name).
// "openrouter/anthropic/claude-sonnet-4-5" -> ("openrouter", "anthropic/claude-sonnet-4-5").
func SplitModel(s string) (provider, name string) {
	idx := strings.Index(s, "/")
	if idx < 0 {
		return s, s
	}
	return s[:idx], s[idx+1:]
}

// Resolve constructs an ADK LLM for the given provider + model + API key.
func Resolve(provider, name, apiKey string) (adkmodel.LLM, error) {
	switch provider {
	case "anthropic":
		return genaianthropic.New(genaianthropic.Config{
			APIKey:    apiKey,
			ModelName: name,
		}), nil
	default:
		baseURL, ok := DefaultBaseURLs[provider]
		if !ok {
			return nil, fmt.Errorf("unknown provider %q (known: anthropic, openai, openrouter, ollama)", provider)
		}
		return genaiopenai.New(genaiopenai.Config{
			APIKey:    apiKey,
			BaseURL:   baseURL,
			ModelName: name,
		}), nil
	}
}
