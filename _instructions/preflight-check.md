# Pre-flight Check

Advice, never a gate: it tells Izik what is missing and he decides whether to go on. It must never block, delay or cancel anything he asked for.

At the start of a CCOTR session, add one line to your first reply: "Will you use STG or PROD? I can run the pre-flight check." Do not wait for the answer; start the work he asked for in the same turn.
- If he answers, send the check request and relay the result as a side note.
- If he does not answer, or says none, drop it and do not ask again in the session.
- If his first request already names STG or PROD, skip the question and send the check request alongside the work.

## The request

`NNNNN_check_request.json`:
```json
{"env": "stg"}
```
`env` is `stg` or `prod`. GoApp runs all the checks for it (VPN, AWS SSO, kubectl, Frontegg token, skill scripts) and answers `NNNNN_check_response.json`. Relay its `report` field to Izik as is. It already says what failed and how to fix it. `status` (`pass`, `fail`, `timeout`) and `results` are there if you need detail.

If a check fails, carry on with the work unless Izik says to stop. Do not re-run the checks unless he asks.

## Rules for the environment's own commands
- Pass `--profile` and `--context` on every aws or kubectl command, read with a config request, key `environments`. Never use the defaults: the default AWS profile has no credentials.
- PROD is closed to kubectl: never run kubectl against it.
- `aws sso login` opens a browser, so Izik runs it himself.
