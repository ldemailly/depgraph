package main

import (
	"context"
	"errors"

	// "fmt" // Removed unused import
	"net/http"
	"strconv"

	"fortio.org/log" // Using fortio log
	"github.com/google/go-github/v62/github"
)

// --- Structs ---
// ModuleInfo stores details about modules found in the scanned owners (orgs or users)
type ModuleInfo struct {
	Path               string // Module path from go.mod
	RepoPath           string // Repository path (owner/repo) where it was found
	IsFork             bool
	OriginalModulePath string            // Module path from the parent repo's go.mod (if fork)
	Owner              string            // Owner (org or user) where the module definition was found
	OwnerIdx           int               // Index of the owner in the input list (for coloring)
	Deps               map[string]string // path -> version
	Fetched            bool              // Indicates if the go.mod was successfully fetched and parsed
	TreatAsExternal    bool              // NEW: If true, treat this node as external in graph output, even if found in owners (used for forks covering original paths)
}

// --- End Structs ---

// --- Utility Functions ---
// isNotFoundError checks if an error is a GitHub API 404 Not Found error
func isNotFoundError(err error) bool {
	var ge *github.ErrorResponse
	if errors.As(err, &ge) {
		// Consider both 404 and 403 (sometimes returned for private repos/users) as "not found" for our purpose
		return ge.Response.StatusCode == http.StatusNotFound || ge.Response.StatusCode == http.StatusForbidden
	}
	return false
}

// --- End Utility Functions ---

// --- GitHub Client Wrapper ---

// ClientWrapper wraps the GitHub client and cache settings
type ClientWrapper struct {
	client   *github.Client
	cacheDir string
	useCache bool
}

// NewClientWrapper creates a new GitHub client wrapper
func NewClientWrapper(client *github.Client, cacheDir string, useCache bool) *ClientWrapper {
	return &ClientWrapper{
		client:   client,
		cacheDir: cacheDir,
		useCache: useCache,
	}
}

// --- Cached GitHub API Methods ---

func (cw *ClientWrapper) getCachedListByOrg(ctx context.Context, owner string, opt *github.RepositoryListByOrgOptions) ([]*github.Repository, *github.Response, error) {
	keyParts := []string{"ListByOrg", owner, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(cw.cacheDir, keyParts...)
	var cachedData CachedListResponse
	hit, readErr := readCache(cacheKey, &cachedData, cw.useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
		// Don't treat read error as fatal, proceed as cache miss
	}
	if hit {
		log.LogVf("Cache hit for ListByOrg owner=%s page=%d", owner, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Infof("Cache miss for ListByOrg owner=%s page=%d, calling API", owner, opt.Page)
	repos, resp, apiErr := cw.client.Repositories.ListByOrg(ctx, owner, opt)
	if apiErr != nil {
		return nil, resp, apiErr // Return API error
	}
	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
	if writeErr != nil {
		log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
		// Don't treat write error as fatal
	}
	return repos, resp, nil
}

func (cw *ClientWrapper) getCachedListByUser(ctx context.Context, user string, opt *github.RepositoryListByUserOptions) ([]*github.Repository, *github.Response, error) {
	keyParts := []string{"ListByUser", user, opt.Type, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(cw.cacheDir, keyParts...)
	var cachedData CachedListResponse
	hit, readErr := readCache(cacheKey, &cachedData, cw.useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.LogVf("Cache hit for ListByUser user=%s type=%s page=%d", user, opt.Type, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Infof("Cache miss for ListByUser user=%s type=%s page=%d, calling API", user, opt.Type, opt.Page)
	repos, resp, apiErr := cw.client.Repositories.ListByUser(ctx, user, opt)
	if apiErr != nil {
		return nil, resp, apiErr
	}
	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
	if writeErr != nil {
		log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
	}
	return repos, resp, nil
}

func (cw *ClientWrapper) getCachedGetContents(ctx context.Context, owner, repo, path string, opt *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
	ref := ""
	if opt != nil {
		ref = opt.Ref
	}
	keyParts := []string{"GetContents", owner, repo, path, ref}
	cacheKey := getCacheKey(cw.cacheDir, keyParts...)
	var cachedData CachedContentResponse
	hit, readErr := readCache(cacheKey, &cachedData, cw.useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
	}

	if hit {
		if !cachedData.Found {
			log.LogVf("Cache hit indicates Not Found for GetContents repo=%s/%s path=%s ref=%s", owner, repo, path, ref)
			// Return nil for file/dir content, but a valid (empty) response to signal cache hit
			return nil, nil, &github.Response{}, nil
		} else {
			log.LogVf("Cache hit indicates Found for GetContents repo=%s/%s path=%s ref=%s", owner, repo, path, ref)
			// Return cached content and empty response
			return cachedData.FileContent, nil, &github.Response{}, nil
		}
	}

	log.Infof("Cache miss for GetContents repo=%s/%s path=%s ref=%s, calling API", owner, repo, path, ref)
	fileContent, dirContent, resp, apiErr := cw.client.Repositories.GetContents(ctx, owner, repo, path, opt)

	// Check for 404 specifically *after* the API call
	if apiErr != nil {
		if isNotFoundError(apiErr) {
			log.LogVf("API reported Not Found for GetContents repo=%s/%s path=%s ref=%s. Caching result.", owner, repo, path, ref)
			dataToCache := CachedContentResponse{Found: false} // Cache the not-found status
			writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
			if writeErr != nil {
				log.Errf("Error writing 'Not Found' cache for %v: %v", keyParts, writeErr)
			}
			// Return nil content, the original response (containing 404), and the original error
			return nil, nil, resp, apiErr
		} else {
			// Return other API errors directly
			return nil, nil, resp, apiErr
		}
	}

	// If no error and fileContent is not nil, cache the found status and content
	if fileContent != nil {
		dataToCache := CachedContentResponse{Found: true, FileContent: fileContent}
		writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
		if writeErr != nil {
			log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
		}
	} else {
		// Don't cache directory listings or if fileContent is nil for some reason
		log.LogVf("Skipping cache write for directory listing or nil file content: %v", keyParts)
		// Still need to cache the "found" status for directories? Maybe not necessary.
		// Let's cache directories as "Not Found" for simplicity, as we only care about go.mod files.
		if dirContent != nil {
			dataToCache := CachedContentResponse{Found: false}
			writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
			if writeErr != nil {
				log.Errf("Error writing 'Not Found' (directory) cache for %v: %v", keyParts, writeErr)
			}
		}
	}

	// Return the actual results from the API call
	return fileContent, dirContent, resp, nil
}

// New: Cached wrapper for getting full repo details
func (cw *ClientWrapper) getCachedGetRepo(ctx context.Context, owner, repo string) (*github.Repository, *github.Response, error) {
	keyParts := []string{"GetRepo", owner, repo}
	cacheKey := getCacheKey(cw.cacheDir, keyParts...)
	var cachedData CachedRepoResponse
	hit, readErr := readCache(cacheKey, &cachedData, cw.useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.LogVf("Cache hit for GetRepo owner=%s repo=%s", owner, repo)
		// Check if cached repo is nil, might happen if previous fetch failed but was cached improperly
		if cachedData.Repo == nil {
			log.Warnf("Cache hit for GetRepo %s/%s but cached data is nil, treating as miss.", owner, repo)
		} else {
			return cachedData.Repo, &github.Response{}, nil // Return minimal response on hit
		}
	}

	log.Infof("Cache miss for GetRepo owner=%s repo=%s, calling API", owner, repo)
	fullRepo, resp, apiErr := cw.client.Repositories.Get(ctx, owner, repo)
	if apiErr != nil {
		// Do not cache errors for GetRepo, as they might be transient
		return nil, resp, apiErr
	}

	// Only cache successful responses
	if fullRepo != nil {
		dataToCache := CachedRepoResponse{Repo: fullRepo}
		writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
		if writeErr != nil {
			log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
		}
	}
	return fullRepo, resp, nil
}

// --- End Cached GitHub API Methods ---
