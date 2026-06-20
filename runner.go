package camel

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/microsoft/go-mssqldb"
	_ "modernc.org/sqlite"
)

type Runner struct {
	Config Config
	Dir    string
	DB     *sql.DB
}

type MigrationStatus struct {
	Name    string
	Applied bool
}

func NewRunner(cfg Config, dir string) (*Runner, error) {
	driver := normalizeDriver(cfg.DB.Driver)
	db, err := sql.Open(driver, cfg.DB.Source)
	if err != nil {
		return nil, err
	}
	return &Runner{Config: cfg, Dir: dir, DB: db}, nil
}

func (r *Runner) Close() error {
	if r.DB == nil {
		return nil
	}
	return r.DB.Close()
}

func (r *Runner) EnsureRepository() error {
	_, err := r.DB.Exec(repositorySQL(r.Config))
	return err
}

func (r *Runner) Migrate(pretend bool) error {
	migrations, err := ListMigrations(r.Config, r.Dir)
	if err != nil {
		return err
	}
	if pretend {
		// Pretend is read-only: show the SQL for pending migrations only, and
		// never create the tracking table. If the table doesn't exist yet,
		// nothing has been applied, so treat every migration as pending.
		applied, err := r.applied()
		if err != nil {
			applied = map[string]bool{}
		}
		for _, migration := range migrations {
			if applied[migration.Name] {
				continue
			}
			statements, err := StatementsFor(migration, DirectionUp, r.Config.DB.Driver)
			if err != nil {
				return err
			}
			printStatements(migration.Name, statements)
		}
		return nil
	}

	if err := r.EnsureRepository(); err != nil {
		return err
	}
	applied, err := r.applied()
	if err != nil {
		return err
	}

	// All migrations applied in this run share one batch number, so a later
	// `rollback` reverses them as a unit.
	batch, err := r.nextBatch()
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if applied[migration.Name] {
			continue
		}
		statements, err := StatementsFor(migration, DirectionUp, r.Config.DB.Driver)
		if err != nil {
			return err
		}
		if err := r.execAll(statements); err != nil {
			return fmt.Errorf("%s: %w", migration.Name, err)
		}
		if err := r.record(migration.Name, batch); err != nil {
			return err
		}
		fmt.Printf("Migrated %s\n", migration.Name)
	}
	return nil
}

// RollbackOptions controls how many applied migrations Rollback reverses.
//
// With the zero value, Rollback reverses the most recent batch (every migration
// sharing the highest batch number), matching the grouping that a single
// `migrate` run records. Steps overrides that to reverse exactly the last N
// applied migrations regardless of batch. All reverses everything (reset).
type RollbackOptions struct {
	Steps   int  // reverse the last N migrations; 0 means "last batch"
	All     bool // reverse every applied migration; overrides Steps
	Pretend bool // print SQL without executing
}

// AppliedMigration is one recorded row in the migration tracking table.
type AppliedMigration struct {
	Name  string
	Batch int
}

func (r *Runner) Rollback(opts RollbackOptions) error {
	if err := r.EnsureRepository(); err != nil {
		return err
	}
	applied, err := r.appliedList()
	if err != nil {
		return err
	}
	if len(applied) == 0 {
		fmt.Println("Nothing to rollback.")
		return nil
	}

	targets := selectRollbackTargets(applied, opts)
	for _, target := range targets {
		path := filepath.Join(r.Dir, r.Config.Migration.Directory, target.Name)
		migration, err := LoadMigration(path)
		if err != nil {
			return err
		}
		statements, err := StatementsFor(migration, DirectionDown, r.Config.DB.Driver)
		if err != nil {
			return err
		}
		if opts.Pretend {
			printStatements(migration.Name, statements)
			continue
		}
		if err := r.execAll(statements); err != nil {
			return fmt.Errorf("%s: %w", target.Name, err)
		}
		if err := r.remove(target.Name); err != nil {
			return err
		}
		fmt.Printf("Rolled back %s\n", target.Name)
	}
	return nil
}

// Reset rolls back every applied migration, newest first.
func (r *Runner) Reset(pretend bool) error {
	return r.Rollback(RollbackOptions{All: true, Pretend: pretend})
}

// selectRollbackTargets picks which applied migrations to reverse, given the
// full applied list ordered newest-first (batch DESC, migration DESC).
func selectRollbackTargets(applied []AppliedMigration, opts RollbackOptions) []AppliedMigration {
	if opts.All {
		return applied
	}
	if opts.Steps > 0 {
		if opts.Steps >= len(applied) {
			return applied
		}
		return applied[:opts.Steps]
	}
	// Default: the most recent batch.
	lastBatch := applied[0].Batch
	var targets []AppliedMigration
	for _, m := range applied {
		if m.Batch != lastBatch {
			break
		}
		targets = append(targets, m)
	}
	return targets
}

func (r *Runner) Status() ([]MigrationStatus, error) {
	if err := r.EnsureRepository(); err != nil {
		return nil, err
	}
	migrations, err := ListMigrations(r.Config, r.Dir)
	if err != nil {
		return nil, err
	}
	applied, err := r.applied()
	if err != nil {
		return nil, err
	}

	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, migration := range migrations {
		statuses = append(statuses, MigrationStatus{Name: migration.Name, Applied: applied[migration.Name]})
	}
	return statuses, nil
}

func (r *Runner) execAll(statements []string) error {
	tx, err := r.DB.Begin()
	if err != nil {
		return err
	}
	for _, statement := range statements {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.Exec(statement); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (r *Runner) applied() (map[string]bool, error) {
	rows, err := r.DB.Query(fmt.Sprintf("SELECT migration FROM %s", quoteIdent(r.Config.DB.Driver, r.Config.Migration.Table)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

// appliedList returns every recorded migration newest-first (batch DESC,
// migration DESC), which is the order rollback walks.
func (r *Runner) appliedList() ([]AppliedMigration, error) {
	query := fmt.Sprintf("SELECT migration, batch FROM %s ORDER BY batch DESC, migration DESC", quoteIdent(r.Config.DB.Driver, r.Config.Migration.Table))
	rows, err := r.DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []AppliedMigration
	for rows.Next() {
		var m AppliedMigration
		if err := rows.Scan(&m.Name, &m.Batch); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (r *Runner) record(name string, batch int) error {
	query := fmt.Sprintf("INSERT INTO %s (migration, batch) VALUES (%s, %s)", quoteIdent(r.Config.DB.Driver, r.Config.Migration.Table), placeholder(r.Config.DB.Driver, 1), placeholder(r.Config.DB.Driver, 2))
	_, err := r.DB.Exec(query, name, batch)
	return err
}

func (r *Runner) remove(name string) error {
	query := fmt.Sprintf("DELETE FROM %s WHERE migration = %s", quoteIdent(r.Config.DB.Driver, r.Config.Migration.Table), placeholder(r.Config.DB.Driver, 1))
	_, err := r.DB.Exec(query, name)
	return err
}

func (r *Runner) nextBatch() (int, error) {
	query := fmt.Sprintf("SELECT COALESCE(MAX(batch), 0) + 1 FROM %s", quoteIdent(r.Config.DB.Driver, r.Config.Migration.Table))
	var batch int
	err := r.DB.QueryRow(query).Scan(&batch)
	return batch, err
}

func repositorySQL(cfg Config) string {
	table := quoteIdent(cfg.DB.Driver, cfg.Migration.Table)
	switch cfg.DB.Driver {
	case "mysql":
		return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (migration VARCHAR(255) PRIMARY KEY, batch INTEGER NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", table)
	case "sqlite":
		return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (migration TEXT PRIMARY KEY, batch INTEGER NOT NULL, applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)", table)
	case "mssql", "sqlserver":
		return fmt.Sprintf("IF NOT EXISTS (SELECT * FROM sysobjects WHERE name='%s' AND xtype='U') CREATE TABLE %s (migration NVARCHAR(255) PRIMARY KEY, batch INT NOT NULL, applied_at DATETIME NOT NULL DEFAULT GETDATE())", cfg.Migration.Table, table)
	default:
		return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (migration VARCHAR(255) PRIMARY KEY, batch INTEGER NOT NULL, applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", table)
	}
}

func placeholder(driver string, index int) string {
	switch driver {
	case "postgres":
		return fmt.Sprintf("$%d", index)
	case "mssql", "sqlserver":
		// The sqlserver driver uses named parameters (@p1, @p2, ...).
		return fmt.Sprintf("@p%d", index)
	default:
		return "?"
	}
}

func normalizeDriver(driver string) string {
	switch driver {
	case "postgres", "postgresql":
		return "postgres"
	case "mssql", "sqlserver":
		return "sqlserver"
	default:
		return driver
	}
}

func printStatements(name string, statements []string) {
	fmt.Printf("-- %s\n", name)
	for _, statement := range statements {
		fmt.Println(statement)
	}
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
