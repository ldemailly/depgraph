package main

import (
	"context"
	"crypto/sha1" // For cache key hashing
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http" // For escaping cache key parts
	"os"
	"path/filepath" // For cache path manipulation
	"sort"
	"strconv" // For page number conversion
	"strings"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// --- Structs (ModuleInfo, Color Palettes) ---
type ModuleInfo struct {
	Path               string
	RepoPath           string
	IsFork             bool
	OriginalModulePath string
	Owner              string
	OwnerIdx           int
	Deps               map[string]string
	Fetched            bool
}

var orgNonForkColors = []string{"lightblue", "lightgreen", "lightsalmon", "lightgoldenrodyellow", "lightpink"}
var orgForkColors = []string{"steelblue", "darkseagreen", "coral", "darkkhaki", "mediumvioletred"}
var externalColor = "lightgrey"

// --- End Structs ---

// --- Caching Data Structures ---
type CachedListResponse struct {
	Repos    []*github.Repository
	NextPage int
}

// Updated CachedContentResponse to store Found status
type CachedContentResponse struct {
	Found       bool                      // Explicitly store if the file was found
	FileContent *github.RepositoryContent // Content only if Found is true
}

// --- End Caching Data Structures ---

// --- Global Cache Variables ---
var cacheDir string
var useCache bool

// --- End Global Cache Variables ---

// --- Utility Functions (isNotFoundError) ---
func isNotFoundError(err error) bool {
	var ge *github.ErrorResponse
	if errors.As(err, &ge) {
		return ge.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// --- End Utility Functions ---

// --- Cache Handling Functions ---
// (initCache, clearCache, getCacheKey remain the same)
func initCache() error {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return fmt.Errorf("failed to get user cache directory: %w", err)
	}
	cacheDir = filepath.Join(userCacheDir, "depgraph_cache")
	return os.MkdirAll(cacheDir, 0755)
}
func clearCache() error {
	if cacheDir == "" {
		if err := initCache(); err != nil {
			return err
		}
	}
	log.Printf("Clearing cache directory: %s", cacheDir)
	return os.RemoveAll(cacheDir)
}
func getCacheKey(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		io.WriteString(h, p)
		io.WriteString(h, "|")
	}
	hash := fmt.Sprintf("%x", h.Sum(nil))
	return filepath.Join(cacheDir, hash+".json")
}

// (readCache remains the same)
func readCache(key string, target interface{}) (bool, error) {
	if !useCache {
		return false, nil
	}
	data, err := os.ReadFile(key)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("error reading cache file %s: %w", key, err)
	}
	err = json.Unmarshal(data, target)
	if err != nil {
		log.Printf("Error unmarshaling cache file %s, ignoring cache: %v", key, err)
		return false, nil
	}
	return true, nil
}

// (writeCache remains the same)
func writeCache(key string, data interface{}) error {
	if !useCache {
		return nil
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Printf("Error marshaling data for cache key %s: %v", key, err)
		return fmt.Errorf("failed to marshal data for cache key %s: %w", key, err)
	}
	err = os.WriteFile(key, jsonData, 0644)
	if err != nil {
		log.Printf("Error writing cache file %s: %v", key, err)
		return fmt.Errorf("failed to write cache file %s: %w", key, err)
	}
	return nil
}

// --- End Cache Handling Functions ---

// --- Cached GitHub API Wrappers ---
// (getCachedListByOrg and getCachedList remain the same)
func getCachedListByOrg(ctx context.Context, client *github.Client, owner string, opt *github.RepositoryListByOrgOptions) ([]*github.Repository, *github.Response, error) {
	keyParts := []string{"ListByOrg", owner, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(keyParts...)
	var cachedData CachedListResponse
	hit, readErr := readCache(cacheKey, &cachedData)
	if readErr != nil {
		log.Printf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.Printf("Cache hit for ListByOrg owner=%s page=%d", owner, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Printf("Cache miss for ListByOrg owner=%s page=%d, calling API", owner, opt.Page)
	repos, resp, apiErr := client.Repositories.ListByOrg(ctx, owner, opt)
	if apiErr != nil {
		return nil, resp, apiErr
	}
	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache)
	if writeErr != nil {
		log.Printf("Error writing cache for %v: %v", keyParts, writeErr)
	}
	return repos, resp, nil
}
func getCachedList(ctx context.Context, client *github.Client, owner string, opt *github.RepositoryListOptions) ([]*github.Repository, *github.Response, error) {
	keyParts := []string{"List", owner, opt.Type, opt.Visibility, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(keyParts...)
	var cachedData CachedListResponse
	hit, readErr := readCache(cacheKey, &cachedData)
	if readErr != nil {
		log.Printf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.Printf("Cache hit for List owner=%s page=%d", owner, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Printf("Cache miss for List owner=%s page=%d, calling API", owner, opt.Page)
	repos, resp, apiErr := client.Repositories.List(ctx, owner, opt)
	if apiErr != nil {
		return nil, resp, apiErr
	}
	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache)
	if writeErr != nil {
		log.Printf("Error writing cache for %v: %v", keyParts, writeErr)
	}
	return repos, resp, nil
}

// Updated getCachedGetContents to cache "Not Found" results
func getCachedGetContents(ctx context.Context, client *github.Client, owner, repo, path string, opt *github.RepositoryContentGetOptions) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
	ref := ""
	if opt != nil {
		ref = opt.Ref
	}
	keyParts := []string{"GetContents", owner, repo, path, ref}
	cacheKey := getCacheKey(keyParts...)
	var cachedData CachedContentResponse

	hit, readErr := readCache(cacheKey, &cachedData)
	if readErr != nil {
		log.Printf("Error reading cache for %v: %v", keyParts, readErr)
	}

	if hit {
		if !cachedData.Found {
			log.Printf("Cache hit (Not Found) for GetContents repo=%s/%s path=%s ref=%s", owner, repo, path, ref)
			// Return nil content and nil error to indicate cached "Not Found"
			return nil, nil, &github.Response{}, nil
		} else {
			log.Printf("Cache hit for GetContents repo=%s/%s path=%s ref=%s", owner, repo, path, ref)
			return cachedData.FileContent, nil, &github.Response{}, nil
		}
	}

	// Cache miss or cache read error
	log.Printf("Cache miss for GetContents repo=%s/%s path=%s ref=%s, calling API", owner, repo, path, ref)
	fileContent, dirContent, resp, apiErr := client.Repositories.GetContents(ctx, owner, repo, path, opt)

	// Handle API call result and cache appropriately
	if apiErr != nil {
		if isNotFoundError(apiErr) {
			// Cache the "Not Found" result
			log.Printf("API reported Not Found for GetContents repo=%s/%s path=%s ref=%s. Caching result.", owner, repo, path, ref)
			dataToCache := CachedContentResponse{Found: false}
			writeErr := writeCache(cacheKey, dataToCache)
			if writeErr != nil {
				log.Printf("Error writing 'Not Found' cache for %v: %v", keyParts, writeErr)
			}
			// Return nil content and nil error to signal cached "Not Found" state to caller
			return nil, nil, resp, nil // Changed: return nil error for cached 404
		} else {
			// Other API error, don't cache, return error
			return nil, nil, resp, apiErr
		}
	}

	// API call successful
	if fileContent != nil {
		// Cache the found file content
		dataToCache := CachedContentResponse{Found: true, FileContent: fileContent}
		writeErr := writeCache(cacheKey, dataToCache)
		if writeErr != nil {
			log.Printf("Error writing cache for %v: %v", keyParts, writeErr)
		}
	} else {
		// Don't cache directory listings
		log.Printf("Skipping cache write for directory listing: %v", keyParts)
	}

	return fileContent, dirContent, resp, nil // Return successful result
}

// --- End Cached GitHub API Wrappers ---

// Fetches and parses go.mod files for repos in specified GitHub orgs or user accounts.
// Outputs the dependency graph in DOT format.
func main() {
	// --- Command Line Flags ---
	noExt := flag.Bool("noext", false, "Exclude external (non-org/user) dependencies from the graph")
	useCacheFlag := flag.Bool("use-cache", true, "Enable filesystem caching for GitHub API calls")
	clearCacheFlag := flag.Bool("clear-cache", false, "Clear the cache directory before running")
	flag.Parse()
	useCache = *useCacheFlag
	if err := initCache(); err != nil {
		log.Fatalf("Failed to initialize cache: %v", err)
	}
	if *clearCacheFlag {
		if err := clearCache(); err != nil {
			log.Fatalf("Failed to clear cache: %v", err)
		}
		if err := initCache(); err != nil {
			log.Fatalf("Failed to re-initialize cache after clearing: %v", err)
		}
	}
	owners := flag.Args()
	if len(owners) < 1 {
		log.Fatalf("Usage: %s [-noext] [-use-cache=true|false] [-clear-cache] <owner1> [owner2]...", os.Args[0])
	}
	ownerIndexMap := make(map[string]int)
	for i, owner := range owners {
		ownerIndexMap[owner] = i
	}
	// --- End Command Line Flags ---

	// --- GitHub Client Setup ---
	token := os.Getenv("GITHUB_TOKEN")
	ctx := context.Background()
	var httpClient *http.Client = nil
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		httpClient = oauth2.NewClient(ctx, ts)
	} else {
		httpClient = http.DefaultClient
		log.Println("Warning: GITHUB_TOKEN environment variable not set. Using unauthenticated access (may hit rate limits).")
	}
	client := github.NewClient(httpClient)
	// --- End GitHub Client Setup ---

	// Store module info: map[modulePath]ModuleInfo
	modulesFoundInOwners := make(map[string]*ModuleInfo)
	// Keep track of all unique module paths encountered (sources and dependencies)
	allModulePaths := make(map[string]bool)

	// --- Scan Owners (Orgs or Users) ---
	for i, owner := range owners {
		log.Printf("Processing owner %d: %s\n", i+1, owner)
		var repos []*github.Repository
		var resp *github.Response
		var err error
		isOrg := true
		var orgOpt *github.RepositoryListByOrgOptions
		var userOpt *github.RepositoryListOptions
		orgOpt = &github.RepositoryListByOrgOptions{Type: "public", ListOptions: github.ListOptions{PerPage: 100}}
		repos, resp, err = getCachedListByOrg(ctx, client, owner, orgOpt)
		if err != nil {
			if isNotFoundError(err) {
				log.Printf("    Owner %s not found as an organization, trying as a user...", owner)
				isOrg = false
				userOpt = &github.RepositoryListOptions{Type: "owner", Visibility: "public", ListOptions: github.ListOptions{PerPage: 100}}
				repos, resp, err = getCachedList(ctx, client, owner, userOpt)
			}
			if err != nil {
				log.Printf("Error listing repositories for %s: %v", owner, err)
				continue
			}
		}
		currentPage := 1
		for { // Pagination loop
			if repos == nil {
				log.Printf("    No repositories found or error occurred for page %d for %s", currentPage, owner)
				break
			}
			log.Printf("    Processing page %d for %s (as %s), %d repos", currentPage, owner, map[bool]string{true: "org", false: "user"}[isOrg], len(repos))
			for _, repo := range repos { // Repo loop
				if repo.GetArchived() {
					continue
				}
				isFork := repo.GetFork()
				repoName := repo.GetName()
				repoOwnerLogin := repo.GetOwner().GetLogin()
				repoPath := fmt.Sprintf("%s/%s", repoOwnerLogin, repoName)
				contentOwner := repoOwnerLogin

				// Use cached GetContents wrapper
				fileContent, _, _, err_content := getCachedGetContents(ctx, client, contentOwner, repoName, "go.mod", nil)

				// Check for actual errors from the wrapper
				if err_content != nil {
					// Log only unexpected errors (wrapper now returns nil error for cached 404)
					log.Printf("        Warn: Error checking go.mod for %s: %v", repoPath, err_content)
					continue // Skip repo on actual error
				}
				// Check if content is nil (indicates file not found, either live or cached)
				if fileContent == nil {
					// log.Printf("        Info: go.mod not found in %s", repoPath) // Optional info log
					continue // Skip repo
				}

				// Proceed only if content is not nil and error is nil
				content, err_decode := fileContent.GetContent()
				if err_decode != nil {
					log.Printf("        Warn: Error decoding go.mod content for %s: %v", repoPath, err_decode)
					continue
				}
				modFile, err_parse := modfile.Parse(repoPath+"/go.mod", []byte(content), nil)
				if err_parse != nil {
					log.Printf("        Warn: Error parsing go.mod for %s: %v", repoPath, err_parse)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Printf("        Warn: Empty module path in go.mod for %s", repoPath)
					continue
				}

				allModulePaths[modulePath] = true
				originalModulePath := ""
				if isFork && repo.GetParent() != nil { // Fetch parent go.mod if fork
					parent := repo.GetParent()
					parentOwner := parent.GetOwner().GetLogin()
					parentRepo := parent.GetName()
					parentRepoPath := fmt.Sprintf("%s/%s", parentOwner, parentRepo)
					parentFileContent, _, _, err_parent_content := getCachedGetContents(ctx, client, parentOwner, parentRepo, "go.mod", nil) // Use cached version
					if err_parent_content != nil {
						log.Printf("            Warn: Error checking parent go.mod for %s: %v", parentRepoPath, err_parent_content)
					} else if parentFileContent != nil {
						parentContent, err_parent_decode := parentFileContent.GetContent()
						if err_parent_decode == nil {
							parentModFile, err_parent_parse := modfile.Parse(parentRepoPath+"/go.mod", []byte(parentContent), nil)
							if err_parent_parse == nil {
								originalModulePath = parentModFile.Module.Mod.Path
							} else {
								log.Printf("            Warn: Error parsing parent go.mod for %s: %v", parentRepoPath, err_parent_parse)
							}
						} else {
							log.Printf("            Warn: Error decoding parent go.mod content for %s: %v", parentRepoPath, err_parent_decode)
						}
					} // else: parent go.mod not found (already logged by getCachedGetContents if needed)
				}
				info := &ModuleInfo{Path: modulePath, RepoPath: repoPath, IsFork: isFork, OriginalModulePath: originalModulePath, Owner: owner, OwnerIdx: i, Deps: make(map[string]string), Fetched: true}
				modulesFoundInOwners[modulePath] = info
				for _, req := range modFile.Require {
					if !req.Indirect {
						info.Deps[req.Mod.Path] = req.Mod.Version
						allModulePaths[req.Mod.Path] = true
					}
				}
			} // End repo loop

			// Pagination
			if resp == nil || resp.NextPage == 0 {
				break
			}
			if isOrg {
				if orgOpt == nil {
					log.Printf("    Error: orgOpt is nil during pagination for org %s", owner)
					break
				}
				orgOpt.Page = resp.NextPage
				repos, resp, err = getCachedListByOrg(ctx, client, owner, orgOpt)
			} else {
				if userOpt == nil {
					log.Printf("    Error: userOpt is nil during pagination for user %s", owner)
					break
				}
				userOpt.Page = resp.NextPage
				repos, resp, err = getCachedList(ctx, client, owner, userOpt)
			}
			if err != nil {
				log.Printf("Error fetching next page for %s: %v", owner, err)
				break
			}
			currentPage++
		} // End pagination loop
	} // End loop owners
	// --- End Scan Owners ---

	// --- Determine Nodes to Include in Graph ---
	// (Logic remains the same)
	nodesToGraph := make(map[string]bool)
	referencedModules := make(map[string]bool)
	forksDependingOnNonFork := make(map[string]bool)
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && !info.IsFork {
			nodesToGraph[modPath] = true
			for depPath := range info.Deps {
				referencedModules[depPath] = true
			}
		}
	}
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			for depPath := range info.Deps {
				if nodesToGraph[depPath] {
					if depInfo, found := modulesFoundInOwners[depPath]; found && !depInfo.IsFork {
						forksDependingOnNonFork[modPath] = true
						break
					}
				}
			}
		}
	}
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			if forksDependingOnNonFork[modPath] || referencedModules[modPath] {
				nodesToGraph[modPath] = true
				for depPath := range info.Deps {
					referencedModules[depPath] = true
				}
			}
		}
	}
	if !*noExt {
		for modPath := range allModulePaths {
			_, foundInOwner := modulesFoundInOwners[modPath]
			if !foundInOwner && referencedModules[modPath] {
				nodesToGraph[modPath] = true
			}
		}
	}
	// --- End Determine Nodes to Include in Graph ---

	// --- Generate DOT Output ---
	// (Logic remains the same)
	fmt.Println("digraph dependencies {")
	fmt.Println("  rankdir=\"LR\";")
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];")
	fmt.Println("  edge [fontname=\"Helvetica\", fontsize=10];")
	fmt.Println("\n  // Node Definitions")
	sortedNodes := make([]string, 0, len(nodesToGraph))
	for nodePath := range nodesToGraph {
		sortedNodes = append(sortedNodes, nodePath)
	}
	sort.Strings(sortedNodes)
	for _, nodePath := range sortedNodes {
		label := nodePath
		color := externalColor
		info, foundInScanned := modulesFoundInOwners[nodePath]
		if foundInScanned {
			if !info.IsFork {
				ownerIdx := info.OwnerIdx
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
			} else {
				ownerIdx := info.OwnerIdx
				color = orgForkColors[ownerIdx%len(orgForkColors)]
				label = info.RepoPath
				if info.OriginalModulePath != "" && info.Path != info.OriginalModulePath {
					label = fmt.Sprintf("%s\\n(module: %s)", info.RepoPath, info.Path)
				}
			}
		} else if *noExt {
			continue
		}
		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		fmt.Printf("  \"%s\" [label=\"%s\", fillcolor=\"%s\"];\n", nodePath, escapedLabel, color)
	}
	fmt.Println("\n  // Edges (Dependencies)")
	sourceModulesInGraph := []string{}
	for modPath := range modulesFoundInOwners {
		if nodesToGraph[modPath] {
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)
	for _, sourceModPath := range sourceModulesInGraph {
		info := modulesFoundInOwners[sourceModPath]
		if info == nil {
			continue
		}
		depPaths := make([]string, 0, len(info.Deps))
		for depPath := range info.Deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)
		for _, depPath := range depPaths {
			if nodesToGraph[depPath] {
				version := info.Deps[depPath]
				escapedVersion := strings.ReplaceAll(version, "\"", "\\\"")
				fmt.Printf("  \"%s\" -> \"%s\" [label=\"%s\"];\n", sourceModPath, depPath, escapedVersion)
			}
		}
	}
	fmt.Println("}")
	// --- End Generate DOT Output ---
}
