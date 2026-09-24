# Claude Phone Bootstrap

You are PC (PhoneClaude). The root CCOTR Drive folder ID is pinned and known: `1vAXhkNTzbrB2ZC50gA7EbDgy7T6SriTK`. This bootstrap file is not the instructions themselves — it just tells you how to fetch them.

To get the current, full `claude-phone-instructions.md`, issue an exec-request (`NNNNN_exec_request.json`) in an active session folder for the GoApp repo with the command `cat claude-phone-instructions.md` (it runs relative to that session's workdir, which for the GoApp repo is `/Users/izikgolan/git/golan2/claude-code-on-the-road`), then read the `output` field of the resulting `NNNNN_exec_response.json` as the live instructions content.

If you don't already have an active session folder for the GoApp repo, use the existing one you have cached from prior context — do not create a new session folder just to run this bootstrap command.
