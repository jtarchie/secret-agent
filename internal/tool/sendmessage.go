package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"mvdan.cc/sh/v3/interp"

	"github.com/jtarchie/secret-agent/internal/chat"
)

// SendBuiltinName is the shell builtin registered inside sh: bodies. A
// script invokes it as:
//
//	sa_send <transport> <to> <body>
//
// where <transport> is a key in the SenderRegistry ("signal"|"slack"|
// "imessage"), <to> is a transport-specific recipient identifier, and
// <body> is the message text (quote it to preserve whitespace).
const SendBuiltinName = "sa_send"

// SendMessageToolName is the canonical name of the framework-provided
// ADK tool that lets a bot's LLM dispatch an outbound message during a
// turn. Surfaced on every agent whose runtime has a non-empty
// SenderRegistry attached.
const SendMessageToolName = "send_message"

// SendBuiltinMiddleware returns a mvdan.cc/sh/v3/interp ExecHandlers
// middleware that intercepts `sa_send` invocations and routes them
// through the given SenderRegistry. Unrelated commands are passed through
// to the next handler unchanged. Returns an error for malformed or
// unroutable invocations so the shell reports a non-zero exit status.
func SendBuiltinMiddleware(reg chat.SenderRegistry) func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 || args[0] != SendBuiltinName {
				return next(ctx, args)
			}
			if len(args) != 4 {
				return fmt.Errorf("sa_send: expected 3 arguments (transport, to, body), got %d", len(args)-1)
			}
			err := DispatchSend(ctx, reg, args[1], args[2], args[3])
			if err != nil {
				return fmt.Errorf("sa_send: %w", err)
			}
			return nil
		}
	}
}

// DispatchSend looks up the named transport and forwards to its Sender.
// Shared by the ADK send_message tool, the sa_send shell builtin, and
// the expr/js bindings.
func DispatchSend(ctx context.Context, reg chat.SenderRegistry, transport, to, body string) error {
	if reg == nil {
		return errors.New("no transports configured for send")
	}
	if transport == "" {
		return errors.New("transport is required")
	}
	if to == "" {
		return errors.New("to is required")
	}
	sender, ok := reg[transport]
	if !ok {
		return fmt.Errorf("no sender registered for transport %q", transport)
	}
	err := sender.Send(ctx, to, body)
	if err != nil {
		return fmt.Errorf("%s send: %w", transport, err)
	}
	return nil
}

// sendMessageResult is the payload the send_message tool hands back to
// the LLM. Kept tiny so callers can't accidentally exfiltrate anything.
type sendMessageResult struct {
	OK bool `json:"ok"`
}

// NewSendMessageTool returns an ADK tool that lets a bot's LLM dispatch
// an outbound message via any transport in the registry. Callers must
// gate on len(reg) > 0 before invoking; the function panics on an empty
// registry to make the misuse loud in tests.
func NewSendMessageTool(reg chat.SenderRegistry) (adktool.Tool, error) {
	if len(reg) == 0 {
		return nil, errors.New("NewSendMessageTool: registry is empty")
	}
	knownTransports := make([]any, 0, len(reg))
	for k := range reg {
		knownTransports = append(knownTransports, k)
	}
	schema := &jsonschema.Schema{
		Type: "object",
		Properties: map[string]*jsonschema.Schema{
			"transport": {
				Type:        "string",
				Description: "which configured transport to send via",
				Enum:        knownTransports,
			},
			"to": {
				Type:        "string",
				Description: "recipient identifier. Signal: E.164 phone or group ID. Slack: U.../C.../D... ID. iMessage: E.164/email or chat GUID.",
			},
			"body": {
				Type:        "string",
				Description: "message text",
			},
		},
		Required: []string{"transport", "to", "body"},
	}
	t, err := functiontool.New(
		functiontool.Config{
			Name:        SendMessageToolName,
			Description: "Dispatch an unsolicited message to a specific recipient via a configured transport. Use when you need to notify a user who did not originate the current turn (e.g. a scheduled reminder dispatch).",
			InputSchema: schema,
		},
		func(ctx adktool.Context, args map[string]any) (sendMessageResult, error) {
			transport, _ := args["transport"].(string)
			to, _ := args["to"].(string)
			body, _ := args["body"].(string)
			err := DispatchSend(ctx, reg, transport, to, body)
			if err != nil {
				return sendMessageResult{}, err
			}
			return sendMessageResult{OK: true}, nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("new send_message tool: %w", err)
	}
	return t, nil
}

// MarshalSendResult returns a small JSON snippet for callers that need to
// stringify DispatchSend outcomes (e.g. expr/js runtimes).
func MarshalSendResult(err error) (string, error) {
	type result struct {
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	r := result{OK: err == nil}
	if err != nil {
		r.Error = err.Error()
	}
	b, mErr := json.Marshal(r)
	if mErr != nil {
		return "", fmt.Errorf("marshal send result: %w", mErr)
	}
	return string(b), nil
}
