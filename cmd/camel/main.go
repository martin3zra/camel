package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/martin3zra/camel"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	dir, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	switch os.Args[1] {
	case "init":
		if err := camel.InitProject(dir); err != nil {
			log.Fatal(err)
		}
		fmt.Println("Created camel.yaml and database/")
	case "config":
		cfg, err := camel.LoadConfig(dir)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%+v\n", cfg)
	case "make", "create":
		format, name := parseMakeArgs(os.Args[2:])
		if name == "" {
			log.Fatal("migration name is required")
		}
		cfg, err := camel.LoadConfig(dir)
		if err != nil {
			log.Fatal(err)
		}
		path, err := camel.CreateMigrationFile(dir, cfg, name, format)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Created %s\n", path)
	case "plan":
		cmd := flag.NewFlagSet("plan", flag.ExitOnError)
		file := cmd.String("file", "", "migration file to plan")
		direction := cmd.String("direction", "up", "up or down")
		_ = cmd.Parse(os.Args[2:])
		cfg, err := camel.LoadConfig(dir)
		if err != nil {
			log.Fatal(err)
		}
		if *file == "" {
			migrations, err := camel.ListMigrations(cfg, dir)
			if err != nil {
				log.Fatal(err)
			}
			for _, migration := range migrations {
				printPlan(migration, camel.Direction(*direction), cfg.DB.Driver)
			}
			return
		}
		path := *file
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		migration, err := camel.LoadMigration(path)
		if err != nil {
			log.Fatal(err)
		}
		printPlan(migration, camel.Direction(*direction), cfg.DB.Driver)
	case "migrate":
		cmd := flag.NewFlagSet("migrate", flag.ExitOnError)
		pretend := cmd.Bool("pretend", false, "print SQL without executing")
		dryRun := cmd.Bool("dry-run", false, "run pending migrations against the real database inside a rolled-back transaction; nothing is committed or recorded")
		_ = cmd.Parse(os.Args[2:])
		if *pretend && *dryRun {
			log.Fatal("--pretend and --dry-run are mutually exclusive")
		}
		runWithDB(dir, func(r *camel.Runner) error {
			if *dryRun {
				return r.MigrateDryRun()
			}
			return r.Migrate(*pretend)
		})
	case "rollback":
		cmd := flag.NewFlagSet("rollback", flag.ExitOnError)
		pretend := cmd.Bool("pretend", false, "print SQL without executing")
		step := cmd.Int("step", 0, "reverse the last N migrations (default: last batch)")
		all := cmd.Bool("all", false, "reverse every applied migration")
		_ = cmd.Parse(os.Args[2:])
		runWithDB(dir, func(r *camel.Runner) error {
			return r.Rollback(camel.RollbackOptions{Steps: *step, All: *all, Pretend: *pretend})
		})
	case "reset":
		cmd := flag.NewFlagSet("reset", flag.ExitOnError)
		pretend := cmd.Bool("pretend", false, "print SQL without executing")
		_ = cmd.Parse(os.Args[2:])
		runWithDB(dir, func(r *camel.Runner) error {
			return r.Reset(*pretend)
		})
	case "dump":
		cmd := flag.NewFlagSet("dump", flag.ExitOnError)
		prune := cmd.Bool("prune", false, "delete applied migration files after dumping")
		_ = cmd.Parse(os.Args[2:])
		runWithDB(dir, func(r *camel.Runner) error {
			return r.Dump(*prune)
		})
	case "skill":
		if len(os.Args) < 3 || os.Args[2] != "install" {
			usage()
			os.Exit(1)
		}
		cmd := flag.NewFlagSet("skill install", flag.ExitOnError)
		force := cmd.Bool("force", false, "overwrite existing skill files")
		_ = cmd.Parse(os.Args[3:])
		written, skipped, err := camel.InstallDBScaffoldSkill(dir, *force)
		if err != nil {
			log.Fatal(err)
		}
		for _, path := range written {
			fmt.Printf("Installed %s\n", path)
		}
		for _, path := range skipped {
			fmt.Printf("Skipped %s (already exists, use --force to overwrite)\n", path)
		}
	case "status":
		runWithDB(dir, func(r *camel.Runner) error {
			statuses, err := r.Status()
			if err != nil {
				return err
			}
			for _, status := range statuses {
				mark := "pending"
				if status.Applied {
					mark = "applied"
				}
				fmt.Printf("%-8s %s\n", mark, status.Name)
			}
			return nil
		})
	default:
		usage()
		os.Exit(1)
	}
}

func printPlan(migration camel.Migration, direction camel.Direction, driver string) {
	statements, err := camel.StatementsFor(migration, direction, driver)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("-- %s\n", migration.Name)
	for _, statement := range statements {
		fmt.Println(statement)
	}
}

func runWithDB(dir string, fn func(*camel.Runner) error) {
	cfg, err := camel.LoadConfig(dir)
	if err != nil {
		log.Fatal(err)
	}
	runner, err := camel.NewRunner(cfg, dir)
	if err != nil {
		log.Fatal(err)
	}
	defer runner.Close()
	if err := fn(runner); err != nil {
		log.Fatal(err)
	}
}

// parseMakeArgs extracts the --format flag and migration name from args,
// accepting them in any order (e.g. both `make name --format json` and
// `make --format json name` work).
func parseMakeArgs(args []string) (format, name string) {
	format = "yaml"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--format" || arg == "-format":
			if i+1 < len(args) {
				format = args[i+1]
				i++
			}
		case strings.HasPrefix(arg, "--format="):
			format = strings.TrimPrefix(arg, "--format=")
		case strings.HasPrefix(arg, "-format="):
			format = strings.TrimPrefix(arg, "-format=")
		case !strings.HasPrefix(arg, "-"):
			if name == "" {
				name = arg
			}
		}
	}
	return format, name
}

func usage() {
	fmt.Println("Usage:")
	fmt.Println("  camel init")
	fmt.Println("  camel config")
	fmt.Println("  camel make <name> [--format yaml|json|sql]")
	fmt.Println("  camel plan [--file path] [--direction up|down]")
	fmt.Println("  camel migrate [--pretend] [--dry-run]")
	fmt.Println("  camel rollback [--step N] [--all] [--pretend]")
	fmt.Println("  camel reset [--pretend]")
	fmt.Println("  camel status")
	fmt.Println("  camel dump [--prune]")
	fmt.Println("  camel skill install [--force]")
}
