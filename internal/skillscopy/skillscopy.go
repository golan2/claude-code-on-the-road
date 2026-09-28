// Package skillscopy implements the periodic skill-copy mechanism decided in
// HLD-skills-support.md: every skill found under a configured set of source
// roots is mirrored into a shared directory, with disable-model-invocation
// unconditionally stripped in each copy, so a headless `claude -p` session
// can force-invoke any skill by name — including ones the source flags
// manual-invocation-only — without ever touching the original skill files.
package skillscopy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

const stateFileName = "skills_state.json"

const skillManifestName = "SKILL.md"

// disableModelInvocationPattern matches a SKILL.md frontmatter line setting
// disable-model-invocation to true. Only ever rewritten when present — a
// skill that doesn't set the field at all is already invocable, so nothing
// needs adding.
var disableModelInvocationPattern = regexp.MustCompile(`(?m)^disable-model-invocation:\s*true\s*$`)

// fileState is what Sync remembers about one previously-copied source file,
// keyed by that file's absolute source path in state.Files.
type fileState struct {
	SourceModTimeUnixNano int64  `json:"sourceModTimeUnixNano"`
	DestPath              string `json:"destPath"`
}

// state is the on-disk shape of skills_state.json, GoApp's baseline for
// change detection. It lives inside CopyDir itself, alongside the copies it
// describes — never in GoApp's own install folder, and never in the
// Drive-synced watchFolder.
type state struct {
	Files map[string]fileState `json:"files"`
}

// Config holds the two settings Sync needs from the application's own
// config.Config, kept narrow here so this package doesn't import config and
// create a dependency cycle.
type Config struct {
	SkillPaths []string // manually-curated source roots to copy skills from
	CopyDir    string   // destination directory; also state.Files' home
}

// Sync performs one full copy pass: discover every skill under cfg.SkillPaths,
// copy any file that's new or changed (by source mtime) since the last
// successful pass, strip disable-model-invocation in every copied SKILL.md,
// and remove copies whose source file no longer exists. It logs a one-line
// summary via logf (which callers typically wire to log.Printf) and returns
// an error only for a failure severe enough to abort the whole pass (e.g.
// CopyDir itself can't be created) — a single file's copy or stat failure is
// logged and skipped, not fatal to the rest of the pass.
func Sync(cfg Config, logf func(format string, args ...any)) error {
	if len(cfg.SkillPaths) == 0 {
		return nil
	}
	if err := os.MkdirAll(cfg.CopyDir, 0o755); err != nil {
		return fmt.Errorf("skillscopy: create copy dir %q: %w", cfg.CopyDir, err)
	}

	st, err := loadState(cfg.CopyDir)
	if err != nil {
		return fmt.Errorf("skillscopy: load state: %w", err)
	}

	rootIdentifiers := assignRootIdentifiers(cfg.SkillPaths)

	seen := make(map[string]bool)
	copied, skipped, failed := 0, 0, 0

	for _, root := range cfg.SkillPaths {
		skillDirs, err := discoverSkillDirs(root)
		if err != nil {
			logf("ERROR: skillscopy: discover skills under %q: %v", root, err)
			continue
		}
		for _, skillDir := range skillDirs {
			relSkill, err := filepath.Rel(root, skillDir)
			if err != nil {
				logf("ERROR: skillscopy: %v", err)
				continue
			}
			destSkillDir := filepath.Join(cfg.CopyDir, rootIdentifiers[root], relSkill)

			err = filepath.WalkDir(skillDir, func(srcFile string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				relFile, err := filepath.Rel(skillDir, srcFile)
				if err != nil {
					return nil
				}
				destFile := filepath.Join(destSkillDir, relFile)
				seen[srcFile] = true

				info, err := os.Stat(srcFile)
				if err != nil {
					logf("ERROR: skillscopy: stat %q: %v", srcFile, err)
					failed++
					return nil
				}
				modNano := info.ModTime().UnixNano()

				if existing, ok := st.Files[srcFile]; ok && existing.SourceModTimeUnixNano == modNano && existing.DestPath == destFile {
					skipped++
					return nil
				}

				if err := copyOneFile(srcFile, destFile, filepath.Base(srcFile) == skillManifestName); err != nil {
					logf("ERROR: skillscopy: copy %q -> %q: %v", srcFile, destFile, err)
					failed++
					return nil
				}
				st.Files[srcFile] = fileState{SourceModTimeUnixNano: modNano, DestPath: destFile}
				copied++
				return nil
			})
			if err != nil {
				logf("ERROR: skillscopy: walk %q: %v", skillDir, err)
			}
		}
	}

	removed := removeStale(st, seen, logf)
	removeEmptyDirs(cfg.CopyDir, logf)

	if err := saveState(cfg.CopyDir, st); err != nil {
		return fmt.Errorf("skillscopy: save state: %w", err)
	}

	if copied > 0 || removed > 0 {
		logf("skillscopy: copied %d, skipped %d (unchanged), removed %d, failed %d", copied, skipped, removed, failed)
	}
	return nil
}

// assignRootIdentifiers picks the directory-safe identifier each source root
// is mirrored under inside CopyDir, preserving hierarchy so two roots that
// happen to share a basename (e.g. two different "skills" folders) don't
// collide. The common case (distinctly-named roots) gets a plain, readable
// basename; a collision gets a numeric suffix by order of appearance.
func assignRootIdentifiers(roots []string) map[string]string {
	ids := make(map[string]string, len(roots))
	counts := make(map[string]int)
	for _, root := range roots {
		base := filepath.Base(root)
		counts[base]++
		if counts[base] == 1 {
			ids[root] = base
		} else {
			ids[root] = fmt.Sprintf("%s-%d", base, counts[base])
		}
	}
	return ids
}

// discoverSkillDirs finds every skill directory under root — one directly
// containing SKILL.md. It does not descend into a found skill looking for
// further nested skills; a skill's own subdirectories are supporting files
// (scripts, references), not more skills.
func discoverSkillDirs(root string) ([]string, error) {
	if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var skillDirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if _, err := os.Stat(filepath.Join(path, skillManifestName)); err == nil {
			skillDirs = append(skillDirs, path)
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return skillDirs, nil
}

// copyOneFile copies srcFile to destFile via write-temp-then-rename, so a
// concurrently-running claude session under CopyDir never observes a
// half-written file. When stripManifest is true (the file is a SKILL.md),
// disable-model-invocation is unconditionally rewritten to false in the
// copy's content before it's written.
func copyOneFile(srcFile, destFile string, stripManifest bool) error {
	content, err := os.ReadFile(srcFile)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if stripManifest {
		content = disableModelInvocationPattern.ReplaceAll(content, []byte("disable-model-invocation: false"))
	}

	if err := os.MkdirAll(filepath.Dir(destFile), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	tmp := destFile + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := os.WriteFile(tmp, content, 0o644); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, destFile); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// removeStale deletes every copy whose source file was not seen in this
// pass (the source was deleted, or its root was removed from config) and
// drops the matching state entry. Returns the number of files removed.
func removeStale(st *state, seen map[string]bool, logf func(format string, args ...any)) int {
	removed := 0
	for src, fs := range st.Files {
		if seen[src] {
			continue
		}
		if err := os.Remove(fs.DestPath); err != nil && !os.IsNotExist(err) {
			logf("ERROR: skillscopy: remove stale copy %q: %v", fs.DestPath, err)
		}
		delete(st.Files, src)
		removed++
	}
	return removed
}

// removeEmptyDirs prunes directories under copyDir left empty by
// removeStale, walking bottom-up so a skill folder emptied of all its files
// is removed too, not just left as an empty shell. copyDir itself and
// skills_state.json are never touched.
func removeEmptyDirs(copyDir string, logf func(format string, args ...any)) {
	var dirs []string
	_ = filepath.WalkDir(copyDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == copyDir {
			return nil
		}
		dirs = append(dirs, path)
		return nil
	})
	// Deepest paths first, so a parent only gets considered after its
	// children have already been removed if they were empty too.
	for i := len(dirs) - 1; i >= 0; i-- {
		entries, err := os.ReadDir(dirs[i])
		if err != nil || len(entries) > 0 {
			continue
		}
		if err := os.Remove(dirs[i]); err != nil {
			logf("ERROR: skillscopy: remove empty dir %q: %v", dirs[i], err)
		}
	}
}

func loadState(copyDir string) (*state, error) {
	data, err := os.ReadFile(filepath.Join(copyDir, stateFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &state{Files: make(map[string]fileState)}, nil
		}
		return nil, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", stateFileName, err)
	}
	if st.Files == nil {
		st.Files = make(map[string]fileState)
	}
	return &st, nil
}

func saveState(copyDir string, st *state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	path := filepath.Join(copyDir, stateFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bytes.TrimSpace(data), 0o644); err != nil {
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename state: %w", err)
	}
	return nil
}
