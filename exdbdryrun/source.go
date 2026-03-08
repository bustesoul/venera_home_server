package exdbdryrun

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	metadatapkg "venera_home_server/metadata"
)

type Source struct {
	path   string
	db     *sql.DB
	schema SchemaReport
	table  *TableInfo
}

type BrowseQuery struct {
	Table string `json:"table,omitempty"`
	Q     string `json:"q,omitempty"`
	Page  int    `json:"page,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type BrowseResult struct {
	Path        string              `json:"path,omitempty"`
	Schema      SchemaReport        `json:"schema"`
	Table       string              `json:"table,omitempty"`
	Columns     []string            `json:"columns,omitempty"`
	Items       []map[string]string `json:"items,omitempty"`
	Page        int                 `json:"page"`
	Limit       int                 `json:"limit"`
	Total       int                 `json:"total"`
	HasPrevious bool                `json:"has_previous"`
	HasNext     bool                `json:"has_next"`
	Query       string              `json:"query,omitempty"`
}

func OpenSource(ctx context.Context, path string, forcedTable string) (*Source, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("exdb path is required")
	}
	db, err := sql.Open("sqlite3", trimmed)
	if err != nil {
		return nil, fmt.Errorf("open exdb: %w", err)
	}
	schema, err := inspectSchema(ctx, db, forcedTable)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	source := &Source{path: trimmed, db: db, schema: schema}
	if chosen := schema.findChosen(); chosen != nil {
		copy := *chosen
		source.table = &copy
	}
	return source, nil
}

func (s *Source) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Source) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Source) Schema() SchemaReport {
	if s == nil {
		return SchemaReport{}
	}
	return s.schema
}

func (s *Source) MatchRecord(ctx context.Context, rec metadatapkg.Record, minScore float64) (*Candidate, []Candidate, error) {
	if s == nil || s.db == nil || s.table == nil {
		return nil, nil, nil
	}
	if minScore <= 0 {
		minScore = 0.72
	}
	return findBestMatch(ctx, s.db, *s.table, rec, minScore)
}

func (s *Source) Browse(ctx context.Context, query BrowseQuery) (BrowseResult, error) {
	result := BrowseResult{
		Path:   s.Path(),
		Schema: s.Schema(),
		Query:  strings.TrimSpace(query.Q),
	}
	if s == nil || s.db == nil {
		return result, fmt.Errorf("source is not open")
	}
	table, err := s.resolveTable(query.Table)
	if err != nil {
		return result, err
	}
	page := query.Page
	if page <= 0 {
		page = 1
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	offset := (page - 1) * limit
	if offset < 0 {
		offset = 0
	}
	columns := browseColumns(table)
	where := []string{"1=1"}
	args := []any{}
	if q := strings.TrimSpace(query.Q); q != "" {
		searchColumns := browseSearchColumns(table)
		if len(searchColumns) > 0 {
			parts := make([]string, 0, len(searchColumns))
			for _, column := range searchColumns {
				parts = append(parts, fmt.Sprintf("%s LIKE ? ESCAPE '\\' COLLATE NOCASE", quoteIdent(column)))
				args = append(args, "%"+escapeLike(q)+"%")
			}
			where = append(where, "("+strings.Join(parts, " OR ")+")")
		}
	}
	countStmt := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", quoteIdent(table.Name), strings.Join(where, " AND "))
	var total int
	if err := s.db.QueryRowContext(ctx, countStmt, args...).Scan(&total); err != nil {
		return result, err
	}
	orderExpr := `rowid DESC`
	for _, candidate := range []string{table.Mapping.ID, table.Mapping.GID, table.Mapping.TitleJPN, table.Mapping.Title} {
		if strings.TrimSpace(candidate) != "" {
			orderExpr = quoteIdent(candidate) + ` DESC`
			break
		}
	}
	stmt := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT ? OFFSET ?",
		quoteIdentifiers(columns),
		quoteIdent(table.Name),
		strings.Join(where, " AND "),
		orderExpr,
	)
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.db.QueryContext(ctx, stmt, queryArgs...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return result, err
		}
		mapped := map[string]string{}
		for i, column := range columns {
			mapped[column] = stringifyDBValue(values[i])
		}
		items = append(items, mapped)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.Table = table.Name
	result.Columns = columns
	result.Items = items
	result.Page = page
	result.Limit = limit
	result.Total = total
	result.HasPrevious = page > 1
	result.HasNext = offset+len(items) < total
	return result, nil
}

func (s *Source) resolveTable(name string) (TableInfo, error) {
	requested := strings.TrimSpace(name)
	if requested == "" {
		if s.table == nil {
			return TableInfo{}, fmt.Errorf("no queryable table found in exdb")
		}
		return *s.table, nil
	}
	for _, table := range s.schema.Tables {
		if strings.EqualFold(table.Name, requested) {
			return table, nil
		}
	}
	return TableInfo{}, fmt.Errorf("table %q not found", requested)
}

func browseColumns(table TableInfo) []string {
	columns := mappingSelectedColumns(table.Mapping)
	if len(columns) > 0 {
		return columns
	}
	if len(table.Columns) <= 12 {
		return append([]string(nil), table.Columns...)
	}
	return append([]string(nil), table.Columns[:12]...)
}

func browseSearchColumns(table TableInfo) []string {
	columns := []string{table.Mapping.GID, table.Mapping.Token}
	columns = append(columns, mappingSearchColumns(table.Mapping)...)
	return uniqueValues(columns...)
}
