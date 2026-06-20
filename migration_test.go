package camel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleYAML = `up:
  post:
    action: create
    table: posts
    columns:
      id: id
      title: string:150
      slug: string:180 unique
down:
  post:
    action: drop
    table: posts
`

const sampleJSON = `{
  "up": {"post": {"action": "create", "table": "posts", "columns": {"id": "id", "title": "string:150"}}},
  "down": {"post": {"action": "drop", "table": "posts"}}
}`

func TestLoadMigrationYAMLAndJSON(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "1_create.yaml")
	jsonPath := filepath.Join(dir, "2_create.json")
	mustWrite(t, yamlPath, sampleYAML)
	mustWrite(t, jsonPath, sampleJSON)

	for _, path := range []string{yamlPath, jsonPath} {
		m, err := LoadMigration(path)
		if err != nil {
			t.Fatalf("LoadMigration(%s): %v", path, err)
		}
		up, ok := m.File.Up["post"]
		if !ok {
			t.Fatalf("%s: missing up.post intent", path)
		}
		if up.Action != "create" || up.Table != "posts" {
			t.Fatalf("%s: unexpected intent %+v", path, up)
		}
		if up.Columns["title"] != "string:150" {
			t.Fatalf("%s: title column = %q", path, up.Columns["title"])
		}
	}
}

func TestLoadMigrationSQL(t *testing.T) {
	dir := t.TempDir()
	content := `-- Camel schema dump
-- Driver: sqlite

CREATE TABLE "posts" (
  "id" INTEGER NOT NULL PRIMARY KEY
);
CREATE UNIQUE INDEX "posts_slug_uq" ON "posts" ("slug");
`
	path := filepath.Join(dir, "00000000000000_schema_dump.sql")
	mustWrite(t, path, content)

	m, err := LoadMigration(path)
	if err != nil {
		t.Fatalf("LoadMigration: %v", err)
	}
	intent, ok := m.File.Up["schema"]
	if !ok {
		t.Fatal("expected up.schema intent")
	}
	if intent.Action != "raw" {
		t.Fatalf("expected action raw, got %q", intent.Action)
	}
	if len(intent.Statements) != 2 {
		t.Fatalf("want 2 statements (comments stripped), got %d: %v", len(intent.Statements), intent.Statements)
	}
	// No -- down section → down block is empty raw intent
	downIntent := m.File.Down["schema"]
	if len(downIntent.Statements) != 0 {
		t.Fatalf("no -- down section: want 0 down statements, got %d", len(downIntent.Statements))
	}
}

func TestListMigrationsPicksUpSQLFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Migration: MigrationConfig{Directory: "db", Pattern: "*.yaml"}}
	os.MkdirAll(filepath.Join(dir, "db"), 0755)

	mustWrite(t, filepath.Join(dir, "db", "00000000000000_schema_dump.sql"), "CREATE TABLE x (id INTEGER);\n")
	mustWrite(t, filepath.Join(dir, "db", "20260101_add_y.yaml"), sampleYAML)

	migrations, err := ListMigrations(cfg, dir)
	if err != nil {
		t.Fatalf("ListMigrations: %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("want 2 migrations, got %d", len(migrations))
	}
	// .sql file sorts before the yaml (00000... < 20260...)
	if migrations[0].Name != "00000000000000_schema_dump.sql" {
		t.Fatalf("expected dump first, got %q", migrations[0].Name)
	}
}

func TestParseSQLFileSemicolonNoSections(t *testing.T) {
	// Schema dump style: no -- up/down markers, semicolons as separators.
	content := []byte(`-- header comment
-- another comment

CREATE TABLE "posts" ("id" INTEGER NOT NULL PRIMARY KEY);

CREATE UNIQUE INDEX "posts_slug_uq" ON "posts" ("slug");

`)
	up, down := parseSQLFile(content)
	if len(up) != 2 {
		t.Fatalf("want 2 up statements, got %d: %v", len(up), up)
	}
	if !strings.Contains(up[0], "CREATE TABLE") {
		t.Errorf("first stmt should be CREATE TABLE: %s", up[0])
	}
	if !strings.Contains(up[1], "CREATE UNIQUE INDEX") {
		t.Errorf("second stmt should be CREATE UNIQUE INDEX: %s", up[1])
	}
	if len(down) != 0 {
		t.Errorf("no down section: want 0 down statements, got %d", len(down))
	}
}

func TestParseSQLFileWithSections(t *testing.T) {
	// Procedure style: -- up / -- down sections, GO separator.
	content := []byte(`-- Migration: create_my_proc.sql

-- up
CREATE PROCEDURE my_proc()
BEGIN
  UPDATE posts SET slug = LOWER(slug) WHERE slug IS NULL;
  UPDATE posts SET updated_at = NOW();
END
GO

-- down
DROP PROCEDURE IF EXISTS my_proc
GO
`)
	up, down := parseSQLFile(content)
	if len(up) != 1 {
		t.Fatalf("want 1 up statement (the full procedure), got %d: %v", len(up), up)
	}
	if !strings.Contains(up[0], "CREATE PROCEDURE") {
		t.Errorf("up must contain CREATE PROCEDURE: %s", up[0])
	}
	if strings.Contains(up[0], "GO") {
		t.Errorf("GO must not appear in the parsed statement: %s", up[0])
	}
	if len(down) != 1 {
		t.Fatalf("want 1 down statement, got %d: %v", len(down), down)
	}
	if !strings.Contains(down[0], "DROP PROCEDURE") {
		t.Errorf("down must contain DROP PROCEDURE: %s", down[0])
	}
}

func TestParseSQLFileGOCaseInsensitive(t *testing.T) {
	content := []byte("-- up\nSELECT 1\ngo\nSELECT 2\nGo\n")
	up, _ := parseSQLFile(content)
	if len(up) != 2 {
		t.Fatalf("GO is case-insensitive: want 2 statements, got %d: %v", len(up), up)
	}
}

func TestStatementsForUpAndDown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1_create.yaml")
	mustWrite(t, path, sampleYAML)
	m, err := LoadMigration(path)
	if err != nil {
		t.Fatalf("LoadMigration: %v", err)
	}

	up, err := StatementsFor(m, DirectionUp, "postgres")
	if err != nil {
		t.Fatalf("up: %v", err)
	}
	if len(up) != 1 || !strings.Contains(up[0], `CREATE TABLE "posts"`) {
		t.Fatalf("up statements = %v", up)
	}

	down, err := StatementsFor(m, DirectionDown, "postgres")
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if len(down) != 1 || down[0] != `DROP TABLE "posts";` {
		t.Fatalf("down statements = %v", down)
	}
}

func TestListMigrationsSortsAndMixesFormats(t *testing.T) {
	dir := t.TempDir()
	migDir := filepath.Join(dir, "database")
	if err := os.MkdirAll(migDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Intentionally out of order on disk; ListMigrations must sort by filename.
	mustWrite(t, filepath.Join(migDir, "20240102_b.yaml"), sampleYAML)
	mustWrite(t, filepath.Join(migDir, "20240101_a.json"), sampleJSON)

	cfg := DefaultConfig()
	migs, err := ListMigrations(cfg, dir)
	if err != nil {
		t.Fatalf("ListMigrations: %v", err)
	}
	if len(migs) != 2 {
		t.Fatalf("want 2 migrations (yaml+json), got %d", len(migs))
	}
	if migs[0].Name != "20240101_a.json" || migs[1].Name != "20240102_b.yaml" {
		t.Fatalf("order = [%s, %s]", migs[0].Name, migs[1].Name)
	}
}

func TestCreateMigrationFile(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()

	yamlPath, err := CreateMigrationFile(dir, cfg, "Create Posts Table!", "yaml")
	if err != nil {
		t.Fatalf("CreateMigrationFile yaml: %v", err)
	}
	if !strings.HasSuffix(yamlPath, "_create_posts_table.yaml") {
		t.Errorf("name not cleaned: %s", yamlPath)
	}
	// Generated stub must itself be loadable and produce SQL.
	m, err := LoadMigration(yamlPath)
	if err != nil {
		t.Fatalf("load generated stub: %v", err)
	}
	if _, err := StatementsFor(m, DirectionUp, "sqlite"); err != nil {
		t.Fatalf("generated stub did not produce SQL: %v", err)
	}

	jsonPath, err := CreateMigrationFile(dir, cfg, "create_posts_table", "json")
	if err != nil {
		t.Fatalf("CreateMigrationFile json: %v", err)
	}
	if !strings.HasSuffix(jsonPath, ".json") {
		t.Errorf("expected .json, got %s", jsonPath)
	}
}

func TestOrderIntentsRespectsForeignKeyDependency(t *testing.T) {
	// "post" references "users"; alphabetically post < user, but users must be
	// created first.
	intents := map[string]TableIntent{
		"post": {
			Action:  "create",
			Table:   "posts",
			Columns: map[string]string{"id": "id", "author_id": "bigInteger"},
			Foreign: []ForeignIntent{{Name: "fk", Columns: []string{"author_id"}, RefTable: "users", RefColumns: []string{"id"}}},
		},
		"user": {
			Action:  "create",
			Table:   "users",
			Columns: map[string]string{"id": "id"},
		},
	}

	up := orderIntents(intents, DirectionUp)
	if len(up) != 2 || up[0] != "user" || up[1] != "post" {
		t.Fatalf("up order = %v, want [user post]", up)
	}

	// Down reverses: drop the dependent (post) before the referenced (user).
	down := orderIntents(intents, DirectionDown)
	if len(down) != 2 || down[0] != "post" || down[1] != "user" {
		t.Fatalf("down order = %v, want [post user]", down)
	}
}

func TestOrderIntentsStableWithoutDependencies(t *testing.T) {
	intents := map[string]TableIntent{
		"zebra": {Action: "create", Columns: map[string]string{"id": "id"}},
		"alpha": {Action: "create", Columns: map[string]string{"id": "id"}},
		"mango": {Action: "create", Columns: map[string]string{"id": "id"}},
	}
	got := orderIntents(intents, DirectionUp)
	want := []string{"alpha", "mango", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestStatementsForOrdersForeignKeyTablesFirst(t *testing.T) {
	dir := t.TempDir()
	body := `up:
  post:
    action: create
    table: posts
    columns: {id: id, author_id: bigInteger}
    foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
  user:
    action: create
    table: users
    columns: {id: id}
`
	path := filepath.Join(dir, "1.yaml")
	mustWrite(t, path, body)
	m, err := LoadMigration(path)
	if err != nil {
		t.Fatalf("LoadMigration: %v", err)
	}
	stmts, err := StatementsFor(m, DirectionUp, "postgres")
	if err != nil {
		t.Fatalf("StatementsFor: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	ui := strings.Index(joined, `CREATE TABLE "users"`)
	pi := strings.Index(joined, `CREATE TABLE "posts"`)
	if ui == -1 || pi == -1 || ui > pi {
		t.Fatalf("users must be created before posts:\n%s", joined)
	}
}

func TestStatementsForDownDropsInFKSafeOrder(t *testing.T) {
	// Down intents are plain `drop` actions with no FK info. The ordering must
	// still be derived from the up intents so dependent tables are dropped
	// before the tables they reference (required by SQL Server / Postgres).
	dir := t.TempDir()
	body := `up:
  post:
    action: create
    table: posts
    columns: {id: id, author_id: bigInteger}
    foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
  user:
    action: create
    table: users
    columns: {id: id}
down:
  post:
    action: drop
    table: posts
  user:
    action: drop
    table: users
`
	path := filepath.Join(dir, "1.yaml")
	mustWrite(t, path, body)
	m, err := LoadMigration(path)
	if err != nil {
		t.Fatalf("LoadMigration: %v", err)
	}
	stmts, err := StatementsFor(m, DirectionDown, "postgres")
	if err != nil {
		t.Fatalf("StatementsFor: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	pi := strings.Index(joined, `DROP TABLE "posts"`)
	ui := strings.Index(joined, `DROP TABLE "users"`)
	if pi == -1 || ui == -1 || pi > ui {
		t.Fatalf("posts must be dropped before users (FK constraint):\n%s", joined)
	}
}

func TestDeriveScaffold(t *testing.T) {
	cases := []struct {
		name   string
		key    string
		table  string
		action string
	}{
		{"create_posts_table", "post", "posts", "create"},
		{"create_users_table", "user", "users", "create"},
		{"add_views_to_posts_table", "post", "posts", "alter"},
		{"add_author_id_to_posts", "post", "posts", "alter"},
		{"update_posts_table", "post", "posts", "alter"},
		{"alter_users_table", "user", "users", "alter"},
		{"modify_orders_table", "order", "orders", "alter"},
		{"posts", "post", "posts", "create"}, // bare fallback
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, table, action := deriveScaffold(c.name)
			if key != c.key || table != c.table || action != c.action {
				t.Fatalf("deriveScaffold(%q) = (%q,%q,%q), want (%q,%q,%q)",
					c.name, key, table, action, c.key, c.table, c.action)
			}
		})
	}
}

func TestCreateMigrationFileUsesNameForTable(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()

	// create_*_table -> create stub for that table, and must round-trip to SQL.
	createPath, err := CreateMigrationFile(dir, cfg, "create_articles_table", "yaml")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	m, err := LoadMigration(createPath)
	if err != nil {
		t.Fatalf("load create stub: %v", err)
	}
	stmts, err := StatementsFor(m, DirectionUp, "postgres")
	if err != nil {
		t.Fatalf("stub did not generate SQL: %v", err)
	}
	if len(stmts) != 1 || !strings.Contains(stmts[0], `CREATE TABLE "articles"`) {
		t.Fatalf("expected CREATE TABLE articles, got: %v", stmts)
	}

	// add_*_to_*_table -> alter stub (commented body) must still parse.
	alterPath, err := CreateMigrationFile(dir, cfg, "add_slug_to_articles_table", "yaml")
	if err != nil {
		t.Fatalf("alter: %v", err)
	}
	am, err := LoadMigration(alterPath)
	if err != nil {
		t.Fatalf("load alter stub: %v", err)
	}
	intent, ok := am.File.Up["article"]
	if !ok || intent.Action != "alter" || intent.Table != "articles" {
		t.Fatalf("alter stub intent = %+v (ok=%v)", intent, ok)
	}
}

func TestCreateMigrationFileRejectsBadFormat(t *testing.T) {
	if _, err := CreateMigrationFile(t.TempDir(), DefaultConfig(), "x", "xml"); err == nil {
		t.Fatal("expected error for unsupported format")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
