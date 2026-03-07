package httpapi

import (
    apppkg "venera_home_server/internal/app"
    backendpkg "venera_home_server/internal/backend"
    configpkg "venera_home_server/internal/config"
)

type App = apppkg.App
type Comic = apppkg.Comic
type Chapter = apppkg.Chapter
type PageRef = apppkg.PageRef
type CategoryGroup = apppkg.CategoryGroup
type CategoryItem = apppkg.CategoryItem
type Backend = backendpkg.Backend
type LibraryConfig = configpkg.LibraryConfig
