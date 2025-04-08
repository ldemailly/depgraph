package main

import (
	"context"
	"errors"
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
}

// --- End Structs ---

// --- Utility Functions ---
// isNotFoundError checks if an error is a GitHub API 404 Not Found error
func isNotFoundError(err error) bool {
	var ge *github.ErrorResponse
	if errors.As(err, &ge) {
		return ge.Response.StatusCode == http.StatusNotFound
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
	}
	if hit {
		log.LogVf("Cache hit for ListByOrg owner=%s page=%d", owner, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Infof("Cache miss for ListByOrg owner=%s page=%d, calling API", owner, opt.Page)
	repos, resp, apiErr := cw.client.Repositories.ListByOrg(ctx, owner, opt)
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
			return nil, nil, &github.Response{}, nil
		} else {
			log.LogVf("Cache hit indicates Found for GetContents repo=%s/%s path=%s ref=%s", owner, repo, path, ref)
			return cachedData.FileContent, nil, &github.Response{}, nil
		}
	}

	log.Infof("Cache miss for GetContents repo=%s/%s path=%s ref=%s, calling API", owner, repo, path, ref)
	fileContent, dirContent, resp, apiErr := cw.client.Repositories.GetContents(ctx, owner, repo, path, opt)

	if apiErr != nil {
		if isNotFoundError(apiErr) {
			log.LogVf("API reported Not Found for GetContents repo=%s/%s path=%s ref=%s. Caching result.", owner, repo, path, ref)
			dataToCache := CachedContentResponse{Found: false}
			writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
			if writeErr != nil {
				log.Errf("Error writing 'Not Found' cache for %v: %v", keyParts, writeErr)
			}
			return nil, nil, resp, nil
		} else {
			return nil, nil, resp, apiErr
		}
	}
	if fileContent != nil {
		dataToCache := CachedContentResponse{Found: true, FileContent: fileContent}
		writeErr := writeCache(cacheKey, dataToCache, cw.useCache)
		if writeErr != nil {
			log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
		}
	} else {
		log.LogVf("Skipping cache write for directory listing: %v", keyParts)
	}

	return fileContent, dirContent, resp, nil
}

// --- End Cached GitHub API Methods ---
