package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shohiebsense/leutik-tarik/migrations"
)

// defaultMigrateTable records what has been applied. It is created with inline
// DDL rather than a migration, because it must exist before any migration runs.
const defaultMigrateTable = "schema_migrations"

func migrateTableDDL(table string) string {
	return `CREATE TABLE IF NOT EXISTS ` + table + ` (
	version     UInt32,
	name        String,
	checksum    String,
	applied_at  DateTime64(3, 'UTC')
) ENGINE = MergeTree ORDER BY version`
}

// WithMigrateTable overrides the bookkeeping table. Integration tests use it to
// stay isolated from the real migration history.
func (c *ClickHouse) WithMigrateTable(name string) *ClickHouse {
	c.migrateTable = name
	return c
}

func (c *ClickHouse) table() string {
	if c.migrateTable == "" {
		return defaultMigrateTable
	}
	return c.migrateTable
}

// AppliedMigration is one row of schema_migrations.
type AppliedMigration struct {
	Version  int
	Name     string
	Checksum string
}

// Migrate applies every embedded migration that has not run yet, in version
// order, and is safe to call repeatedly.
//
// It deliberately supports arbitrary statements, not just CREATE: schema
// evolution uses `ALTER TABLE ... ADD COLUMN` in a new migration, which is the
// gap that previously made CREATE TABLE IF NOT EXISTS silently keep old
// columns (docs/review-2026-11-17.md).
func (c *ClickHouse) Migrate(ctx context.Context) error {
	all, err := migrations.All()
	if err != nil {
		return err
	}
	return c.MigrateFrom(ctx, all)
}

// MigrateFrom is Migrate with an explicit migration list, so tests can drive
// the ALTER path without inventing files.
func (c *ClickHouse) MigrateFrom(ctx context.Context, all []migrations.Migration) error {
	if err := c.exec(ctx, migrateTableDDL(c.table())); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	applied, err := c.appliedMigrations(ctx)
	if err != nil {
		return err
	}

	for _, m := range all {
		sum := checksum(m.SQL)
		if prev, ok := applied[m.Version]; ok {
			// A changed checksum means history was rewritten. Refusing is the
			// only safe answer: the database state no longer matches the code.
			if prev.Checksum != sum {
				return fmt.Errorf("migration %d (%s) changed after being applied: database state no longer matches the repository", m.Version, m.Name)
			}
			continue
		}
		for _, stmt := range SplitStatements(m.SQL) {
			if err := c.exec(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
			}
		}
		if err := c.exec(ctx, fmt.Sprintf(
			"INSERT INTO %s (version, name, checksum, applied_at) VALUES (%d, '%s', '%s', now64(3))",
			c.table(), m.Version, escapeString(m.Name), sum)); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	return nil
}

func (c *ClickHouse) appliedMigrations(ctx context.Context) (map[int]AppliedMigration, error) {
	body, err := c.rawQuery(ctx, "SELECT version, name, checksum FROM "+c.table()+" FORMAT JSONEachRow")
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	out := make(map[int]AppliedMigration)
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Version  int    `json:"version"`
			Name     string `json:"name"`
			Checksum string `json:"checksum"`
		}
		if err := jsonUnmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("parse schema_migrations row: %w", err)
		}
		out[row.Version] = AppliedMigration{Version: row.Version, Name: row.Name, Checksum: row.Checksum}
	}
	return out, nil
}

// exec runs a statement with no result set.
func (c *ClickHouse) exec(ctx context.Context, sql string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/?query="+url.QueryEscape(sql), strings.NewReader(""))
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg := readLimited(resp.Body)
		return fmt.Errorf("http %d: %s", resp.StatusCode, msg)
	}
	return nil
}

func (c *ClickHouse) rawQuery(ctx context.Context, sql string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/?query="+url.QueryEscape(sql), strings.NewReader(""))
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, readLimited(resp.Body))
	}
	return readAllLimited(resp.Body)
}

// SplitStatements splits a migration file into statements, dropping comment-only
// chunks. ClickHouse's HTTP endpoint executes one statement per request, so
// this cannot be skipped.
func SplitStatements(sql string) []string {
	var out []string
	var cur strings.Builder

	flush := func() {
		stmt := strings.TrimSpace(cur.String())
		cur.Reset()
		if stmt == "" || onlyComments(stmt) {
			return
		}
		out = append(out, stmt)
	}

	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			// Keep comments out of the statement body; ClickHouse would accept
			// them, but a comment-only chunk must not become a statement.
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if strings.HasSuffix(trimmed, ";") {
			flush()
		}
	}
	flush()
	return out
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		return false
	}
	return true
}

func checksum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// escapeString escapes single quotes for inline SQL. Migration names are
// repository-controlled, but escaping keeps the door shut.
func escapeString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// small json helpers, kept here so the file has no extra imports elsewhere
func jsonUnmarshal(s string, v any) error { return json.Unmarshal([]byte(s), v) }

func readLimited(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 4<<10))
	return strings.TrimSpace(string(b))
}

func readAllLimited(r io.Reader) ([]byte, error) { return io.ReadAll(io.LimitReader(r, 8<<20)) }

func atoiTrim(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

// ExecForTest runs a statement; exported for integration tests only.
func (c *ClickHouse) ExecForTest(ctx context.Context, sql string) error { return c.exec(ctx, sql) }

// ColumnCountForTest returns the column count of a table; test helper only.
func (c *ClickHouse) ColumnCountForTest(ctx context.Context, table string) (int, error) {
	body, err := c.rawQuery(ctx, "SELECT count() FROM system.columns WHERE table = '"+escapeString(table)+"' FORMAT TSV")
	if err != nil {
		return 0, err
	}
	return atoiTrim(string(body))
}
