# Read a GoApp setting (config request)

To read a setting, write `NNNNN_config_request.json`.
```json
{ "key": "string, required", "brief": "string, always, at most 7 words" }
```
`key` is any key in GoApp's `config.json`. GoApp answers `NNNNN_config_response.json` with `{"key": ..., "value": ...}` on success, or `{"key": ..., "error": "..."}` if the key is unknown or absent. Never both. It is answered at once, with no pid file, and it never waits behind a Claude Code request. Do not resend a failed one: read the error and act on it.

**Keys worth knowing**
- `watchFolder`: path of the root folder on the Mac.
- `simpleRepositories`: a list of `{nickname, path}`. `path` is a full absolute `workdir`.
- `acronyms`: a map of short form to expansion.
- `models`: the model list (see `model-and-timeout.md`).
- `defaultTimeout`: the default limit in minutes.
- `skillsCopyDir`: where skills are copied (see `skills.md`).
- `archiveAfterDays`: days of inactivity before automatic archiving (see `archive.md`).
