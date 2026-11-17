// Package migrations embeds the ClickHouse DDL so a single binary can migrate
// a fresh database without shipping loose files.
package migrations

import (
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration is one versioned DDL step.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// All returns the embedded migrations ordered by version.
func All() ([]Migration, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		content, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: version, Name: e.Name(), SQL: string(content)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// parseVersion reads the numeric prefix of a migration filename.
func parseVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return 0, fmt.Errorf("migration %q must be named <version>_<name>.sql", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q has a non-numeric version: %w", name, err)
	}
	return v, nil
}
