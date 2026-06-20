package camel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigDefaultsWhenMissingFields(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, DefaultConfigFile), "db:\n  driver: mysql\n  source: dsn\n")

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DB.Driver != "mysql" || cfg.DB.Source != "dsn" {
		t.Fatalf("db = %+v", cfg.DB)
	}
	// Migration block omitted -> defaults fill in.
	if cfg.Migration.Directory != "database" || cfg.Migration.Pattern != "*.yaml" || cfg.Migration.Table != "camel_migrations" {
		t.Fatalf("migration defaults not applied: %+v", cfg.Migration)
	}
}

func TestLoadConfigEnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, DefaultConfigFile), "db:\n  driver: sqlite\n  source: file.db\n")

	t.Setenv("DB_DRIVER", "postgres")
	t.Setenv("DB_SOURCE", "postgres://x")

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DB.Driver != "postgres" || cfg.DB.Source != "postgres://x" {
		t.Fatalf("env did not override file: %+v", cfg.DB)
	}
}

func TestLoadConfigDotEnvUsedWhenNoProcessEnv(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, DefaultConfigFile), "db:\n  driver: sqlite\n  source: file.db\n")
	mustWrite(t, filepath.Join(dir, ".env"), "# comment\nDB_DRIVER=mysql\nDB_SOURCE=\"mysql://y\"\n")

	// Ensure process env does not shadow the .env values under test.
	t.Setenv("DB_DRIVER", "")
	t.Setenv("DB_SOURCE", "")

	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.DB.Driver != "mysql" || cfg.DB.Source != "mysql://y" {
		t.Fatalf(".env not applied: %+v", cfg.DB)
	}
}

func TestInitProjectCreatesFilesAndIsIdempotentGuard(t *testing.T) {
	dir := t.TempDir()
	if err := InitProject(dir); err != nil {
		t.Fatalf("InitProject: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DefaultConfigFile)); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if info, err := os.Stat(filepath.Join(dir, "database")); err != nil || !info.IsDir() {
		t.Fatalf("database dir not created: %v", err)
	}
	// Second init must refuse rather than clobber.
	if err := InitProject(dir); err == nil {
		t.Fatal("expected error re-initializing existing project")
	}
}

func TestReadDotEnvParsing(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, ".env"), "\n# c\nFOO=bar\nBAZ = \"q u x\"\nNOEQ\n")
	env, err := ReadDotEnv(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf("ReadDotEnv: %v", err)
	}
	if env["FOO"] != "bar" {
		t.Errorf("FOO = %q", env["FOO"])
	}
	if env["BAZ"] != "q u x" {
		t.Errorf("BAZ = %q", env["BAZ"])
	}
	if _, ok := env["NOEQ"]; ok {
		t.Errorf("line without = should be skipped")
	}
}
