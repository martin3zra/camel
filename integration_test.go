package camel

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// These exercise the runner against a real server. They are skipped unless a DSN
// is provided, so the default `go test ./...` stays self-contained. The harness
// in the Makefile (`make integration`) spins up the databases and sets the DSNs:
//
//	CAMEL_TEST_POSTGRES_DSN='postgres://user:pass@localhost:5432/camel?sslmode=disable'
//	CAMEL_TEST_MYSQL_DSN='user:pass@tcp(localhost:3306)/camel?multiStatements=true'
//	CAMEL_TEST_MSSQL_DSN='sqlserver://sa:pass@localhost:1433?database=camel'
func TestIntegrationPostgres(t *testing.T) {
	requireDSN(t, "CAMEL_TEST_POSTGRES_DSN")
	runLiveMigrationCycle(t, "postgres", os.Getenv("CAMEL_TEST_POSTGRES_DSN"))
}

func TestIntegrationMySQL(t *testing.T) {
	requireDSN(t, "CAMEL_TEST_MYSQL_DSN")
	runLiveMigrationCycle(t, "mysql", os.Getenv("CAMEL_TEST_MYSQL_DSN"))
}

func TestIntegrationMSSQL(t *testing.T) {
	requireDSN(t, "CAMEL_TEST_MSSQL_DSN")
	runLiveMigrationCycle(t, "mssql", os.Getenv("CAMEL_TEST_MSSQL_DSN"))
}

func requireDSN(t *testing.T, env string) {
	t.Helper()
	if os.Getenv(env) == "" {
		t.Skipf("set %s to run", env)
	}
}

// Staged migrations: each is its own batch so rollback granularity is testable.
// Together they cover create (with inline FK + index), every alter operation,
// and a raw data migration — the dialect-heavy paths.
var (
	liveUsers = `up:
  user:
    action: create
    table: users
    columns:
      id: id
      email: string:255 unique
down:
  user:
    action: drop
    table: users
`
	livePosts = `up:
  post:
    action: create
    table: posts
    columns:
      id: id
      author_id: bigInteger
      title: string:150
    foreign:
      - name: posts_author_fk
        columns: [author_id]
        references_table: users
        references_columns: [id]
        on_delete: cascade
    indexes:
      - name: posts_author_idx
        columns: [author_id]
down:
  post:
    action: drop
    table: posts
`
	liveAlter = `up:
  post:
    action: alter
    table: posts
    add_columns:
      view_count: integer default:0
    rename_columns:
      title: headline
    modify_columns:
      headline: string:200
    add_indexes:
      - name: posts_headline_idx
        columns: [headline]
down:
  post:
    action: alter
    table: posts
    drop_indexes: [posts_headline_idx]
    rename_columns:
      headline: title
    drop_columns: [view_count]
`
	// No explicit ids: SQL Server rejects inserts into IDENTITY columns, and
	// portable raw SQL shouldn't assume them anyway.
	liveSeed = `up:
  seed:
    action: raw
    statements:
      - "INSERT INTO users (email) VALUES ('a@b.c');"
      - "INSERT INTO posts (author_id, headline) VALUES ((SELECT MIN(id) FROM users), 'hello');"
down:
  seed:
    action: raw
    statements:
      - "DELETE FROM posts WHERE headline = 'hello';"
      - "DELETE FROM users WHERE email = 'a@b.c';"
`
)

func runLiveMigrationCycle(t *testing.T, driver, dsn string) {
	t.Helper()
	dir := t.TempDir()
	migDir := filepath.Join(dir, "database")
	if err := os.MkdirAll(migDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.DB.Driver = driver
	cfg.DB.Source = dsn
	cfg.Migration.Table = "camel_migrations_test"

	runner, err := NewRunner(cfg, dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	defer runner.Close()

	// Best-effort teardown of anything a prior failed run left behind, both
	// before and after, so the target database can be reused.
	reset := func() {
		runner.DB.Exec(`DROP TABLE ` + quoteIdent(driver, "posts"))
		runner.DB.Exec(`DROP TABLE ` + quoteIdent(driver, "users"))
		runner.DB.Exec(`DROP TABLE ` + quoteIdent(driver, cfg.Migration.Table))
	}
	reset()
	defer reset()

	// Apply each migration as its own batch.
	stages := []struct {
		file, body string
	}{
		{"20240101_users.yaml", liveUsers},
		{"20240102_posts.yaml", livePosts},
		{"20240103_alter.yaml", liveAlter},
		{"20240104_seed.yaml", liveSeed},
	}
	for _, s := range stages {
		writeMigration(t, migDir, s.file, s.body)
		if err := runner.Migrate(false); err != nil {
			t.Fatalf("migrate %s: %v", s.file, err)
		}
	}

	// All four applied: schema and seed data must be present.
	if !liveTableExists(t, runner.DB, driver, "posts") || !liveTableExists(t, runner.DB, driver, "users") {
		t.Fatal("expected posts and users tables")
	}
	if !liveColumnExists(t, runner.DB, driver, "posts", "view_count") {
		t.Error("alter add_columns: view_count missing")
	}
	if !liveColumnExists(t, runner.DB, driver, "posts", "headline") {
		t.Error("alter rename_columns: headline missing")
	}
	if liveColumnExists(t, runner.DB, driver, "posts", "title") {
		t.Error("alter rename_columns: old name title should be gone")
	}
	if got := liveCount(t, runner.DB, "posts"); got != 1 {
		t.Errorf("raw seed: want 1 post, got %d", got)
	}

	statuses, err := runner.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 4 {
		t.Fatalf("want 4 statuses, got %d", len(statuses))
	}

	// Roll back the seed batch only: rows gone, schema intact.
	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("rollback seed: %v", err)
	}
	if got := liveCount(t, runner.DB, "posts"); got != 0 {
		t.Errorf("after seed rollback: want 0 posts, got %d", got)
	}
	if !liveTableExists(t, runner.DB, driver, "posts") {
		t.Error("seed rollback must not drop the table")
	}

	// Roll back the alter batch: rename reverts, added column/index gone.
	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("rollback alter: %v", err)
	}
	if !liveColumnExists(t, runner.DB, driver, "posts", "title") {
		t.Error("alter rollback: title should be restored")
	}
	if liveColumnExists(t, runner.DB, driver, "posts", "view_count") {
		t.Error("alter rollback: view_count should be dropped")
	}

	// Roll back everything else.
	if err := runner.Reset(false); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if liveTableExists(t, runner.DB, driver, "posts") || liveTableExists(t, runner.DB, driver, "users") {
		t.Error("reset should drop all tables")
	}
}

// currentDBPredicate scopes an information_schema query to the database the
// connection is using, so tables of the same name in other schemas (common on a
// shared dev server) don't cause false positives.
func currentDBPredicate(driver string) string {
	switch driver {
	case "mysql":
		return " AND table_schema = DATABASE()"
	case "mssql", "sqlserver":
		return " AND table_catalog = DB_NAME()"
	default: // postgres
		return " AND table_schema = current_schema()"
	}
}

func liveTableExists(t *testing.T, db *sql.DB, driver, table string) bool {
	t.Helper()
	query := "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = " +
		placeholder(driver, 1) + currentDBPredicate(driver)
	var n int
	if err := db.QueryRow(query, table).Scan(&n); err != nil {
		t.Fatalf("liveTableExists(%s): %v", table, err)
	}
	return n > 0
}

func liveColumnExists(t *testing.T, db *sql.DB, driver, table, column string) bool {
	t.Helper()
	query := "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = " +
		placeholder(driver, 1) + " AND column_name = " + placeholder(driver, 2) + currentDBPredicate(driver)
	var n int
	if err := db.QueryRow(query, table, column).Scan(&n); err != nil {
		t.Fatalf("liveColumnExists(%s.%s): %v", table, column, err)
	}
	return n > 0
}

func liveCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}
