package signal

import (
	"log/slog"
	"testing"
	"time"
)

func nullLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestClassify_PlainDM(t *testing.T) {
	tr := &Transport{account: "+15550000000"}
	env := envelope{
		SourceUuid:   "peer-uuid",
		SourceNumber: "+15551111111",
		DataMessage:  &dataMessage{Message: "hi"},
	}
	conv, ok := tr.classify(env, env.DataMessage, nullLogger(), "hi")
	if !ok {
		t.Fatal("expected classify to accept a plain DM")
	}
	if conv.kind != "dm" || conv.key != "peer-uuid" || conv.recipient != "+15551111111" {
		t.Errorf("unexpected conv: %+v", conv)
	}
	if conv.groupID != "" {
		t.Errorf("DM should not have groupID: %q", conv.groupID)
	}
}

func TestClassify_Group(t *testing.T) {
	tr := &Transport{account: "+15550000000"}
	env := envelope{
		SourceUuid: "peer-uuid",
		DataMessage: &dataMessage{
			Message:   "hi folks",
			GroupInfo: &groupInfo{GroupID: "GZ==", Type: "DELIVER"},
		},
	}
	conv, ok := tr.classify(env, env.DataMessage, nullLogger(), "hi folks")
	if !ok {
		t.Fatal("expected group message to classify")
	}
	if conv.kind != "group" || conv.groupID != "GZ==" || conv.key != "group:GZ==" {
		t.Errorf("unexpected conv: %+v", conv)
	}
	if conv.recipient != "" {
		t.Errorf("group conv should not have recipient: %q", conv.recipient)
	}
}

func TestClassify_NoteToSelf(t *testing.T) {
	account := "+15550000000"
	tr := &Transport{account: account}
	sent := &dataMessage{
		Message:           "note to self",
		DestinationNumber: account,
	}
	env := envelope{
		SourceUuid:   "self-uuid",
		SourceNumber: account,
		SyncMessage:  &syncMessage{SentMessage: sent},
	}
	conv, ok := tr.classify(env, sent, nullLogger(), "note to self")
	if !ok {
		t.Fatal("expected Note-to-Self to classify")
	}
	if conv.kind != "self" || conv.key != "self:"+account || conv.recipient != account {
		t.Errorf("unexpected conv: %+v", conv)
	}
}

func TestClassify_SyncToExternalDropped(t *testing.T) {
	tr := &Transport{account: "+15550000000"}
	sent := &dataMessage{
		Message:           "hi friend",
		DestinationNumber: "+15559999999",
	}
	env := envelope{
		SourceNumber: tr.account,
		SyncMessage:  &syncMessage{SentMessage: sent},
	}
	_, ok := tr.classify(env, sent, nullLogger(), "hi friend")
	if ok {
		t.Fatal("expected sync-to-external to be dropped")
	}
}

func TestClassify_OwnEchoDropped(t *testing.T) {
	account := "+15550000000"
	tr := &Transport{account: account}
	body := "reply from bot"
	tr.rememberOutbound(body)

	sent := &dataMessage{
		Message:           body,
		DestinationNumber: account,
	}
	env := envelope{
		SourceNumber: account,
		SyncMessage:  &syncMessage{SentMessage: sent},
	}
	_, ok := tr.classify(env, sent, nullLogger(), body)
	if ok {
		t.Fatal("expected own-echo to be dropped")
	}
	// Subsequent identical message — from the user, after TTL map entry
	// was consumed — should now classify as Note-to-Self.
	conv, ok := tr.classify(env, sent, nullLogger(), body)
	if !ok {
		t.Fatal("second identical message should classify once echo is consumed")
	}
	if conv.kind != "self" {
		t.Errorf("expected self, got %q", conv.kind)
	}
}

func TestIsOwnEcho_TTL(t *testing.T) {
	tr := &Transport{account: "+15550000000"}
	tr.outbound.Store("stale", time.Now().Add(-2*outboundEchoTTL))
	if tr.isOwnEcho("stale") {
		t.Error("expected stale entry to be treated as non-echo")
	}
	if _, present := tr.outbound.Load("stale"); present {
		t.Error("stale entry should have been deleted")
	}
}

func TestEffectiveDataMessage(t *testing.T) {
	dm := &dataMessage{Message: "a"}
	sent := &dataMessage{Message: "b"}
	cases := []struct {
		name string
		env  envelope
		want *dataMessage
	}{
		{"direct", envelope{DataMessage: dm}, dm},
		{"sync sent", envelope{SyncMessage: &syncMessage{SentMessage: sent}}, sent},
		{"neither", envelope{}, nil},
	}
	for _, tc := range cases {
		if got := tc.env.effectiveDataMessage(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDestinationMatches(t *testing.T) {
	acct := "+15550000000"
	cases := []struct {
		name string
		dm   *dataMessage
		want bool
	}{
		{"nil", nil, false},
		{"by number", &dataMessage{DestinationNumber: acct}, true},
		{"by destination", &dataMessage{Destination: acct}, true},
		{"by uuid match vs account", &dataMessage{DestinationUuid: acct}, true},
		{"mismatch", &dataMessage{DestinationNumber: "+1someone-else"}, false},
		{"empty", &dataMessage{}, false},
	}
	for _, tc := range cases {
		if got := tc.dm.destinationMatches(acct); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
