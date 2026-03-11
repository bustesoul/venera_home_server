/** @type {import('./_venera_.js')} */
class VeneraHome extends ComicSource {
    name = "Venera Home"

    key = "venera_home"

    version = "0.4.0"

    minAppVersion = "1.6.0"

    url = "https://cdn.jsdelivr.net/gh/venera-app/venera-configs@main/venera_home.js"

    get baseUrl() {
        let value = this.loadSetting('server_url') || ''
        value = value.trim()
        return value.replace(/\/$/, '')
    }

    get token() {
        return (this.loadSetting('token') || '').trim()
    }

    get defaultLibrary() {
        return (this.loadSetting('default_library') || '').trim()
    }

    get defaultSort() {
        return this.loadSetting('default_sort') || 'updated_desc'
    }

    get defaultPageSize() {
        return this.loadSetting('page_size') || '24'
    }

    get imageMode() {
        let value = (this.loadSetting('image_mode') || 'default').toString().trim().toLowerCase()
        return value === 'origin' ? 'origin' : 'default'
    }

    get headers() {
        let headers = {
            "Accept": "application/json"
        }
        if (this.token) {
            headers["Authorization"] = `Bearer ${this.token}`
        }
        return headers
    }

    get jsonHeaders() {
        return {
            ...this.headers,
            "Content-Type": "application/json"
        }
    }

    get mediaHeaders() {
        let headers = {}
        if (this.token) {
            headers["Authorization"] = `Bearer ${this.token}`
        }
        headers['X-Venera-Image-Mode'] = this.imageMode
        return headers
    }

    withMediaMode(url) {
        if (!url) {
            return url
        }
        let target = url
        let mode = encodeURIComponent(this.imageMode)
        if (/[?&]mode=/.test(target)) {
            return target.replace(/([?&])mode=[^&#]*/g, `$1mode=${mode}`)
        }
        let separator = target.includes('?') ? '&' : '?'
        return `${target}${separator}mode=${mode}`
    }

    get readerTuning() {
        return {
            maxConcurrent: 2,
            burstWindowMs: 250,
            jumpThreshold: 4,
            slotTimeoutMs: 15000,
            maxTrackedChapters: 6,
            chapterIdleMs: 10 * 60 * 1000,
        }
    }

    getReaderStore() {
        if (!this._readerStore) {
            this._readerStore = {
                chapters: {},
            }
        }
        return this._readerStore
    }

    makeReaderChapterKey(comicId, epId) {
        return `${comicId}::${epId}`
    }

    ensureReaderState(comicId, epId) {
        let store = this.getReaderStore()
        let key = this.makeReaderChapterKey(comicId, epId)
        let now = Date.now()
        let state = store.chapters[key]
        if (!state) {
            state = {
                key,
                images: [],
                indexByImage: {},
                pending: [],
                active: 0,
                focusIndex: 0,
                focusUntil: 0,
                requestSeq: 0,
                touchedAt: now,
            }
            store.chapters[key] = state
        }
        state.touchedAt = now
        this.cleanupReaderStates(key)
        return state
    }

    cleanupReaderStates(activeKey) {
        let store = this.getReaderStore()
        let tuning = this.readerTuning
        let now = Date.now()
        let keys = Object.keys(store.chapters)
        keys.sort((a, b) => {
            let ta = store.chapters[a] && store.chapters[a].touchedAt ? store.chapters[a].touchedAt : 0
            let tb = store.chapters[b] && store.chapters[b].touchedAt ? store.chapters[b].touchedAt : 0
            return tb - ta
        })
        for (let i = 0; i < keys.length; i++) {
            let key = keys[i]
            let state = store.chapters[key]
            if (!state || key === activeKey) {
                continue
            }
            let stale = now - (state.touchedAt || 0) > tuning.chapterIdleMs
            let overflow = i >= tuning.maxTrackedChapters
            if ((stale || overflow) && state.active === 0 && (!state.pending || state.pending.length === 0)) {
                delete store.chapters[key]
            }
        }
    }

    rememberChapterImages(comicId, epId, images) {
        let state = this.ensureReaderState(comicId, epId)
        state.images = Array.isArray(images) ? images.slice() : []
        state.indexByImage = {}
        for (let i = 0; i < state.images.length; i++) {
            state.indexByImage[state.images[i]] = i
        }
        return state
    }

    resolveReaderPageIndex(state, image) {
        if (Object.prototype.hasOwnProperty.call(state.indexByImage, image)) {
            return state.indexByImage[image]
        }
        let index = state.images.indexOf(image)
        if (index >= 0) {
            state.indexByImage[image] = index
            return index
        }
        return 0
    }

    updateReaderFocus(state, index) {
        let tuning = this.readerTuning
        let now = Date.now()
        if (state.focusUntil === 0 || now > state.focusUntil || Math.abs(index - state.focusIndex) >= tuning.jumpThreshold) {
            state.focusIndex = index
            state.focusUntil = now + tuning.burstWindowMs
            return
        }
        if (index < state.focusIndex) {
            state.focusIndex = index
        }
    }

    classifyReaderRole(state, index) {
        let distance = Math.abs(index - state.focusIndex)
        if (distance === 0) {
            return 'current'
        }
        if (distance === 1) {
            return 'near'
        }
        if (distance <= 3) {
            return index >= state.focusIndex ? 'ahead' : 'behind'
        }
        return 'distant'
    }

    scoreReaderTask(state, task) {
        let distance = Math.abs(task.index - state.focusIndex)
        if (distance === 0) {
            return task.order
        }
        if (distance === 1) {
            return 1000 + task.order
        }
        if (distance <= 3) {
            return 5000 + distance * 100 + task.order
        }
        return 20000 + distance * 100 + task.order
    }

    pickNextReaderTaskIndex(state) {
        let bestIndex = 0
        let bestScore = this.scoreReaderTask(state, state.pending[0])
        for (let i = 1; i < state.pending.length; i++) {
            let score = this.scoreReaderTask(state, state.pending[i])
            if (score < bestScore) {
                bestScore = score
                bestIndex = i
            }
        }
        return bestIndex
    }

    createReaderRelease(state) {
        let released = false
        return () => {
            if (released) {
                return
            }
            released = true
            if (state.active > 0) {
                state.active -= 1
            }
            this.pumpReaderQueue(state)
        }
    }

    buildReaderHintHeaders(index, role) {
        return {
            ...this.mediaHeaders,
            'X-Venera-Reader': '1',
            'X-Venera-Load-Mode': `queued-${this.readerTuning.maxConcurrent}`,
            'X-Venera-Load-Role': role,
            'X-Venera-Page-Index': String(index),
        }
    }

    buildReaderLoadConfig(image, state, index, release, attempt) {
        let role = this.classifyReaderRole(state, index)
        let headers = this.buildReaderHintHeaders(index, role)
        setTimeout(() => release(), this.readerTuning.slotTimeoutMs)
        let config = {
            url: image,
            headers,
            onResponse: (data) => {
                release()
                return data
            },
        }
        if (attempt < 1) {
            config.onLoadFailed = () => {
                release()
                return {
                    url: image,
                    headers: this.buildReaderHintHeaders(index, 'retry'),
                }
            }
        }
        return config
    }

    pumpReaderQueue(state) {
        while (state.active < this.readerTuning.maxConcurrent && state.pending.length > 0) {
            let nextIndex = this.pickNextReaderTaskIndex(state)
            let task = state.pending.splice(nextIndex, 1)[0]
            state.active += 1
            let release = this.createReaderRelease(state)
            task.resolve(this.buildReaderLoadConfig(task.image, state, task.index, release, task.attempt))
        }
    }

    async scheduleReaderImageLoad(image, comicId, epId) {
        let state = this.ensureReaderState(comicId, epId)
        let index = this.resolveReaderPageIndex(state, image)
        this.updateReaderFocus(state, index)
        return await new Promise((resolve) => {
            state.pending.push({
                image,
                index,
                attempt: 0,
                order: ++state.requestSeq,
                resolve,
            })
            this.pumpReaderQueue(state)
        })
    }

    ensureConfigured() {
        if (!this.baseUrl) {
            throw this.translate('Server URL is required')
        }
    }

    buildUrl(path, query) {
        this.ensureConfigured()
        let url = `${this.baseUrl}/api/v1${path}`
        if (!query) {
            return url
        }
        let parts = []
        for (let [key, value] of Object.entries(query)) {
            if (value === null || value === undefined || value === '') {
                continue
            }
            parts.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`)
        }
        if (parts.length > 0) {
            url += `?${parts.join('&')}`
        }
        return url
    }

    async request(method, path, query, body) {
        let url = this.buildUrl(path, query)
        let res = null
        if (method === 'GET') {
            res = await Network.get(url, this.headers)
        } else if (method === 'POST') {
            res = await Network.post(url, this.jsonHeaders, body ? JSON.stringify(body) : '{}')
        } else if (method === 'DELETE') {
            res = await Network.delete(url, this.headers)
        } else {
            throw `Unsupported method: ${method}`
        }
        if (res.status < 200 || res.status >= 300) {
            let message = `HTTP ${res.status}`
            try {
                let data = JSON.parse(res.body)
                if (data.error && data.error.message) {
                    message = data.error.message
                }
            } catch (_) {
            }
            throw message
        }
        let parsed = JSON.parse(res.body)
        if (parsed.error) {
            throw parsed.error.message || 'Request failed'
        }
        return parsed.data
    }

    async refreshBootstrap() {
        let bootstrap = await this.request('GET', '/bootstrap', null, null)
        this.saveData('bootstrap', bootstrap)
        return bootstrap
    }

    async refreshCategories() {
        let categories = await this.request('GET', '/categories', null, null)
        this.saveData('categories', categories)
        return categories
    }

    createEmptyBootstrap() {
        return {
            libraries: [],
            capabilities: {},
            defaults: {
                sort: 'updated_desc',
                page_size: 24,
            },
        }
    }

    createEmptyCategories() {
        return { groups: [] }
    }

    getMaxPage(data) {
        return data.paging && data.paging.max_page ? data.paging.max_page : 1
    }

    getBootstrapCache() {
        return this.loadData('bootstrap') || this.createEmptyBootstrap()
    }

    getCategoryCache() {
        return this.loadData('categories') || this.createEmptyCategories()
    }

    getScopedLibrary() {
        return this.defaultLibrary || null
    }

    getLibraryIdByLabel(label) {
        let bootstrap = this.getBootstrapCache()
        let libraries = bootstrap.libraries || []
        let match = libraries.find((item) => item.id === label || item.name === label)
        return match ? match.id : label
    }

    pickCover(item) {
        return this.withMediaMode(item.cover_url || item.cover || '')
    }

    flattenTags(tagsMap) {
        if (!tagsMap || typeof tagsMap !== 'object') {
            return []
        }
        let result = []
        for (let key of Object.keys(tagsMap)) {
            let values = tagsMap[key]
            if (!Array.isArray(values)) {
                continue
            }
            for (let value of values) {
                if (value && result.length < 8) {
                    if (key === 'Tag' || key === 'Author') {
                        result.push(value)
                    } else {
                        result.push(`${key}:${value}`)
                    }
                }
            }
        }
        return result
    }

    getDetailTagOrder() {
        return [
            'Category',
            'Author',
            'artist',
            'group',
            'cosplayer',
            'parody',
            'character',
            'female',
            'male',
            'mixed',
            'other',
            'language',
            'reclass',
            'uploader',
            'Tag',
            'Library',
            'Storage',
        ]
    }

    mergeDetailTags(data) {
        let tags = {}
        if (data && data.tags && typeof data.tags === 'object') {
            for (let key of Object.keys(data.tags)) {
                let values = data.tags[key]
                if (!Array.isArray(values)) {
                    continue
                }
                let cleanValues = values.filter(value => !!value)
                if (cleanValues.length > 0) {
                    tags[key] = cleanValues
                }
            }
        }
        let order = this.getDetailTagOrder()
        let rankMap = {}
        for (let i = 0; i < order.length; i++) {
            rankMap[order[i]] = i
        }
        let keys = Object.keys(tags)
        keys.sort((left, right) => {
            let leftRank = Object.prototype.hasOwnProperty.call(rankMap, left) ? rankMap[left] : order.length + 1000
            let rightRank = Object.prototype.hasOwnProperty.call(rankMap, right) ? rankMap[right] : order.length + 1000
            if (leftRank !== rightRank) {
                return leftRank - rightRank
            }
            return left.localeCompare(right)
        })
        let ordered = {}
        for (let key of keys) {
            ordered[key] = tags[key]
        }
        return ordered
    }

    formatDetailDescription(data) {
        let lines = []
        let relativePath = data && data.relative_path ? data.relative_path.toString().trim() : ''
        let description = data && data.description ? data.description.toString().trim() : ''
        if (relativePath) {
            lines.push(`Relative Path: ${relativePath}`)
        }
        if (description) {
            if (lines.length > 0) {
                lines.push('')
            }
            lines.push(description)
        }
        return lines.join('\n')
    }

    toComic(item) {
        return new Comic({
            id: item.id,
            title: item.title,
            subTitle: item.subtitle || '',
            cover: this.pickCover(item),
            tags: Array.isArray(item.tags) ? item.tags : this.flattenTags(item.tags),
            description: item.description || '',
            favoriteId: item.favorite_id || null,
            stars: item.stars || null,
        })
    }

    makeTarget(page, attributes) {
        return { page, attributes }
    }

    makeCategoryTarget(category, param) {
        return this.makeTarget('category', {
            category,
            param,
        })
    }

    makeSearchTarget(keyword) {
        return this.makeTarget('search', {
            keyword,
        })
    }

    async ensureCaches() {
        let bootstrap = this.getBootstrapCache()
        let categories = this.getCategoryCache()
        if (!bootstrap.libraries || bootstrap.libraries.length === 0) {
            await this.refreshBootstrap()
        }
        if (!categories.groups || categories.groups.length === 0) {
            await this.refreshCategories()
        }
    }

    async init() {
        try {
            await this.ensureCaches()
        } catch (_) {
            this.saveData('bootstrap', this.createEmptyBootstrap())
            this.saveData('categories', this.createEmptyCategories())
        }
    }

    mapCategoryGroup(groupKey) {
        let categories = this.getCategoryCache()
        let group = (categories.groups || []).find((item) => item.key === groupKey)
        if (!group) {
            return []
        }
        return (group.items || []).map((item) => ({
            label: item.count ? `${item.label} (${item.count})` : item.label,
            target: this.makeCategoryTarget(groupKey, item.id),
        }))
    }

    explore = [
        {
            title: "Home",
            type: "multiPartPage",
            load: async () => {
                await this.ensureCaches()
                let data = await this.request('GET', '/home', {
                    library_id: this.getScopedLibrary(),
                    page_size: this.defaultPageSize,
                }, null)
                return (data.sections || []).map((section) => {
                    let viewMore = null
                    if (section.view_more && section.view_more.category) {
                        viewMore = this.makeCategoryTarget(section.view_more.category, section.view_more.param || null)
                    }
                    return {
                        title: section.title,
                        comics: (section.items || []).map((item) => this.toComic(item)),
                        viewMore,
                    }
                })
            }
        }
    ]

    category = {
        title: "Venera Home",
        parts: [
            {
                name: "Libraries",
                type: "dynamic",
                loader: () => this.mapCategoryGroup('library')
            },
            {
                name: "Tags",
                type: "dynamic",
                loader: () => this.mapCategoryGroup('tag')
            },
            {
                name: "Authors",
                type: "dynamic",
                loader: () => this.mapCategoryGroup('author')
            },
            {
                name: "Storage",
                type: "dynamic",
                loader: () => this.mapCategoryGroup('storage')
            }
        ],
        enableRankingPage: false,
    }

    categoryComics = {
        load: async (category, param, options, page) => {
            await this.ensureCaches()
            let sort = options && options[0] ? options[0] : this.defaultSort
            let data = await this.request('GET', '/comics', {
                library_id: this.getScopedLibrary(),
                category: category || 'all',
                param: param,
                sort: sort,
                page: page,
                page_size: this.defaultPageSize,
            }, null)
            return {
                comics: (data.items || []).map((item) => this.toComic(item)),
                maxPage: this.getMaxPage(data),
            }
        },
        optionList: [
            {
                options: [
                    "updated_desc-Recently updated",
                    "added_desc-Recently added",
                    "title_asc-Title A-Z",
                    "title_desc-Title Z-A",
                    "random-Random"
                ]
            }
        ]
    }

    search = {
        load: async (keyword, options, page) => {
            await this.ensureCaches()
            let sort = options && options[0] ? options[0] : this.defaultSort
            let data = await this.request('GET', '/search', {
                q: keyword,
                library_id: this.getScopedLibrary(),
                sort: sort,
                page: page,
                page_size: this.defaultPageSize,
            }, null)
            return {
                comics: (data.items || []).map((item) => this.toComic(item)),
                maxPage: this.getMaxPage(data),
            }
        },
        optionList: [
            {
                type: "select",
                options: [
                    "updated_desc-Recently updated",
                    "added_desc-Recently added",
                    "title_asc-Title A-Z",
                    "title_desc-Title Z-A"
                ],
                label: "sort",
                default: "updated_desc",
            }
        ],
        enableTagsSuggestions: false,
        onTagSuggestionSelected: (namespace, tag) => `${namespace}:${tag}`,
    }

    favorites = {
        multiFolder: true,
        addOrDelFavorite: async (comicId, folderId, isAdding) => {
            if (isAdding) {
                await this.request('POST', '/favorites/items', null, {
                    comic_id: comicId,
                    folder_id: folderId,
                })
            } else {
                await this.request('DELETE', '/favorites/items', {
                    comic_id: comicId,
                    folder_id: folderId,
                }, null)
            }
            return 'ok'
        },
        loadFolders: async (comicId) => {
            await this.ensureCaches()
            let data = await this.request('GET', '/favorites/folders', {
                comic_id: comicId || null,
            }, null)
            let folders = {}
            for (let folder of (data.folders || [])) {
                folders[folder.id] = folder.name
            }
            return {
                folders,
                favorited: data.favorited || [],
            }
        },
        addFolder: async (name) => {
            await this.request('POST', '/favorites/folders', null, { name })
            return 'ok'
        },
        deleteFolder: async (folderId) => {
            await this.request('DELETE', `/favorites/folders/${encodeURIComponent(folderId)}`, null, null)
        },
        loadComics: async (page, folder) => {
            await this.ensureCaches()
            let data = await this.request('GET', '/favorites/comics', {
                folder_id: folder,
                page: page,
                page_size: this.defaultPageSize,
            }, null)
            return {
                comics: (data.items || []).map((item) => this.toComic(item)),
                maxPage: this.getMaxPage(data),
            }
        },
        singleFolderForSingleComic: false,
    }

    comic = {
        loadInfo: async (id) => {
            await this.ensureCaches()
            let data = await this.request('GET', `/comics/${encodeURIComponent(id)}`, null, null)
            let chapters = {}
            for (let chapter of (data.chapters || [])) {
                chapters[chapter.id] = chapter.title
            }
            return new ComicDetails({
                title: data.title,
                subTitle: data.subtitle || '',
                cover: this.withMediaMode(data.cover_url || ''),
                description: this.formatDetailDescription(data),
                tags: this.mergeDetailTags(data),
                chapters: chapters,
                isFavorite: data.favorite && data.favorite.is_favorited ? data.favorite.is_favorited : false,
                thumbnails: null,
                recommend: (data.recommend || []).map((item) => this.toComic(item)),
                updateTime: data.update_time || null,
                uploadTime: data.upload_time || null,
                url: data.source_url || null,
                stars: data.stars || null,
            })
        },
        loadThumbnails: async (id, next) => {
            let data = await this.request('GET', `/comics/${encodeURIComponent(id)}/thumbnails`, {
                next: next || null,
            }, null)
            return {
                thumbnails: data.thumbnails || [],
                next: data.next || null,
            }
        },
        loadEp: async (comicId, epId) => {
            let data = await this.request('GET', `/comics/${encodeURIComponent(comicId)}/chapters/${encodeURIComponent(epId)}/pages`, null, null)
            let images = data.images || []
            this.rememberChapterImages(comicId, epId, images)
            return {
                images,
            }
        },
        onImageLoad: async (image, comicId, epId) => {
            return await this.scheduleReaderImageLoad(image, comicId, epId)
        },
        onThumbnailLoad: (url) => {
            return {
                url,
                headers: {
                    ...this.mediaHeaders,
                    'X-Venera-Reader': '1',
                    'X-Venera-Load-Role': 'thumbnail',
                }
            }
        },
        idMatch: '^[A-Za-z0-9_:-]+$',
        onClickTag: (namespace, tag) => {
            if (namespace === 'Library') {
                return this.makeCategoryTarget('library', this.getLibraryIdByLabel(tag))
            }
            if (namespace === 'Storage') {
                return this.makeCategoryTarget('storage', tag)
            }
            if (namespace === 'Author') {
                return this.makeSearchTarget(`author:${tag}`)
            }
            if (namespace === 'Tag') {
                return this.makeSearchTarget(`tag:${tag}`)
            }
            if (namespace) {
                return this.makeSearchTarget(`tag:${namespace}:${tag}`)
            }
            return this.makeSearchTarget(`tag:${tag}`)
        },
        enableTagsTranslate: true,
    }

    settings = {
        server_url: {
            title: "Server URL",
            type: "input",
            validator: '^https?:\\/\\/.+$',
            default: 'http://127.0.0.1:34123',
        },
        token: {
            title: "Token",
            type: "input",
            default: '',
        },
        default_library: {
            title: "Default Library ID",
            type: "input",
            default: '',
        },
        default_sort: {
            title: "Default Sort",
            type: "select",
            options: [
                { value: 'updated_desc', text: 'Recently updated' },
                { value: 'added_desc', text: 'Recently added' },
                { value: 'title_asc', text: 'Title A-Z' },
                { value: 'title_desc', text: 'Title Z-A' },
            ],
            default: 'updated_desc',
        },
        page_size: {
            title: "Page Size",
            type: "select",
            options: [
                { value: '12', text: '12' },
                { value: '24', text: '24' },
                { value: '36', text: '36' },
                { value: '48', text: '48' },
            ],
            default: '24',
        },
        image_mode: {
            title: 'Image Mode',
            type: 'select',
            options: [
                { value: 'default', text: 'High Quality Compressed' },
                { value: 'origin', text: 'Origin' },
            ],
            default: 'default',
        },
        test_connection: {
            title: "Test Connection",
            type: "callback",
            buttonText: "Test",
            callback: async () => {
                let title = this.translate('Connection Failed')
                let message = `${this.translate('Server URL')}: ${this.baseUrl || '(empty)'}`
                try {
                    let bootstrap = await this.refreshBootstrap()
                    await this.refreshCategories()
                    let libraries = Array.isArray(bootstrap.libraries) ? bootstrap.libraries : []
                    let lines = [
                        `${this.translate('Server URL')}: ${this.baseUrl}`,
                        `${this.translate('Connected')}: ${libraries.length} ${this.translate('libraries')}`
                    ]
                    if (libraries.length > 0) {
                        lines.push('')
                        lines.push(`${this.translate('Libraries')}:`)
                        for (let i = 0; i < libraries.length; i++) {
                            let item = libraries[i] || {}
                            lines.push(`${i + 1}. ${item.name || '(unnamed)'} (${item.id || '-'})`)
                        }
                    }
                    title = this.translate('Connection Success')
                    message = lines.join('\n')
                } catch (e) {
                    let detail = e && e.message ? e.message : `${e}`
                    message = `${message}\n\n${this.translate('Error')}: ${detail}`
                }
                UI.showDialog(
                    title,
                    message,
                    [
                        {
                            text: this.translate('OK'),
                            callback: () => { }
                        }
                    ]
                )
            }
        },
        rescan_library: {
            title: "Library Scan",
            type: "callback",
            buttonText: "Scan",
            callback: async () => {
                let title = this.translate('Library Scan Failed')
                let message = `${this.translate('Server URL')}: ${this.baseUrl || '(empty)'}`
                try {
                    let bootstrap = await this.refreshBootstrap()
                    let libraries = bootstrap.libraries || []
                    let options = [this.translate('All Libraries')].concat(libraries.map((item) => `${item.name} (${item.id})`))
                    let selected = await UI.showSelectDialog(this.translate('Select library to scan'), options, 0)
                    if (selected === null) {
                        return
                    }
                    let payload = {}
                    let targetLabel = this.translate('All Libraries')
                    if (selected > 0) {
                        let target = libraries[selected - 1] || {}
                        payload.library_id = target.id
                        targetLabel = `${target.name || '(unnamed)'} (${target.id || '-'})`
                    }
                    await this.request('POST', '/admin/rescan', null, payload)
                    await this.refreshBootstrap()
                    await this.refreshCategories()
                    title = this.translate('Library Scan Submitted')
                    message = [
                        `${this.translate('Server URL')}: ${this.baseUrl}`,
                        `${this.translate('Target Library')}: ${targetLabel}`,
                        '',
                        this.translate('Library scan requested')
                    ].join('\n')
                } catch (e) {
                    let detail = e && e.message ? e.message : `${e}`
                    message = `${message}\n\n${this.translate('Error')}: ${detail}`
                }
                UI.showDialog(
                    title,
                    message,
                    [
                        {
                            text: this.translate('OK'),
                            callback: () => { }
                        }
                    ]
                )
            }
        },
        open_server: {
            title: "Open Server",
            type: "callback",
            buttonText: "Open",
            callback: () => {
                this.ensureConfigured()
                UI.launchUrl(this.baseUrl)
            }
        }
    }

    translation = {
        'en': {
            'Home': 'Home',
            'Libraries': 'Libraries',
            'Tags': 'Tags',
            'Authors': 'Authors',
            'Storage': 'Storage',
            'Tag': 'Tag',
            'Author': 'Author',
            'Library': 'Library',
            'language': 'Language',
            'artist': 'Artist',
            'male': 'Male',
            'female': 'Female',
            'mixed': 'Mixed',
            'other': 'Other',
            'parody': 'Parody',
            'character': 'Character',
            'group': 'Group',
            'cosplayer': 'Cosplayer',
            'reclass': 'Reclass',
            'uploader': 'Uploader',
            'Server URL': 'Server URL',
            'Token': 'Token',
            'Default Library ID': 'Default Library ID',
            'Default Sort': 'Default Sort',
            'Page Size': 'Page Size',
            'Image Mode': 'Image Mode',
            'Test Connection': 'Test Connection',
            'Library Scan': 'Library Scan',
            'Scan': 'Scan',
            'Open Server': 'Open Server',
            'Server URL is required': 'Server URL is required',
            'Connected': 'Connected',
            'Connection Success': 'Connection Success',
            'Connection Failed': 'Connection Failed',
            'Library Scan Submitted': 'Library Scan Submitted',
            'Library Scan Failed': 'Library Scan Failed',
            'Target Library': 'Target Library',
            'Error': 'Error',
            'OK': 'OK',
            'All Libraries': 'All Libraries',
            'libraries': 'libraries',
            'Select library to scan': 'Select library to scan',
            'Library scan requested': 'Library scan requested',
        },
        'zh_CN': {
            'Home': '首页',
            'Libraries': '书库',
            'Tags': '标签',
            'Authors': '作者',
            'Storage': '存储',
            'Tag': '标签',
            'Author': '作者',
            'Library': '书库',
            'language': '语言',
            'artist': '画师',
            'male': '男性',
            'female': '女性',
            'mixed': '混合',
            'other': '其它',
            'parody': '原作',
            'character': '角色',
            'group': '团队',
            'cosplayer': 'Coser',
            'reclass': '重新分类',
            'uploader': '上传者',
            'Server URL': '服务器地址',
            'Token': '令牌',
            'Default Library ID': '默认书库 ID',
            'Default Sort': '默认排序',
            'Page Size': '分页大小',
            'Image Mode': '图片模式',
            'Test Connection': '测试连接',
            'Library Scan': '书库扫描',
            'Scan': '扫描',
            'Open Server': '打开服务器',
            'Server URL is required': '需要先填写服务器地址',
            'Connected': '已连接',
            'Connection Success': '连接成功',
            'Connection Failed': '连接失败',
            'Library Scan Submitted': '书库扫描已提交',
            'Library Scan Failed': '书库扫描失败',
            'Target Library': '目标书库',
            'Error': '错误',
            'OK': '确定',
            'All Libraries': '全部书库',
            'libraries': '个书库',
            'Select library to scan': '选择要扫描的书库',
            'Library scan requested': '已提交书库扫描请求',
        },
        'zh_TW': {
            'Home': '首頁',
            'Libraries': '書庫',
            'Tags': '標籤',
            'Authors': '作者',
            'Storage': '儲存',
            'Tag': '標籤',
            'Author': '作者',
            'Library': '書庫',
            'language': '語言',
            'artist': '畫師',
            'male': '男性',
            'female': '女性',
            'mixed': '混合',
            'other': '其他',
            'parody': '原作',
            'character': '角色',
            'group': '團隊',
            'cosplayer': 'Coser',
            'reclass': '重新分類',
            'uploader': '上傳者',
            'Server URL': '伺服器位址',
            'Token': '權杖',
            'Default Library ID': '預設書庫 ID',
            'Default Sort': '預設排序',
            'Page Size': '分頁大小',
            'Image Mode': '圖片模式',
            'Test Connection': '測試連線',
            'Library Scan': '書庫掃描',
            'Scan': '掃描',
            'Open Server': '打開伺服器',
            'Server URL is required': '需要先填寫伺服器位址',
            'Connected': '已連線',
            'Connection Success': '連線成功',
            'Connection Failed': '連線失敗',
            'Library Scan Submitted': '書庫掃描已送出',
            'Library Scan Failed': '書庫掃描失敗',
            'Target Library': '目標書庫',
            'Error': '錯誤',
            'OK': '確定',
            'All Libraries': '全部書庫',
            'libraries': '個書庫',
            'Select library to scan': '選擇要掃描的書庫',
            'Library scan requested': '已送出書庫掃描請求',
        },
    }
}
