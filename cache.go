package main

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"fortio.org/log" // Using fortio log (Removed explicit name)
	"github.com/google/go-github/v62/github"
)

// --- Caching Data Structures ---
type CachedListResponse struct {
	Repos    []*github.Repository
	NextPage int
}
type CachedContentResponse struct {
	Found       bool
	FileContent *github.RepositoryContent
}

// --- End Caching Data Structures ---

// --- Cache Handling Functions ---

// initCache sets up and returns the cache directory path
func initCache() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache directory: %w", err)
	}
	cacheDir := filepath.Join(userCacheDir, "depgraph_cache")
	log.LogVf("Using cache directory: %s", cacheDir) // Verbose log
	return cacheDir, os.MkdirAll(cacheDir, 0755)
}

// clearCache removes the cache directory
func clearCache(cacheDir string) error {
	if cacheDir == "" {
		return errors.New("cache directory not initialized")
	}
	log.Infof("Clearing cache directory: %s", cacheDir)
	return os.RemoveAll(cacheDir)
}

// getCacheKey generates a filename for the cache based on input parameters
func getCacheKey(cacheDir string, parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		io.WriteString(h, p)
		io.WriteString(h, "|") // Separator
	}
	hash := fmt.Sprintf("%x", h.Sum(nil))
	return filepath.Join(cacheDir, hash+".json")
}

// readCache attempts to read and unmarshal data from a cache file
func readCache(key string, target interface{}, useCache bool) (bool, error) {
	if !useCache {
		return false, nil
	}
	data, err := os.ReadFile(key)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // Cache miss - normal
		}
		// Log actual file read errors
		return false, fmt.Errorf("error reading cache file %s: %w", key, err)
	}

	err = json.Unmarshal(data, target)
	if err != nil {
		// Log unmarshal errors clearly
		log.Warnf("Error unmarshaling cache file %s, ignoring cache: %v", key, err)
		return false, nil // Treat as cache miss
	}

	// Debug log (Verbose level) to check the 'Found' status after unmarshal
	if contentCache, ok := target.(*CachedContentResponse); ok {
		log.LogVf("Cache read successful for %s - Cached Found status: %v", key, contentCache.Found)
	}

	return true, nil
}

// writeCache marshals and writes data to a cache file
func writeCache(key string, data interface{}, useCache bool) error {
	if !useCache {
		return nil
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		// Log marshal errors clearly
		log.Errf("Error marshaling data for cache key %s: %v", key, err)
		return fmt.Errorf("failed to marshal data for cache key %s: %w", key, err)
	}

	err = os.WriteFile(key, jsonData, 0644)
	if err != nil {
		// Log write errors clearly
		log.Errf("Error writing cache file %s: %v", key, err)
		return fmt.Errorf("failed to write cache file %s: %w", key, err)
	}
	log.LogVf("Cache write: %s", key)
	return nil
}

// --- End Cache Handling Functions ---
