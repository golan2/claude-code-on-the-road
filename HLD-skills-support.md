# HLD: Skills Support in GoApp

Status: Decided — no code changes made yet
Author: investigation conducted with Claude Code, session 00011–00015
Date: 2026-09-28

## 1. Purpose

GoApp relays voice-transcribed Slack instructions into headless `claude` CLI
sessions (`internal/claudecode/claudecode.go`, `Invoke`). Claude Code supports
"skills" — folders under `SKILL.md` that extend a session with domain-specific
instructions, optionally gated by a `disable-model-invocation` frontmatter
flag that restricts a skill to manual (slash-command) invocation only.

This document decides how GoApp should support (a) listing skills available
to a session, and (b) invoking both auto-invocable and manual-invocation-only
skills through its existing headless invocation path.

## 2. Background: how GoApp invokes Claude Code today

`buildArgs()` in `internal/claudecode/claudecode.go` constructs:

```
claude -p "<prompt>" --output-format stream-json --verbose \
  --permission-mode <mode> --add-dir <dir> [--resume <sessionID>]
```

This is a one-shot, non-interactive (`-p`/`--print`) invocation. Every call
starts a fresh `claude` process, which performs its own skill discovery at
startup from its standard paths (`~/.claude/skills`, project `.claude/skills`,
enabled plugins). GoApp does not pass any skill-related flags today, and
`config.json`'s `simpleRepositories` list currently mixes in a few skill
folders as directory nicknames — this is unrelated plumbing (nickname → cwd
for voice commands) and is not a skill-awareness mechanism.

## 3. Decisions

### 3.1 GoApp does not scan or enumerate skill folders itself

GoApp will **not** build a static or cached config key that walks
`~/.claude/skills`, project `.claude/skills`, or any other path and parses
`SKILL.md` frontmatter. When a user needs to know what skills are available,
GoApp issues a normal request into a `claude -p` session asking it to list
its currently available skills, and relays the answer back. Skill discovery
and listing stay entirely CC's responsibility, invoked per request rather than
maintained as GoApp state.

**Why:** Claude Code's own discovery logic (path precedence, plugin
resolution, symlink handling, frontmatter schema) is internal and can change
between CLI versions. A GoApp-side scanner would be a second implementation
of that logic, prone to drifting out of sync, and — because GoApp is a
long-running daemon (polls every 5s, launches sessions on demand) — any
cached scan result risks staleness relative to on-disk edits. A fresh CC
session always reflects current state by construction; asking it directly for
its own list gets the same answer with strictly less code and no
duplicate-maintenance burden. This matches GoApp's existing pattern of
keeping `config.json` a thin, hand-maintained surface rather than adding a
filesystem-scanning subsystem for a need only a small hand-maintained list
(or a live per-request question) already covers.

### 3.2 Auto-invocable skills work today — no changes needed

Skills without `disable-model-invocation` set trigger correctly from plain
natural-language prompts through GoApp's existing `-p` mechanism, unmodified.

**Evidence (session 00012):** invoking the real `skill-bill` skill (no flag
set) via:

```
claude -p "Stop pausing for confirmation on reversible, in-scope work and \
just push forward for the rest of this session." \
--output-format stream-json --verbose --permission-mode bypassPermissions \
--add-dir /tmp
```

produced, as the very first assistant event:

```
TOOL_USE: Skill {"skill": "skill-bill"}
```

— triggered purely by the prompt text matching the skill's description, with
full agentic follow-through (it checked related plugin skills and wrote
memory files). No slash syntax, no special flags, no GoApp changes required.

### 3.3 Manual-invocation skills do not work today, and this is a hard boundary, not a gap

Skills with `disable-model-invocation: true` cannot be triggered through
GoApp's current `-p` invocation, in any prompt form, and this is confirmed to
be intentional CLI design rather than a missing feature GoApp can route
around at the protocol level.

**Evidence (session 00012, `grill-me` skill):**

| Prompt sent via `-p` | Result |
|---|---|
| Natural language matching the skill's description | Model never attempted the Skill tool — answered in-character itself. No invocation. |
| `/grill-me <text>` embedded in the `-p` string | Model called `Skill(skill: "grill-me", ...)` itself; tool refused: *"This skill only runs when you invoke it directly — go ahead and type `/grill-me` yourself."* |
| `/grill-me` alone via `--input-format stream-json` with a proper `{"type":"user","message":...}` JSON envelope | Same refusal. |

**Evidence (session 00015, exhaustive mechanism search):**

- `claude --help` was reviewed in full — no flag or input mode represents a
  genuine user-issued slash command distinct from plain text. The one
  related flag, `--disable-slash-commands`, only turns skills off entirely
  (the opposite direction).
- The `claude` CLI ships as a single compiled binary (no separate JS
  source), so internal logic was inspected via strings extracted from that
  binary — a read-only diagnostic, nothing executed or modified. This
  surfaced the actual mechanism: a real interactive slash command is
  converted, before a message is ever constructed, into a special `isMeta`
  message wrapped in `<command-name>`, `<command-message>`,
  `<command-args>` tags. That conversion happens in the terminal's own
  input-handling layer. Text sent via `-p` or `--input-format stream-json`
  never passes through that layer — it always arrives as ordinary content,
  so the model has to act on it itself, and gets refused.
- The refusal is a hard-coded reason code (`disable_model_invocation`)
  checked against the skill's own frontmatter, with an explicit message:
  *"Ask the user to run /x … It cannot be invoked via the Skill tool … by
  typing it themselves."*
- `settings.json` has a `skillOverrides` key, which looked promising, but
  every reference to it in the binary only adds *more* restriction
  (`override_disabled`, *"is disabled via skillOverrides"*). It is a
  kill-switch for skills, not an enable-switch — there is no field or code
  path that uses it to force `disable-model-invocation` back to `false`.
  This avenue is a confirmed dead end.

**Conclusion:** no combination of prompt phrasing, CLI flag, or settings key
gives a headless caller genuine manual-invocation semantics for a skill whose
frontmatter sets `disable-model-invocation: true`. This is a deliberate
boundary between interactive and programmatic callers, not an oversight.

### 3.4 The fix: one-time startup copy with the flag stripped

At GoApp startup, before any sessions are launched, GoApp copies every
manual-invocation skill (i.e. every skill folder whose `SKILL.md` sets
`disable-model-invocation: true`) to a separate, shared location, and in the
copy only, rewrites that frontmatter field to `false`. All headless sessions
GoApp launches are pointed at this shared location (in addition to, or
instead of, the standard discovery paths, per implementation), so these
skills become normally auto-invocable from plain prompts — matching the
behavior already proven in 3.2.

The original skill files, wherever they live (`~/.claude/skills`, project
`.claude/skills`, etc.), are left completely untouched. The interactive TUI
continues to see and enforce the real flag; only GoApp's headless copies have
it stripped.

The destination directory for this copy is a new `config.json` key,
`skillsCopyDir` (camelCase, consistent with GoApp's existing config fields
like `watchFolder` and `permissionMode`), rather than a hardcoded path.
Default value: `/tmp/ccotr-skills`. This keeps the location operator-visible
and changeable (e.g. if `/tmp` is unsuitable on a given machine) without a
code change, following the same pattern GoApp already uses for other
filesystem paths in config (`watchFolder`).

**Evidence this works (session 00015, live test on `grill-me`):**

1. Backed up the real `~/.agents/skills/grill-me/SKILL.md` (sha256 recorded).
2. Edited the copy in place: `disable-model-invocation: true` → `false`.
3. Made it discoverable to a fresh headless session (symlinked into
   `~/.claude/skills/grill-me` — required regardless, since `grill-me`'s real
   location is outside CC's discovery paths).
4. Ran the unmodified GoApp-style invocation:
   `claude -p "I want a relentless interview to sharpen a plan or design. \
   Here's my plan: '...'. Grill me on it." --output-format stream-json \
   --verbose --permission-mode bypassPermissions --add-dir /tmp`
5. Result: the model auto-invoked `Skill(skill: "grill-me", ...)` from plain
   natural language — no slash syntax, no refusal — and proceeded with the
   skill's actual behavior.
6. Restored the original file from backup; verified byte-identical via
   sha256; removed the temporary symlink.

The flip is read by the runtime as authoritative with no caching or
session-start snapshotting that would make it unreliable — matching what 3.2
already showed for unflagged skills.

### 3.5 No locking mechanism needed

The copy-and-strip step runs exactly once, at GoApp process startup, before
any `claude` subprocess is launched. There is no writer to the copied files
after that point — sessions only *read* from the shared copy location. This
removes the concurrency hazard that would otherwise exist if the flag were
flipped per-invocation on a shared/original file (a design considered and
rejected during investigation, see §4): with a startup-only copy, there is no
window in which one session's flip/restore could race another session's read
of the same file, so no per-skill mutex, file lock, or serialization is
required.

## 4. Alternatives considered and rejected

- **GoApp statically enumerates/scans skill folders as a maintained config
  key.** Rejected per §3.1 — duplicates CC's own discovery logic, risks
  staleness in a long-running daemon, and isn't needed since a per-request
  ask to CC gets the same answer.
- **Flip the `disable-model-invocation` flag on the real skill file, per
  invocation, then restore it after the subprocess exits.** Works
  mechanically (proven in §3.4's live test), but was rejected as the
  production design because it mutates a file the interactive TUI and other
  concurrent GoApp sessions may be reading at the same time — `config.json`
  sets `maxConcurrentSessions: 10`, so two concurrent invocations of the same
  manual-invocation skill, or a concurrent interactive use, would race the
  flip/restore. It would require a per-skill lock to be correct. The
  one-time-startup-copy design (§3.4) achieves the same effect without ever
  touching the original file and without any concurrency hazard.
- **Puppet a genuine slash-command invocation from GoApp.** Rejected per
  §3.3 — confirmed unreachable from any documented or undocumented CLI/SDK
  surface; not a viable engineering path regardless of effort.
- **`skillOverrides` in `settings.json`.** Rejected per §3.3 — confirmed to
  only support disabling skills further, not re-enabling model invocation.

## 5. Summary of behavior after this change

| Skill type | Mechanism | Status |
|---|---|---|
| Auto-invocable (no flag) | Existing `claude -p` invocation, unmodified | Already works — no change |
| Manual-invocation-only (`disable-model-invocation: true`) | Startup-time copy to shared location with flag stripped; sessions invoke the copy via plain prompts | Requires the copy step (this HLD) |
| Skill listing | Per-request ask to a `claude -p` session ("what skills are available to you?") | No GoApp-side scanning, ever |

## 6. Open items for implementation (not decided here)

- How the shared copy directory (`skillsCopyDir`) is wired into session
  launch (e.g. an additional `--add-dir`/skills path vs. placing the copy
  directly under a path CC already discovers).
- How GoApp identifies which installed skills currently set
  `disable-model-invocation: true` at startup (a lightweight frontmatter
  scan at startup only, not the ongoing enumeration rejected in §3.1 — this
  is a one-time bootstrap step, not a maintained discovery subsystem).
- Refresh strategy if a skill's manual-invocation status changes while
  GoApp is running (acceptable to require a GoApp restart, given this is a
  startup-only step per §3.5).
