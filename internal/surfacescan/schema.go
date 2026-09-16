package surfacescan

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The schema is derived by replaying the migration files in filename order, which is the same
// order Postgres applies them.
//
// HONEST LIMITATION. This is a text replay, not an introspection: it understands create/alter/drop
// of tables and columns, and nothing else. A column reshaped by a DO block or a function will be
// reported as whatever the last plain statement said. The authoritative version is introspecting a
// real Postgres with every migration applied — which is exactly the CI portability gate in H.6 —
// and this scanner should be replaced by that once it exists. Until then this is dependency-free
// and correct for the ~99% of our migrations that are plain DDL, which is enough to answer the
// only question asked of it here: what is there to document?

var (
	createTableRe = regexp.MustCompile(`(?is)create\s+table\s+(?:if\s+not\s+exists\s+)?(?:public\.)?"?([a-z_][a-z0-9_]*)"?\s*\((.*?)\n\s*\)\s*;`)
	// One ALTER TABLE may carry SEVERAL clauses:
	//     alter table public.run_tasks
	//       add column if not exists gate jsonb,
	//       add column if not exists gate_verdict text;
	// Matching "alter table X add column Y" alone finds only the FIRST — which is how the G.0
	// migration's three new columns arrived in the reference as one. So the table name and the
	// clauses are matched separately: alterHeadRe finds the statement, addClauseRe every ADD
	// COLUMN inside it.
	alterHeadRe  = regexp.MustCompile(`(?is)alter\s+table\s+(?:if\s+exists\s+)?(?:public\.)?"?([a-z_][a-z0-9_]*)"?\b(.*?);`)
	addClauseRe  = regexp.MustCompile(`(?is)add\s+column\s+(?:if\s+not\s+exists\s+)?"?([a-z_][a-z0-9_]*)"?`)
	dropClauseRe = regexp.MustCompile(`(?is)drop\s+column\s+(?:if\s+exists\s+)?"?([a-z_][a-z0-9_]*)"?`)
	dropTableRe  = regexp.MustCompile(`(?is)drop\s+table\s+(?:if\s+exists\s+)?(?:public\.)?"?([a-z_][a-z0-9_]*)"?`)
	// A table rename carries the columns across, so the replay must follow it — otherwise the
	// reference keeps documenting a name that no longer exists and loses the one that does.
	// `rename to` only: `rename constraint … to …` and `rename column … to …` leave the table alone.
	// Applied BEFORE the ADD/DROP COLUMN clauses in the same file, so a rename followed by an alter
	// on the new name lands on one table rather than two — the shape a rename migration actually has.
	renameTableRe = regexp.MustCompile(`(?is)alter\s+table\s+(?:if\s+exists\s+)?(?:public\.)?"?([a-z_][a-z0-9_]*)"?\s+rename\s+to\s+(?:public\.)?"?([a-z_][a-z0-9_]*)"?`)
	colNameRe     = regexp.MustCompile(`^"?([a-z_][a-z0-9_]*)"?\s`)
)

// Constraint keywords that begin a table-level clause rather than a column definition.
var notAColumn = map[string]bool{
	"primary": true, "foreign": true, "unique": true, "check": true,
	"constraint": true, "exclude": true, "like": true,
}

func scanTables(root string) []Item {
	dir := filepath.Join(root, "supabase", "migrations")
	paths, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(paths) == 0 {
		return nil
	}
	sort.Strings(paths) // filename order IS apply order

	tables := map[string]map[string]bool{}
	source := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		sql := stripComments(string(b))
		for _, m := range createTableRe.FindAllStringSubmatch(sql, -1) {
			name := m[1]
			if tables[name] == nil {
				tables[name] = map[string]bool{}
				source[name] = rel(root, p) // the migration that INTRODUCED it — the useful one to cite
			}
			for _, c := range columnsOf(m[2]) {
				tables[name][c] = true
			}
		}
		for _, m := range renameTableRe.FindAllStringSubmatch(sql, -1) {
			from, to := m[1], m[2]
			if tables[from] == nil {
				continue
			}
			if tables[to] == nil {
				tables[to] = map[string]bool{}
			}
			for c := range tables[from] {
				tables[to][c] = true
			}
			// Keep the ORIGINAL migration as the source: it is still where the table came from, and
			// citing the rename would send a reader to a file with no columns in it.
			source[to] = source[from]
			delete(tables, from)
			delete(source, from)
		}
		for _, stmt := range alterHeadRe.FindAllStringSubmatch(sql, -1) {
			name, body := stmt[1], stmt[2]
			adds := addClauseRe.FindAllStringSubmatch(body, -1)
			drops := dropClauseRe.FindAllStringSubmatch(body, -1)
			if len(adds) == 0 && len(drops) == 0 {
				continue // a constraint, an index, an owner change — not our concern
			}
			if tables[name] == nil {
				tables[name] = map[string]bool{}
				source[name] = rel(root, p)
			}
			for _, a := range adds {
				tables[name][a[1]] = true
			}
			for _, d := range drops {
				delete(tables[name], d[1])
			}
		}
		for _, m := range dropTableRe.FindAllStringSubmatch(sql, -1) {
			delete(tables, m[1])
			delete(source, m[1])
		}
	}

	var out []Item
	for name, cols := range tables {
		var list []string
		for c := range cols {
			list = append(list, c)
		}
		sort.Strings(list)
		out = append(out, Item{
			Ref:    "table:" + name,
			Kind:   "table",
			Name:   name,
			Detail: list,
			Source: source[name],
		})
	}
	return out
}

// columnsOf pulls column names out of a CREATE TABLE body. Nested parens (numeric(10,2), check
// constraints, default expressions) are skipped by tracking depth, so only top-level definitions
// are considered.
func columnsOf(body string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, firstIdent(body[start:i]))
				start = i + 1
			}
		}
	}
	out = append(out, firstIdent(body[start:]))
	return sortedUnique(out)
}

func firstIdent(clause string) string {
	clause = strings.TrimSpace(clause)
	if clause == "" {
		return ""
	}
	if notAColumn[strings.ToLower(strings.Fields(clause)[0])] {
		return ""
	}
	if m := colNameRe.FindStringSubmatch(clause + " "); m != nil {
		return m[1]
	}
	return ""
}

// stripComments removes `-- …` line comments so a commented-out DDL statement (or, more often,
// a long explanatory header that mentions a table by name) never enters the schema.
func stripComments(sql string) string {
	var b strings.Builder
	for _, line := range strings.Split(sql, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
