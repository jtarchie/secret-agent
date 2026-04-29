package slack

import (
	"strings"
	"sync"
	"time"

	"github.com/jtarchie/secret-agent/internal/chat"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// shouldDispatch reports whether a MessageEvent should flow to the dispatcher.
// botID is the authenticated bot's ID (from auth.test) used to filter our own
// echoes. Returning false with a reason lets the caller log the drop.
func shouldDispatch(ev *slackevents.MessageEvent, botID string) (bool, string) {
	if ev == nil {
		return false, "nil message event"
	}
	// Our own outgoing messages show up as inbound events — drop them.
	if botID != "" && ev.BotID == botID {
		return false, "own bot echo"
	}
	// Any bot message (including our own and other apps') is dropped to
	// avoid reply loops between bots in the same workspace.
	if ev.BotID != "" {
		return false, "bot message"
	}
	// Subtypes we never want to act on: message_changed, message_deleted,
	// bot_message, channel_join, channel_leave, etc. file_share is the one
	// subtype we keep since it's a user uploading a file with optional text.
	switch ev.SubType {
	case "", "file_share":
		// keep
	default:
		return false, "subtype: " + ev.SubType
	}
	if ev.User == "" {
		return false, "missing user"
	}
	// Require something we can forward: text or at least one file.
	if strings.TrimSpace(ev.Text) == "" && !hasFiles(ev) {
		return false, "empty message"
	}
	return true, ""
}

// kindFor translates Slack's ChannelType to the Envelope.Kind vocabulary
// used by the router and bot tools.
func kindFor(channelType string) string {
	if channelType == slackevents.ChannelTypeIM {
		return "dm"
	}
	return "group"
}

// convIDFor builds a conversation key unique per Slack thread so each
// thread gets its own memory/buffer. Root-level messages key on the Ts.
func convIDFor(channel, ts, threadTS string) string {
	key := threadTS
	if key == "" {
		key = ts
	}
	return "slack:" + channel + ":" + key
}

// replyTS picks the thread_ts to use when posting a reply so the bot
// answers in the same thread as the prompt (or starts a thread from the
// prompt's own ts when the message wasn't already threaded).
func replyTS(ts, threadTS string) string {
	if threadTS != "" {
		return threadTS
	}
	return ts
}

// hasFiles reports whether the event carries file uploads.
// slack-go decodes the underlying Msg into ev.Message for every inbound
// MessageEvent (see its UnmarshalJSON), so Files is accessible there.
func hasFiles(ev *slackevents.MessageEvent) bool {
	if ev.Message == nil {
		return false
	}
	return len(ev.Message.Files) > 0
}

// Attachment is a minimal view of a Slack file: only the fields the
// transport needs to download it and pass it into chat.Message.Attachments.
type fileRef struct {
	ID          string
	Name        string
	ContentType string
	DownloadURL string
}

// filesFor returns the files attached to a message event, flattened to
// the minimal representation the transport needs for download.
func filesFor(ev *slackevents.MessageEvent) []fileRef {
	if !hasFiles(ev) {
		return nil
	}
	out := make([]fileRef, 0, len(ev.Message.Files))
	for _, f := range ev.Message.Files {
		url := f.URLPrivateDownload
		if url == "" {
			url = f.URLPrivate
		}
		if url == "" {
			continue
		}
		name := f.Name
		if name == "" {
			name = f.ID
		}
		out = append(out, fileRef{
			ID:          f.ID,
			Name:        name,
			ContentType: f.Mimetype,
			DownloadURL: url,
		})
	}
	return out
}

// buildEnvelope turns a filtered MessageEvent into the chat.Envelope the
// router expects. Pure so it's trivially testable.
func buildEnvelope(ev *slackevents.MessageEvent) chat.Envelope {
	return chat.Envelope{
		ConvID:    convIDFor(ev.Channel, ev.TimeStamp, ev.ThreadTimeStamp),
		Kind:      kindFor(ev.ChannelType),
		Transport: "slack",
		SenderID:  ev.User,
		GroupID:   slackGroupID(ev),
	}
}

// slackGroupID is the Channel ID for non-DM conversations. DMs leave
// GroupID empty, matching the Signal transport's convention.
func slackGroupID(ev *slackevents.MessageEvent) string {
	if ev.ChannelType == slackevents.ChannelTypeIM {
		return ""
	}
	return ev.Channel
}

// channelTypeForID infers a Slack ChannelType from the channel ID prefix.
// AppMentionEvent doesn't include channel_type, so we have to guess.
// Slack ID prefixes: C=public channel, G=private channel, D=DM, M=mpim.
func channelTypeForID(channel string) string {
	switch {
	case strings.HasPrefix(channel, "D"):
		return slackevents.ChannelTypeIM
	case strings.HasPrefix(channel, "G"):
		return slackevents.ChannelTypeGroup
	case strings.HasPrefix(channel, "M"):
		return slackevents.ChannelTypeMPIM
	default:
		return slackevents.ChannelTypeChannel
	}
}

// messageFromAppMention adapts an AppMentionEvent into the MessageEvent
// shape so the rest of the pipeline (shouldDispatch, buildEnvelope,
// handleMessage) can process it uniformly. AppMentionEvent fires even when
// the bot isn't a channel member, so it's the only path for @-mentions in
// channels the bot was never invited to.
func messageFromAppMention(ev *slackevents.AppMentionEvent) *slackevents.MessageEvent {
	msg := &slackevents.MessageEvent{
		Type:            "message",
		User:            ev.User,
		Text:            ev.Text,
		TimeStamp:       ev.TimeStamp,
		ThreadTimeStamp: ev.ThreadTimeStamp,
		Channel:         ev.Channel,
		ChannelType:     channelTypeForID(ev.Channel),
		BotID:           ev.BotID,
	}
	// hasFiles/filesFor read from ev.Message.Files; mirror the slack-go
	// MessageEvent shape so file uploads still work for app_mentions.
	msg.Message = &slackgo.Msg{Text: ev.Text, User: ev.User, Files: ev.Files}
	return msg
}

// formatThreadHistory turns Slack thread replies into a <thread_history>
// block prepended to the current message so the model has context for
// @-mentions in threads it wasn't previously part of. The current message
// (currentTS) is excluded so it isn't duplicated. Bot-authored messages —
// matched by BotID or by botUserID on the User field — are labeled
// "assistant" so the model can distinguish its prior turns from human ones.
// Returns "" if no usable prior messages remain.
func formatThreadHistory(msgs []slackgo.Message, currentTS, botUserID string) string {
	var sb strings.Builder
	wrote := false
	for _, m := range msgs {
		if m.Timestamp == currentTS {
			continue
		}
		// Skip edits/deletions/joins/leaves; keep plain text and bot_message.
		switch m.SubType {
		case "", "bot_message", "thread_broadcast", "file_share":
			// keep
		default:
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		speaker := m.User
		if m.BotID != "" || (botUserID != "" && m.User == botUserID) {
			speaker = "assistant"
		}
		if speaker == "" {
			speaker = "unknown"
		}
		if !wrote {
			sb.WriteString("<thread_history>\n")
			wrote = true
		}
		sb.WriteString(speaker)
		sb.WriteString(": ")
		sb.WriteString(text)
		sb.WriteString("\n")
	}
	if !wrote {
		return ""
	}
	sb.WriteString("</thread_history>\n\n")
	return sb.String()
}

// eventCache deduplicates events keyed by channel+timestamp. Slack
// delivers both a MessageEvent and an AppMentionEvent for the same
// physical @-mention when the bot is in a channel and subscribed to
// message.channels — without dedup the bot would respond twice.
type eventCache struct {
	mu    sync.Mutex
	items map[string]time.Time
	ttl   time.Duration
}

func newEventCache(ttl time.Duration) *eventCache {
	return &eventCache{items: map[string]time.Time{}, ttl: ttl}
}

// seen returns true if key was recorded within the TTL. It records the
// key as a side effect, so the second call within the window returns true.
func (c *eventCache) seen(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for k, t := range c.items {
		if now.Sub(t) > c.ttl {
			delete(c.items, k)
		}
	}
	_, ok := c.items[key]
	c.items[key] = now
	return ok
}
