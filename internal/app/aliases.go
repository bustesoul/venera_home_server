package app

import (
    backendpkg "venera_home_server/internal/backend"
    configpkg "venera_home_server/internal/config"
    favoritespkg "venera_home_server/internal/favorites"
)

type Backend = backendpkg.Backend
type Entry = backendpkg.Entry
type Config = configpkg.Config
type LibraryConfig = configpkg.LibraryConfig
type FavoritesStore = favoritespkg.FavoritesStore
