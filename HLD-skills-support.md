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

### 3.4 The fix: periodic copy of ALL skills into `skillsCopyDir`, with `disable-model-invocation` stripped unconditionally

GoApp copies **every** discoverable skill — not just the ones flagged
`disable-model-invocation: true` — from its known source paths into a
separate, shared directory, and in every copy, unconditionally rewrites
`disable-model-invocation` to `false`, regardless of what the source set it
to. Copying everything and stripping the field unconditionally means the
copy step needs no conditional logic branching on the flag's value — it's a
uniform operation applied to every skill — and it has a useful side effect:
any skill becomes force-invocable by name from a headless session, even one
that was already auto-invocable at the source, in addition to that skill's
normal description-triggered auto-invocation continuing to work unaffected.

**Hierarchy is preserved, not flattened.** Each source root is mirrored
under `skillsCopyDir` (e.g.
`skillsCopyDir/<source-root-identifier>/<skill-name>/SKILL.md`) rather than
dumping every skill's files into one flat directory. This means two
differently-sourced skills that happen to share a name do not collide, and
which source produced a given copy stays visible from its path alone.

**The copy runs at GoApp startup, and periodically thereafter** (interval to
be chosen at implementation time), so edits, additions, and newly-flagged
skills at the source are picked up without requiring a GoApp restart. This
supersedes the earlier revision of this HLD, which specified a one-time,
startup-only copy limited to flagged skills — see §4 for why that was
superseded.

**Change detection** is mtime-based, tracked in a state file,
`skills_state.json`, that GoApp maintains **inside `skillsCopyDir` itself**
(alongside the copied skills — not in GoApp's own install folder, and not
inside the Google-Drive-synced `watchFolder`). It maps each source skill
file's path to the last modification time GoApp saw for it, **read from the
source file**, never from the copy — the copy's own mtime is always
freshly-written right after GoApp touches it, so comparing against the copy
would make every file look changed on every single pass. On each periodic
check, GoApp:

1. Stats every known source skill file and compares its current mtime
   against the baseline recorded in `skills_state.json` (a file absent from
   the baseline — new or never-copied — counts as changed).
2. Re-copies and re-strips only the files that changed.
3. Updates `skills_state.json`'s entry for each file **after** its copy
   succeeds, so a crash mid-cycle just means that file is safely re-copied
   (idempotent) on the next pass rather than corrupting the baseline.

The original skill files, wherever they live (`~/.claude/skills`, project
`.claude/skills`, etc.), are left completely untouched by all of this. The
interactive TUI continues to see and enforce the real flag; only the
copies under `skillsCopyDir` have it stripped.

The destination directory is the `config.json` key `skillsCopyDir`
(camelCase, consistent with GoApp's existing config fields like
`watchFolder` and `permissionMode`), rather than a hardcoded path. Default
value: `/tmp/ccotr-skills`. This keeps the location operator-visible and
changeable (e.g. if `/tmp` is unsuitable on a given machine) without a code
change, following the same pattern GoApp already uses for other filesystem
paths in config (`watchFolder`).

**Evidence the strip mechanism itself works (session 00015, live test on
`grill-me`):** this validates that rewriting `disable-model-invocation` to
`false` in a skill's `SKILL.md` reliably changes runtime behavior — the
premise this whole design depends on, independent of whether the strip is
applied once or on a recurring schedule.

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

### 3.5 Concurrency: single writer, changed-files-only, atomic replace

GoApp is the only writer to `skillsCopyDir` — every `claude` session GoApp
launches only *reads* from it, so there is no cross-process coordination
problem of the kind the per-invocation flip-and-restore alternative would
have had (see §4). Because each periodic pass only touches files whose
*source* mtime changed since the last successful baseline update, steady-state
write volume is small and mostly idle.

Two implementation-level precautions (not requiring a locking mechanism, but
worth calling out explicitly) keep this safe under GoApp's own concurrency
(`config.json`'s `maxConcurrentSessions: 10` may have several sessions
reading `skillsCopyDir` while a periodic copy pass is in flight):

- Write each copied file via write-temp-then-rename inside `skillsCopyDir`,
  so an in-flight `claude` session never observes a half-written `SKILL.md`.
- Update `skills_state.json`'s entry for a file only after that file's copy
  has landed, so the baseline never claims a copy is current before it
  actually is.

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
  copy-based design (§3.4) achieves the same effect without ever touching the
  original file and without that concurrency hazard.
- **One-time, startup-only copy limited to just the flagged skills**
  (the design in the previous revision of this HLD). Superseded by §3.4's
  periodic, copy-everything approach for two reasons: (1) a startup-only copy
  can't notice a skill that gets newly flagged, edited, added, or removed
  after GoApp launches without a full GoApp restart; (2) limiting the copy to
  flagged skills only required GoApp to parse every source skill's
  frontmatter first to decide what to copy — extra logic in exchange for no
  real benefit, since copying every skill and unconditionally stripping the
  field is simpler and also gives every skill a guaranteed by-name invocation
  path as a side effect.
- **Puppet a genuine slash-command invocation from GoApp.** Rejected per
  §3.3 — confirmed unreachable from any documented or undocumented CLI/SDK
  surface; not a viable engineering path regardless of effort.
- **`skillOverrides` in `settings.json`.** Rejected per §3.3 — confirmed to
  only support disabling skills further, not re-enabling model invocation.

## 5. Summary of behavior after this change

| Skill type (as authored at the source) | Mechanism | Status |
|---|---|---|
| Any skill — auto-invocable or manual-invocation-only alike | Periodically copied (startup, then on an interval) into `skillsCopyDir`, source hierarchy preserved, `disable-model-invocation` stripped to `false` in every copy; sessions can trigger any copied skill both by description-matching and by name via a plain prompt | New copy mechanism (this HLD) |
| Skill listing | Per-request ask to a `claude -p` session ("what skills are available to you?") | No GoApp-side scanning, ever |

## 6. Open items for implementation (not decided here)

- How the shared copy directory (`skillsCopyDir`) is wired into session
  launch (e.g. an additional `--add-dir`/skills path vs. placing the copy
  directly under a path CC already discovers) — still open; everything else
  about the copy mechanism itself (what gets copied, when, and how changes
  are detected) is decided in §3.4.
- The periodic re-copy interval (seconds/minutes) is not yet chosen.
- Deletion handling: §3.4's mtime comparison naturally picks up new and
  modified source files, but doesn't by itself detect a skill folder that
  was *removed* from a source path — `skills_state.json` would keep
  referencing a file that no longer exists. Whether stale copies (and their
  baseline entries) get cleaned up, and on what trigger, is not yet decided.
