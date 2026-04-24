package slack

import (
	"testing"
	"time"

	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

func TestKindFor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{slackevents.ChannelTypeIM, "dm"},
		{slackevents.ChannelTypeMPIM, "group"},
		{slackevents.ChannelTypeChannel, "group"},
		{slackevents.ChannelTypeGroup, "group"},
		{"", "group"},
	}
	for _, c := range cases {
		if got := kindFor(c.in); got != c.want {
			t.Errorf("kindFor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestConvIDForThreadOverridesTs(t *testing.T) {
	if got := convIDFor("C1", "111.222", ""); got != "slack:C1:111.222" {
		t.Errorf("root message: got %q", got)
	}
	if got := convIDFor("C1", "111.222", "100.000"); got != "slack:C1:100.000" {
		t.Errorf("threaded: got %q", got)
	}
}

func TestReplyTSPrefersThread(t *testing.T) {
	if got := replyTS("111.222", ""); got != "111.222" {
		t.Errorf("root reply: got %q", got)
	}
	if got := replyTS("111.222", "100.000"); got != "100.000" {
		t.Errorf("threaded reply: got %q", got)
	}
}

func TestShouldDispatchFilters(t *testing.T) {
	cases := []struct {
		name  string
		ev    *slackevents.MessageEvent
		botID string
		want  bool
	}{
		{
			name: "plain dm text",
			ev:   newMsg("U1", "hello", "", "C1", slackevents.ChannelTypeIM, ""),
			want: true,
		},
		{
			name: "channel message with trigger",
			ev:   newMsg("U1", "@bot help", "", "C1", slackevents.ChannelTypeChannel, ""),
			want: true,
		},
		{
			name:  "own bot echo",
			ev:    withBot(newMsg("", "reply", "", "C1", slackevents.ChannelTypeIM, ""), "B123"),
			botID: "B123",
			want:  false,
		},
		{
			name: "other bot message",
			ev:   withBot(newMsg("", "reply", "", "C1", slackevents.ChannelTypeIM, ""), "B999"),
			want: false,
		},
		{
			name: "message_changed is ignored",
			ev:   withSubtype(newMsg("U1", "edited", "", "C1", slackevents.ChannelTypeIM, ""), "message_changed"),
			want: false,
		},
		{
			name: "message_deleted is ignored",
			ev:   withSubtype(newMsg("U1", "", "", "C1", slackevents.ChannelTypeIM, ""), "message_deleted"),
			want: false,
		},
		{
			name: "missing user is ignored",
			ev:   newMsg("", "hi", "", "C1", slackevents.ChannelTypeIM, ""),
			want: false,
		},
		{
			name: "empty text with no files is ignored",
			ev:   newMsg("U1", "", "", "C1", slackevents.ChannelTypeIM, ""),
			want: false,
		},
		{
			name: "file_share subtype is kept",
			ev:   withSubtypeAndFile(newMsg("U1", "here", "", "C1", slackevents.ChannelTypeIM, ""), "file_share"),
			want: true,
		},
	}
	for _, c := range cases {
		got, reason := shouldDispatch(c.ev, c.botID)
		if got != c.want {
			t.Errorf("%s: got %v (reason=%q), want %v", c.name, got, reason, c.want)
		}
	}
}

func TestBuildEnvelopeDMLeavesGroupEmpty(t *testing.T) {
	ev := newMsg("U1", "hi", "", "D1", slackevents.ChannelTypeIM, "")
	env := buildEnvelope(ev)
	if env.Transport != "slack" || env.Kind != "dm" || env.SenderID != "U1" || env.GroupID != "" {
		t.Errorf("unexpected dm envelope: %+v", env)
	}
	if env.ConvID != "slack:D1:" {
		t.Errorf("convID = %q (empty Ts expected since test fixture didn't set one)", env.ConvID)
	}
}

func TestBuildEnvelopeChannelSetsGroupID(t *testing.T) {
	// Threaded reply: ts=reply-time, threadTS=parent thread anchor.
	ev := newMsg("U1", "hi @bot", "999.000", "C1", slackevents.ChannelTypeChannel, "1000.001")
	env := buildEnvelope(ev)
	if env.Kind != "group" || env.GroupID != "C1" || env.SenderID != "U1" {
		t.Errorf("unexpected channel envelope: %+v", env)
	}
	if env.ConvID != "slack:C1:999.000" {
		t.Errorf("convID = %q, want threaded parent", env.ConvID)
	}
}

// newMsg builds a MessageEvent shaped like the ones slackevents decodes
// from live Slack JSON. Files are attached on the nested Message, matching
// the library's UnmarshalJSON behavior.
func newMsg(user, text, threadTS, channel, kind, ts string) *slackevents.MessageEvent {
	ev := &slackevents.MessageEvent{
		Type:            "message",
		User:            user,
		Text:            text,
		ThreadTimeStamp: threadTS,
		TimeStamp:       ts,
		Channel:         channel,
		ChannelType:     kind,
	}
	ev.Message = &slackgo.Msg{Text: text, User: user}
	return ev
}

func withBot(ev *slackevents.MessageEvent, botID string) *slackevents.MessageEvent {
	ev.BotID = botID
	return ev
}

func withSubtype(ev *slackevents.MessageEvent, sub string) *slackevents.MessageEvent {
	ev.SubType = sub
	return ev
}

func withSubtypeAndFile(ev *slackevents.MessageEvent, sub string) *slackevents.MessageEvent {
	ev.SubType = sub
	ev.Message.Files = []slackgo.File{{ID: "F1", Name: "photo.png", URLPrivateDownload: "https://slack/files/F1/download"}}
	return ev
}

func TestChannelTypeForID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"C123", slackevents.ChannelTypeChannel},
		{"G123", slackevents.ChannelTypeGroup},
		{"D123", slackevents.ChannelTypeIM},
		{"M123", slackevents.ChannelTypeMPIM},
		{"", slackevents.ChannelTypeChannel},
		{"unknown", slackevents.ChannelTypeChannel},
	}
	for _, c := range cases {
		if got := channelTypeForID(c.in); got != c.want {
			t.Errorf("channelTypeForID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMessageFromAppMentionPreservesFields(t *testing.T) {
	mention := &slackevents.AppMentionEvent{
		User:            "U1",
		Text:            "<@UBOT> hello",
		TimeStamp:       "111.222",
		ThreadTimeStamp: "100.000",
		Channel:         "C123",
		BotID:           "",
		Files:           []slackgo.File{{ID: "F1", Name: "n.png"}},
	}
	msg := messageFromAppMention(mention)
	if msg.User != "U1" || msg.Text != "<@UBOT> hello" {
		t.Errorf("user/text not preserved: %+v", msg)
	}
	if msg.TimeStamp != "111.222" || msg.ThreadTimeStamp != "100.000" {
		t.Errorf("timestamps not preserved: %+v", msg)
	}
	if msg.Channel != "C123" || msg.ChannelType != slackevents.ChannelTypeChannel {
		t.Errorf("channel/type wrong: %+v", msg)
	}
	if msg.Message == nil || len(msg.Message.Files) != 1 || msg.Message.Files[0].ID != "F1" {
		t.Errorf("files not threaded onto Message: %+v", msg.Message)
	}
}

func TestMessageFromAppMentionInfersDMChannelType(t *testing.T) {
	mention := &slackevents.AppMentionEvent{User: "U1", Text: "hi", Channel: "D123"}
	msg := messageFromAppMention(mention)
	if msg.ChannelType != slackevents.ChannelTypeIM {
		t.Errorf("expected IM channel type for D-prefix channel, got %q", msg.ChannelType)
	}
	if got := buildEnvelope(msg); got.Kind != "dm" || got.GroupID != "" {
		t.Errorf("DM envelope wrong: %+v", got)
	}
}

func TestEventCacheDedups(t *testing.T) {
	c := newEventCache(time.Minute)
	if c.seen("C1:111.222") {
		t.Error("first call should not be marked seen")
	}
	if !c.seen("C1:111.222") {
		t.Error("second call with same key should be marked seen")
	}
	if c.seen("C1:333.444") {
		t.Error("different key should not be marked seen")
	}
}

func TestEventCacheExpiresOldEntries(t *testing.T) {
	c := newEventCache(time.Millisecond)
	c.seen("C1:111.222")
	time.Sleep(5 * time.Millisecond)
	if c.seen("C1:111.222") {
		t.Error("entry past TTL should not be marked seen")
	}
}
