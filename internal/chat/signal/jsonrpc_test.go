package signal

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// A realistic `receive` notification captured from signal-cli (minimized).
const receiveNotif = `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"source":"+15551234567","sourceNumber":"+15551234567","sourceUuid":"abcd1234-ef56-7890-abcd-ef1234567890","sourceName":"Alice","timestamp":1700000000000,"dataMessage":{"timestamp":1700000000000,"message":"hello bot"}},"account":"+15557654321"}}` + "\n"

// A receive notification carrying an image attachment.
const receiveAttachNotif = `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"sourceUuid":"abcd1234-ef56-7890-abcd-ef1234567890","timestamp":1700000000000,"dataMessage":{"timestamp":1700000000000,"message":"look","attachments":[{"id":"I4vFnQf-_9E1tpkDLSQo","contentType":"image/jpeg","filename":"photo.jpg","size":12345}]}}}}` + "\n"

// A receive notification from a group — must be skipped by the transport.
const receiveGroupNotif = `{"jsonrpc":"2.0","method":"receive","params":{"envelope":{"sourceUuid":"abcd1234-ef56-7890-abcd-ef1234567890","timestamp":1700000000000,"dataMessage":{"message":"hi group","groupInfo":{"groupId":"gid==","type":"DELIVER"}}}}}` + "\n"

// A response to id=1 (the "send" call we issued).
const sendResponse = `{"jsonrpc":"2.0","id":1,"result":{"timestamp":1700000000001}}` + "\n"

// An error response for id=2.
const errorResponse = `{"jsonrpc":"2.0","id":2,"error":{"code":-1,"message":"unknown account"}}` + "\n"

// waitPending blocks until the client has n pending requests registered.
func waitPending(c *client, n int) {
	for {
		c.pendingM.Lock()
		have := len(c.pending)
		c.pendingM.Unlock()
		if have == n {
			return
		}
	}
}

func TestClientRoundtripCall(t *testing.T) {
	var reqBuf bytes.Buffer
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	c := newClient(&reqBuf)
	notifs := make(chan frame, 4)
	readDone := make(chan error, 1)
	go func() { readDone <- c.read(pr, notifs) }()
	go func() {
		for range notifs {
		}
	}()

	type callResult struct {
		raw json.RawMessage
		err error
	}
	resCh := make(chan callResult, 1)
	go func() {
		r, err := c.call("send", sendParams{
			Recipient: []string{"+15551234567"},
			Message:   "hi",
		})
		resCh <- callResult{raw: r, err: err}
	}()

	waitPending(c, 1)
	_, err := pw.Write([]byte(sendResponse))
	if err != nil {
		t.Fatalf("pipe write: %v", err)
	}

	got := <-resCh
	if got.err != nil {
		t.Fatalf("call: %v", got.err)
	}
	var decoded struct {
		Timestamp int64 `json:"timestamp"`
	}
	err = json.Unmarshal(got.raw, &decoded)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Timestamp != 1700000000001 {
		t.Fatalf("timestamp = %d, want 1700000000001", decoded.Timestamp)
	}

	// Verify we emitted a well-formed JSON-RPC request.
	var sent outRequest
	err = json.Unmarshal(bytes.TrimSpace(reqBuf.Bytes()), &sent)
	if err != nil {
		t.Fatalf("decode sent: %v; raw=%s", err, reqBuf.String())
	}
	if sent.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want 2.0", sent.JSONRPC)
	}
	if sent.Method != "send" {
		t.Errorf("method = %q, want send", sent.Method)
	}
	if sent.ID != 1 {
		t.Errorf("id = %d, want 1", sent.ID)
	}

	_ = pw.Close()
	err = <-readDone
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reader err: %v", err)
	}
}

func TestClientErrorResponse(t *testing.T) {
	var reqBuf bytes.Buffer
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()

	c := newClient(&reqBuf)
	notifs := make(chan frame, 4)
	go func() { _ = c.read(pr, notifs) }()
	go func() {
		for range notifs {
		}
	}()

	type callResult struct {
		err error
	}

	r1 := make(chan callResult, 1)
	go func() {
		_, err := c.call("send", sendParams{Message: "ok"})
		r1 <- callResult{err}
	}()
	waitPending(c, 1)
	_, _ = pw.Write([]byte(sendResponse))
	if got := <-r1; got.err != nil {
		t.Fatalf("call 1: %v", got.err)
	}

	r2 := make(chan callResult, 1)
	go func() {
		_, err := c.call("send", sendParams{Message: "bad"})
		r2 <- callResult{err}
	}()
	waitPending(c, 1)
	_, _ = pw.Write([]byte(errorResponse))
	got := <-r2
	if got.err == nil {
		t.Fatal("expected RPC error, got nil")
	}
	if !strings.Contains(got.err.Error(), "unknown account") {
		t.Errorf("err = %v; want contains 'unknown account'", got.err)
	}
}

func TestClientReceiveNotification(t *testing.T) {
	var reqBuf bytes.Buffer
	respReader := strings.NewReader(receiveNotif + receiveGroupNotif)

	c := newClient(&reqBuf)
	notifs := make(chan frame, 4)
	readDone := make(chan error, 1)
	go func() { readDone <- c.read(respReader, notifs) }()

	var dm *dataMessage
	var groupSeen bool
	for f := range notifs {
		if f.Method != "receive" {
			continue
		}
		var rp receiveParams
		err := json.Unmarshal(f.Params, &rp)
		if err != nil {
			t.Fatalf("decode params: %v", err)
		}
		if rp.Envelope.DataMessage == nil {
			continue
		}
		if rp.Envelope.DataMessage.GroupInfo != nil {
			groupSeen = true
			continue
		}
		dm = rp.Envelope.DataMessage
	}

	if dm == nil {
		t.Fatal("expected a DM-shaped dataMessage")
	}
	if dm.Message != "hello bot" {
		t.Errorf("message = %q, want %q", dm.Message, "hello bot")
	}
	if !groupSeen {
		t.Error("expected to observe the group notification too")
	}

	err := <-readDone
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reader err: %v", err)
	}
}

// waitForPending spins until c has exactly n pending responses registered.
// Used to serialize goroutine startup in TestClientConcurrentCalls so the
// IDs assigned by nextID.Add are deterministic.
func waitForPending(c *client, n int) {
	for {
		c.pendingM.Lock()
		got := len(c.pending)
		c.pendingM.Unlock()
		if got >= n {
			return
		}
	}
}

func TestClientConcurrentCalls(t *testing.T) {
	// Two calls sent concurrently should each get their matching response by id.
	// We synthesize responses in the *reverse* order they were issued to
	// prove id-routing works.

	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	defer func() { _ = pr.Close() }()

	var reqBuf bytes.Buffer
	c := newClient(&reqBuf)
	notifs := make(chan frame, 4)
	go func() { _ = c.read(pr, notifs) }()
	go func() {
		for range notifs {
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	var res1, res2 json.RawMessage
	var err1, err2 error

	// Start "a" and wait for it to register pending before starting "b" so
	// id-assignment order is deterministic: a gets id=1, b gets id=2.
	// Without this, the goroutine scheduler picks which goroutine calls
	// nextID.Add(1) first, and the response-by-id assertions below flip.
	go func() {
		defer wg.Done()
		res1, err1 = c.call("a", nil)
	}()
	waitForPending(c, 1)

	go func() {
		defer wg.Done()
		res2, err2 = c.call("b", nil)
	}()
	waitForPending(c, 2)

	// Respond id=2 first, then id=1.
	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"v":"second"}}` + "\n"))
	_, _ = pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"v":"first"}}` + "\n"))

	wg.Wait()

	if err1 != nil || err2 != nil {
		t.Fatalf("errs: %v %v", err1, err2)
	}
	if !bytes.Contains(res1, []byte(`"first"`)) {
		t.Errorf("res1 = %s", res1)
	}
	if !bytes.Contains(res2, []byte(`"second"`)) {
		t.Errorf("res2 = %s", res2)
	}
}

func TestReceiveAttachmentDecoding(t *testing.T) {
	var reqBuf bytes.Buffer
	respReader := strings.NewReader(receiveAttachNotif)

	c := newClient(&reqBuf)
	notifs := make(chan frame, 2)
	readDone := make(chan error, 1)
	go func() { readDone <- c.read(respReader, notifs) }()

	var dm *dataMessage
	for f := range notifs {
		if f.Method != "receive" {
			continue
		}
		var rp receiveParams
		err := json.Unmarshal(f.Params, &rp)
		if err != nil {
			t.Fatalf("decode params: %v", err)
		}
		dm = rp.Envelope.DataMessage
	}

	if dm == nil || len(dm.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %+v", dm)
	}
	a := dm.Attachments[0]
	if a.ID != "I4vFnQf-_9E1tpkDLSQo" {
		t.Errorf("id = %q", a.ID)
	}
	if a.ContentType != "image/jpeg" {
		t.Errorf("contentType = %q", a.ContentType)
	}
	if a.Filename != "photo.jpg" {
		t.Errorf("filename = %q", a.Filename)
	}

	tp := &Transport{stateDir: "/tmp/state"}
	atts := tp.attachmentsFor(dm.Attachments)
	if len(atts) != 1 {
		t.Fatalf("attachmentsFor len = %d", len(atts))
	}
	wantPath := "/tmp/state/attachments/I4vFnQf-_9E1tpkDLSQo"
	if atts[0].Path != wantPath {
		t.Errorf("path = %q, want %q", atts[0].Path, wantPath)
	}
	if atts[0].Filename != "photo.jpg" {
		t.Errorf("filename = %q", atts[0].Filename)
	}
	if atts[0].ContentType != "image/jpeg" {
		t.Errorf("contentType = %q", atts[0].ContentType)
	}

	err := <-readDone
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reader err: %v", err)
	}
}

func TestEnvelopePeerID(t *testing.T) {
	cases := []struct {
		env    envelope
		peerID string
		rcp    string
	}{
		{
			env:    envelope{SourceUuid: "uuid", SourceNumber: "+1"},
			peerID: "uuid",
			rcp:    "+1",
		},
		{
			env:    envelope{SourceNumber: "+1"},
			peerID: "+1",
			rcp:    "+1",
		},
		{
			env:    envelope{Source: "+1"},
			peerID: "+1",
			rcp:    "+1",
		},
	}
	for i, tc := range cases {
		if got := tc.env.peerID(); got != tc.peerID {
			t.Errorf("case %d peerID = %q, want %q", i, got, tc.peerID)
		}
		if got := tc.env.peerRecipient(); got != tc.rcp {
			t.Errorf("case %d peerRecipient = %q, want %q", i, got, tc.rcp)
		}
	}
}
