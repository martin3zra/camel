package camel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v2"
)

const DefaultConfigFile = "camel.yaml"

type Config struct {
	DB        DBConfig        `json:"db" yaml:"db"`
	Migration MigrationConfig `json:"migration" yaml:"migration"`
}

type DBConfig struct {
	Driver string `json:"driver" yaml:"driver"`
	Source string `json:"source" yaml:"source"`
}

type MigrationConfig struct {
	Directory string `json:"directory" yaml:"directory"`
	Pattern   string `json:"pattern" yaml:"pattern"`
	Table     string `json:"table" yaml:"table"`
}

func DefaultConfig() Config {
	return Config{
		DB: DBConfig{
			Driver: "sqlite",
			Source: "camel.sqlite",
		},
		Migration: MigrationConfig{
			Directory: "database",
			Pattern:   "*.yaml",
			Table:     "camel_migrations",
		},
	}
}

func DefaultConfigYAML(env map[string]string) string {
	driver := valueOr(env["DB_DRIVER"], env["DATABASE_DRIVER"], "sqlite")
	source := valueOr(env["DB_SOURCE"], env["DATABASE_URL"], "camel.sqlite")

	return fmt.Sprintf(`# camel.yaml
# Camel reads .env first, then environment variables. DB_DRIVER and DB_SOURCE override these values.
db:
  driver: %q # sqlite, postgres, mysql, mssql
  source: %q

migration:
  directory: "database"
  pattern: "*.yaml"
  table: "camel_migrations"
`, driver, source)
}

func InitProject(dir string) error {
	env, _ := ReadDotEnv(filepath.Join(dir, ".env"))
	configPath := filepath.Join(dir, DefaultConfigFile)
	if _, err := os.Stat(configPath); err == nil {
		return fmt.Errorf("%s already exists", DefaultConfigFile)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	if err := os.WriteFile(configPath, []byte(DefaultConfigYAML(env)), 0644); err != nil {
		return err
	}

	migrationDir := filepath.Join(dir, "database")
	return os.MkdirAll(migrationDir, 0755)
}

func LoadConfig(dir string) (Config, error) {
	cfg := DefaultConfig()

	env, _ := ReadDotEnv(filepath.Join(dir, ".env"))
	configPath := filepath.Join(dir, DefaultConfigFile)
	content, err := os.ReadFile(configPath)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", DefaultConfigFile, err)
	}

	switch strings.ToLower(filepath.Ext(configPath)) {
	case ".json":
		err = json.Unmarshal(content, &cfg)
	default:
		err = yaml.Unmarshal(content, &cfg)
	}
	if err != nil {
		return cfg, fmt.Errorf("parse %s: %w", DefaultConfigFile, err)
	}

	cfg.DB.Driver = valueOr(os.Getenv("DB_DRIVER"), os.Getenv("DATABASE_DRIVER"), env["DB_DRIVER"], env["DATABASE_DRIVER"], cfg.DB.Driver)
	cfg.DB.Source = valueOr(os.Getenv("DB_SOURCE"), os.Getenv("DATABASE_URL"), env["DB_SOURCE"], env["DATABASE_URL"], cfg.DB.Source)
	cfg.Migration.Directory = valueOr(cfg.Migration.Directory, "database")
	cfg.Migration.Pattern = valueOr(cfg.Migration.Pattern, "*.yaml")
	cfg.Migration.Table = valueOr(cfg.Migration.Table, "camel_migrations")

	return cfg, nil
}

func ReadDotEnv(path string) (map[string]string, error) {
	values := map[string]string{}
	content, err := os.ReadFile(path)
	if err != nil {
		return values, err
	}

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			values[key] = value
		}
	}

	return values, nil
}

func valueOr(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
