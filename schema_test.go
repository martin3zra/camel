package camel

import (
	"strings"
	"testing"
)

func TestParseColumn(t *testing.T) {
	tests := []struct {
		name       string
		definition string
		want       Column
		wantErr    bool
	}{
		{
			name:       "id",
			definition: "id",
			want:       Column{Name: "id", Type: "bigInteger", Primary: true, AutoIncrement: true, Unsigned: true},
		},
		{
			name:       "title",
			definition: "string:150",
			want:       Column{Name: "title", Type: "string", Attributes: []string{"150"}},
		},
		{
			name:       "slug",
			definition: "string:180 unique",
			want:       Column{Name: "slug", Type: "string", Attributes: []string{"180"}, Unique: true},
		},
		{
			name:       "content",
			definition: "longText nullable",
			want:       Column{Name: "content", Type: "longText", Nullable: true},
		},
		{
			name:       "amount",
			definition: "decimal:8,2",
			want:       Column{Name: "amount", Type: "decimal", Attributes: []string{"8", "2"}},
		},
		{
			name:       "default_quoted",
			definition: "string:20 default:'draft'",
			// Quotes are retained: the default is emitted raw as a SQL literal.
			want: Column{Name: "default_quoted", Type: "string", Attributes: []string{"20"}, Default: strptr("'draft'")},
		},
		{
			name:       "created_at",
			definition: "timestamp useCurrent",
			want:       Column{Name: "created_at", Type: "timestamp", UseCurrent: true},
		},
		{
			name:       "int alias",
			definition: "int",
			want:       Column{Name: "int alias", Type: "integer"},
		},
		{
			name:       "unknown token errors",
			definition: "bigtext",
			wantErr:    true,
		},
		{
			name:       "default without value errors",
			definition: "string default",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseColumn(tt.name, tt.definition)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none (column=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !columnEqual(got, tt.want) {
				t.Fatalf("ParseColumn(%q) =\n  %+v\nwant\n  %+v", tt.definition, got, tt.want)
			}
		})
	}
}

func TestParseColumnDefaultKeepsQuotedSpaceAsOneToken(t *testing.T) {
	// default:'a b' must stay a single token despite the space, with quotes retained.
	col, err := ParseColumn("note", "string default:'a b'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if col.Default == nil || *col.Default != "'a b'" {
		t.Fatalf("default = %v, want \"'a b'\"", col.Default)
	}
}

func TestGenerateSQLCreateAllDrivers(t *testing.T) {
	intent := TableIntent{
		Action: "create",
		Table:  "posts",
		Columns: map[string]string{
			"id":     "id",
			"title":  "string:150",
			"slug":   "string:180 unique",
			"active": "boolean default:1",
		},
	}

	// Per-driver substrings that must appear in the single CREATE statement.
	wants := map[string][]string{
		"postgres": {`CREATE TABLE "posts"`, `"id" BIGSERIAL NOT NULL PRIMARY KEY`, `"title" VARCHAR(150) NOT NULL`, `"slug" VARCHAR(180) NOT NULL UNIQUE`, `"active" BOOLEAN`},
		"mysql":    {"CREATE TABLE `posts`", "`id` BIGINT AUTO_INCREMENT NOT NULL PRIMARY KEY", "`title` VARCHAR(150) NOT NULL", "`active` BOOLEAN"},
		"sqlite":   {`CREATE TABLE "posts"`, `"id" INTEGER`, `"title" TEXT NOT NULL`, `"slug" TEXT NOT NULL UNIQUE`},
		"mssql":    {`CREATE TABLE [posts]`, `[id] BIGINT IDENTITY(1,1) NOT NULL PRIMARY KEY`, `[title] NVARCHAR(150) NOT NULL`},
	}

	for driver, substrs := range wants {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 {
				t.Fatalf("want 1 statement, got %d: %v", len(stmts), stmts)
			}
			for _, s := range substrs {
				if !strings.Contains(stmts[0], s) {
					t.Errorf("%s output missing %q\n--- got ---\n%s", driver, s, stmts[0])
				}
			}
		})
	}
}

func TestGenerateSQLDeterministicColumnOrder(t *testing.T) {
	intent := TableIntent{
		Action:  "create",
		Table:   "things",
		Columns: map[string]string{"zebra": "integer", "alpha": "integer", "mango": "integer"},
	}
	stmts, err := GenerateSQL("thing", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	out := stmts[0]
	ai := strings.Index(out, `"alpha"`)
	mi := strings.Index(out, `"mango"`)
	zi := strings.Index(out, `"zebra"`)
	if !(ai < mi && mi < zi) {
		t.Fatalf("columns not sorted alpha<mango<zebra:\n%s", out)
	}
}

func TestGenerateSQLTableNameDefaultsToPluralKey(t *testing.T) {
	intent := TableIntent{Action: "create", Columns: map[string]string{"id": "id"}}
	stmts, err := GenerateSQL("category", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if !strings.Contains(stmts[0], `"categorys"`) {
		t.Fatalf("expected pluralized key as table name, got:\n%s", stmts[0])
	}
}

func TestGenerateSQLCreateWithForeignKeyInline(t *testing.T) {
	intent := TableIntent{
		Action:  "create",
		Table:   "posts",
		Columns: map[string]string{"id": "id", "author_id": "bigInteger"},
		Foreign: []ForeignIntent{{
			Name:       "posts_author_fk",
			Columns:    []string{"author_id"},
			RefTable:   "users",
			RefColumns: []string{"id"},
			OnDelete:   "cascade",
		}},
	}
	// Inline FK must work on every driver, SQLite included.
	for _, driver := range []string{"postgres", "mysql", "sqlite", "mssql"} {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 {
				t.Fatalf("want single CREATE TABLE, got %d: %v", len(stmts), stmts)
			}
			fk := quoteIdent(driver, "posts_author_fk")
			if !strings.Contains(stmts[0], "CONSTRAINT "+fk+" FOREIGN KEY") {
				t.Errorf("missing inline FK constraint:\n%s", stmts[0])
			}
			if !strings.Contains(stmts[0], "ON DELETE CASCADE") {
				t.Errorf("missing ON DELETE CASCADE:\n%s", stmts[0])
			}
		})
	}
}

func TestGenerateSQLCreateWithIndexes(t *testing.T) {
	intent := TableIntent{
		Action:  "create",
		Table:   "posts",
		Columns: map[string]string{"id": "id", "slug": "string:180"},
		Indexes: []IndexIntent{{Name: "posts_slug_uq", Columns: []string{"slug"}, Unique: true}},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if len(stmts) != 2 {
		t.Fatalf("want CREATE TABLE + CREATE INDEX, got %d: %v", len(stmts), stmts)
	}
	if !strings.HasPrefix(stmts[0], `CREATE TABLE "posts"`) {
		t.Errorf("first stmt should be CREATE TABLE: %s", stmts[0])
	}
	if stmts[1] != `CREATE UNIQUE INDEX "posts_slug_uq" ON "posts" ("slug");` {
		t.Errorf("index stmt = %q", stmts[1])
	}
}

func TestGenerateSQLCreateForeignValidation(t *testing.T) {
	intent := TableIntent{
		Action:  "create",
		Table:   "posts",
		Columns: map[string]string{"id": "id"},
		Foreign: []ForeignIntent{{Name: "bad", Columns: []string{"author_id"}}}, // no RefTable/RefColumns
	}
	if _, err := GenerateSQL("post", intent, "postgres"); err == nil {
		t.Fatal("expected error for incomplete inline FK")
	}
}

func TestGenerateSQLDrop(t *testing.T) {
	stmts, err := GenerateSQL("post", TableIntent{Action: "drop", Table: "posts"}, "mysql")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if len(stmts) != 1 || stmts[0] != "DROP TABLE `posts`;" {
		t.Fatalf("drop = %v", stmts)
	}
}

func TestGenerateSQLAlterAddDrop(t *testing.T) {
	intent := TableIntent{
		Action:      "alter",
		Table:       "posts",
		AddColumns:  map[string]string{"views": "integer nullable"},
		DropColumns: []string{"legacy"},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, `ALTER TABLE "posts" ADD "views" INTEGER`) {
		t.Errorf("missing ADD: %s", joined)
	}
	if !strings.Contains(joined, `ALTER TABLE "posts" DROP COLUMN "legacy"`) {
		t.Errorf("missing DROP: %s", joined)
	}
}

func TestGenerateSQLAlterUniqueColumnAutoSplit(t *testing.T) {
	intent := TableIntent{
		Action:     "alter",
		Table:      "posts",
		AddColumns: map[string]string{"slug": "string:180 unique"},
	}
	// Auto-split must produce: plain ADD COLUMN + CREATE UNIQUE INDEX,
	// on every driver (including SQLite which rejects ADD COLUMN ... UNIQUE).
	for _, driver := range []string{"postgres", "mysql", "sqlite", "mssql"} {
		stmts, err := GenerateSQL("post", intent, driver)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", driver, err)
		}
		if len(stmts) != 2 {
			t.Fatalf("%s: want 2 statements, got %d: %v", driver, len(stmts), stmts)
		}
		// First: ADD COLUMN without UNIQUE
		if strings.Contains(stmts[0], "UNIQUE") {
			t.Errorf("%s: ADD COLUMN must not contain UNIQUE: %s", driver, stmts[0])
		}
		if !strings.Contains(stmts[0], "ADD") {
			t.Errorf("%s: first statement must be ADD COLUMN: %s", driver, stmts[0])
		}
		// Second: CREATE UNIQUE INDEX with auto-generated name posts_slug_uq
		if !strings.Contains(stmts[1], "CREATE UNIQUE INDEX") {
			t.Errorf("%s: second statement must be CREATE UNIQUE INDEX: %s", driver, stmts[1])
		}
		if !strings.Contains(stmts[1], "posts_slug_uq") {
			t.Errorf("%s: auto-index name must be posts_slug_uq: %s", driver, stmts[1])
		}
	}
}

func TestGenerateSQLAlterNonUniqueColumnNoSplit(t *testing.T) {
	intent := TableIntent{
		Action:     "alter",
		Table:      "posts",
		AddColumns: map[string]string{"views": "integer default:0"},
	}
	stmts, err := GenerateSQL("post", intent, "sqlite")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(stmts) != 1 {
		t.Fatalf("want 1 statement for non-unique column, got %d: %v", len(stmts), stmts)
	}
}

func TestGenerateSQLCreateRequiresColumns(t *testing.T) {
	_, err := GenerateSQL("post", TableIntent{Action: "create", Table: "posts"}, "postgres")
	if err == nil {
		t.Fatal("expected error for create with no columns")
	}
}

func TestGenerateSQLRaw(t *testing.T) {
	intent := TableIntent{
		Action: "raw",
		Statements: []string{
			"INSERT INTO roles (name) VALUES ('admin');",
			"",
			"UPDATE users SET role_id = 1 WHERE is_admin = true;",
		},
	}
	// Raw passes through verbatim (blank lines dropped), regardless of driver.
	for _, driver := range []string{"postgres", "mysql", "sqlite", "mssql"} {
		stmts, err := GenerateSQL("seed", intent, driver)
		if err != nil {
			t.Fatalf("%s: %v", driver, err)
		}
		if len(stmts) != 2 {
			t.Fatalf("%s: want 2 statements (blank dropped), got %d: %v", driver, len(stmts), stmts)
		}
		if stmts[0] != "INSERT INTO roles (name) VALUES ('admin');" ||
			stmts[1] != "UPDATE users SET role_id = 1 WHERE is_admin = true;" {
			t.Fatalf("%s: statements altered: %v", driver, stmts)
		}
	}
}

func TestGenerateSQLRawEmptyErrors(t *testing.T) {
	if _, err := GenerateSQL("seed", TableIntent{Action: "raw"}, "postgres"); err == nil {
		t.Error("expected error: raw with no statements")
	}
	if _, err := GenerateSQL("seed", TableIntent{Action: "raw", Statements: []string{"", "  "}}, "postgres"); err == nil {
		t.Error("expected error: raw with only blank statements")
	}
}

func TestGenerateSQLUnsupportedAction(t *testing.T) {
	_, err := GenerateSQL("post", TableIntent{Action: "truncate", Table: "posts"}, "postgres")
	if err == nil {
		t.Fatal("expected error for unsupported action")
	}
}

func TestSQLTypeMapping(t *testing.T) {
	tests := []struct {
		col    Column
		driver string
		want   string
	}{
		{Column{Type: "string"}, "postgres", "VARCHAR(255)"},
		{Column{Type: "string"}, "sqlite", "TEXT"},
		{Column{Type: "string"}, "mssql", "NVARCHAR(255)"},
		{Column{Type: "string", Attributes: []string{"64"}}, "mysql", "VARCHAR(64)"},
		{Column{Type: "integer", AutoIncrement: true}, "postgres", "SERIAL"},
		{Column{Type: "bigInteger", AutoIncrement: true}, "postgres", "BIGSERIAL"},
		{Column{Type: "bigInteger"}, "sqlite", "INTEGER"},
		{Column{Type: "boolean"}, "mssql", "BIT"},
		{Column{Type: "decimal", Attributes: []string{"10", "4"}}, "mysql", "DECIMAL(10,4)"},
		{Column{Type: "decimal"}, "postgres", "DECIMAL(8,2)"},
		{Column{Type: "json"}, "sqlite", "TEXT"},
		{Column{Type: "jsonb"}, "postgres", "JSONB"},
		{Column{Type: "uuid"}, "mysql", "CHAR(36)"},
		{Column{Type: "enum", Attributes: []string{"a", "b"}}, "mysql", "ENUM('a','b')"},
		{Column{Type: "enum", Attributes: []string{"a", "b"}}, "postgres", "TEXT"},
		{Column{Type: "timestamp"}, "mssql", "DATETIME2"},
	}
	for _, tt := range tests {
		got := sqlType(tt.col, tt.driver)
		if got != tt.want {
			t.Errorf("sqlType(%+v, %q) = %q, want %q", tt.col, tt.driver, got, tt.want)
		}
	}
}

func TestQuoteIdent(t *testing.T) {
	cases := []struct {
		driver, ident, want string
	}{
		{"postgres", "posts", `"posts"`},
		{"mysql", "posts", "`posts`"},
		{"mssql", "posts", "[posts]"},
		{"sqlite", "posts", `"posts"`},
		{"postgres", `we"ird`, `"we""ird"`},
		{"mysql", "ba`d", "`ba``d`"},
		{"mssql", "br]k", "[br]]k]"},
	}
	for _, c := range cases {
		if got := quoteIdent(c.driver, c.ident); got != c.want {
			t.Errorf("quoteIdent(%q,%q) = %q, want %q", c.driver, c.ident, got, c.want)
		}
	}
}

func strptr(s string) *string { return &s }

func columnEqual(a, b Column) bool {
	if a.Name != b.Name || a.Type != b.Type || a.Nullable != b.Nullable ||
		a.Primary != b.Primary || a.Unique != b.Unique || a.AutoIncrement != b.AutoIncrement ||
		a.Unsigned != b.Unsigned || a.UseCurrent != b.UseCurrent {
		return false
	}
	if (a.Default == nil) != (b.Default == nil) {
		return false
	}
	if a.Default != nil && *a.Default != *b.Default {
		return false
	}
	if len(a.Attributes) != len(b.Attributes) {
		return false
	}
	for i := range a.Attributes {
		if a.Attributes[i] != b.Attributes[i] {
			return false
		}
	}
	return true
}
