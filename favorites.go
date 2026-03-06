package main

import (
    "encoding/json"
    "os"
    "path/filepath"
    "sort"
    "sync"
)

type FavoritesStore struct {
    path    string
    mu      sync.RWMutex
    folders map[string]string
    items   map[string]map[string]bool
}

func LoadFavoritesStore(dataDir string) (*FavoritesStore, error) {
    path := filepath.Join(dataDir, "favorites.json")
    store := &FavoritesStore{
        path: path,
        folders: map[string]string{"default": "Default"},
        items: map[string]map[string]bool{},
    }
    raw, err := os.ReadFile(path)
    if err == nil {
        var file favoritesFile
        if json.Unmarshal(raw, &file) == nil {
            if len(file.Folders) > 0 {
                store.folders = file.Folders
            }
            for folderID, ids := range file.Items {
                set := map[string]bool{}
                for _, id := range ids {
                    set[id] = true
                }
                store.items[folderID] = set
            }
        }
    }
    return store, nil
}

func (s *FavoritesStore) saveLocked() error {
    file := favoritesFile{Folders: s.folders, Items: map[string][]string{}}
    for folderID, set := range s.items {
        ids := make([]string, 0, len(set))
        for id := range set {
            ids = append(ids, id)
        }
        sort.Strings(ids)
        file.Items[folderID] = ids
    }
    raw, err := json.MarshalIndent(file, "", "  ")
    if err != nil {
        return err
    }
    if err := ensureDir(filepath.Dir(s.path)); err != nil {
        return err
    }
    return os.WriteFile(s.path, raw, 0o644)
}

func (s *FavoritesStore) ListFolders() []FavoriteFolder {
    s.mu.RLock()
    defer s.mu.RUnlock()
    out := make([]FavoriteFolder, 0, len(s.folders))
    for id, name := range s.folders {
        out = append(out, FavoriteFolder{ID: id, Name: name})
    }
    sort.Slice(out, func(i, j int) bool { return naturalLess(out[i].Name, out[j].Name) })
    return out
}

func (s *FavoritesStore) ComicFolders(comicID string) []string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    out := []string{}
    for folderID, items := range s.items {
        if items[comicID] {
            out = append(out, folderID)
        }
    }
    sort.Strings(out)
    return out
}

func (s *FavoritesStore) AddFolder(name string) (FavoriteFolder, error) {
    s.mu.Lock()
    defer s.mu.Unlock()
    id := shaID("folder", name)
    for {
        if _, exists := s.folders[id]; !exists {
            break
        }
        id = shaID(id, name)
    }
    s.folders[id] = name
    if err := s.saveLocked(); err != nil {
        return FavoriteFolder{}, err
    }
    return FavoriteFolder{ID: id, Name: name}, nil
}

func (s *FavoritesStore) DeleteFolder(id string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    delete(s.folders, id)
    delete(s.items, id)
    return s.saveLocked()
}

func (s *FavoritesStore) AddItem(folderID, comicID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if _, ok := s.folders[folderID]; !ok {
        return os.ErrNotExist
    }
    if s.items[folderID] == nil {
        s.items[folderID] = map[string]bool{}
    }
    s.items[folderID][comicID] = true
    return s.saveLocked()
}

func (s *FavoritesStore) RemoveItem(folderID, comicID string) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if s.items[folderID] != nil {
        delete(s.items[folderID], comicID)
    }
    return s.saveLocked()
}

func (s *FavoritesStore) FolderComicIDs(folderID string) []string {
    s.mu.RLock()
    defer s.mu.RUnlock()
    set := s.items[folderID]
    out := make([]string, 0, len(set))
    for id := range set {
        out = append(out, id)
    }
    sort.Strings(out)
    return out
}
