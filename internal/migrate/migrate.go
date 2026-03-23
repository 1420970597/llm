package migrate

import (
  "context"
  "fmt"
  "io/fs"
  "os"
  "path/filepath"
  "sort"
  "strings"

  "github.com/jackc/pgx/v5/pgxpool"
)

func Run(ctx context.Context, db *pgxpool.Pool, migrationPath string) error {
  entries, err := os.ReadDir(migrationPath)
  if err != nil {
    return fmt.Errorf("read migrations: %w", err)
  }

  names := make([]string, 0, len(entries))
  for _, entry := range entries {
    if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
      continue
    }
    names = append(names, entry.Name())
  }
  sort.Strings(names)

  if _, err := db.Exec(ctx, `
    CREATE TABLE IF NOT EXISTS schema_migrations (
      filename TEXT PRIMARY KEY,
      applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
    )`); err != nil {
    return fmt.Errorf("create schema_migrations: %w", err)
  }

  for _, name := range names {
    var exists bool
    if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE filename = $1)`, name).Scan(&exists); err != nil {
      return fmt.Errorf("check migration %s: %w", name, err)
    }
    if exists {
      continue
    }

    content, err := fs.ReadFile(os.DirFS(filepath.Dir(migrationPath)), filepath.Join(filepath.Base(migrationPath), name))
    if err != nil {
      return fmt.Errorf("read migration %s: %w", name, err)
    }

    if _, err := db.Exec(ctx, string(content)); err != nil {
      return fmt.Errorf("apply migration %s: %w", name, err)
    }
    if _, err := db.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, name); err != nil {
      return fmt.Errorf("record migration %s: %w", name, err)
    }
  }

  return nil
}
