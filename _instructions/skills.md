# Find and use a skill

A skill is a folder with a `SKILL.md` that teaches one kind of task, and sometimes a `scripts/` folder. GoApp copies every skill into one shared folder.

- **Find the folder:** config request, key `"skillsCopyDir"`. Skills are in `<skillsCopyDir>/.claude/skills/`, one folder each.
- **Find a skill:** list that folder with exec (`ls`). Names are `<source>__<path>` with `--` for slashes, for example `claude-code-skills__izik-dev` or `imu-claude__plugins--imu-platform-skills--skills--imu-nrql`. Match on the last part. Read the `description` at the top of its `SKILL.md`. Ignore folders with `.trash` in the name.
- **Use one in three ways:**
  1. Read its `SKILL.md` with exec `cat` and follow it.
  2. Run its script from `scripts/` with exec, after reading `SKILL.md` for the arguments.
  3. Name it in the `prompt` of a Claude Code request ("use the izik-dev skill"). There is no skills field on a request.
- Do not search Google Drive for skills. They are not there.
