// Package router selects among multiple bots for each incoming chat message.
// A Router owns the per-bot trigger matcher, per-conv prior-message buffer,
// and per-bot attachment policy. Transports build a chat.Envelope with
// sender metadata and call Router.Dispatch to obtain a reply stream.
package router

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/jtarchie/secret-agent/internal/bot"
	"github.com/jtarchie/secret-agent/internal/chat"
	"github.com/jtarchie/secret-agent/internal/tool"
)

// Handler is the backend channel the router delegates to once a Route is
// selected. In practice it is Runtime.HandlerFor(convID).
type Handler = func(ctx context.Context, msg chat.Message) <-chan chat.Chunk

// HandlerFactory binds a conversation ID to a Handler.
type HandlerFactory = func(convID string) Handler

// Route pairs a bot's declaration with its runtime handler factory and the
// compiled scope/trigger filters derived from the bot YAML.
type Route struct {
	Bot           *bot.Bot
	Handler       HandlerFactory
	matcher       *triggerMatcher
	users         map[string]struct{}
	groups        map[string]struct{}
	slackUsers    map[string]struct{}
	slackChannels map[string]struct{}
	imessageUsers map[string]struct{}
	imessageChats map[string]struct{}
	attachmentsOK bool
	bufferingOn   bool
}

// RouteFromBot builds a Route from a parsed bot definition and its handler
// factory. Validation happens in Router.New — this just compiles the filters.
func RouteFromBot(b *bot.Bot, h HandlerFactory) (Route, error) {
	m, err := newTriggerMatcher(b.Triggers)
	if err != nil {
		return Route{}, fmt.Errorf("bot %q: %w", b.Name, err)
	}

	users := make(map[string]struct{}, len(b.Users))
	for _, u := range b.Users {
		users[u] = struct{}{}
	}
	groups := make(map[string]struct{}, len(b.Groups))
	for _, g := range b.Groups {
		groups[g] = struct{}{}
	}
	slackUsers := make(map[string]struct{}, len(b.SlackUsers))
	for _, u := range b.SlackUsers {
		slackUsers[u] = struct{}{}
	}
	slackChannels := make(map[string]struct{}, len(b.SlackChannels))
	for _, c := range b.SlackChannels {
		slackChannels[c] = struct{}{}
	}
	imessageUsers := make(map[string]struct{}, len(b.IMessageUsers))
	for _, u := range b.IMessageUsers {
		imessageUsers[u] = struct{}{}
	}
	imessageChats := make(map[string]struct{}, len(b.IMessageChats))
	for _, c := range b.IMessageChats {
		imessageChats[c] = struct{}{}
	}

	return Route{
		Bot:           b,
		Handler:       h,
		matcher:       m,
		users:         users,
		groups:        groups,
		slackUsers:    slackUsers,
		slackChannels: slackChannels,
		imessageUsers: imessageUsers,
		imessageChats: imessageChats,
		attachmentsOK: b.Permissions.AttachmentsAllowed(),
		bufferingOn:   b.Permissions.MemoryOrDefault() == bot.MemoryFull,
	}, nil
}

// Router is a chat.Dispatcher that selects one Route per message based on
// sender scope and trigger match.
type Router struct {
	routes  []Route
	buffers sync.Map // convID → *peerBuffer (shared across bots in the conv)
	logger  *slog.Logger
}

// Option customizes Router construction.
type Option func(*Router)

// WithLogger sets the slog.Logger used for routing decisions.
func WithLogger(l *slog.Logger) Option { return func(r *Router) { r.logger = l } }

// New validates that the supplied routes have globally disjoint triggers
// (case-insensitive) and returns a Router. In multi-route mode every route
// must declare at least one trigger; a single-route router behaves as
// before (triggers optional).
func New(routes []Route, opts ...Option) (*Router, error) {
	if len(routes) == 0 {
		return nil, errors.New("router: at least one route is required")
	}

	if len(routes) > 1 {
		var missing []string
		for _, r := range routes {
			if len(r.Bot.Triggers) == 0 {
				missing = append(missing, r.Bot.Name)
			}
		}
		if len(missing) > 0 {
			return nil, fmt.Errorf("router: in multi-bot mode every bot must declare at least one trigger; missing on: %s", strings.Join(missing, ", "))
		}

		triggerOwners := map[string][]string{}
		for _, r := range routes {
			for _, t := range r.Bot.Triggers {
				key := strings.ToLower(strings.TrimSpace(t))
				triggerOwners[key] = append(triggerOwners[key], r.Bot.Name)
			}
		}
		var conflicts []string
		for trigger, owners := range triggerOwners {
			if len(owners) > 1 {
				sort.Strings(owners)
				conflicts = append(conflicts, fmt.Sprintf("%q declared by bots [%s]", trigger, strings.Join(owners, " ")))
			}
		}
		if len(conflicts) > 0 {
			sort.Strings(conflicts)
			return nil, fmt.Errorf("router: trigger conflicts: %s", strings.Join(conflicts, "; "))
		}
	}

	rtr := &Router{routes: routes}
	for _, opt := range opts {
		opt(rtr)
	}
	if rtr.logger == nil {
		rtr.logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return rtr, nil
}

// Routes returns the routes in declaration order. Intended for callers
// that need to iterate bots (e.g. MCP preflight).
func (r *Router) Routes() []Route { return r.routes }

// closedChan returns a pre-closed reply channel used when a message is
// dropped or buffered (no bot reply is produced).
func closedChan() <-chan chat.Chunk {
	ch := make(chan chat.Chunk)
	close(ch)
	return ch
}

// scopeMatches reports whether a route covers this envelope's sender and
// group context. Empty user/group scope means "all".
//
// Which scope lists are consulted depends on env.Transport:
//
//	"signal" (or "") → users + groups, keyed on SenderPhone / GroupID
//	"slack"          → slackUsers + slackChannels, keyed on SenderID / GroupID
//	"imessage"       → imessageUsers + imessageChats, keyed on SenderID / GroupID
//	"cli"            → always matches (CLI has no identity scoping)
func (rt *Route) scopeMatches(env chat.Envelope) bool {
	if env.Transport == "cli" {
		return true
	}

	users, groups, senderKey := rt.users, rt.groups, env.SenderPhone
	switch env.Transport {
	case "slack":
		users, groups, senderKey = rt.slackUsers, rt.slackChannels, env.SenderID
	case "imessage":
		users, groups, senderKey = rt.imessageUsers, rt.imessageChats, env.SenderID
	}

	if env.Kind == "group" {
		if len(groups) > 0 {
			if _, ok := groups[env.GroupID]; !ok {
				return false
			}
		}
		if len(users) > 0 && !senderInSet(senderKey, users) {
			return false
		}
		return true
	}

	if len(users) > 0 && !senderInSet(senderKey, users) {
		return false
	}
	return true
}

func senderInSet(senderKey string, users map[string]struct{}) bool {
	if senderKey == "" {
		return false
	}
	_, ok := users[senderKey]
	return ok
}

// Dispatch selects the single Route whose scope covers the envelope and
// whose trigger matches msg.Text, then delegates to that route's handler.
// If no scoped route triggers, the message is buffered (when any scoped
// route has buffering enabled) and a closed channel is returned.
func (r *Router) Dispatch(ctx context.Context, env chat.Envelope, msg chat.Message) <-chan chat.Chunk {
	scoped := r.scopedRoutes(env)
	if len(scoped) == 0 {
		r.logger.Debug("route: no scope match — dropping",
			"conv", env.ConvID, "kind", env.Kind,
			"sender", env.SenderPhone, "group", env.GroupID,
		)
		return closedChan()
	}

	selected := selectTriggered(scoped, msg.Text)
	if selected == nil {
		r.handleNoTrigger(scoped, env, msg)
		return closedChan()
	}

	text := r.flushBuffered(selected, env, msg.Text)
	atts := r.filterAttachments(selected, env, msg.Attachments)

	r.logger.Info("route: dispatching",
		"conv", env.ConvID, "kind", env.Kind, "bot", selected.Bot.Name,
		"bytes", len(text), "attachments", len(atts),
	)

	handler := selected.Handler(env.ConvID)
	return handler(tool.WithEnvelope(ctx, env), chat.Message{Text: text, Attachments: atts})
}

// scopedRoutes returns the routes whose user/group scope covers env.
func (r *Router) scopedRoutes(env chat.Envelope) []*Route {
	out := make([]*Route, 0, len(r.routes))
	for i := range r.routes {
		if r.routes[i].scopeMatches(env) {
			out = append(out, &r.routes[i])
		}
	}
	return out
}

// selectTriggered returns the first scoped route whose trigger matches
// text. A route with no triggers (single-bot mode) always matches.
// Disjointness across bots guarantees at most one match in multi-bot mode.
func selectTriggered(scoped []*Route, text string) *Route {
	for _, rt := range scoped {
		if rt.matcher == nil || len(rt.matcher.res) == 0 {
			return rt
		}
		if rt.matcher.Matches(text) {
			return rt
		}
	}
	return nil
}

// handleNoTrigger logs/buffers a message that didn't match any trigger.
// Group messages are silent by design; DMs are buffered when any scoped
// route opts in, otherwise dropped.
func (r *Router) handleNoTrigger(scoped []*Route, env chat.Envelope, msg chat.Message) {
	if env.Kind == "group" {
		r.logger.Debug("route: group message with no trigger — silent",
			"conv", env.ConvID, "group", env.GroupID,
		)
		return
	}
	bufferingOn := false
	for _, rt := range scoped {
		if rt.bufferingOn {
			bufferingOn = true
			break
		}
	}
	if !bufferingOn {
		r.logger.Debug("route: untriggered message dropped (buffering off)",
			"conv", env.ConvID, "kind", env.Kind,
		)
		return
	}
	r.bufferFor(env.ConvID).Append(msg.Text)
	r.logger.Info("route: buffered untriggered message",
		"conv", env.ConvID, "kind", env.Kind,
		"bytes", len(msg.Text),
	)
}

// flushBuffered prepends any prior-buffered messages for this conversation
// onto the current turn's text. No-op for group messages or when the
// selected route does not opt into buffering.
func (r *Router) flushBuffered(selected *Route, env chat.Envelope, text string) string {
	if !selected.bufferingOn || env.Kind == "group" {
		return text
	}
	prior := r.bufferFor(env.ConvID).Drain()
	if len(prior) == 0 {
		return text
	}
	r.logger.Info("route: flushing buffered prior messages into turn",
		"conv", env.ConvID, "bot", selected.Bot.Name, "prior_count", len(prior),
	)
	return wrapWithPrior(prior, text)
}

// filterAttachments enforces the selected route's per-bot attachment
// policy by stripping the attachment slice when the bot disallows them.
// Note: the underlying files have already been downloaded to disk; this
// only removes the reference for the runtime turn.
func (r *Router) filterAttachments(selected *Route, env chat.Envelope, atts []chat.Attachment) []chat.Attachment {
	if selected.attachmentsOK || len(atts) == 0 {
		return atts
	}
	r.logger.Debug("route: stripping attachments per bot permissions",
		"conv", env.ConvID, "bot", selected.Bot.Name, "count", len(atts),
	)
	return nil
}

func (r *Router) bufferFor(convID string) *peerBuffer {
	if v, ok := r.buffers.Load(convID); ok {
		return v.(*peerBuffer)
	}
	v, _ := r.buffers.LoadOrStore(convID, &peerBuffer{})
	return v.(*peerBuffer)
}
