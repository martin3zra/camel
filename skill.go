package camel

import (
	"embed"
	"errors"
	"os"
	"path/filepath"
)

//go:embed skills/db-scaffold/SKILL.md skills/db-scaffold/db-scaffold.mdc
var dbScaffoldFS embed.FS

// SkillFile is one file the db-scaffold skill installs into a target
// project, mapping an embedded source path to a destination relative to the
// project root.
type SkillFile struct {
	Source string
	Dest   string
}

// DBScaffoldFiles lists every file InstallDBScaffoldSkill writes. The same
// SKILL.md is installed for both Claude Code and Cursor (Cursor supports the
// identical Agent Skill format); the .mdc is a thin Cursor rule pointing at
// it, kept as a fallback for Cursor setups that only honor rules.
func DBScaffoldFiles() []SkillFile {
	return []SkillFile{
		{Source: "skills/db-scaffold/SKILL.md", Dest: filepath.Join(".claude", "skills", "db-scaffold", "SKILL.md")},
		{Source: "skills/db-scaffold/SKILL.md", Dest: filepath.Join(".cursor", "skills", "db-scaffold", "SKILL.md")},
		{Source: "skills/db-scaffold/db-scaffold.mdc", Dest: filepath.Join(".cursor", "rules", "db-scaffold.mdc")},
	}
}

// InstallDBScaffoldSkill writes the bundled db-scaffold skill into dir (a
// target project root). Files that already exist are left untouched and
// reported as skipped unless force is true.
func InstallDBScaffoldSkill(dir string, force bool) (written, skipped []string, err error) {
	for _, f := range DBScaffoldFiles() {
		content, readErr := dbScaffoldFS.ReadFile(f.Source)
		if readErr != nil {
			return written, skipped, readErr
		}

		dest := filepath.Join(dir, f.Dest)
		if !force {
			if _, statErr := os.Stat(dest); statErr == nil {
				skipped = append(skipped, f.Dest)
				continue
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return written, skipped, statErr
			}
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return written, skipped, err
		}
		if err := os.WriteFile(dest, content, 0644); err != nil {
			return written, skipped, err
		}
		written = append(written, f.Dest)
	}
	return written, skipped, nil
}
