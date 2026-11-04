> **This README is for whoever is working on GoApp, not for PC.** PC's own instructions are in the [_instructions](_instructions/) folder.

# claude-code-on-the-road

A Go console app that bridges voice-command sessions from a phone to Claude Code running on a Mac, using a locally-synced Google Drive folder as the relay.

## How it works

- A Google Drive folder (synced locally on the Mac) holds one subfolder per session, directly under the watch folder. The scan is flat: only the watch folder's direct child folders are looked at (and `_archived` is skipped), never anything deeper.
- PhoneClaude (PC) writes numbered request files (`00001_cc_request.json`, `00002_cc_request.json`, ...) into a session folder.
- This app (GoApp) polls that folder tree, picks up new request files, and invokes the `claude` CLI (`claude -p`) with the prompt, resuming the session by UUID when one already exists for that folder.
- GoApp writes the result back as a matching numbered response file (`00001_cc_response.json`, ...) in the same folder, wrapped in its own envelope alongside Claude Code's raw JSON output.
- PC reads the response file to continue the conversation.

The full protocol — file schemas, session lifecycle, and every request type below — is specified in [_instructions/instructions.md](_instructions/instructions.md) and the tool files next to it, which are also PC's own reference documents (synced into the `_instructions` folder in the Drive root so PC can read them directly; see "PC's instructions folder").

## Running GoApp

From the repo root:

```bash
go run .
```

It has to be run from the repo root because `config.json` is read from the current directory. The core keys in `config.json`:

| Key | Default | Meaning |
|---|---|---|
| `watchFolder` | required | The Drive-synced root folder GoApp polls for session folders. |
| `pollIntervalSeconds` | 5 | How often GoApp rescans the folder tree for new requests. |
| `defaultTimeout` | 10 | The default limit, in minutes, on how long a Claude Code or exec subprocess may run before it is killed. A request's own `timeout` overrides it for that request only. The old key `timeoutMinutes` is still read when `defaultTimeout` is absent, with a deprecation warning at startup. |
| `permissionMode` | `bypassPermissions` | The default Claude Code permission mode. A request's own `permissionMode` overrides it for that request and every later request in the session. |
| `maxConcurrentSessions` | 10 | The most Claude Code invocations allowed to run at once. |

The other keys (`models`, `claudePath`, `skillsCopyDir`, `skillPaths`, `skillsCopyIntervalSeconds`, `logFile`) are described in the sections below.

### Concurrency and timeouts

Claude Code requests in the same session folder run strictly one at a time, in ordinal order. Different session folders run in parallel, up to `maxConcurrentSessions`. Exec and config requests bypass the per-session lock entirely.

When a Claude Code request or exec command exceeds its timeout (the request's `timeout`, or `defaultTimeout` when it has none), GoApp kills the whole process group, so Claude's own child processes aren't left orphaned. A Claude Code `timeout` response carries an error message and no output; an exec `timeout` response keeps the output produced before the kill.

## Protocol features

The relay isn't limited to plain "send a prompt, get a Claude Code reply" turns. Each session folder supports several independent request types, all driven by numbered files sharing one ordinal sequence:

### Claude Code requests

The core flow: `NNNNN_cc_request.json` carries a `prompt` (and an optional `workdir`) and GoApp runs it through `claude -p`, resuming the existing session UUID for that folder when there is one. As soon as the subprocess launches, GoApp writes its PID to `NNNNN_pid.json`, then writes the final `NNNNN_cc_response.json` with the outcome (`success`, `claude_code_error`, or `timeout`) and Claude Code's raw result. A `NNNNN_cancel_request.json` for the same ordinal kills that process group and gets a `NNNNN_cancel_response.json` back.

Every request file of any type may also carry a `brief` field — a short human-readable phrase in PC's own words (e.g. "fix the login bug"), capped at 7 words; GoApp trims anything longer to its first 7 words rather than rejecting it. It is appended to the existing log line when the request is picked up and again when its response is written (`... - brief: "fix the login bug"`), so every request is still exactly one line each time and log lines can be correlated with what PC actually asked for. GoApp never fails a request over a missing `brief` — the log line just carries `- WARNING: no brief provided`. Unknown/extra fields on any request file are likewise ignored rather than rejected.

Plain `NNNNN_request.json` is still accepted by GoApp for backward compatibility only; new requests use `NNNNN_cc_request.json`. The same applies to responses: GoApp writes `NNNNN_cc_response.json`, and a request already answered under the legacy `NNNNN_response.json` name is never reprocessed. A legacy-named request gets a legacy-named response back, for backward compatibility only.

### Status requests

While a Claude Code request is still in flight, PC can ask for a progress snapshot by writing `NNNNN_status_request_MMM.json`. If the underlying process is still running, GoApp replies with `NNNNN_status_response_MMM.json` containing elapsed time, the last tool call, and the partial output accumulated so far. If the request has already finished, no status response is written — the final response is authoritative instead.

### Exec requests

For raw shell commands that don't need Claude Code's judgment, `NNNNN_exec_request.json` carries a `command` string that GoApp runs directly through a shell, starting from the session's workdir (`/tmp` if it has none). Unlike Claude Code requests, exec-requests aren't serialized behind the per-session lock — they run immediately, concurrently with an in-flight Claude Code request or other exec-requests. GoApp replies with `NNNNN_exec_response.json` (exit code, merged stdout+stderr, and a `truncated` flag).

An exec-request is only ever run once, even though "pending" is derived purely from "no response file yet": GoApp keeps an in-memory set of `folder:ordinal` keys currently being processed (mirrored for config-requests too), so a command that outlives a single `pollIntervalSeconds` tick doesn't get re-dispatched as a second, concurrent execution by the next tick.

An exec request gets a `NNNNN_pid.json` the moment its command launches, the same "picked up" signal a Claude Code request gets. There is no ack file.

A session can be **no-CC**: bootstrapped entirely by exec-requests, never invoking Claude Code. The first exec-request written into a brand-new session folder creates `__session_conf.json` directly — with no Claude Code session ID yet, and with a `workdir` only if the request carried one. Such a session can still receive a regular request later, at which point it behaves exactly like one that had Claude Code from the start.

### Cancel requests

`NNNNN_cancel_request.json` stops a Claude Code request or an exec request that is still running; the ordinal is the request to cancel, and GoApp tells which kind it is from the files in the folder. An exec request is cancellable from the moment it launches, because its PID file is written immediately; a cancelled exec gets an exec-response with `exitCode` -1 and whatever output it had produced. GoApp reads that request's `NNNNN_pid.json` and sends SIGKILL to the whole process group (Claude Code's tool subprocesses inherit its stdout, so killing only the main process would leave the request hanging with no response). It answers with `NNNNN_cancel_response.json` (`status` `success` with the `pid`, or `error` with a `message`). It refuses with `request already finished` once the request has its response (`NNNNN_cc_response.json` or `NNNNN_exec_response.json`), because by then the PID may belong to an unrelated process, and with `PID file not found` if the request hasn't launched yet. Cancel requests are checked before the per-session lock, since that lock is held for the whole run they need to interrupt, and like exec and config requests they are tracked in memory so a slow cancel isn't dispatched twice.

### Config requests

`NNNNN_config_request.json` looks up a single key from GoApp's own `config.json` (currently the repository list and the acronym map) and gets back `NNNNN_config_response.json` with either a `value` or an `error`. These are answered synchronously — no subprocess, no ack file — and, like exec-requests, aren't blocked by the Claude Code lock.

### Session lifecycle

- **New sessions** start with any request (`00001_cc_request.json`, or `00001_exec_request.json` for a no-CC session). `workdir` is optional on every request type: the first request that carries one sets it, after which it is immutable — a new repository always means a new session folder. A session with no `workdir` runs in `/tmp` (the least harmful place), and a later request that carries a `workdir` sets it. The working directory is fixed by design, so one session always stays tied to one repository; this is a rule GoApp enforces, not a limit of `claude --resume`.
- **Session folder access.** Every Claude Code invocation gets the session's own folder through `--add-dir` (alongside `skillsCopyDir`), which is what makes files dropped into a session folder readable to Claude Code.
- **PID files** (`NNNNN_pid.json`) record the Claude Code process ID for a given request as soon as it launches, well before the final response is ready. A missing PID file after a reasonable delay signals something failed before Claude Code even launched.
- **Archiving** moves a finished session folder into the root's `_archived` folder, after which GoApp stops scanning it entirely. GoApp does this itself at every start (see "Archiver"); PC can also move a named session there, and can move one back out.

### `__session_conf.json`

Every session folder that has ever run a regular request or a no-CC-bootstrapping exec-request gets a `__session_conf.json`, written and owned entirely by GoApp — PC has no need to read or write it. It holds the session's `workdir` (empty until a request carries one), its `permissionMode`, and the Claude Code `sessionId` to resume (empty for a no-CC session that hasn't invoked Claude Code yet). GoApp creates it when the first Claude Code or exec request is written into the folder.

Any request type makes a folder a session and gets handled: GoApp scans for `_cc_request.json`, `_exec_request.json` and `_config_request.json` files alike, and no request needs a prior request or a `workdir` to be answered. Config requests never touch `__session_conf.json` at all.

## Models and effort

A Claude Code request may carry `model` and `effort`. Both are optional and sticky for the session, like `permissionMode`: a request that sets one replaces the stored value, and a request that omits it keeps it. They are stored in `__session_conf.json`, and an empty value means `claude` uses its own default.

`models` in `config.json` is the list PC may choose from. Each entry has a `name` (what PC sends), an `id` (the full model id GoApp passes as `claude --model`, so the version is pinned here and only changes when the config does), a `grade` and a `useFor` sentence that tell PC what each model is for. PC reads the list with a config-request on key `models`. Names must be unique and every entry needs a name and an id, or startup fails. A `model` that isn't listed, or any `model` when the list is absent, gets an error response naming the allowed values. The stored value is the short name, so changing an `id` in the config takes effect on the next request.

`effort` is one of `low`, `medium`, `high`, `xhigh` or `max`, passed as `claude --effort`. These are fixed by the CLI, so they are documented in `_instructions/model-and-timeout.md` and not kept in config. Any other value gets an error response.

## Skills support

GoApp doesn't scan or enumerate skills itself; listing skills is just another prompt to a `claude -p` session, since Claude Code's own discovery logic can change between CLI versions. What GoApp does is make every skill invocable by name from a headless session, including ones whose `SKILL.md` sets `disable-model-invocation: true` (a bare `-p` session refuses those, because a real slash command exists only in the interactive TUI).

Once at startup and then every `skillsCopyIntervalSeconds` (default 300), GoApp copies every skill found under each root in `skillPaths` (a hand-maintained list in `config.json`) into `skillsCopyDir`. `skillsCopyDir` is required, and a missing or empty key fails startup, but a directory that doesn't exist yet is created (macOS wipes `/tmp` on reboot). In every copy `disable-model-invocation` is rewritten to `false`; the original skill files are never touched. `skillsCopyDir` is passed to every Claude Code invocation as an extra `--add-dir`.

- **Layout:** each copy is `skillsCopyDir/.claude/skills/<name>/`. `--add-dir` only discovers skills at exactly that depth, so the layout is flat. `<name>` is `<source root basename>__<path within the root>`, with `/` in the path replaced by `--`, so skills sharing a name from different roots can't collide.
- **Change detection:** mtime-based, against a baseline in `skills_state.json` inside `skillsCopyDir`. The mtime is read from the source file, never the copy. Only new or changed files are re-copied, and a skill removed from its source has its copy and baseline entry deleted on the next pass. No restart is needed.
- **Logging:** after each request GoApp logs the skills Claude Code actually invoked (`skills invoked: x, y`), taken from the `Skill` tool_use events in the stream.

There is no skills setting on a request or a session. Skills are used by naming them in the prompt.

## Claude binary and subprocess environment

GoApp spawns the `claude` CLI from an explicit absolute path, the required `claudePath` key in `config.json` (currently `/opt/homebrew/bin/claude`). There is no PATH lookup and no code-level default; startup fails fast if the key is missing, or the file doesn't exist or isn't executable, so a GoApp launched from an IDE or launcher with a minimal `PATH` can't fail later, mid-request.

The subprocess inherits GoApp's environment with one override: `USER` is forced to the real OS account name. On macOS Claude Code finds its login credential in the keychain under the account named by `$USER`, so a `USER` that differs from the real account makes every request fail with `Not logged in · Please run /login`.

## PC's instructions folder

PC's instructions are several files, not one, so PC does not load every detail at once. `_instructions/instructions.md` is the main file: the rules, the relay overview, and a table naming the tool files, which PC opens only when a task needs them (`relay-details.md`, `continuity.md`, `shell.md`, and so on). The repo folder `_instructions/` is mirrored into `_instructions/` directly under the Drive root.

At every start GoApp compares the version line, `<!-- version: N -->`, which must be the first line of `instructions.md`, with the Drive copy's. One version covers the whole folder. If the local version is higher, or Drive has none, GoApp replaces the folder: every `.md` file is written in place (only when its content differs, so Drive keeps each file's identity), and every file in the Drive folder that is no longer in the repo is deleted, so no dead files remain. `instructions.md` is written last, so an interrupted sync is simply redone at the next start. If the versions are equal or Drive's is higher, nothing is touched, so an old binary can never overwrite a newer folder. A sub-folder inside the Drive folder is reported and left alone. The log says what happened: `Synced instructions to Drive _instructions (version 41): wrote 11, removed 0 {...}`.

**Maintainers:** bump the version line by 1 whenever any file in `_instructions/` changes, otherwise the change never reaches Drive. A test checks that every file the main file names exists.

`_instructions` is a reserved folder name, like `_archived`: the session scan skips both by name, so neither is ever a session and the archiver never touches them. `bootstrap.md` (PC's entry point) points at the `_instructions` folder. GoApp no longer syncs a root-level `instructions.md`; an old copy left in the Drive root is stale and can be deleted.

## Pre-flight checks

`config.json` has an `environments` map, one entry per environment (`stg`, `prod`): `awsProfile` (required), `kubeContext` (optional, leave it out where kubectl is closed, as for PROD) and `frontegg` (true turns on the token check). The default AWS profile has no credentials, so every aws and kubectl command must pass these explicitly. PC reads the map with a config request for its own commands. `vpnCheckHost` (default `imugit.imubit.com`) is the host the VPN check calls.

`NNNNN_check_request.json` with `{"env": "stg"}` runs the checks for that environment in parallel (30 seconds each) and is answered with `NNNNN_check_response.json`: an overall `status` (`pass`, `fail`, `timeout`), a ready-to-relay `report` (a header and one line per check, failures with their fix, and a failed `kube` marked as probably the VPN when the VPN check failed) and a result per check with `name`, `status`, `exitCode`, `output` and `fix`. The checks are:
- `vpn`: `curl -sS -m 5 https://<vpnCheckHost>/api/v4/version` gets any HTTP response.
- `aws-sso`: `aws sts get-caller-identity --profile P`.
- `kube`: `kubectl --context C auth can-i get pods`, only when the environment has a `kubeContext`.
- `frontegg`: only when the environment has `frontegg: true`. `~/.frontegg` (bare JWT, `{"token":...}` or an `Authorization: Bearer` line) holds a JWT whose `exp` is in the future.
- `skill-scripts`: every `scripts/*.sh` under `skillPaths` is executable.

An unknown `env` fails and lists the known ones. A check request cannot be cancelled.

## Archiver

When `archiveAfterDays` is set in `config.json` (a positive number; absent or 0 turns it off, a negative number stops startup), GoApp archives idle sessions once at every start, before polling begins, so nothing is in flight. A session is idle when the newest modification time of its request, response and pid files is older than that many days. It is zipped to `_archived/<name>.zip` (entries under `<name>/`) and the folder is deleted once the zip is complete. An old unanswered request does not protect a folder. A session whose zip already exists is skipped and logged. The same pass also zips any plain folder found inside `_archived` (a session moved there by hand). When anything was archived GoApp logs one line, `archiver: archived N (idle > D days) {a,b,c}`, and one more, `archiver: zipped N unzipped folders in _archived {...}`, for the hand-moved ones.

Drive Desktop may refresh file times on a re-sync, which can only make a folder look newer (not archived), never older. A session unzipped back to the root keeps its old file times, so it is archived again at the next start unless a new request has arrived in it.

## Logging

Every event GoApp prints to stdout — startup, request/response/exec/config activity, warnings, and errors — is also appended to a log file, so history survives after the terminal is gone and after restarts. The path defaults to `goapp.log` (relative to the working directory GoApp is run from) and is configurable via `logFile` in `config.json`. It's opened in append mode, so it accumulates across runs rather than being wiped on each restart; the startup line records the PID so you can tell one run's entries apart from another's.

At startup GoApp checks the previous `goapp_started.json`: if the process it names is still alive, GoApp logs `WARNING: another GoApp may already be running: pid N, started ..., command: ...`. Two GoApps on one folder each run every request and write every log line twice. It only warns and never stops either process, and it can see only the most recent earlier start, because each start overwrites the marker.

Every request gets a `REQ` line and a `RES` line with the same prefix, `[session][NNNNN_type]`, where `type` is `cc`, `exec`, `config` or `cancel`. All types look the same; only the type differs: `[s1][00002_exec][REQ] - brief: "say hi"` then `[s1][00002_exec][RES] [exit 0] - brief: "say hi"`. The bracket after `RES` is the outcome: `exit N` (exec), `success`, `error` or `timeout` (cc), `ok` (config, cancel), and `error: <message>` on any failure. A failed response starts with `ERROR:` and a timed-out Claude Code one with `TIMEOUT:`. Side notes about a request (no workdir, skills invoked, and so on) use `[INFO]`. A request whose `timeout` is longer than `defaultTimeout` has ` [TO=<n>min]` appended to its `REQ` line after the brief, for example `[s1][00002_exec][REQ] - brief: "long timeout" [TO=30min]`; a default or shorter timeout adds nothing.

A panic in any single request's handling is caught and logged with a full stack trace, and does not take down the rest of the process. Every goroutine is wrapped in `recoverPanic`.

## Work tracking

[TODO.md](TODO.md) in the repo root tracks CCOTR work status: open items, backlog, and completed items.
