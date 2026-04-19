package hook

import (
	"fmt"
	"strings"

	"google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/genai"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// LLMCallbacks is the set of ADK callback slices a bot's hooks populate.
// Empty slices are safe to pass to llmagent.Config.
type LLMCallbacks struct {
	BeforeModel []llmagent.BeforeModelCallback
	AfterModel  []llmagent.AfterModelCallback
	BeforeTool  []llmagent.BeforeToolCallback
	AfterTool   []llmagent.AfterToolCallback
	BeforeAgent []agent.BeforeAgentCallback
	AfterAgent  []agent.AfterAgentCallback
}

// BuildLLMCallbacks fans compiled hooks out into per-event ADK callback
// slices. Hooks with a Filter that doesn't match the runtime tool name
// short-circuit to pass-through within their adapter.
func BuildLLMCallbacks(hs []Compiled) LLMCallbacks {
	var cbs LLMCallbacks
	for _, h := range hs {
		switch h.Event {
		case bot.HookBeforeTool:
			cbs.BeforeTool = append(cbs.BeforeTool, beforeTool(h))
		case bot.HookAfterTool:
			cbs.AfterTool = append(cbs.AfterTool, afterTool(h))
		case bot.HookBeforeModel:
			cbs.BeforeModel = append(cbs.BeforeModel, beforeModel(h))
		case bot.HookAfterModel:
			cbs.AfterModel = append(cbs.AfterModel, afterModel(h))
		case bot.HookBeforeAgent:
			cbs.BeforeAgent = append(cbs.BeforeAgent, beforeAgent(h))
		case bot.HookAfterAgent:
			cbs.AfterAgent = append(cbs.AfterAgent, afterAgent(h))
		}
	}
	return cbs
}

// beforeTool returns an ADK BeforeToolCallback that runs the hook's script
// with env={tool_name, args}. Script returns:
//   - nil: pass-through (ADK runs the real tool with the original args)
//   - map: the real tool is SKIPPED and the map is used as the tool's result
//   - non-map: wrapped as {"output": value} and used as the result
//   - error: veto (ADK aborts the tool call and surfaces the error).
//
// ADK has no return-value path to rewrite args in-place; hooks that want
// to skip the real tool must return the replacement result directly.
func beforeTool(h Compiled) llmagent.BeforeToolCallback {
	return func(tctx adktool.Context, t adktool.Tool, args map[string]any) (map[string]any, error) {
		if h.Filter != "" && h.Filter != t.Name() {
			return nil, nil
		}
		env := map[string]any{
			"tool_name": t.Name(),
			"args":      cloneArgs(args),
		}
		out, err := h.Run(tctx, env)
		if err != nil {
			return nil, fmt.Errorf("before_tool %q: %w", t.Name(), err)
		}
		if out == nil {
			return nil, nil
		}
		m, ok := out.(map[string]any)
		if !ok {
			return map[string]any{"output": out}, nil
		}
		return m, nil
	}
}

// afterTool returns an ADK AfterToolCallback that runs the hook's script
// with env={tool_name, args, result, error}. A non-nil return replaces the
// result; a script error replaces the error.
func afterTool(h Compiled) llmagent.AfterToolCallback {
	return func(tctx adktool.Context, t adktool.Tool, args, result map[string]any, inErr error) (map[string]any, error) {
		if h.Filter != "" && h.Filter != t.Name() {
			return nil, nil
		}
		env := map[string]any{
			"tool_name": t.Name(),
			"args":      cloneArgs(args),
			"result":    cloneArgs(result),
			"error":     errString(inErr),
		}
		out, err := h.Run(tctx, env)
		if err != nil {
			return nil, fmt.Errorf("after_tool %q: %w", t.Name(), err)
		}
		if out == nil {
			return nil, nil
		}
		m, ok := out.(map[string]any)
		if !ok {
			return map[string]any{"output": out}, nil
		}
		return m, nil
	}
}

// beforeModel runs a hook before the LLM call. The hook's return value is
// ignored (we ship veto-only: return an error to abort). Response
// substitution is deferred.
func beforeModel(h Compiled) llmagent.BeforeModelCallback {
	return func(cctx agent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
		env := map[string]any{
			"model":    req.Model,
			"messages": contentsToAny(req.Contents),
		}
		_, err := h.Run(cctx, env)
		if err != nil {
			return nil, fmt.Errorf("before_model: %w", err)
		}
		return nil, nil
	}
}

// afterModel runs a hook after the LLM call. When the script returns a
// non-nil value it overwrites the response text; a script error surfaces
// as a veto.
func afterModel(h Compiled) llmagent.AfterModelCallback {
	return func(cctx agent.CallbackContext, resp *model.LLMResponse, inErr error) (*model.LLMResponse, error) {
		env := map[string]any{
			"response": responseToAny(resp),
			"error":    errString(inErr),
		}
		out, err := h.Run(cctx, env)
		if err != nil {
			return nil, fmt.Errorf("after_model: %w", err)
		}
		if out == nil {
			return nil, nil
		}
		text, ok := textFromAny(out)
		if !ok {
			// Non-string, non-{text:...} values are ignored for now — keep
			// the response substitution narrow and well-typed.
			return nil, nil
		}
		return &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{Text: text}}}}, nil
	}
}

// beforeAgent runs a hook at the start of each agent invocation. A non-nil
// string/text return is wrapped as *genai.Content and short-circuits the
// agent (ADK treats it as the agent's final output); an error vetoes the
// invocation entirely.
func beforeAgent(h Compiled) agent.BeforeAgentCallback {
	return func(cctx agent.CallbackContext) (*genai.Content, error) {
		env := agentEnv(cctx)
		out, err := h.Run(cctx, env)
		if err != nil {
			return nil, fmt.Errorf("before_agent: %w", err)
		}
		return contentFromAny(out), nil
	}
}

// afterAgent runs a hook after an agent invocation completes. Symmetric to
// beforeAgent: a non-nil text return replaces the agent's final content.
func afterAgent(h Compiled) agent.AfterAgentCallback {
	return func(cctx agent.CallbackContext) (*genai.Content, error) {
		env := agentEnv(cctx)
		out, err := h.Run(cctx, env)
		if err != nil {
			return nil, fmt.Errorf("after_agent: %w", err)
		}
		return contentFromAny(out), nil
	}
}

func agentEnv(cctx agent.CallbackContext) map[string]any {
	state := map[string]any{}
	if s := cctx.ReadonlyState(); s != nil {
		for k, v := range s.All() {
			state[k] = v
		}
	}
	env := map[string]any{
		"agent_name":    cctx.AgentName(),
		"session_id":    cctx.SessionID(),
		"user_id":       cctx.UserID(),
		"session_state": state,
	}
	if uc := cctx.UserContent(); uc != nil {
		env["user_content"] = contentToAny(uc)
	}
	return env
}

// --- helpers ------------------------------------------------------------

// cloneArgs returns a non-nil shallow copy of m. When m is nil (e.g. ADK
// passes a nil `result` to after_tool after before_tool vetoed), we still
// yield an empty map so scripts can access `result.x` without tripping on
// `undefined` in goja. Hooks should check the `error` field first; this
// default just keeps them from crashing when they forget.
func cloneArgs(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func errString(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func contentsToAny(cs []*genai.Content) []any {
	out := make([]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, contentToAny(c))
	}
	return out
}

func contentToAny(c *genai.Content) map[string]any {
	if c == nil {
		return nil
	}
	parts := make([]any, 0, len(c.Parts))
	for _, p := range c.Parts {
		if p == nil {
			continue
		}
		parts = append(parts, map[string]any{"text": p.Text})
	}
	return map[string]any{"role": c.Role, "parts": parts}
}

func responseToAny(r *model.LLMResponse) map[string]any {
	if r == nil {
		return nil
	}
	out := map[string]any{}
	if r.Content != nil {
		out["content"] = contentToAny(r.Content)
		text := ""
		var textSb253 strings.Builder
		for _, p := range r.Content.Parts {
			if p != nil {
				textSb253.WriteString(p.Text)
			}
		}
		text += textSb253.String()
		out["text"] = text
	}
	return out
}

func textFromAny(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case map[string]any:
		if t, ok := x["text"].(string); ok {
			return t, true
		}
	}
	return "", false
}

func contentFromAny(v any) *genai.Content {
	text, ok := textFromAny(v)
	if !ok {
		return nil
	}
	return &genai.Content{Parts: []*genai.Part{{Text: text}}}
}
