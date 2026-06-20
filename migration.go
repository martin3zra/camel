package camel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

type MigrationFile struct {
	Up   map[string]TableIntent `json:"up" yaml:"up"`
	Down map[string]TableIntent `json:"down" yaml:"down"`
}

type TableIntent struct {
	Table  string `json:"table,omitempty" yaml:"table,omitempty"`
	Action string `json:"action,omitempty" yaml:"action,omitempty"`

	// create
	Columns map[string]string `json:"columns,omitempty" yaml:"columns,omitempty"`
	Indexes []IndexIntent     `json:"indexes,omitempty" yaml:"indexes,omitempty"`
	Foreign []ForeignIntent   `json:"foreign,omitempty" yaml:"foreign,omitempty"`

	// raw: statements executed verbatim, in order (action: raw)
	Statements []string `json:"statements,omitempty" yaml:"statements,omitempty"`

	// alter
	AddColumns    map[string]string `json:"add_columns,omitempty" yaml:"add_columns,omitempty"`
	DropColumns   []string          `json:"drop_columns,omitempty" yaml:"drop_columns,omitempty"`
	ModifyColumns map[string]string `json:"modify_columns,omitempty" yaml:"modify_columns,omitempty"`
	RenameColumns map[string]string `json:"rename_columns,omitempty" yaml:"rename_columns,omitempty"`
	AddIndexes    []IndexIntent     `json:"add_indexes,omitempty" yaml:"add_indexes,omitempty"`
	DropIndexes   []string          `json:"drop_indexes,omitempty" yaml:"drop_indexes,omitempty"`
	AddForeign    []ForeignIntent   `json:"add_foreign,omitempty" yaml:"add_foreign,omitempty"`
	DropForeign   []string          `json:"drop_foreign,omitempty" yaml:"drop_foreign,omitempty"`
}

// IndexIntent describes a CREATE INDEX.
type IndexIntent struct {
	Name    string   `json:"name" yaml:"name"`
	Columns []string `json:"columns" yaml:"columns"`
	Unique  bool     `json:"unique,omitempty" yaml:"unique,omitempty"`
}

// ForeignIntent describes a foreign-key constraint added via ALTER.
type ForeignIntent struct {
	Name       string   `json:"name" yaml:"name"`
	Columns    []string `json:"columns" yaml:"columns"`
	RefTable   string   `json:"references_table" yaml:"references_table"`
	RefColumns []string `json:"references_columns" yaml:"references_columns"`
	OnDelete   string   `json:"on_delete,omitempty" yaml:"on_delete,omitempty"`
	OnUpdate   string   `json:"on_update,omitempty" yaml:"on_update,omitempty"`
}

type Migration struct {
	Path string
	Name string
	File MigrationFile
}

type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

func LoadMigration(path string) (Migration, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Migration{}, err
	}

	var file MigrationFile
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		err = json.Unmarshal(content, &file)
	default:
		err = yaml.Unmarshal(content, &file)
	}
	if err != nil {
		return Migration{}, fmt.Errorf("parse migration %s: %w", path, err)
	}

	return Migration{Path: path, Name: filepath.Base(path), File: file}, nil
}

func ListMigrations(cfg Config, dir string) ([]Migration, error) {
	pattern := filepath.Join(dir, cfg.Migration.Directory, cfg.Migration.Pattern)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	if strings.HasSuffix(cfg.Migration.Pattern, ".yaml") {
		jsonPattern := filepath.Join(dir, cfg.Migration.Directory, strings.TrimSuffix(cfg.Migration.Pattern, ".yaml")+".json")
		jsonPaths, err := filepath.Glob(jsonPattern)
		if err != nil {
			return nil, err
		}
		paths = append(paths, jsonPaths...)
	}
	sort.Strings(paths)

	migrations := make([]Migration, 0, len(paths))
	for _, path := range paths {
		migration, err := LoadMigration(path)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}
	return migrations, nil
}

func CreateMigrationFile(dir string, cfg Config, name string, format string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("migration name is required")
	}
	if format == "" {
		format = "yaml"
	}
	format = strings.TrimPrefix(strings.ToLower(format), ".")
	if format != "yaml" && format != "json" {
		return "", fmt.Errorf("unsupported migration format %q", format)
	}

	migrationDir := filepath.Join(dir, cfg.Migration.Directory)
	if err := os.MkdirAll(migrationDir, 0755); err != nil {
		return "", err
	}

	cleanName := cleanMigrationName(name)
	filename := fmt.Sprintf("%s_%s.%s", time.Now().Format("20060102150405"), cleanName, format)
	path := filepath.Join(migrationDir, filename)

	key, table, action := deriveScaffold(cleanName)

	var content string
	if format == "json" {
		content = sampleMigrationJSON(key, table, action)
	} else {
		content = sampleMigrationYAML(filename, key, table, action)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
}

func StatementsFor(migration Migration, direction Direction, driver string) ([]string, error) {
	intents := migration.File.Up
	if direction == DirectionDown {
		intents = migration.File.Down
	}

	// Always derive ordering from the up intents, which carry FK dependency
	// info. Down intents (drop/alter) omit foreign keys, so ordering them
	// directly would miss dependencies and produce the wrong drop order on
	// engines that enforce FK constraints (SQL Server, Postgres).
	orderSource := migration.File.Up
	if len(orderSource) == 0 {
		orderSource = intents
	}

	var statements []string
	for _, key := range orderIntents(orderSource, direction) {
		intent, ok := intents[key]
		if !ok {
			continue
		}
		sql, err := GenerateSQL(key, intent, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, sql...)
	}
	return statements, nil
}

// orderIntents returns the intent keys ordered so that a table referenced by a
// foreign key is created before the table that references it. The base order is
// alphabetical (stable, deterministic); foreign-key edges within the same
// migration override it. For the down direction the order is reversed, so
// dependent tables are dropped before the tables they point at.
func orderIntents(intents map[string]TableIntent, direction Direction) []string {
	keys := make([]string, 0, len(intents))
	for key := range intents {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	// Map resolved table name -> key, so FK references (which name tables, not
	// keys) can be matched back to the intent that defines them.
	tableToKey := make(map[string]string, len(keys))
	for _, key := range keys {
		tableToKey[resolveTable(key, intents[key])] = key
	}

	// deps[key] = set of keys that must come first.
	deps := make(map[string]map[string]bool, len(keys))
	for _, key := range keys {
		deps[key] = map[string]bool{}
		intent := intents[key]
		for _, fk := range append(append([]ForeignIntent{}, intent.Foreign...), intent.AddForeign...) {
			if ref, ok := tableToKey[fk.RefTable]; ok && ref != key {
				deps[key][ref] = true
			}
		}
	}

	// Stable topological sort (Kahn over alphabetical keys). On a cycle the
	// remaining keys fall back to alphabetical order.
	resolved := make(map[string]bool, len(keys))
	order := make([]string, 0, len(keys))
	for len(order) < len(keys) {
		progressed := false
		for _, key := range keys {
			if resolved[key] {
				continue
			}
			ready := true
			for dep := range deps[key] {
				if !resolved[dep] {
					ready = false
					break
				}
			}
			if ready {
				resolved[key] = true
				order = append(order, key)
				progressed = true
			}
		}
		if !progressed { // cycle: append the rest in alphabetical order
			for _, key := range keys {
				if !resolved[key] {
					resolved[key] = true
					order = append(order, key)
				}
			}
		}
	}

	if direction == DirectionDown {
		for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
			order[i], order[j] = order[j], order[i]
		}
	}
	return order
}

func resolveTable(key string, intent TableIntent) string {
	if intent.Table != "" {
		return intent.Table
	}
	return pluralize(key)
}

func cleanMigrationName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	re := regexp.MustCompile(`[^a-z0-9_]+`)
	name = re.ReplaceAllString(name, "")
	name = strings.Trim(name, "_")
	if name == "" {
		return "migration"
	}
	return name
}

// deriveScaffold guesses the intent from a cleaned migration name, following
// Laravel-style conventions:
//
//	create_posts_table        -> create table posts
//	add_views_to_posts_table  -> alter table posts
//	update_posts_table        -> alter table posts
//	<anything else>           -> create table <name minus _table>, key = name
//
// The returned key is the YAML map key (logical name) for the intent.
func deriveScaffold(clean string) (key, table, action string) {
	if t, ok := strings.CutPrefix(clean, "create_"); ok {
		if t2, ok := strings.CutSuffix(t, "_table"); ok {
			return singularKey(t2), t2, "create"
		}
	}
	if idx := strings.LastIndex(clean, "_to_"); idx != -1 {
		rest := clean[idx+len("_to_"):]
		if t, ok := strings.CutSuffix(rest, "_table"); ok && t != "" {
			return singularKey(t), t, "alter"
		}
		if rest != "" {
			return singularKey(rest), rest, "alter"
		}
	}
	for _, prefix := range []string{"update_", "alter_", "modify_", "change_"} {
		if t, ok := strings.CutPrefix(clean, prefix); ok {
			t = strings.TrimSuffix(t, "_table")
			if t != "" {
				return singularKey(t), t, "alter"
			}
		}
	}
	table = strings.TrimSuffix(clean, "_table")
	if table == "" {
		table = clean
	}
	return singularKey(table), table, "create"
}

// singularKey trims a trailing plural "s" so the map key reads naturally
// (posts -> post). Cosmetic only; the table name is set explicitly.
func singularKey(table string) string {
	if len(table) > 1 && strings.HasSuffix(table, "s") {
		return strings.TrimSuffix(table, "s")
	}
	return table
}

func sampleMigrationYAML(filename, key, table, action string) string {
	header := fmt.Sprintf(`# Migration: %s
# Inspired by Laravel Shift Blueprint's type syntax:
#   column: type:attribute modifier modifier:value
`, filename)

	if action == "alter" {
		return header + fmt.Sprintf(`
up:
  %s:
    action: alter
    table: %s
    add_columns:
      # new_column: string:150 nullable
    # modify_columns:
    #   existing_column: string:200
    # rename_columns:
    #   old_name: new_name
    # add_indexes:
    #   - name: %s_idx
    #     columns: [column]
    #     unique: true
    # add_foreign:
    #   - name: %s_fk
    #     columns: [other_id]
    #     references_table: others
    #     references_columns: [id]
    #     on_delete: cascade

down:
  %s:
    action: alter
    table: %s
    drop_columns:
      # - new_column
`, key, table, table, table, key, table)
	}

	return header + fmt.Sprintf(`
up:
  %s:
    action: create
    table: %s
    columns:
      id: id
      # title: string:150
      created_at: timestamp useCurrent
      updated_at: timestamp nullable

down:
  %s:
    action: drop
    table: %s
`, key, table, key, table)
}

func sampleMigrationJSON(key, table, action string) string {
	if action == "alter" {
		return fmt.Sprintf(`{
  "up": {
    "%s": {
      "action": "alter",
      "table": "%s",
      "add_columns": {}
    }
  },
  "down": {
    "%s": {
      "action": "alter",
      "table": "%s",
      "drop_columns": []
    }
  }
}
`, key, table, key, table)
	}

	return fmt.Sprintf(`{
  "up": {
    "%s": {
      "action": "create",
      "table": "%s",
      "columns": {
        "id": "id",
        "created_at": "timestamp useCurrent",
        "updated_at": "timestamp nullable"
      }
    }
  },
  "down": {
    "%s": {
      "action": "drop",
      "table": "%s"
    }
  }
}
`, key, table, key, table)
}
