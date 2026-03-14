package app

import (
	"context"

	backendpkg "venera_home_server/backend"
	"venera_home_server/config"
	"venera_home_server/favorites"
	metadatapkg "venera_home_server/metadata"
)

func (a *App) Config() *config.Config {
	return a.cfg
}

func (a *App) Libraries() []config.LibraryConfig {
	return append([]config.LibraryConfig(nil), a.cfg.Libraries...)
}

func (a *App) LibraryComicIDs(libraryID string) []string {
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	return append([]string(nil), a.libraries[libraryID]...)
}

func (a *App) Comics() []*Comic {
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	out := make([]*Comic, 0, len(a.comics))
	for _, comic := range a.comics {
		out = append(out, comic)
	}
	return out
}

func (a *App) ComicByID(id string) *Comic {
	return a.comicByID(id)
}

func (a *App) ComicByLocator(locator metadatapkg.Locator) *Comic {
	if a == nil || !locator.Valid() {
		return nil
	}
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	for _, comic := range a.comics {
		if comic == nil {
			continue
		}
		if comic.LibraryID == locator.LibraryID && comic.RootType == locator.RootType && comic.RootRef == locator.RootRef {
			return comic
		}
	}
	return nil
}

func (a *App) ChapterByID(id string) *Chapter {
	a.comicsMu.RLock()
	defer a.comicsMu.RUnlock()
	return a.chapters[id]
}

func (a *App) Backend(libraryID string) backendpkg.Backend {
	return a.backends[libraryID]
}

func (a *App) Favorites() *favorites.FavoritesStore {
	return a.favorites
}

func (a *App) MaterializeChapterPages(ctx context.Context, chapter *Chapter) ([]PageRef, error) {
	return a.materializeChapterPages(ctx, chapter)
}
