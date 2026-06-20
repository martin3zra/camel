package camel

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// sqliteRunner builds a Runner backed by a real on-disk SQLite database in a
// temp dir, plus a migrations directory the caller can populate.
func sqliteRunner(t *testing.T) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	migDir := filepath.Join(dir, "database")
	if err := os.MkdirAll(migDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.DB.Driver = "sqlite"
	cfg.DB.Source = filepath.Join(dir, "camel.sqlite")

	runner, err := NewRunner(cfg, dir)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	t.Cleanup(func() { runner.Close() })
	return runner, migDir
}

func writeMigration(t *testing.T, migDir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(migDir, name), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name = ?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("tableExists(%s): %v", name, err)
	}
	return got == name
}

const createPosts = `up:
  post:
    action: create
    table: posts
    columns:
      id: id
      title: string:150
down:
  post:
    action: drop
    table: posts
`

const createUsers = `up:
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

func TestRunnerMigrateCreatesTablesAndRecords(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)

	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if !tableExists(t, runner.DB, "posts") || !tableExists(t, runner.DB, "users") {
		t.Fatal("expected posts and users tables to exist")
	}

	applied, err := runner.applied()
	if err != nil {
		t.Fatalf("applied: %v", err)
	}
	if !applied["20240101_posts.yaml"] || !applied["20240102_users.yaml"] {
		t.Fatalf("both migrations should be recorded: %v", applied)
	}
}

func TestRunnerMigrateIsIdempotent(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)

	if err := runner.Migrate(false); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	// Re-running must skip already-applied migrations rather than re-execute
	// (which would fail with "table already exists").
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("second migrate should be a no-op, got: %v", err)
	}
}

func TestRunnerMigratePretendDoesNotTouchDB(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)

	if err := runner.Migrate(true); err != nil {
		t.Fatalf("pretend migrate: %v", err)
	}
	if tableExists(t, runner.DB, "posts") {
		t.Fatal("pretend must not create tables")
	}
	if tableExists(t, runner.DB, "camel_migrations") {
		t.Fatal("pretend must not create the tracking table")
	}
}

func TestRunnerMigratePretendSkipsApplied(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// posts is applied; a second pretend over the same set has nothing pending.
	// Re-running pretend must succeed and (since applied is skipped) not error
	// even though the tracking table now exists.
	if err := runner.Migrate(true); err != nil {
		t.Fatalf("pretend after apply: %v", err)
	}

	// Add a new pending migration: only it is pending now. Pretend must not
	// re-execute or re-create the applied posts table, and must stay read-only.
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)
	if err := runner.Migrate(true); err != nil {
		t.Fatalf("pretend with one pending: %v", err)
	}
	// users was only pretended, never applied.
	if tableExists(t, runner.DB, "users") {
		t.Fatal("pretend must not create the pending table")
	}
	applied, err := runner.applied()
	if err != nil {
		t.Fatalf("applied: %v", err)
	}
	if applied["20240102_users.yaml"] {
		t.Fatal("pretend must not record the pending migration")
	}
}

func TestRunnerStatusReflectsAppliedState(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)

	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Add a third, unapplied migration.
	writeMigration(t, migDir, "20240103_extra.yaml", createUsersAs("extra", "extras"))

	statuses, err := runner.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("want 3 statuses, got %d", len(statuses))
	}
	wantApplied := map[string]bool{
		"20240101_posts.yaml": true,
		"20240102_users.yaml": true,
		"20240103_extra.yaml": false,
	}
	for _, s := range statuses {
		if s.Applied != wantApplied[s.Name] {
			t.Errorf("%s applied=%v, want %v", s.Name, s.Applied, wantApplied[s.Name])
		}
	}
}

func TestRunnerRollbackReversesLastMigrationOnly(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// Two SEPARATE runs -> posts in batch 1, users in batch 2.
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 1: %v", err)
	}
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 2: %v", err)
	}

	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Default rollback reverses only the last batch (users), leaving posts.
	if tableExists(t, runner.DB, "users") {
		t.Error("users should be dropped by rollback")
	}
	if !tableExists(t, runner.DB, "posts") {
		t.Error("posts should survive a single rollback")
	}
	applied, _ := runner.applied()
	if applied["20240102_users.yaml"] {
		t.Error("rolled-back migration should no longer be recorded")
	}
	if !applied["20240101_posts.yaml"] {
		t.Error("posts migration should still be recorded")
	}
}

func TestRunnerRollbackEmptyIsClean(t *testing.T) {
	runner, _ := sqliteRunner(t)
	// No migrations applied: rollback must not error.
	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("rollback on empty repo: %v", err)
	}
}

func TestRunnerBatchIncrements(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 1: %v", err)
	}
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 2: %v", err)
	}

	batch := func(name string) int {
		var b int
		q := fmt.Sprintf("SELECT batch FROM %s WHERE migration = ?", runner.Config.Migration.Table)
		if err := runner.DB.QueryRow(q, name).Scan(&b); err != nil {
			t.Fatalf("batch(%s): %v", name, err)
		}
		return b
	}
	if batch("20240101_posts.yaml") != 1 {
		t.Errorf("first migration should be batch 1")
	}
	if batch("20240102_users.yaml") != 2 {
		t.Errorf("second migration (separate run) should be batch 2")
	}
}

func TestRunnerMigrateRollsBackTransactionOnBadSQL(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// "drop" on a nonexistent table fails; the whole migration tx must roll back
	// and the migration must NOT be recorded.
	bad := `up:
  ghost:
    action: drop
    table: does_not_exist
down:
  ghost:
    action: drop
    table: does_not_exist
`
	writeMigration(t, migDir, "20240101_bad.yaml", bad)
	if err := runner.Migrate(false); err == nil {
		t.Fatal("expected migrate to fail on bad SQL")
	}
	applied, _ := runner.applied()
	if applied["20240101_bad.yaml"] {
		t.Error("failed migration must not be recorded")
	}
}

func TestRunnerRollbackLastBatch(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// Two migrations applied in ONE run -> same batch.
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	// Default rollback reverses the whole last batch -> both gone.
	if tableExists(t, runner.DB, "posts") || tableExists(t, runner.DB, "users") {
		t.Fatal("default rollback should reverse the entire last batch")
	}
	applied, _ := runner.applied()
	if len(applied) != 0 {
		t.Fatalf("no migrations should remain recorded, got %v", applied)
	}
}

func TestRunnerRollbackStep(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// Three migrations in one batch.
	writeMigration(t, migDir, "20240101_a.yaml", createUsersAs("a", "a_tbl"))
	writeMigration(t, migDir, "20240102_b.yaml", createUsersAs("b", "b_tbl"))
	writeMigration(t, migDir, "20240103_c.yaml", createUsersAs("c", "c_tbl"))
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Reverse only the last 2 (c, then b), newest first.
	if err := runner.Rollback(RollbackOptions{Steps: 2}); err != nil {
		t.Fatalf("Rollback step: %v", err)
	}
	if tableExists(t, runner.DB, "c_tbl") || tableExists(t, runner.DB, "b_tbl") {
		t.Error("the two newest tables should be dropped")
	}
	if !tableExists(t, runner.DB, "a_tbl") {
		t.Error("the oldest table should survive --step 2")
	}
	applied, _ := runner.applied()
	if !applied["20240101_a.yaml"] || len(applied) != 1 {
		t.Fatalf("only the oldest migration should remain, got %v", applied)
	}
}

func TestRunnerRollbackStepExceedingCountReversesAll(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// Asking for more steps than exist is not an error; it reverses everything.
	if err := runner.Rollback(RollbackOptions{Steps: 99}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if tableExists(t, runner.DB, "posts") {
		t.Error("posts should be dropped")
	}
}

func TestRunnerReset(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// Two separate runs -> two batches; reset must cross batch boundaries.
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 1: %v", err)
	}
	writeMigration(t, migDir, "20240102_users.yaml", createUsers)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate 2: %v", err)
	}

	if err := runner.Reset(false); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if tableExists(t, runner.DB, "posts") || tableExists(t, runner.DB, "users") {
		t.Fatal("reset should drop every table across all batches")
	}
	applied, _ := runner.applied()
	if len(applied) != 0 {
		t.Fatalf("reset should clear the tracking table, got %v", applied)
	}
}

func TestRunnerRollbackPretendLeavesDBUntouched(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	writeMigration(t, migDir, "20240101_posts.yaml", createPosts)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if err := runner.Rollback(RollbackOptions{Pretend: true}); err != nil {
		t.Fatalf("pretend rollback: %v", err)
	}
	// Pretend prints SQL but must not drop the table or unrecord the migration.
	if !tableExists(t, runner.DB, "posts") {
		t.Error("pretend rollback must not drop the table")
	}
	applied, _ := runner.applied()
	if !applied["20240101_posts.yaml"] {
		t.Error("pretend rollback must not unrecord the migration")
	}
}

func TestSelectRollbackTargets(t *testing.T) {
	// newest-first, two batches: batch 2 = {c,b}, batch 1 = {a}
	applied := []AppliedMigration{
		{Name: "c", Batch: 2},
		{Name: "b", Batch: 2},
		{Name: "a", Batch: 1},
	}
	names := func(ms []AppliedMigration) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = m.Name
		}
		return out
	}

	cases := []struct {
		name string
		opts RollbackOptions
		want []string
	}{
		{"default last batch", RollbackOptions{}, []string{"c", "b"}},
		{"step 1", RollbackOptions{Steps: 1}, []string{"c"}},
		{"step 3", RollbackOptions{Steps: 3}, []string{"c", "b", "a"}},
		{"step over count", RollbackOptions{Steps: 50}, []string{"c", "b", "a"}},
		{"all", RollbackOptions{All: true}, []string{"c", "b", "a"}},
		{"all beats steps", RollbackOptions{All: true, Steps: 1}, []string{"c", "b", "a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := names(selectRollbackTargets(applied, tc.opts))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestRunnerRawMigrationExecutes(t *testing.T) {
	runner, migDir := sqliteRunner(t)
	// Create a table (batch 1), then a raw data migration (batch 2) that seeds
	// and later cleans it, so rolling back the raw migration leaves the table.
	writeMigration(t, migDir, "20240101_users.yaml", createUsers)
	if err := runner.Migrate(false); err != nil {
		t.Fatalf("migrate users: %v", err)
	}
	writeMigration(t, migDir, "20240102_seed.yaml", `up:
  seed:
    action: raw
    statements:
      - "INSERT INTO users (id, email) VALUES (1, 'a@b.c');"
      - "INSERT INTO users (id, email) VALUES (2, 'c@d.e');"
down:
  seed:
    action: raw
    statements:
      - "DELETE FROM users WHERE id IN (1, 2);"
`)

	if err := runner.Migrate(false); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	var count int
	if err := runner.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("raw seed should insert 2 rows, got %d", count)
	}

	// Roll back only the raw migration (its own batch); the down clears the seed.
	if err := runner.Rollback(RollbackOptions{}); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if err := runner.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&count); err != nil {
		t.Fatalf("count after rollback: %v", err)
	}
	if count != 0 {
		t.Fatalf("raw down should remove seeded rows, got %d", count)
	}
	if tableExists(t, runner.DB, "users") == false {
		t.Fatal("users table should survive rolling back only the raw migration")
	}
}

// createUsersAs returns a create-migration body for an arbitrary key/table so
// tests can add distinct unapplied migrations.
func createUsersAs(key, table string) string {
	return fmt.Sprintf(`up:
  %s:
    action: create
    table: %s
    columns:
      id: id
down:
  %s:
    action: drop
    table: %s
`, key, table, key, table)
}
