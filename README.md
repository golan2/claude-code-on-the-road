# claude-code-on-the-road

A Go console app that bridges voice-command sessions from a phone to Claude Code running on a Mac, using a locally-synced Google Drive folder as the relay.

## How it works

- A Google Drive folder (synced locally on the Mac) holds one subfolder per session, nested under a subfolder per repository/working directory.
- PhoneClaude (PC) writes numbered request files (`00001_cc_request.json`, `00002_cc_request.json`, ...) into a session folder.
- This app (GoApp) polls that folder tree, picks up new request files, and invokes the `claude` CLI (`claude -p`) with the prompt, resuming the session by UUID when one already exists for that folder.
- GoApp writes the result back as a matching numbered response file (`00001_cc_response.json`, ...) in the same folder, wrapped in its own envelope alongside Claude Code's raw JSON output.
- PC reads the response file to continue the conversation.

The full protocol — file schemas, session lifecycle, and every request type below — is specified in [instructions.md](instructions.md), which is also PC's own reference document (synced verbatim into the Drive root so PC can read it directly).

## Protocol features

The relay isn't limited to plain "send a prompt, get a Claude Code reply" turns. Each session folder supports several independent request types, all driven by numbered files sharing one ordinal sequence:

### Claude Code requests

The core flow: `NNNNN_cc_request.json` carries a `prompt` (and `workdir` on the first request of a session) and GoApp runs it through `claude -p`, resuming the existing session UUID for that folder when there is one. GoApp acknowledges pickup with `NNNNN_ack.json` as soon as the subprocess launches, then writes the final `NNNNN_cc_response.json` with the outcome (`success`, `claude_code_error`, or `timeout`) and Claude Code's raw result.

Plain `NNNNN_request.json` is still accepted by GoApp for backward compatibility only; new requests use `NNNNN_cc_request.json`. The same applies to responses: GoApp writes `NNNNN_cc_response.json`, and a request already answered under the legacy `NNNNN_response.json` name is never reprocessed. A legacy-named request gets a legacy-named response back, for backward compatibility only.

### Status requests

While a Claude Code request is still in flight, PC can ask for a progress snapshot by writing `NNNNN_status_request_MMM.json`. If the underlying process is still running, GoApp replies with `NNNNN_status_response_MMM.json` containing elapsed time, the last tool call, and the partial output accumulated so far. If the request has already finished, no status response is written — the final response is authoritative instead.

### Exec requests

For raw shell commands that don't need Claude Code's judgment, `NNNNN_exec_request.json` carries a `command` string that GoApp runs directly through a shell, starting from the session's workdir. Unlike Claude Code requests, exec-requests aren't serialized behind the per-session lock — they run immediately, concurrently with an in-flight Claude Code request or other exec-requests. GoApp replies with `NNNNN_exec_response.json` (exit code, merged stdout+stderr, and a `truncated` flag).

An exec-request is only ever run once, even though "pending" is derived purely from "no response file yet": GoApp keeps an in-memory set of `folder:ordinal` keys currently being processed (mirrored for config-requests too), so a command that outlives a single `pollIntervalSeconds` tick doesn't get re-dispatched as a second, concurrent execution by the next tick.

Unlike a regular request, an exec-request's `NNNNN_ack.json` is **conditional**, not guaranteed: GoApp only writes it if the command is still running after `execAckDelaySeconds` (default 5) has elapsed since launch. A command that finishes within that window gets no ack at all — its exec-response arrives just as fast and is proof enough it was picked up. This keeps the common case (a quick command) down to one file instead of two, while a genuinely slow command still gets its "picked up" signal.

A session can be **no-CC**: bootstrapped entirely by exec-requests, never invoking Claude Code. The first exec-request written into a brand-new session folder includes `workdir`, the same way a first regular request would, and GoApp creates `__session_conf.json` from it directly — with no Claude Code session ID yet. Such a session can still receive a regular request later, at which point it behaves exactly like one that had Claude Code from the start.

### Config requests

`NNNNN_config_request.json` looks up a single key from GoApp's own `config.json` (currently the repository list and the acronym map) and gets back `NNNNN_config_response.json` with either a `value` or an `error`. These are answered synchronously — no subprocess, no ack file — and, like exec-requests, aren't blocked by the Claude Code lock.

### Session lifecycle

- **New sessions** start with `00001_cc_request.json` including `workdir` (or `00001_exec_request.json` including `workdir`, for a no-CC session); the working directory is then immutable for that session's lifetime — a new repository always means a new session folder.
- **Acks** (`NNNNN_ack.json`) confirm GoApp successfully launched the Claude Code subprocess for a given request, well before the final response is ready. A missing ack after a reasonable delay signals something failed before Claude Code even launched.
- **Archiving** moves a finished session folder into the root's `_archived` folder (a plain Drive move), after which GoApp stops scanning it entirely.

### `__session_conf.json`

Every session folder that has ever run a regular request or a no-CC-bootstrapping exec-request gets a `__session_conf.json`, written and owned entirely by GoApp — PC never reads or writes it (`instructions.md` explicitly tells PC to never touch it). It holds the session's fixed `workdir`, its `permissionMode`, and the Claude Code `sessionId` to resume (empty for a no-CC session that hasn't invoked Claude Code yet). GoApp only ever recognizes a folder as needing this file — and derives `workdir` from a request's payload — on the first request of that kind ever written into the folder; every session, including a no-CC one, has exactly one bootstrapping moment, not one per request type.

A session folder with no `__session_conf.json` yet is a folder that has never received a first request (regular or exec) with `workdir`. Sending an exec-request into such a folder without `workdir` fails with `"no session configuration found"` — the folder is discoverable (GoApp scans for `_cc_request.json`, `_exec_request.json`, and `_config_request.json` files alike), but there's nothing to run the command against yet.

## Skills support

GoApp doesn't scan or enumerate skills itself, and never will — asking a `claude -p` session what skills are available is just another prompt, relayed like any other, since Claude Code's own discovery logic (path precedence, plugin resolution, frontmatter schema) is internal and can change between CLI versions. What GoApp *does* do is make every skill headlessly invocable by name, including ones whose `SKILL.md` sets `disable-model-invocation: true` (manual/slash-command-only skills) — a bare `-p` session hard-refuses those otherwise, since a real slash command is a construct of the interactive TUI's own input layer, one that text sent via `-p` never passes through.

Every `skillsCopyIntervalSeconds` (default 300 = 5 minutes, configurable), and once at startup, GoApp copies every skill found under each root listed in `skillPaths` (a manually-curated list in `config.json` — GoApp does not infer this from `simpleRepositories` or any session's project directory) into `skillsCopyDir` (set explicitly in `config.json`, currently `/tmp/ccotr-skills` — required — a missing or empty key fails fast at startup, but a directory that doesn't exist yet is created, since GoApp populates it itself and macOS wipes `/tmp` on reboot). Every copy lands directly under `skillsCopyDir/.claude/skills/<name>/` — confirmed empirically that `--add-dir <dir>` only discovers skills at exactly that one fixed depth (`<dir>/.claude/skills/<skill-name>/SKILL.md`), never deeper, so this is the only layout that actually works. Since every copy must therefore be a flat, immediate child of `.claude/skills`, `<name>` folds in both the source root's identity and the skill's own path within that root (joined with `__`/`--`) rather than expressing that hierarchy as nested directories — so two differently-sourced skills sharing a name still can't collide, and provenance stays visible in the folder name itself. `disable-model-invocation` is unconditionally rewritten to `false` in every copy — auto-invocable skills keep working exactly as before, and manual-invocation-only skills become force-invocable by name too. The *original* skill files are never touched, only the copies. `skillsCopyDir` is passed to every Claude Code invocation as an extra `--add-dir`.

Change detection is mtime-based: GoApp keeps a baseline in `skills_state.json` inside `skillsCopyDir` itself (never in GoApp's own install folder or the Drive-synced `watchFolder`), mapping each source file's path to the source mtime last seen — read from the source, never the copy, since the copy's own mtime is freshly rewritten on every touch. Only changed or new files are re-copied each pass; a skill removed from its source is detected the same way and both its copy and baseline entry are deleted (with any now-empty directories pruned) on the next pass — no GoApp restart needed for any of this.

### Using a skill in a session

There is no per-request or per-session skills setting. Every skill is available to every session through `skillsCopyDir`, and PC asks for one by naming it in the prompt. Claude Code invokes it, and because GoApp resumes the same Claude Code session for the folder, it normally stays in context for later requests (this is not guaranteed across conversation compaction). After each request GoApp logs which skills Claude Code actually invoked (`skills invoked: x, y`), taken from the `Skill` tool_use events in the stream, so you can see whether a skill was really used.

## Logging

Every event GoApp prints to stdout — startup, request/response/exec/config activity, warnings, and errors — is also appended to a log file, so history survives after the terminal is gone and after restarts. The path defaults to `goapp.log` (relative to the working directory GoApp is run from) and is configurable via `logFile` in `config.json`. It's opened in append mode, so it accumulates across runs rather than being wiped on each restart; the startup line records the PID so you can tell one run's entries apart from another's.

A panic in any single request's handling is caught, logged with a full stack trace, and does not take down the rest of the process — earlier, an unrecovered panic in one goroutine would silently kill all of GoApp, leaving every other in-flight or future request unanswered with nothing but a lost stderr line to explain why.

## Status

Implemented — see [main.go](main.go) and the `internal/` packages (`config`, `session`, `claudecode`, `shellexec`, `response`, `applog`) for the current GoApp implementation.
