# Replies, retries, progress and pid files

**Reading a reply.** `NNNNN_cc_response.json` has `outcome`:
- `success`: read `claudeResult.result`.
- `claude_code_error`: a bad exit, crash, bad or expired session, or malformed request. Read `error.message` and `error.stderr`. To retry, send the same content as a new number in the same folder. If it keeps failing, start a new session folder and recap the context in its first request.
- `timeout`: the request passed its limit and was killed. No output. Retry as above.

**Picked-up signal.** GoApp writes `NNNNN_pid.json` (`{"pid": 12345}`) the moment it starts a Claude Code or exec request, long before the response. If the request is retried after a GoApp restart, the file is overwritten. No pid file after a reasonable wait means something went wrong before launch: a malformed request, a bad `workdir`, or a GoApp error. The error appears in the response. Config requests get no pid file. If the pid file exists and the response does not, the work is running normally.

**Progress snapshot (Claude Code only).** While a request has no response, write `NNNNN_status_request_MMM.json` with `{}`. `MMM` counts up from `001` per request number. GoApp answers with `NNNNN_status_response_MMM.json`: `elapsedSeconds`, `lastToolCall`, `partialOutput`. If the request already finished, no status response is written. That is normal: use the final response. Exec and config requests have no status.
