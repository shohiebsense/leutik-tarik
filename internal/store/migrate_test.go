package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/shohiebsense/leutik-tarik/internal/store"
	"github.com/shohiebsense/leutik-tarik/migrations"
)

func TestSplitStatements(t *testing.T) {
	sql := `-- a comment
CREATE TABLE a (x UInt8);
-- another comment

ALTER TABLE a ADD COLUMN y UInt8;
`
	got := store.SplitStatements(sql)
	if len(got) != 2 {
		t.Fatalf("want 2 statements, got %d: %#v", len(got), got)
	}
	if got[0] != "CREATE TABLE a (x UInt8);" {
		t.Errorf("stmt 0 = %q", got[0])
	}
	if got[1] != "ALTER TABLE a ADD COLUMN y UInt8;" {
		t.Errorf("stmt 1 = %q", got[1])
	}
	if only := store.SplitStatements("-- just a comment\n"); len(only) != 0 {
		t.Errorf("comment-only file produced %d statements", len(only))
	}
}

// The gap that broke queries for real: a second schema change must ALTER the
// existing table, and must not be re-applied.
func TestMigrateAlterPath(t *testing.T) {
	url := os.Getenv("LEUTIK_CLICKHOUSE_URL")
	if url == "" {
		t.Skip("LEUTIK_CLICKHOUSE_URL not set")
	}
	ch, err := store.NewClickHouse(url)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	const tbl = "alter_probe"
	ch = ch.WithMigrateTable("alter_probe_migrations")
	mustExec(t, ch, "DROP TABLE IF EXISTS "+tbl)
	mustExec(t, ch, "DROP TABLE IF EXISTS alter_probe_migrations")

	base := []migrations.Migration{
		{Version: 1, Name: "001_base.sql", SQL: "CREATE TABLE " + tbl + " (x UInt8);"},
		{Version: 2, Name: "002_add_y.sql", SQL: "ALTER TABLE " + tbl + " ADD COLUMN y String;"},
	}
	if err := ch.MigrateFrom(ctx, base); err != nil {
		t.Fatalf("migrate base: %v", err)
	}
	if n := columnCount(t, ch, tbl); n != 2 {
		t.Fatalf("after ALTER want 2 columns, got %d", n)
	}
	// Re-running must be a no-op, not an "column already exists" failure.
	if err := ch.MigrateFrom(ctx, base); err != nil {
		t.Fatalf("second migrate must be idempotent: %v", err)
	}

	// Editing an already-applied migration must be refused.
	rewritten := []migrations.Migration{
		base[0],
		{Version: 2, Name: "002_add_y.sql", SQL: "ALTER TABLE " + tbl + " ADD COLUMN z String;"},
	}
	if err := ch.MigrateFrom(ctx, rewritten); err == nil {
		t.Fatal("expected refusal when an applied migration changed")
	}

	mustExec(t, ch, "DROP TABLE IF EXISTS "+tbl)
	mustExec(t, ch, "DROP TABLE IF EXISTS alter_probe_migrations")
}

func mustExec(t *testing.T, ch *store.ClickHouse, sql string) {
	t.Helper()
	if err := ch.ExecForTest(context.Background(), sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func columnCount(t *testing.T, ch *store.ClickHouse, table string) int {
	t.Helper()
	n, err := ch.ColumnCountForTest(context.Background(), table)
	if err != nil {
		t.Fatalf("column count: %v", err)
	}
	return n
}
