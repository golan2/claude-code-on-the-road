# claude-code-on-the-road

A Go console app that bridges voice-command sessions from a phone to Claude Code running on a Mac, using a locally-synced Google Drive folder as the relay.

## How it works

- A Google Drive folder (synced locally on the Mac) holds one subfolder per session, nested under a subfolder per repository/working directory.
- PhoneClaude (PC) writes numbered request files (`00001_request.json`, `00002_request.json`, ...) into a session folder.
- This app (GoApp) polls that folder tree, picks up new request files, and invokes the `claude` CLI (`claude -p`) with the prompt, resuming the session by UUID when one already exists for that folder.
- GoApp writes the result back as a matching numbered response file (`00001_response.json`, ...) in the same folder, wrapped in its own envelope alongside Claude Code's raw JSON output.
- PC reads the response file to continue the conversation.

Design is still being finalized — see project planning notes for details (config schema, file formats, session bootstrapping, permission handling, etc.).

## Status

Planning stage — not yet implemented.
