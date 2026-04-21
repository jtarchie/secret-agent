package imessage

// Webhook event shapes for the BlueBubbles Server. The server POSTs JSON
// like `{type: "new-message", data: <Message>}` to whatever URL the user
// registers in the server UI. See:
// https://docs.bluebubbles.app/server/developer-guides/rest-api-and-webhooks

type webhookEvent struct {
	Type string         `json:"type"`
	Data newMessageData `json:"data"`
}

type newMessageData struct {
	GUID        string       `json:"guid"`
	Text        string       `json:"text"`
	IsFromMe    bool         `json:"isFromMe"`
	DateCreated int64        `json:"dateCreated"`
	Handle      *handle      `json:"handle"`
	Chats       []chatRef    `json:"chats"`
	Attachments []attachment `json:"attachments"`
}

type handle struct {
	Address string `json:"address"`
	Service string `json:"service"`
}

type chatRef struct {
	GUID         string        `json:"guid"`
	DisplayName  string        `json:"displayName"`
	Participants []participant `json:"participants"`
}

type participant struct {
	Address string `json:"address"`
	Service string `json:"service"`
}

type attachment struct {
	GUID         string `json:"guid"`
	MIMEType     string `json:"mimeType"`
	TransferName string `json:"transferName"`
}

// primaryChat returns chats[0] or an empty chatRef.
func (d newMessageData) primaryChat() chatRef {
	if len(d.Chats) == 0 {
		return chatRef{}
	}
	return d.Chats[0]
}

// isGroup reports whether this message arrived in a group chat. BlueBubbles
// doesn't put a clean DM-vs-group flag on the event, so we use the
// participant count as the heuristic (DM = 1 other participant; group > 1).
func (d newMessageData) isGroup() bool {
	return len(d.primaryChat().Participants) > 1
}

// senderAddress returns the address iMessage identifies the sender by
// (phone E.164 or email). Empty when the event carries no handle.
func (d newMessageData) senderAddress() string {
	if d.Handle == nil {
		return ""
	}
	return d.Handle.Address
}
