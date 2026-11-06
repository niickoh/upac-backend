package app

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/url"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openDB usa exactamente la misma conexión que sphere-go: pgxpool, sslmode
// configurable, connect_timeout de 10 s, sesión en UTC y máximo 10 conexiones.
func openDB(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.DBUser, cfg.DBPass),
		Host:   net.JoinHostPort(cfg.DBHost, cfg.DBPort),
		Path:   "/" + cfg.DBName,
	}
	q := u.Query()
	q.Set("sslmode", cfg.DBSSLMode)
	q.Set("connect_timeout", "10")
	u.RawQuery = q.Encode()

	pc, err := pgxpool.ParseConfig(u.String())
	if err != nil {
		return nil, fmt.Errorf("configuración de base de datos inválida: %w", err)
	}
	pc.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pc.MaxConns = 10
	pc.MaxConnIdleTime = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("no se pudo conectar a PostgreSQL: %w", err)
	}
	return pool, nil
}

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migrate aplica en orden los .sql de migrations/ que aún no estén registrados
// en inscripciones_migraciones. Cada archivo corre en su propia transacción.
func migrate(ctx context.Context, db *pgxpool.Pool) error {
	if _, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS inscripciones_migraciones (
		nombre     varchar(255) PRIMARY KEY,
		aplicada   timestamptz  NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("tabla de migraciones: %w", err)
	}

	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		var exists bool
		if err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inscripciones_migraciones WHERE nombre = $1)`, f).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		sqlText, err := migrationsFS.ReadFile(f)
		if err != nil {
			return err
		}
		if err := pgx.BeginFunc(ctx, db, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sqlText)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO inscripciones_migraciones (nombre) VALUES ($1)`, f)
			return err
		}); err != nil {
			return fmt.Errorf("migración %s: %w", f, err)
		}
		log.Printf("migración aplicada: %s", f)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}
