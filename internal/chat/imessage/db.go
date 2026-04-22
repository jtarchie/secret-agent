package imessage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
)

// pollQuery returns every message whose ROWID exceeds the cursor, joined
// with the sender handle and the primary chat. The ordering by ROWID ASC
// makes the cursor update monotonic: the max ROWID we see is the new
// cursor value.
//
// Notes on the schema (~/Library/Messages/chat.db):
//   - message.text may be NULL on recent macOS; the real payload then lives
//     in message.attributedBody as a typedstream blob. We return text only
//     here and document the limitation; messages authored by Messages.app
//     (copy/paste rich text etc.) may appear as empty.
//   - message.is_from_me = 1 marks outbound messages; we skip these for
//     echo suppression.
//   - chat.style: 43 = group, 45 = DM. We also count participants as a
//     secondary DM-vs-group signal.
const pollQuery = `
SELECT
    m.ROWID AS rowid,
    m.guid AS msg_guid,
    COALESCE(m.text, '') AS text,
    m.is_from_me AS is_from_me,
    COALESCE(h.id, '') AS sender_address,
    COALESCE(c.guid, '') AS chat_guid,
    COALESCE(c.style, 0) AS chat_style,
    (SELECT COUNT(*) FROM chat_handle_join WHERE chat_id = c.ROWID) AS participant_count
FROM message m
LEFT JOIN handle h ON m.handle_id = h.ROWID
LEFT JOIN chat_message_join cmj ON cmj.message_id = m.ROWID
LEFT JOIN chat c ON c.ROWID = cmj.chat_id
WHERE m.ROWID > %d
ORDER BY m.ROWID ASC
`

// maxROWIDQuery returns the current maximum message ROWID, used at startup
// so we don't flood the dispatcher with the entire message history.
const maxROWIDQuery = `SELECT COALESCE(MAX(ROWID), 0) AS max_rowid FROM message`

// row mirrors one JSON object emitted by `sqlite3 -json`. Every numeric
// column comes through as a JSON number; the isFromMe bit is 0/1, not a
// bool.
type row struct {
	ROWID            int64  `json:"rowid"`
	MsgGUID          string `json:"msg_guid"`
	Text             string `json:"text"`
	IsFromMe         int    `json:"is_from_me"`
	SenderAddress    string `json:"sender_address"`
	ChatGUID         string `json:"chat_guid"`
	ChatStyle        int    `json:"chat_style"`
	ParticipantCount int    `json:"participant_count"`
}

// runSQLite executes one query against the chat.db and parses the JSON
// array of rows sqlite3 emits with the -json flag.
func runSQLite(ctx context.Context, binary, dbPath, query string, dst any) error {
	//nolint:gosec // inputs are internal (fixed binary name + literal queries); no user-provided SQL.
	cmd := exec.CommandContext(ctx, binary, "-json", "-readonly", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if ok := asExitError(err, &ee); ok && len(ee.Stderr) > 0 {
			return fmt.Errorf("sqlite3: %w (stderr: %s)", err, ee.Stderr)
		}
		return fmt.Errorf("sqlite3: %w", err)
	}
	// sqlite3 -json emits an empty string (not "[]") when the result set
	// has zero rows; handle that gracefully.
	if len(out) == 0 {
		return nil
	}
	err = json.Unmarshal(out, dst)
	if err != nil {
		return fmt.Errorf("decode sqlite3 output: %w", err)
	}
	return nil
}

// fetchMaxROWID returns the current largest message ROWID, or 0 if the
// table is empty.
func fetchMaxROWID(ctx context.Context, binary, dbPath string) (int64, error) {
	var out []struct {
		MaxROWID int64 `json:"max_rowid"`
	}
	err := runSQLite(ctx, binary, dbPath, maxROWIDQuery, &out)
	if err != nil {
		return 0, err
	}
	if len(out) == 0 {
		return 0, nil
	}
	return out[0].MaxROWID, nil
}

// fetchNewMessages returns every message with ROWID > cursor, ordered by
// ROWID ascending.
func fetchNewMessages(ctx context.Context, binary, dbPath string, cursor int64) ([]row, error) {
	query := fmt.Sprintf(pollQuery, cursor)
	var out []row
	err := runSQLite(ctx, binary, dbPath, query, &out)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// asExitError is a tiny wrapper that narrows err to *exec.ExitError for
// stderr extraction without pulling in errors.As at every call site.
func asExitError(err error, dst **exec.ExitError) bool {
	ee := &exec.ExitError{}
	if errors.As(err, &ee) {
		*dst = ee
		return true
	}
	return false
}

// styleIsGroup reports whether a chat.style code marks a group chat. 43 is
// the conventional value; anything else (45 = DM, 0 = unknown) is treated
// as a DM. A secondary heuristic (participant count > 1) is applied at the
// dispatch site so odd style values still route correctly.
func styleIsGroup(style int) bool {
	return style == 43
}
