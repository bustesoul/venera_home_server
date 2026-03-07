package exdbdryrun

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	_ "github.com/mattn/go-sqlite3"

	metadatapkg "venera_home_server/metadata"
)

var genericDenseTerms = map[string]struct{}{
	normalizeDense("無台詞"):      {},
	normalizeDense("有台詞"):      {},
	normalizeDense("中国翻訳"):     {},
	normalizeDense("中國翻譯"):     {},
	normalizeDense("中国語"):      {},
	normalizeDense("漢化"):       {},
	normalizeDense("汉化"):       {},
	normalizeDense("漢化組"):      {},
	normalizeDense("无修正"):      {},
	normalizeDense("無修正"):      {},
	normalizeDense("修正"):       {},
	normalizeDense("dl版"):      {},
	normalizeDense("digital"):  {},
	normalizeDense("sample"):   {},
	normalizeDense("zip"):      {},
	normalizeDense("rar"):      {},
	normalizeDense("7z"):       {},
	normalizeDense("cbz"):      {},
	normalizeDense("cbr"):      {},
	normalizeDense("pdf"):      {},
	normalizeDense("notext"):   {},
	normalizeDense("textless"): {},
}

var archiveExtensions = []string{".zip", ".rar", ".7z", ".cbz", ".cbr", ".pdf"}

type Config struct {
	MetadataDBPath string
	ExDBPath       string
	LibraryID      string
	State          string
	Limit          int
	MinScore       float64
	InspectOnly    bool
	Table          string
}

type Report struct {
	GeneratedAt    time.Time     `json:"generated_at"`
	MetadataDBPath string        `json:"metadata_db_path,omitempty"`
	ExDBPath       string        `json:"exdb_path"`
	LibraryID      string        `json:"library_id,omitempty"`
	State          string        `json:"state,omitempty"`
	Limit          int           `json:"limit"`
	MinScore       float64       `json:"min_score"`
	Schema         SchemaReport  `json:"schema"`
	Summary        Summary       `json:"summary"`
	Matches        []RecordMatch `json:"matches,omitempty"`
}

type Summary struct {
	Examined  int            `json:"examined"`
	Matched   int            `json:"matched"`
	Unmatched int            `json:"unmatched"`
	ByMethod  map[string]int `json:"by_method,omitempty"`
}

type SchemaReport struct {
	Tables      []TableInfo `json:"tables"`
	ChosenTable string      `json:"chosen_table,omitempty"`
}

type TableInfo struct {
	Name     string        `json:"name"`
	Kind     string        `json:"kind"`
	Columns  []string      `json:"columns"`
	RowCount int64         `json:"row_count"`
	Score    int           `json:"score"`
	Mapping  ColumnMapping `json:"mapping"`
}

type ColumnMapping struct {
	ID        string `json:"id,omitempty"`
	GID       string `json:"gid,omitempty"`
	Token     string `json:"token,omitempty"`
	Title     string `json:"title,omitempty"`
	TitleJPN  string `json:"title_jpn,omitempty"`
	Artists   string `json:"artists,omitempty"`
	Tags      string `json:"tags,omitempty"`
	Category  string `json:"category,omitempty"`
	Rating    string `json:"rating,omitempty"`
	CoverURL  string `json:"cover_url,omitempty"`
	SourceURL string `json:"source_url,omitempty"`
}

type RecordMatch struct {
	Local        LocalRecordSummary `json:"local"`
	Match        *Candidate         `json:"match,omitempty"`
	Alternatives []Candidate        `json:"alternatives,omitempty"`
}

type LocalRecordSummary struct {
	ID          int64            `json:"id"`
	LibraryID   string           `json:"library_id,omitempty"`
	RootType    string           `json:"root_type,omitempty"`
	RootRef     string           `json:"root_ref,omitempty"`
	FolderPath  string           `json:"folder_path,omitempty"`
	Title       string           `json:"title,omitempty"`
	TitleJPN    string           `json:"title_jpn,omitempty"`
	SourceID    string           `json:"source_id,omitempty"`
	SourceToken string           `json:"source_token,omitempty"`
	Hint        metadatapkg.Hint `json:"hint,omitempty"`
}

type Candidate struct {
	Table     string  `json:"table"`
	RowID     string  `json:"row_id,omitempty"`
	GID       string  `json:"gid,omitempty"`
	Token     string  `json:"token,omitempty"`
	Title     string  `json:"title,omitempty"`
	TitleJPN  string  `json:"title_jpn,omitempty"`
	Artists   string  `json:"artists,omitempty"`
	Tags      string  `json:"tags,omitempty"`
	Category  string  `json:"category,omitempty"`
	Rating    string  `json:"rating,omitempty"`
	CoverURL  string  `json:"cover_url,omitempty"`
	SourceURL string  `json:"source_url,omitempty"`
	Score     float64 `json:"score"`
	Method    string  `json:"method"`
	Reason    string  `json:"reason,omitempty"`
}

type exRow struct {
	Table   string
	Mapping ColumnMapping
	Values  map[string]string
}

type localSearch struct {
	GID        string
	Token      string
	FolderBase string
	Titles     []string
	Artists    []string
	Tags       []string
	Keywords   []string
}

func Run(ctx context.Context, cfg Config) (Report, error) {
	cfg = cfg.withDefaults()
	report := Report{
		GeneratedAt:    time.Now().UTC(),
		MetadataDBPath: strings.TrimSpace(cfg.MetadataDBPath),
		ExDBPath:       strings.TrimSpace(cfg.ExDBPath),
		LibraryID:      strings.TrimSpace(cfg.LibraryID),
		State:          strings.TrimSpace(cfg.State),
		Limit:          cfg.Limit,
		MinScore:       cfg.MinScore,
	}
	if report.ExDBPath == "" {
		return report, errors.New("exdb path is required")
	}

	exdb, err := sql.Open("sqlite3", report.ExDBPath)
	if err != nil {
		return report, fmt.Errorf("open exdb: %w", err)
	}
	defer exdb.Close()

	schema, err := inspectSchema(ctx, exdb, cfg.Table)
	if err != nil {
		return report, err
	}
	report.Schema = schema

	if cfg.InspectOnly {
		return report, nil
	}
	if report.MetadataDBPath == "" {
		return report, errors.New("metadata db path is required unless inspect mode is used")
	}

	records, err := loadLocalRecords(ctx, report.MetadataDBPath, report.LibraryID, report.State, report.Limit)
	if err != nil {
		return report, err
	}
	report.Summary.Examined = len(records)
	report.Summary.ByMethod = map[string]int{}

	chosen := schema.findChosen()
	if chosen == nil {
		report.Summary.Unmatched = len(records)
		for _, rec := range records {
			report.Matches = append(report.Matches, RecordMatch{Local: summarizeLocal(rec)})
		}
		return report, nil
	}

	for _, rec := range records {
		match, alternatives, err := findBestMatch(ctx, exdb, *chosen, rec, cfg.MinScore)
		if err != nil {
			return report, fmt.Errorf("match %s: %w", rec.RootRef, err)
		}
		item := RecordMatch{Local: summarizeLocal(rec), Match: match, Alternatives: alternatives}
		report.Matches = append(report.Matches, item)
		if match != nil {
			report.Summary.Matched++
			report.Summary.ByMethod[match.Method]++
		} else {
			report.Summary.Unmatched++
		}
	}
	if len(report.Summary.ByMethod) == 0 {
		report.Summary.ByMethod = nil
	}
	return report, nil
}

func (cfg Config) withDefaults() Config {
	cfg.MetadataDBPath = strings.TrimSpace(cfg.MetadataDBPath)
	cfg.ExDBPath = strings.TrimSpace(cfg.ExDBPath)
	cfg.LibraryID = strings.TrimSpace(cfg.LibraryID)
	cfg.State = strings.TrimSpace(cfg.State)
	cfg.Table = strings.TrimSpace(cfg.Table)
	if cfg.State == "" {
		cfg.State = "empty"
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 100
	}
	if cfg.MinScore <= 0 {
		cfg.MinScore = 0.72
	}
	return cfg
}

func inspectSchema(ctx context.Context, db *sql.DB, forcedTable string) (SchemaReport, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, type FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return SchemaReport{}, fmt.Errorf("list exdb tables: %w", err)
	}
	defer rows.Close()

	var tables []TableInfo
	for rows.Next() {
		var name string
		var kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return SchemaReport{}, err
		}
		columns, err := listTableColumns(ctx, db, name)
		if err != nil {
			return SchemaReport{}, err
		}
		mapping := inferColumnMapping(columns)
		rowCount := int64(-1)
		if count, err := tableRowCount(ctx, db, name); err == nil {
			rowCount = count
		}
		tables = append(tables, TableInfo{
			Name:     name,
			Kind:     kind,
			Columns:  columns,
			RowCount: rowCount,
			Score:    tableScore(mapping),
			Mapping:  mapping,
		})
	}
	if err := rows.Err(); err != nil {
		return SchemaReport{}, err
	}

	sort.SliceStable(tables, func(i, j int) bool {
		if tables[i].Score != tables[j].Score {
			return tables[i].Score > tables[j].Score
		}
		if tables[i].RowCount != tables[j].RowCount {
			return tables[i].RowCount > tables[j].RowCount
		}
		return tables[i].Name < tables[j].Name
	})

	report := SchemaReport{Tables: tables}
	if forcedTable != "" {
		for _, table := range tables {
			if strings.EqualFold(table.Name, forcedTable) {
				report.ChosenTable = table.Name
				return report, nil
			}
		}
		return report, fmt.Errorf("forced table %q not found in exdb", forcedTable)
	}
	for _, table := range tables {
		if table.Score > 0 {
			report.ChosenTable = table.Name
			break
		}
	}
	return report, nil
}

func (report SchemaReport) findChosen() *TableInfo {
	if strings.TrimSpace(report.ChosenTable) == "" {
		return nil
	}
	for i := range report.Tables {
		if report.Tables[i].Name == report.ChosenTable {
			return &report.Tables[i]
		}
	}
	return nil
}

func listTableColumns(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	stmt := fmt.Sprintf("PRAGMA table_info(%s)", quoteIdent(table))
	rows, err := db.QueryContext(ctx, stmt)
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var cid int
		var name string
		var columnType sql.NullString
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, rows.Err()
}

func tableRowCount(ctx context.Context, db *sql.DB, table string) (int64, error) {
	stmt := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdent(table))
	var count int64
	if err := db.QueryRowContext(ctx, stmt).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func inferColumnMapping(columns []string) ColumnMapping {
	used := map[string]bool{}
	mapping := ColumnMapping{}
	mapping.GID = pickColumn(columns, used,
		[]string{"gid", "galleryid", "gallerygid", "ehgalleryid", "sourceid"},
		[]string{"gallerygid", "galleryid", "ehgalleryid", "gid"},
	)
	mapping.Token = pickColumn(columns, used,
		[]string{"token", "gallerytoken", "ehtoken", "sourcetoken"},
		[]string{"gallerytoken", "ehtoken", "token"},
	)
	mapping.Title = pickColumn(columns, used,
		[]string{"title", "titleen", "englishtitle", "maintitle", "name"},
		[]string{"englishtitle", "maintitle", "title", "name"},
	)
	mapping.TitleJPN = pickColumn(columns, used,
		[]string{"titlejpn", "titlejp", "titleja", "japanesetitle", "titlejapanese"},
		[]string{"japanesetitle", "titlejpn", "titlejp", "titleja"},
	)
	mapping.Artists = pickColumn(columns, used,
		[]string{"artists", "artist", "authors", "author", "creators", "creator", "circle", "group", "groups"},
		[]string{"artists", "artist", "authors", "author", "creators", "creator", "circle", "groups", "group"},
	)
	mapping.Tags = pickColumn(columns, used,
		[]string{"tags", "tag", "tagsjson", "tagjson", "taglist", "genres", "genre"},
		[]string{"tagsjson", "tagjson", "taglist", "tags", "genres", "genre"},
	)
	mapping.Category = pickColumn(columns, used,
		[]string{"category", "type", "class"},
		[]string{"category", "type"},
	)
	mapping.Rating = pickColumn(columns, used,
		[]string{"rating", "score", "stars", "rank"},
		[]string{"rating", "score", "stars", "rank"},
	)
	mapping.CoverURL = pickColumn(columns, used,
		[]string{"coverurl", "thumburl", "thumbnail", "thumb", "cover", "preview", "imageurl"},
		[]string{"coverurl", "thumburl", "thumbnail", "thumb", "cover", "preview", "imageurl"},
	)
	mapping.SourceURL = pickColumn(columns, used,
		[]string{"sourceurl", "galleryurl", "url", "link", "weburl"},
		[]string{"sourceurl", "galleryurl", "weburl", "link", "url"},
	)
	mapping.ID = pickColumn(columns, used,
		[]string{"id", "rowid", "galleryrowid", "pk"},
		[]string{"rowid", "galleryrowid", "pk"},
	)
	return mapping
}

func pickColumn(columns []string, used map[string]bool, exacts []string, contains []string) string {
	normalizedToOriginal := map[string]string{}
	for _, column := range columns {
		normalizedToOriginal[normalizeIdent(column)] = column
	}
	for _, candidate := range exacts {
		normalized := normalizeIdent(candidate)
		if column, ok := normalizedToOriginal[normalized]; ok && !used[column] {
			used[column] = true
			return column
		}
	}
	for _, candidate := range contains {
		normalized := normalizeIdent(candidate)
		for _, column := range columns {
			if used[column] {
				continue
			}
			if strings.Contains(normalizeIdent(column), normalized) {
				used[column] = true
				return column
			}
		}
	}
	return ""
}

func tableScore(mapping ColumnMapping) int {
	score := 0
	if mapping.Title != "" {
		score += 40
	}
	if mapping.TitleJPN != "" {
		score += 25
	}
	if mapping.GID != "" {
		score += 25
	}
	if mapping.Token != "" {
		score += 15
	}
	if mapping.Tags != "" {
		score += 12
	}
	if mapping.Artists != "" {
		score += 10
	}
	if mapping.Category != "" {
		score += 5
	}
	if mapping.CoverURL != "" {
		score += 4
	}
	if mapping.SourceURL != "" {
		score += 3
	}
	return score
}

func loadLocalRecords(ctx context.Context, metadataPath string, libraryID string, state string, limit int) ([]metadatapkg.Record, error) {
	store, err := metadatapkg.OpenStore(filepath.Dir(metadataPath), metadataPath)
	if err != nil {
		return nil, fmt.Errorf("open metadata db: %w", err)
	}
	defer store.Close()
	records, err := store.ListRecords(ctx, metadatapkg.ListQuery{
		LibraryID: libraryID,
		State:     state,
		Limit:     limit,
	})
	if err != nil {
		return nil, fmt.Errorf("list metadata records: %w", err)
	}
	return records, nil
}

func summarizeLocal(rec metadatapkg.Record) LocalRecordSummary {
	return LocalRecordSummary{
		ID:          rec.ID,
		LibraryID:   rec.LibraryID,
		RootType:    rec.RootType,
		RootRef:     rec.RootRef,
		FolderPath:  rec.FolderPath,
		Title:       rec.Title,
		TitleJPN:    rec.TitleJPN,
		SourceID:    rec.SourceID,
		SourceToken: rec.SourceToken,
		Hint:        rec.Hint,
	}
}

func findBestMatch(ctx context.Context, db *sql.DB, table TableInfo, rec metadatapkg.Record, minScore float64) (*Candidate, []Candidate, error) {
	local := buildLocalSearch(rec)
	acc := map[string]Candidate{}

	if local.GID != "" && table.Mapping.GID != "" {
		rows, err := queryRows(ctx, db, table, []filter{{Column: table.Mapping.GID, Op: opExact, Value: local.GID}}, 12)
		if err != nil {
			return nil, nil, err
		}
		accumulateCandidates(acc, rows, local, minScore)
		if bestScore(acc) >= 0.999 {
			return finalizeCandidates(acc)
		}
	}

	titleColumns := nonEmptyStrings(table.Mapping.Title, table.Mapping.TitleJPN)
	if len(titleColumns) > 0 {
		var titleFilters []filter
		for _, title := range local.Titles {
			trimmed := strings.TrimSpace(title)
			if trimmed == "" {
				continue
			}
			for _, column := range titleColumns {
				titleFilters = append(titleFilters, filter{Column: column, Op: opExact, Value: trimmed})
			}
		}
		if len(titleFilters) > 0 {
			rows, err := queryRows(ctx, db, table, titleFilters, 20)
			if err != nil {
				return nil, nil, err
			}
			accumulateCandidates(acc, rows, local, minScore)
		}
	}

	searchColumns := nonEmptyStrings(table.Mapping.Title, table.Mapping.TitleJPN, table.Mapping.Artists, table.Mapping.Tags)
	for _, seed := range searchSeeds(local) {
		if len(searchColumns) == 0 {
			break
		}
		var likeFilters []filter
		for _, column := range searchColumns {
			likeFilters = append(likeFilters, filter{Column: column, Op: opLike, Value: seed})
		}
		rows, err := queryRows(ctx, db, table, likeFilters, 30)
		if err != nil {
			return nil, nil, err
		}
		accumulateCandidates(acc, rows, local, minScore)
		if bestScore(acc) >= 0.97 {
			break
		}
	}

	return finalizeCandidates(acc)
}

func finalizeCandidates(acc map[string]Candidate) (*Candidate, []Candidate, error) {
	if len(acc) == 0 {
		return nil, nil, nil
	}
	candidates := make([]Candidate, 0, len(acc))
	for _, candidate := range acc {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		if candidates[i].Method != candidates[j].Method {
			return candidates[i].Method < candidates[j].Method
		}
		return candidates[i].Title < candidates[j].Title
	})
	best := candidates[0]
	alternatives := []Candidate{}
	if len(candidates) > 1 {
		end := len(candidates)
		if end > 4 {
			end = 4
		}
		alternatives = append(alternatives, candidates[1:end]...)
	}
	return &best, alternatives, nil
}

func bestScore(acc map[string]Candidate) float64 {
	best := 0.0
	for _, candidate := range acc {
		if candidate.Score > best {
			best = candidate.Score
		}
	}
	return best
}

func accumulateCandidates(acc map[string]Candidate, rows []exRow, local localSearch, minScore float64) {
	for _, row := range rows {
		candidate, ok := scoreCandidate(local, row)
		if !ok || candidate.Score < minScore {
			continue
		}
		key := candidateIdentity(row, candidate)
		if existing, ok := acc[key]; !ok || candidate.Score > existing.Score {
			acc[key] = candidate
		}
	}
}

func candidateIdentity(row exRow, candidate Candidate) string {
	if candidate.RowID != "" {
		return "row:" + candidate.RowID
	}
	if candidate.GID != "" || candidate.Token != "" {
		return "gid:" + candidate.GID + ":" + candidate.Token
	}
	title := normalizeDense(candidate.Title)
	titleJPN := normalizeDense(candidate.TitleJPN)
	if title != "" || titleJPN != "" {
		return "title:" + title + ":" + titleJPN
	}
	return fmt.Sprintf("fallback:%s:%d", row.Table, len(row.Values))
}

func buildLocalSearch(rec metadatapkg.Record) localSearch {
	search := localSearch{
		GID:        firstNonEmpty(rec.SourceID, rec.Hint.EHGalleryID),
		Token:      firstNonEmpty(rec.SourceToken, rec.Hint.EHToken),
		FolderBase: cleanSearchText(baseNameFromAnyPath(firstNonEmpty(rec.FolderPath, rec.RootRef))),
		Artists:    append([]string{}, rec.Artists...),
		Tags:       append([]string{}, rec.Tags...),
		Keywords:   append([]string{}, rec.Hint.Keywords...),
	}
	search.Titles = meaningfulValues(rec.Title, rec.TitleJPN, search.FolderBase)
	for _, title := range search.Titles {
		search.Keywords = append(search.Keywords, tokenize(title)...)
	}
	for _, artist := range search.Artists {
		search.Keywords = append(search.Keywords, tokenize(artist)...)
	}
	for _, tag := range search.Tags {
		search.Keywords = append(search.Keywords, tokenize(tag)...)
	}
	search.Keywords = uniqueValues(search.Keywords...)
	return search
}

func searchSeeds(local localSearch) []string {
	seedMap := map[string]string{}
	for _, value := range append(append([]string{}, local.Titles...), local.Keywords...) {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || !isMeaningfulSeed(trimmed) {
			continue
		}
		dense := normalizeDense(trimmed)
		if len([]rune(dense)) < 2 {
			continue
		}
		if _, exists := seedMap[dense]; !exists {
			seedMap[dense] = trimmed
		}
	}
	seeds := make([]string, 0, len(seedMap))
	for _, value := range seedMap {
		seeds = append(seeds, value)
	}
	sort.SliceStable(seeds, func(i, j int) bool {
		return len([]rune(seeds[i])) > len([]rune(seeds[j]))
	})
	if len(seeds) > 4 {
		seeds = seeds[:4]
	}
	return seeds
}

func scoreCandidate(local localSearch, row exRow) (Candidate, bool) {
	candidate := Candidate{
		Table:     row.Table,
		RowID:     row.value(row.Mapping.ID),
		GID:       row.value(row.Mapping.GID),
		Token:     row.value(row.Mapping.Token),
		Title:     row.value(row.Mapping.Title),
		TitleJPN:  row.value(row.Mapping.TitleJPN),
		Artists:   row.value(row.Mapping.Artists),
		Tags:      row.value(row.Mapping.Tags),
		Category:  row.value(row.Mapping.Category),
		Rating:    row.value(row.Mapping.Rating),
		CoverURL:  row.value(row.Mapping.CoverURL),
		SourceURL: row.value(row.Mapping.SourceURL),
	}

	localGID := normalizeDense(local.GID)
	candidateGID := normalizeDense(candidate.GID)
	localToken := normalizeDense(local.Token)
	candidateToken := normalizeDense(candidate.Token)
	if localGID != "" && candidateGID != "" && localGID == candidateGID {
		candidate.Score = 0.97
		candidate.Method = "gid"
		candidate.Reason = "gid matched"
		if localToken != "" && candidateToken != "" && localToken == candidateToken {
			candidate.Score = 1.0
			candidate.Method = "gid_token"
			candidate.Reason = "gid and token matched"
		}
		return candidate, true
	}

	candidateTitles := uniqueValues(candidate.Title, candidate.TitleJPN)
	candidateTitleDense := normalizedValues(candidateTitles...)
	localTitleDense := normalizedValues(local.Titles...)
	folderDense := normalizeDense(local.FolderBase)

	for _, localTitle := range localTitleDense {
		if localTitle == "" {
			continue
		}
		for _, title := range candidateTitleDense {
			if title == "" {
				continue
			}
			if localTitle == title {
				candidate.Score = 0.93
				candidate.Method = "title_exact"
				candidate.Reason = "normalized title matched"
				return candidate, true
			}
		}
	}

	if folderDense != "" {
		for _, title := range candidateTitleDense {
			if title == "" {
				continue
			}
			if folderDense == title {
				candidate.Score = 0.89
				candidate.Method = "folder_exact"
				candidate.Reason = "folder basename matched title"
				return candidate, true
			}
		}
	}

	best := 0.0
	method := ""
	reason := ""
	for _, localTitle := range append(localTitleDense, folderDense) {
		if localTitle == "" || isGenericDense(localTitle) {
			continue
		}
		for _, title := range candidateTitleDense {
			if title == "" {
				continue
			}
			if strings.Contains(title, localTitle) || strings.Contains(localTitle, title) {
				if 0.82 > best {
					best = 0.82
					method = "title_fuzzy"
					reason = "title partially overlapped"
				}
			}
		}
	}

	localKeywords := tokenSet(append(append([]string{}, local.Titles...), append(local.Keywords, local.Artists...)...)...)
	candidateKeywords := tokenSet(candidate.Title, candidate.TitleJPN, candidate.Artists, candidate.Tags, candidate.Category)
	overlap := jaccard(localKeywords, candidateKeywords)
	if overlap > 0 {
		score := 0.55 + overlap*0.30
		artistOverlap := jaccard(tokenSet(local.Artists...), tokenSet(candidate.Artists))
		if artistOverlap > 0 {
			score += 0.05
		}
		if score > best {
			best = score
			method = "keyword"
			reason = fmt.Sprintf("keyword overlap %.2f", overlap)
		}
	}

	if best <= 0 {
		return Candidate{}, false
	}
	candidate.Score = roundScore(best)
	candidate.Method = method
	candidate.Reason = reason
	return candidate, true
}

type filterOp int

const (
	opExact filterOp = iota + 1
	opLike
)

type filter struct {
	Column string
	Op     filterOp
	Value  string
}

func queryRows(ctx context.Context, db *sql.DB, table TableInfo, filters []filter, limit int) ([]exRow, error) {
	selectedColumns := uniqueValues(
		table.Mapping.ID,
		table.Mapping.GID,
		table.Mapping.Token,
		table.Mapping.Title,
		table.Mapping.TitleJPN,
		table.Mapping.Artists,
		table.Mapping.Tags,
		table.Mapping.Category,
		table.Mapping.Rating,
		table.Mapping.CoverURL,
		table.Mapping.SourceURL,
	)
	if len(selectedColumns) == 0 || len(filters) == 0 {
		return nil, nil
	}
	var where []string
	args := make([]any, 0, len(filters))
	for _, item := range filters {
		if strings.TrimSpace(item.Column) == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		switch item.Op {
		case opExact:
			where = append(where, fmt.Sprintf("%s = ? COLLATE NOCASE", quoteIdent(item.Column)))
			args = append(args, strings.TrimSpace(item.Value))
		case opLike:
			where = append(where, fmt.Sprintf("%s LIKE ? ESCAPE '\\' COLLATE NOCASE", quoteIdent(item.Column)))
			args = append(args, "%"+escapeLike(strings.TrimSpace(item.Value))+"%")
		}
	}
	if len(where) == 0 {
		return nil, nil
	}
	stmt := fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s LIMIT %d",
		quoteIdentifiers(selectedColumns),
		quoteIdent(table.Name),
		strings.Join(where, " OR "),
		max(limit, 1),
	)
	rows, err := db.QueryContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []exRow{}
	for rows.Next() {
		values := make([]any, len(selectedColumns))
		dest := make([]any, len(selectedColumns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		mapped := map[string]string{}
		for i, column := range selectedColumns {
			mapped[column] = stringifyDBValue(values[i])
		}
		results = append(results, exRow{Table: table.Name, Mapping: table.Mapping, Values: mapped})
	}
	return results, rows.Err()
}

func (row exRow) value(column string) string {
	if strings.TrimSpace(column) == "" {
		return ""
	}
	return strings.TrimSpace(row.Values[column])
}

func stringifyDBValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case time.Time:
		return typed.Format(time.RFC3339Nano)
	default:
		return fmt.Sprint(typed)
	}
}

func quoteIdentifiers(columns []string) string {
	quoted := make([]string, 0, len(columns))
	for _, column := range columns {
		quoted = append(quoted, quoteIdent(column))
	}
	return strings.Join(quoted, ", ")
}

func quoteIdent(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}

func normalizeIdent(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func normalizeDense(value string) string {
	return normalizeIdent(value)
}

func normalizedValues(values ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		normalized := normalizeDense(value)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

func tokenize(values ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(fields) == 0 {
			fields = []string{strings.ToLower(strings.TrimSpace(value))}
		}
		for _, field := range fields {
			trimmed := strings.TrimSpace(field)
			if len([]rune(trimmed)) < 2 || !shouldKeepToken(trimmed) {
				continue
			}
			if !seen[trimmed] {
				seen[trimmed] = true
				out = append(out, trimmed)
			}
		}
	}
	return out
}

func tokenSet(values ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, token := range tokenize(values...) {
		set[token] = struct{}{}
	}
	return set
}

func jaccard(left map[string]struct{}, right map[string]struct{}) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	union := len(left)
	for token := range right {
		if _, ok := left[token]; ok {
			intersection++
		} else {
			union++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func uniqueValues(values ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func nonEmptyStrings(values ...string) []string {
	return uniqueValues(values...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func meaningfulValues(values ...string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		cleaned := cleanSearchText(value)
		if !isMeaningfulPhrase(cleaned) {
			continue
		}
		dense := normalizeDense(cleaned)
		if seen[dense] {
			continue
		}
		seen[dense] = true
		out = append(out, cleaned)
	}
	return out
}

func cleanSearchText(value string) string {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	for _, ext := range archiveExtensions {
		if strings.HasSuffix(lower, ext) {
			trimmed = strings.TrimSpace(trimmed[:len(trimmed)-len(ext)])
			break
		}
	}
	return trimmed
}

func isMeaningfulSeed(value string) bool {
	cleaned := cleanSearchText(value)
	if !isMeaningfulPhrase(cleaned) {
		return false
	}
	for _, token := range tokenize(cleaned) {
		if shouldKeepToken(token) {
			return true
		}
	}
	return false
}

func isMeaningfulPhrase(value string) bool {
	dense := normalizeDense(cleanSearchText(value))
	if dense == "" || isGenericDense(dense) || isAllDigits(dense) || isCatalogCode(dense) {
		return false
	}
	return true
}

func shouldKeepToken(value string) bool {
	dense := normalizeDense(value)
	if dense == "" || len([]rune(dense)) < 2 {
		return false
	}
	if isGenericDense(dense) || isAllDigits(dense) || isCatalogCode(dense) {
		return false
	}
	return true
}

func isGenericDense(value string) bool {
	_, ok := genericDenseTerms[normalizeDense(value)]
	return ok
}

func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func isCatalogCode(value string) bool {
	for _, prefix := range []string{"rj", "vj", "bj", "dm"} {
		if strings.HasPrefix(value, prefix) && isAllDigits(strings.TrimPrefix(value, prefix)) {
			return true
		}
	}
	return false
}

func baseNameFromAnyPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	replacer := strings.NewReplacer("/", string(filepath.Separator), "\\", string(filepath.Separator))
	normalized := replacer.Replace(trimmed)
	return filepath.Base(filepath.Clean(normalized))
}

func roundScore(value float64) float64 {
	return float64(int(value*1000+0.5)) / 1000
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
