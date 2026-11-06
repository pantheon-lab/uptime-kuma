// Command kuma-import copies monitors, history, the account, status pages and maintenance
// windows from an Uptime Kuma MariaDB database. It's a
// dry run unless -apply is set. Connection details come from the environment, so that passwords
// don't show up in process listings or shell history.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/importer"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

func main() {
	apply := flag.Bool("apply", false, "write to DATABASE_URL. Without it, nothing is written")
	activate := flag.Bool("activate", false, "with -apply, resume monitors that were active in the source. They start after the service restarts")
	verify := flag.Bool("verify", false, "compare what's in DATABASE_URL with the source, monitor by monitor, and exit non-zero if any differ. Imports nothing")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *verify {
		if *apply || *activate {
			log.Error("-verify can't be combined with -apply or -activate: import first, then verify")
			os.Exit(2)
		}
		ok, err := runVerify(log)
		if err != nil {
			log.Error("verify failed", "error", err)
			os.Exit(1)
		}
		if !ok {
			log.Error("some monitors differ from the source")
			os.Exit(3)
		}
		return
	}
	if err := run(*apply, *activate, log); err != nil {
		log.Error("import failed", "error", err)
		os.Exit(1)
	}
}

func run(apply, activate bool, log *slog.Logger) error {
	if activate && !apply {
		return errors.New("-activate needs -apply")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	source, err := openSource(os.Getenv("SOURCE_MARIADB_DSN"))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	options := importer.Options{
		Source: source, Activate: activate, Log: log,
		AuthSecret: os.Getenv("SOURCE_AUTH_SECRET"), DataDir: os.Getenv("SOURCE_DATA_DIR"),
	}
	if apply {
		db, err := openTarget(ctx, "with -apply")
		if err != nil {
			return err
		}
		defer db.Close()
		options.Target = db
	} else {
		log.Info("dry run, nothing will be written. Use -apply to import")
	}

	start := time.Now()
	report, err := importer.Run(ctx, options)
	if err != nil {
		return err
	}
	log.Info("finished", "duration", time.Since(start).Round(time.Millisecond))
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// openTarget opens DATABASE_URL, with the key for stored secrets.
func openTarget(ctx context.Context, required string) (*store.Store, error) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL is required " + required)
	}
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	// The same key as the service's, so that what's imported is encrypted as it would be.
	keys, err := secrets.FromEnv()
	if err != nil {
		db.Close()
		return nil, err
	}
	db.SetKeyring(keys)
	if _, err := db.SealSecrets(ctx, false); err != nil {
		db.Close()
		return nil, fmt.Errorf("stored secrets: %w", err)
	}
	return db, nil
}

// runVerify compares the import with the source, prints the report, and returns whether every
// monitor matches.
func runVerify(log *slog.Logger) (bool, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	source, err := openSource(os.Getenv("SOURCE_MARIADB_DSN"))
	if err != nil {
		return false, err
	}
	defer func() { _ = source.Close() }()
	db, err := openTarget(ctx, "with -verify")
	if err != nil {
		return false, err
	}
	defer db.Close()
	start := time.Now()
	report, err := importer.Verify(ctx, source, db)
	if err != nil {
		return false, err
	}
	log.Info("verified", "monitors", report.Monitors, "matching", report.Matching, "differing", len(report.Differing),
		"duration", time.Since(start).Round(time.Millisecond))
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		return false, err
	}
	return report.OK, nil
}

func openSource(dsn string) (*sql.DB, error) {
	if dsn == "" {
		return nil, errors.New("SOURCE_MARIADB_DSN is required, for example user:password@tcp(host:3306)/kuma")
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		// The error can quote the DSN, which contains the password.
		return nil, errors.New("SOURCE_MARIADB_DSN isn't a valid DSN, for example user:password@tcp(host:3306)/kuma")
	}
	// Datetimes come back as text and are read as UTC, the way the source stored them.
	config.ParseTime = false
	config.Loc = time.UTC
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return nil, errors.New("SOURCE_MARIADB_DSN can't be used")
	}
	return sql.OpenDB(connector), nil
}
