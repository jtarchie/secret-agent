// Package model resolves a provider/model string to an ADK LLM.
package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
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
// If baseURL is non-empty it overrides the provider default (useful for local
// OpenAI-compatible servers like LM Studio / llama.cpp / vLLM).
func Resolve(provider, name, apiKey, baseURL string) (adkmodel.LLM, error) {
	switch provider {
	case "anthropic":
		return genaianthropic.New(genaianthropic.Config{
			APIKey:    apiKey,
			BaseURL:   baseURL,
			ModelName: name,
		}), nil
	default:
		if baseURL == "" {
			def, ok := DefaultBaseURLs[provider]
			if !ok {
				return nil, fmt.Errorf("unknown provider %q: pass --base-url, or use anthropic/openai/openrouter/ollama", provider)
			}
			baseURL = def
		}
		return genaiopenai.New(genaiopenai.Config{
			APIKey:    apiKey,
			BaseURL:   baseURL,
			ModelName: name,
		}), nil
	}
}

// AnthropicDefaultBaseURL is the default Anthropic API base URL.
const AnthropicDefaultBaseURL = "https://api.anthropic.com/v1"

// anthropicAPIVersion is the pinned Anthropic API version header value used
// for the preflight /models request. It does not need to match the client SDK.
const anthropicAPIVersion = "2023-06-01"

// Preflight verifies the provider is reachable and the API key is valid by
// issuing a GET to the provider's /models list endpoint. This call does not
// consume inference tokens on any supported provider.
func Preflight(ctx context.Context, provider, apiKey, baseURL string) error {
	base, err := resolveBaseURL(provider, baseURL)
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(base, "/") + "/models"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build preflight request: %w", err)
	}
	if provider == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	snippet := strings.TrimSpace(string(body))
	if snippet == "" {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return fmt.Errorf("GET %s: %s: %s", url, resp.Status, snippet)
}

// resolveBaseURL returns the API base URL for a provider, mirroring the logic
// used by Resolve so Preflight hits the same endpoint the LLM client will.
func resolveBaseURL(provider, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if provider == "anthropic" {
		return AnthropicDefaultBaseURL, nil
	}
	def, ok := DefaultBaseURLs[provider]
	if !ok {
		return "", fmt.Errorf("unknown provider %q: pass --base-url, or use anthropic/openai/openrouter/ollama", provider)
	}
	return def, nil
}
