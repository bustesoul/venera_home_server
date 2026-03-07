package app

import "time"

type Comic struct {
	ID          string     `json:"id"`
	LibraryID   string     `json:"library_id"`
	LibraryName string     `json:"library_name"`
	Storage     string     `json:"storage"`
	Title       string     `json:"title"`
	Subtitle    string     `json:"subtitle,omitempty"`
	Description string     `json:"description,omitempty"`
	Authors     []string   `json:"authors,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	Language    string     `json:"language,omitempty"`
	SourceURL   string     `json:"source_url,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	AddedAt     time.Time  `json:"added_at"`
	RootType    string     `json:"root_type"`
	RootRef     string     `json:"root_ref"`
	Chapters    []*Chapter `json:"chapters"`
}

type Chapter struct {
	ID          string `json:"id"`
	ComicID     string `json:"comic_id"`
	Title       string `json:"title"`
	Index       int    `json:"index"`
	SourceType  string `json:"source_type"`
	SourceRef   string `json:"source_ref"`
	EntryPrefix string `json:"entry_prefix,omitempty"`
	PageCount   int    `json:"page_count"`
	pages       []PageRef
}

type PageRef struct {
	PageIndex  int
	SourceType string
	SourceRef  string
	EntryName  string
	Name       string
	Size       int64
	ModTime    time.Time
}

type ParsedMetadata struct {
	Title       string   `json:"title,omitempty"`
	Series      string   `json:"series,omitempty"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Description string   `json:"description,omitempty"`
	Authors     []string `json:"authors,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Language    string   `json:"language,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	ScanMode    string   `json:"scan_mode,omitempty"`
	Hidden      bool     `json:"hidden,omitempty"`

	hasExplicitTitle  bool
	hasExplicitSeries bool
}

type CategoryGroup struct {
	Key   string         `json:"key"`
	Title string         `json:"title"`
	Items []CategoryItem `json:"items"`
}

type CategoryItem struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Count int    `json:"count,omitempty"`
}

type FavoriteFolder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type favoritesFile struct {
	Folders map[string]string   `json:"folders"`
	Items   map[string][]string `json:"items"`
}
