# Session folders and the Claude request

**Session folder.** One ongoing Claude Code conversation. Create it directly under the root folder. GoApp scans only the root's direct children, so a folder created deeper is never picked up. Give it a meaningful name.

**Files in a session folder**
- You write: `NNNNN_cc_request.json`, `NNNNN_exec_request.json`, `NNNNN_config_request.json`, `NNNNN_cancel_request.json`, `NNNNN_status_request_MMM.json`.
- GoApp writes: `NNNNN_cc_response.json`, `NNNNN_exec_response.json`, `NNNNN_config_response.json`, `NNNNN_cancel_response.json`, `NNNNN_status_response_MMM.json`, `NNNNN_pid.json`.
- `__session_conf.json` is GoApp's own state.
- Claude Code requests are always `NNNNN_cc_request.json` and `NNNNN_cc_response.json`. No other spelling is valid.

**Numbering.** Five digits, zero-padded, starting at `00001`, no gaps. There is one sequence per session for all request types. The next number is the highest existing one plus one. A cancel request reuses the number of the request it cancels. A status request uses the number of the request it asks about, plus a counter `MMM`. Keep the folder's Drive ID for the whole conversation instead of searching again.

**Starting a session.** Create the folder (after Izik says yes), then write your first request. **Continuing:** write the next request into the same folder.

**`workdir`.** Optional on every request type. It is an absolute path on the Mac, taken from the `simpleRepositories` config key. The first request that carries one sets it for the whole session, and after that it is fixed. A session with no `workdir` runs in `/tmp`. If a request ran in `/tmp` but needed a repository, send another request with the proper `workdir`. It is accepted while the session has none.

**Claude request**
```json
{
  "prompt": "string, required",
  "workdir": "string, optional, absolute path",
  "permissionMode": "string, optional",
  "model": "string, optional",
  "effort": "string, optional",
  "timeout": "number, optional, minutes",
  "brief": "string, always, at most 7 words"
}
```
- `prompt`: the text Claude Code acts on. It can be long.
- `permissionMode`: rarely needed. Once set, it stays for the session until you set another.
- `model`, `effort`, `timeout`: see `model-and-timeout.md`.
- `brief`: see the relay rules in `instructions.md`.
- To use a skill, name it in the `prompt`: see `skills.md`.
- A file you put in a session folder becomes a real file on the Mac. To give Claude Code an attachment, name it in the prompt. Claude Code can read and write its own session folder and the workdir.
