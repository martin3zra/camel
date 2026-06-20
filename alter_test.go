package camel

import (
	"strings"
	"testing"
)

func TestAlterModifyColumn(t *testing.T) {
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		ModifyColumns: map[string]string{"title": "string:200 nullable"},
	}

	tests := []struct {
		driver string
		want   []string // all must be present, in order
	}{
		{"mysql", []string{"ALTER TABLE `posts` MODIFY `title` VARCHAR(200);"}},
		{"postgres", []string{
			`ALTER TABLE "posts" ALTER COLUMN "title" TYPE VARCHAR(200);`,
			`ALTER TABLE "posts" ALTER COLUMN "title" DROP NOT NULL;`,
		}},
		{"mssql", []string{`ALTER TABLE [posts] ALTER COLUMN [title] NVARCHAR(200) NULL;`}},
	}
	for _, tt := range tests {
		t.Run(tt.driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, tt.driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			joined := strings.Join(stmts, "\n")
			for _, w := range tt.want {
				if !strings.Contains(joined, w) {
					t.Errorf("missing %q in:\n%s", w, joined)
				}
			}
		})
	}
}

func TestAlterModifyColumnPostgresSetNotNullAndDefault(t *testing.T) {
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		ModifyColumns: map[string]string{"views": "integer default:0"},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	for _, w := range []string{
		`ALTER COLUMN "views" TYPE INTEGER;`,
		`ALTER COLUMN "views" SET NOT NULL;`,
		`ALTER COLUMN "views" SET DEFAULT 0;`,
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("missing %q in:\n%s", w, joined)
		}
	}
}

func TestAlterModifyColumnSQLiteRejected(t *testing.T) {
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		ModifyColumns: map[string]string{"title": "string:200"},
	}
	if _, err := GenerateSQL("post", intent, "sqlite"); err == nil {
		t.Fatal("expected error: sqlite cannot modify columns")
	}
}

func TestAlterRenameColumn(t *testing.T) {
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		RenameColumns: map[string]string{"body": "content"},
	}
	cases := map[string]string{
		"postgres": `ALTER TABLE "posts" RENAME COLUMN "body" TO "content";`,
		"mysql":    "ALTER TABLE `posts` RENAME COLUMN `body` TO `content`;",
		"sqlite":   `ALTER TABLE "posts" RENAME COLUMN "body" TO "content";`,
		"mssql":    `EXEC sp_rename 'posts.body', 'content', 'COLUMN';`,
	}
	for driver, want := range cases {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 || stmts[0] != want {
				t.Fatalf("got %v, want %q", stmts, want)
			}
		})
	}
}

func TestAlterCreateIndex(t *testing.T) {
	intent := TableIntent{
		Action: "alter",
		Table:  "posts",
		AddIndexes: []IndexIntent{
			{Name: "posts_slug_uq", Columns: []string{"slug"}, Unique: true},
			{Name: "posts_author_created", Columns: []string{"author_id", "created_at"}},
		},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	if !strings.Contains(joined, `CREATE UNIQUE INDEX "posts_slug_uq" ON "posts" ("slug");`) {
		t.Errorf("unique index missing:\n%s", joined)
	}
	if !strings.Contains(joined, `CREATE INDEX "posts_author_created" ON "posts" ("author_id", "created_at");`) {
		t.Errorf("composite index missing:\n%s", joined)
	}
}

func TestAlterDropIndexDialects(t *testing.T) {
	intent := TableIntent{Action: "alter", Table: "posts", DropIndexes: []string{"posts_slug_uq"}}
	cases := map[string]string{
		"postgres": `DROP INDEX "posts_slug_uq";`,
		"sqlite":   `DROP INDEX "posts_slug_uq";`,
		"mysql":    "DROP INDEX `posts_slug_uq` ON `posts`;",
		"mssql":    `DROP INDEX [posts_slug_uq] ON [posts];`,
	}
	for driver, want := range cases {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 || stmts[0] != want {
				t.Fatalf("got %v, want %q", stmts, want)
			}
		})
	}
}

func TestAlterIndexValidation(t *testing.T) {
	noName := TableIntent{Action: "alter", Table: "posts", AddIndexes: []IndexIntent{{Columns: []string{"slug"}}}}
	if _, err := GenerateSQL("post", noName, "postgres"); err == nil {
		t.Error("expected error for index without name")
	}
	noCols := TableIntent{Action: "alter", Table: "posts", AddIndexes: []IndexIntent{{Name: "idx"}}}
	if _, err := GenerateSQL("post", noCols, "postgres"); err == nil {
		t.Error("expected error for index without columns")
	}
}

func TestAlterAddForeignKey(t *testing.T) {
	intent := TableIntent{
		Action: "alter",
		Table:  "posts",
		AddForeign: []ForeignIntent{{
			Name:       "posts_author_fk",
			Columns:    []string{"author_id"},
			RefTable:   "users",
			RefColumns: []string{"id"},
			OnDelete:   "cascade",
			OnUpdate:   "restrict",
		}},
	}
	cases := map[string]string{
		"postgres": `ALTER TABLE "posts" ADD CONSTRAINT "posts_author_fk" FOREIGN KEY ("author_id") REFERENCES "users" ("id") ON DELETE CASCADE ON UPDATE RESTRICT;`,
		"mysql":    "ALTER TABLE `posts` ADD CONSTRAINT `posts_author_fk` FOREIGN KEY (`author_id`) REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT;",
		"mssql":    `ALTER TABLE [posts] ADD CONSTRAINT [posts_author_fk] FOREIGN KEY ([author_id]) REFERENCES [users] ([id]) ON DELETE CASCADE ON UPDATE RESTRICT;`,
	}
	for driver, want := range cases {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 || stmts[0] != want {
				t.Fatalf("got %v, want %q", stmts, want)
			}
		})
	}
}

func TestAlterForeignKeySQLiteRejected(t *testing.T) {
	add := TableIntent{Action: "alter", Table: "posts", AddForeign: []ForeignIntent{{Name: "fk", Columns: []string{"a"}, RefTable: "t", RefColumns: []string{"id"}}}}
	if _, err := GenerateSQL("post", add, "sqlite"); err == nil {
		t.Error("expected error: sqlite cannot add FK via alter")
	}
	drop := TableIntent{Action: "alter", Table: "posts", DropForeign: []string{"fk"}}
	if _, err := GenerateSQL("post", drop, "sqlite"); err == nil {
		t.Error("expected error: sqlite cannot drop FK via alter")
	}
}

func TestAlterDropForeignKey(t *testing.T) {
	intent := TableIntent{Action: "alter", Table: "posts", DropForeign: []string{"posts_author_fk"}}
	cases := map[string]string{
		"postgres": `ALTER TABLE "posts" DROP CONSTRAINT "posts_author_fk";`,
		"mysql":    "ALTER TABLE `posts` DROP FOREIGN KEY `posts_author_fk`;",
		"mssql":    `ALTER TABLE [posts] DROP CONSTRAINT [posts_author_fk];`,
	}
	for driver, want := range cases {
		t.Run(driver, func(t *testing.T) {
			stmts, err := GenerateSQL("post", intent, driver)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			if len(stmts) != 1 || stmts[0] != want {
				t.Fatalf("got %v, want %q", stmts, want)
			}
		})
	}
}

func TestAlterForeignKeyValidation(t *testing.T) {
	bad := TableIntent{Action: "alter", Table: "posts", AddForeign: []ForeignIntent{{Name: "fk", Columns: []string{"a"}}}}
	if _, err := GenerateSQL("post", bad, "postgres"); err == nil {
		t.Error("expected error for FK missing references")
	}
}

func TestReferentialAction(t *testing.T) {
	cases := map[string]string{
		"cascade":     "CASCADE",
		"CASCADE":     "CASCADE",
		"set null":    "SET NULL",
		"set_null":    "SET NULL",
		"no action":   "NO ACTION",
		"restrict":    "RESTRICT",
		"set default": "SET DEFAULT",
		"":            "",
		"garbage":     "",
	}
	for in, want := range cases {
		if got := referentialAction(in); got != want {
			t.Errorf("referentialAction(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAlterDropColumnMSSQLDropsDefaultConstraint(t *testing.T) {
	intent := TableIntent{Action: "alter", Table: "posts", DropColumns: []string{"view_count"}}

	// Postgres/MySQL: a plain DROP COLUMN.
	pg, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	if len(pg) != 1 || pg[0] != `ALTER TABLE "posts" DROP COLUMN "view_count";` {
		t.Fatalf("postgres drop = %v", pg)
	}

	// SQL Server: must look up and drop the bound DEFAULT constraint first.
	ms, err := GenerateSQL("post", intent, "mssql")
	if err != nil {
		t.Fatalf("mssql: %v", err)
	}
	if len(ms) != 1 {
		t.Fatalf("want 1 batch, got %d", len(ms))
	}
	for _, want := range []string{"sys.default_constraints", "DROP CONSTRAINT", "DROP COLUMN [view_count]"} {
		if !strings.Contains(ms[0], want) {
			t.Errorf("mssql drop missing %q:\n%s", want, ms[0])
		}
	}
}

func TestAlterModifyThenRenameOrder(t *testing.T) {
	// Regression: rename must precede modify so a modify can target the new name.
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		RenameColumns: map[string]string{"title": "headline"},
		ModifyColumns: map[string]string{"headline": "string:200"},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	joined := strings.Join(stmts, "\n")
	ri := strings.Index(joined, "RENAME COLUMN")
	mi := strings.Index(joined, `ALTER COLUMN "headline" TYPE`)
	if ri == -1 || mi == -1 || ri > mi {
		t.Fatalf("rename must come before modify:\n%s", joined)
	}
}

func TestAlterCombinedOperationOrder(t *testing.T) {
	// A single alter intent exercising several operation kinds together.
	intent := TableIntent{
		Action:        "alter",
		Table:         "posts",
		AddColumns:    map[string]string{"views": "integer nullable"},
		ModifyColumns: map[string]string{"title": "string:200"},
		RenameColumns: map[string]string{"body": "content"},
		DropColumns:   []string{"legacy"},
		AddIndexes:    []IndexIntent{{Name: "posts_views_idx", Columns: []string{"views"}}},
	}
	stmts, err := GenerateSQL("post", intent, "postgres")
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}
	if len(stmts) < 5 {
		t.Fatalf("expected at least 5 statements, got %d:\n%s", len(stmts), strings.Join(stmts, "\n"))
	}
}
