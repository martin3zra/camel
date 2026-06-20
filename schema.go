package camel

import (
	"fmt"
	"strconv"
	"strings"
)

type Column struct {
	Name          string
	Type          string
	Attributes    []string
	Nullable      bool
	Primary       bool
	Unique        bool
	AutoIncrement bool
	Unsigned      bool
	Default       *string
	UseCurrent    bool
}

func ParseColumn(name string, definition string) (Column, error) {
	column := Column{Name: name, Type: "string"}
	for _, token := range splitDefinition(definition) {
		key, value, hasValue := strings.Cut(token, ":")
		switch key {
		case "id":
			column.Type = "bigInteger"
			column.Primary = true
			column.AutoIncrement = true
			column.Unsigned = true
		case "string", "char", "text", "mediumText", "longText", "integer", "int", "bigInteger", "bigint", "smallInteger", "smallint", "tinyInteger", "boolean", "bool", "decimal", "float", "double", "date", "time", "dateTime", "datetime", "timestamp", "json", "jsonb", "binary", "uuid", "enum":
			column.Type = normalizeType(key)
			if hasValue {
				column.Attributes = splitCSV(value)
			}
		case "nullable":
			column.Nullable = true
		case "primary", "primary_key":
			column.Primary = true
		case "unique":
			column.Unique = true
		case "autoIncrement", "auto_increment":
			column.AutoIncrement = true
		case "unsigned":
			column.Unsigned = true
		case "default":
			if !hasValue {
				return column, fmt.Errorf("%s default modifier requires a value", name)
			}
			clean := strings.Trim(value, `"`)
			column.Default = &clean
		case "useCurrent":
			column.UseCurrent = true
		case "":
		default:
			return column, fmt.Errorf("unknown column token %q for %s", token, name)
		}
	}
	return column, nil
}

func GenerateSQL(key string, intent TableIntent, driver string) ([]string, error) {
	table := intent.Table
	if table == "" {
		table = pluralize(key)
	}
	action := intent.Action
	if action == "" {
		action = "create"
	}

	switch action {
	case "create":
		return createTableSQL(table, intent, driver)
	case "alter":
		return alterTableSQL(table, intent, driver)
	case "drop":
		return []string{fmt.Sprintf("DROP TABLE %s;", quoteIdent(driver, table))}, nil
	case "raw":
		return rawSQL(key, intent)
	default:
		return nil, fmt.Errorf("unsupported action %q for %s", action, table)
	}
}

// rawSQL passes user-authored statements through untouched, trimming blanks. The
// driver is irrelevant — the user owns dialect correctness for raw migrations.
func rawSQL(key string, intent TableIntent) ([]string, error) {
	if len(intent.Statements) == 0 {
		return nil, fmt.Errorf("raw action %q requires at least one statement", key)
	}
	statements := make([]string, 0, len(intent.Statements))
	for _, stmt := range intent.Statements {
		if strings.TrimSpace(stmt) != "" {
			statements = append(statements, stmt)
		}
	}
	if len(statements) == 0 {
		return nil, fmt.Errorf("raw action %q has only empty statements", key)
	}
	return statements, nil
}

func createTableSQL(table string, intent TableIntent, driver string) ([]string, error) {
	names := sortedKeys(intent.Columns)
	if len(names) == 0 {
		return nil, fmt.Errorf("create %s requires columns", table)
	}

	lines := make([]string, 0, len(names))
	for _, name := range names {
		column, err := ParseColumn(name, intent.Columns[name])
		if err != nil {
			return nil, err
		}
		lines = append(lines, "  "+columnSQL(column, driver))
	}

	// Foreign keys are declared inline as table-level constraints. This is the
	// only way to attach a foreign key in SQLite, and works for the others too.
	for _, fk := range intent.Foreign {
		clause, err := inlineForeignSQL(fk, driver)
		if err != nil {
			return nil, err
		}
		lines = append(lines, "  "+clause)
	}

	statements := []string{fmt.Sprintf("CREATE TABLE %s (\n%s\n);", quoteIdent(driver, table), strings.Join(lines, ",\n"))}

	// Indexes follow as separate CREATE INDEX statements (portable across all
	// four drivers, unlike inline index syntax).
	for _, idx := range intent.Indexes {
		stmt, err := createIndexSQL(table, idx, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}

	return statements, nil
}

// inlineForeignSQL renders a table-level FOREIGN KEY constraint for use inside a
// CREATE TABLE column list (no ALTER TABLE prefix).
func inlineForeignSQL(fk ForeignIntent, driver string) (string, error) {
	if len(fk.Columns) == 0 || len(fk.RefColumns) == 0 || fk.RefTable == "" {
		return "", fmt.Errorf("foreign key %q requires columns, references_table and references_columns", fk.Name)
	}
	var b strings.Builder
	if fk.Name != "" {
		b.WriteString("CONSTRAINT " + quoteIdent(driver, fk.Name) + " ")
	}
	fmt.Fprintf(&b, "FOREIGN KEY (%s) REFERENCES %s (%s)",
		quoteColumns(driver, fk.Columns), quoteIdent(driver, fk.RefTable), quoteColumns(driver, fk.RefColumns))
	if action := referentialAction(fk.OnDelete); action != "" {
		b.WriteString(" ON DELETE " + action)
	}
	if action := referentialAction(fk.OnUpdate); action != "" {
		b.WriteString(" ON UPDATE " + action)
	}
	return b.String(), nil
}

// alterTableSQL emits statements in dependency-safe order: teardown before
// buildup. Constraints and indexes are dropped before the columns they may
// reference, and added back only after the columns they depend on exist.
func alterTableSQL(table string, intent TableIntent, driver string) ([]string, error) {
	var statements []string

	// --- teardown: foreign keys, then indexes, then columns ---
	for _, name := range intent.DropForeign {
		stmt, err := dropForeignSQL(table, name, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}
	for _, name := range intent.DropIndexes {
		statements = append(statements, dropIndexSQL(table, name, driver))
	}
	for _, name := range intent.DropColumns {
		statements = append(statements, dropColumnSQL(table, name, driver))
	}

	// --- buildup: add, then rename, then modify, then indexes, then FKs ---
	// Rename precedes modify so a modify can target a column by its new name in
	// the same migration.
	for _, name := range sortedKeys(intent.AddColumns) {
		column, err := ParseColumn(name, intent.AddColumns[name])
		if err != nil {
			return nil, err
		}
		if column.Unique {
			// ADD COLUMN ... UNIQUE is rejected by SQLite, and inline
			// uniqueness can't be dropped independently on any driver.
			// Always split: emit a plain ADD COLUMN then a portable
			// CREATE UNIQUE INDEX. Auto-generated name: {table}_{column}_uq.
			plain := column
			plain.Unique = false
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ADD %s;", quoteIdent(driver, table), columnSQL(plain, driver)))
			autoIdx := IndexIntent{Name: fmt.Sprintf("%s_%s_uq", table, name), Columns: []string{name}, Unique: true}
			stmt, err := createIndexSQL(table, autoIdx, driver)
			if err != nil {
				return nil, err
			}
			statements = append(statements, stmt)
		} else {
			statements = append(statements, fmt.Sprintf("ALTER TABLE %s ADD %s;", quoteIdent(driver, table), columnSQL(column, driver)))
		}
	}

	for _, old := range sortedKeys(intent.RenameColumns) {
		stmt, err := renameColumnSQL(table, old, intent.RenameColumns[old], driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}

	for _, name := range sortedKeys(intent.ModifyColumns) {
		column, err := ParseColumn(name, intent.ModifyColumns[name])
		if err != nil {
			return nil, err
		}
		stmts, err := modifyColumnSQL(table, column, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmts...)
	}

	for _, idx := range intent.AddIndexes {
		stmt, err := createIndexSQL(table, idx, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}

	for _, fk := range intent.AddForeign {
		stmt, err := addForeignSQL(table, fk, driver)
		if err != nil {
			return nil, err
		}
		statements = append(statements, stmt)
	}

	return statements, nil
}

// dropColumnSQL drops a column. On SQL Server a column carrying a DEFAULT
// constraint (e.g. `default:0`) can't be dropped until that constraint is
// removed, and its name is auto-generated — so emit a batch that looks it up and
// drops it first. Postgres/MySQL drop the default along with the column.
func dropColumnSQL(table, column, driver string) string {
	if driver == "mssql" || driver == "sqlserver" {
		return fmt.Sprintf("DECLARE @df sysname; "+
			"SELECT @df = dc.name FROM sys.default_constraints dc "+
			"INNER JOIN sys.columns c ON c.default_object_id = dc.object_id "+
			"WHERE c.object_id = OBJECT_ID('%s') AND c.name = '%s'; "+
			"IF @df IS NOT NULL EXEC('ALTER TABLE %s DROP CONSTRAINT [' + @df + ']'); "+
			"ALTER TABLE %s DROP COLUMN %s;",
			table, column, quoteIdent(driver, table), quoteIdent(driver, table), quoteIdent(driver, column))
	}
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s;", quoteIdent(driver, table), quoteIdent(driver, column))
}

// modifyColumnSQL changes a column's type/nullability/default. The grammar is
// heavily dialect-specific; SQLite has no ALTER COLUMN so it is rejected.
func modifyColumnSQL(table string, column Column, driver string) ([]string, error) {
	t := quoteIdent(driver, table)
	c := quoteIdent(driver, column.Name)
	switch driver {
	case "mysql":
		// MODIFY restates the whole column definition.
		return []string{fmt.Sprintf("ALTER TABLE %s MODIFY %s;", t, columnSQL(column, driver))}, nil
	case "postgres":
		stmts := []string{fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s;", t, c, sqlType(column, driver))}
		if column.Nullable {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s DROP NOT NULL;", t, c))
		} else {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET NOT NULL;", t, c))
		}
		if column.Default != nil {
			stmts = append(stmts, fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s SET DEFAULT %s;", t, c, *column.Default))
		}
		return stmts, nil
	case "mssql", "sqlserver":
		null := "NOT NULL"
		if column.Nullable {
			null = "NULL"
		}
		return []string{fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s %s;", t, c, sqlType(column, driver), null)}, nil
	default: // sqlite
		return nil, fmt.Errorf("modify column %q: SQLite has no ALTER COLUMN — use action: raw with a CREATE TABLE / INSERT / DROP TABLE / RENAME pattern", column.Name)
	}
}

func renameColumnSQL(table, old, newName, driver string) (string, error) {
	if newName == "" {
		return "", fmt.Errorf("rename column %q: new name is empty", old)
	}
	if driver == "mssql" || driver == "sqlserver" {
		// sp_rename takes unquoted object names: 'table.column'.
		return fmt.Sprintf("EXEC sp_rename '%s.%s', '%s', 'COLUMN';", table, old, newName), nil
	}
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s;", quoteIdent(driver, table), quoteIdent(driver, old), quoteIdent(driver, newName)), nil
}

func createIndexSQL(table string, idx IndexIntent, driver string) (string, error) {
	if idx.Name == "" {
		return "", fmt.Errorf("index on %s requires a name", table)
	}
	if len(idx.Columns) == 0 {
		return "", fmt.Errorf("index %q requires at least one column", idx.Name)
	}
	unique := ""
	if idx.Unique {
		unique = "UNIQUE "
	}
	cols := quoteColumns(driver, idx.Columns)
	return fmt.Sprintf("CREATE %sINDEX %s ON %s (%s);", unique, quoteIdent(driver, idx.Name), quoteIdent(driver, table), cols), nil
}

func dropIndexSQL(table, name, driver string) string {
	switch driver {
	case "mysql", "mssql", "sqlserver":
		return fmt.Sprintf("DROP INDEX %s ON %s;", quoteIdent(driver, name), quoteIdent(driver, table))
	default: // postgres, sqlite
		return fmt.Sprintf("DROP INDEX %s;", quoteIdent(driver, name))
	}
}

func addForeignSQL(table string, fk ForeignIntent, driver string) (string, error) {
	if driver == "sqlite" {
		return "", fmt.Errorf("foreign key %q: SQLite cannot add foreign keys via ALTER — declare them at create time with foreign: in the create action", fk.Name)
	}
	if fk.Name == "" {
		return "", fmt.Errorf("foreign key on %s requires a name", table)
	}
	if len(fk.Columns) == 0 || len(fk.RefColumns) == 0 || fk.RefTable == "" {
		return "", fmt.Errorf("foreign key %q requires columns, references_table and references_columns", fk.Name)
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
		quoteIdent(driver, table), quoteIdent(driver, fk.Name),
		quoteColumns(driver, fk.Columns), quoteIdent(driver, fk.RefTable), quoteColumns(driver, fk.RefColumns))
	if action := referentialAction(fk.OnDelete); action != "" {
		stmt += " ON DELETE " + action
	}
	if action := referentialAction(fk.OnUpdate); action != "" {
		stmt += " ON UPDATE " + action
	}
	return stmt + ";", nil
}

func dropForeignSQL(table, name, driver string) (string, error) {
	switch driver {
	case "sqlite":
		return "", fmt.Errorf("foreign key %q: SQLite cannot drop foreign keys via ALTER — they are dropped with the table", name)
	case "mysql":
		return fmt.Sprintf("ALTER TABLE %s DROP FOREIGN KEY %s;", quoteIdent(driver, table), quoteIdent(driver, name)), nil
	default: // postgres, mssql
		return fmt.Sprintf("ALTER TABLE %s DROP CONSTRAINT %s;", quoteIdent(driver, table), quoteIdent(driver, name)), nil
	}
}

// referentialAction normalizes an ON DELETE / ON UPDATE clause to canonical SQL.
func referentialAction(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cascade":
		return "CASCADE"
	case "restrict":
		return "RESTRICT"
	case "set null", "set_null", "setnull":
		return "SET NULL"
	case "set default", "set_default", "setdefault":
		return "SET DEFAULT"
	case "no action", "no_action", "noaction":
		return "NO ACTION"
	default:
		return ""
	}
}

func quoteColumns(driver string, columns []string) string {
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = quoteIdent(driver, c)
	}
	return strings.Join(quoted, ", ")
}

func columnSQL(column Column, driver string) string {
	parts := []string{quoteIdent(driver, column.Name), sqlType(column, driver)}
	if column.AutoIncrement {
		switch driver {
		case "mysql":
			parts = append(parts, "AUTO_INCREMENT")
		case "mssql":
			parts = append(parts, "IDENTITY(1,1)")
		}
	}
	if !column.Nullable {
		parts = append(parts, "NOT NULL")
	}
	if column.Primary {
		parts = append(parts, "PRIMARY KEY")
	}
	if column.Unique {
		parts = append(parts, "UNIQUE")
	}
	if column.Default != nil {
		parts = append(parts, "DEFAULT "+*column.Default)
	}
	if column.UseCurrent {
		parts = append(parts, "DEFAULT "+currentTimestamp(driver))
	}
	return strings.Join(parts, " ")
}

func sqlType(column Column, driver string) string {
	base := normalizeType(column.Type)
	switch base {
	case "string":
		length := attrInt(column, 0, 255)
		if driver == "mssql" {
			return fmt.Sprintf("NVARCHAR(%d)", length)
		}
		if driver == "sqlite" {
			return "TEXT"
		}
		return fmt.Sprintf("VARCHAR(%d)", length)
	case "char":
		length := attrInt(column, 0, 1)
		if driver == "mssql" {
			return fmt.Sprintf("NCHAR(%d)", length)
		}
		return fmt.Sprintf("CHAR(%d)", length)
	case "text":
		if driver == "mssql" {
			return "NVARCHAR(MAX)"
		}
		return "TEXT"
	case "mediumText", "longText":
		if driver == "mysql" && base == "mediumText" {
			return "MEDIUMTEXT"
		}
		if driver == "mysql" && base == "longText" {
			return "LONGTEXT"
		}
		if driver == "mssql" {
			return "NVARCHAR(MAX)"
		}
		return "TEXT"
	case "integer":
		if driver == "postgres" && column.AutoIncrement {
			return "SERIAL"
		}
		return mapType(driver, "INTEGER", "INT", "INTEGER", "INT")
	case "bigInteger":
		if driver == "postgres" && column.AutoIncrement {
			return "BIGSERIAL"
		}
		if driver == "sqlite" {
			return "INTEGER"
		}
		return "BIGINT"
	case "smallInteger":
		if driver == "sqlite" {
			return "INTEGER"
		}
		return "SMALLINT"
	case "tinyInteger":
		return mapType(driver, "SMALLINT", "TINYINT", "INTEGER", "TINYINT")
	case "boolean":
		return mapType(driver, "BOOLEAN", "BOOLEAN", "INTEGER", "BIT")
	case "decimal":
		total := attrInt(column, 0, 8)
		places := attrInt(column, 1, 2)
		return fmt.Sprintf("DECIMAL(%d,%d)", total, places)
	case "float":
		return mapType(driver, "REAL", "FLOAT", "REAL", "REAL")
	case "double":
		return mapType(driver, "DOUBLE PRECISION", "DOUBLE", "REAL", "FLOAT")
	case "date":
		return "DATE"
	case "time":
		return "TIME"
	case "dateTime":
		return mapType(driver, "TIMESTAMP", "DATETIME", "DATETIME", "DATETIME2")
	case "timestamp":
		return mapType(driver, "TIMESTAMP", "TIMESTAMP", "DATETIME", "DATETIME2")
	case "json":
		return mapType(driver, "JSON", "JSON", "TEXT", "NVARCHAR(MAX)")
	case "jsonb":
		return mapType(driver, "JSONB", "JSON", "TEXT", "NVARCHAR(MAX)")
	case "binary":
		return mapType(driver, "BYTEA", "BLOB", "BLOB", "VARBINARY(MAX)")
	case "uuid":
		return mapType(driver, "UUID", "CHAR(36)", "TEXT", "UNIQUEIDENTIFIER")
	case "enum":
		if driver == "mysql" && len(column.Attributes) > 0 {
			values := make([]string, 0, len(column.Attributes))
			for _, value := range column.Attributes {
				values = append(values, "'"+strings.ReplaceAll(value, "'", "''")+"'")
			}
			return "ENUM(" + strings.Join(values, ",") + ")"
		}
		return "TEXT"
	default:
		return strings.ToUpper(base)
	}
}

func normalizeType(value string) string {
	switch value {
	case "int":
		return "integer"
	case "bigint":
		return "bigInteger"
	case "smallint":
		return "smallInteger"
	case "bool":
		return "boolean"
	case "datetime":
		return "dateTime"
	default:
		return value
	}
}

func mapType(driver, postgres, mysql, sqlite, mssql string) string {
	switch driver {
	case "postgres":
		return postgres
	case "mysql":
		return mysql
	case "sqlite":
		return sqlite
	case "mssql", "sqlserver":
		return mssql
	default:
		return postgres
	}
}

func quoteIdent(driver string, ident string) string {
	ident = strings.ReplaceAll(ident, `"`, `""`)
	switch driver {
	case "mysql":
		return "`" + strings.ReplaceAll(ident, "`", "``") + "`"
	case "mssql", "sqlserver":
		return "[" + strings.ReplaceAll(ident, "]", "]]") + "]"
	default:
		return `"` + ident + `"`
	}
}

func currentTimestamp(driver string) string {
	if driver == "mssql" || driver == "sqlserver" {
		return "GETDATE()"
	}
	return "CURRENT_TIMESTAMP"
}

func attrInt(column Column, index int, fallback int) int {
	if len(column.Attributes) <= index {
		return fallback
	}
	value, err := strconv.Atoi(column.Attributes[index])
	if err != nil {
		return fallback
	}
	return value
}

func splitDefinition(definition string) []string {
	var tokens []string
	var current strings.Builder
	var quote rune
	for _, r := range definition {
		switch {
		case quote != 0:
			current.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			current.WriteRune(r)
		case r == ' ' || r == '\t' || r == '\n':
			if current.Len() > 0 {
				tokens = append(tokens, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

func splitCSV(value string) []string {
	items := strings.Split(value, ",")
	for i := range items {
		items[i] = strings.Trim(strings.TrimSpace(items[i]), `"'`)
	}
	return items
}

func pluralize(value string) string {
	if strings.HasSuffix(value, "s") {
		return value
	}
	return value + "s"
}
