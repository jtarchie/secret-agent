package slack

import (
	"strings"

	"github.com/jtarchie/secret-agent/internal/chat"
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
