package imessage

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// sendScriptDM tells Messages.app to send body to a one-to-one recipient.
// Works whether recipient is an E.164 phone or an iCloud email address;
// Messages resolves it against the active iMessage account.
const sendScriptDM = `
on run argv
    set recipient to item 1 of argv
    set body to item 2 of argv
    tell application "Messages"
        set targetService to 1st account whose service type is iMessage
        set targetBuddy to participant recipient of targetService
        send body to targetBuddy
    end tell
end run
`

// sendScriptChat tells Messages.app to send body to an existing chat
// identified by its iMessage chat GUID (e.g. "iMessage;+;chat12345").
// Used for group chats, where addressing by buddy wouldn't target the
// right thread.
const sendScriptChat = `
on run argv
    set chatGuid to item 1 of argv
    set body to item 2 of argv
    tell application "Messages"
        set targetChat to first chat whose id is chatGuid
        send body to targetChat
    end tell
end run
`

// sendDM runs osascript to send body to a direct recipient (phone/email).
func sendDM(ctx context.Context, binary, recipient, body string) error {
	return runOsascript(ctx, binary, sendScriptDM, recipient, body)
}

// sendGroup runs osascript to send body to a group chat by its chat GUID.
func sendGroup(ctx context.Context, binary, chatGUID, body string) error {
	return runOsascript(ctx, binary, sendScriptChat, chatGUID, body)
}

// runOsascript invokes `osascript - arg1 arg2` with script piped via stdin.
// We pipe instead of `-e` so embedded quotes and newlines in body don't
// require AppleScript-level escaping.
func runOsascript(ctx context.Context, binary, script string, args ...string) error {
	full := append([]string{"-"}, args...)
	//nolint:gosec // binary is a fixed config value, args are the bot reply body and sender handle.
	cmd := exec.CommandContext(ctx, binary, full...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("osascript: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
