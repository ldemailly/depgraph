package main

import (
	"context"
	"crypto/sha1" // For cache key hashing
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"

	// Standard log replaced by fortio.org/log
	"net/http" // For escaping cache key parts
	"os"
	"path/filepath" // For cache path manipulation
	"sort"
	"strconv" // For page number conversion
	"strings"

	"fortio.org/cli" // Import fortio cli
	"fortio.org/log" // Import fortio log
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
type CachedContentResponse struct {
	Found       bool
	FileContent *github.RepositoryContent
}

// --- End Caching Data Structures ---

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
// (initCache, clearCache, getCacheKey, readCache, writeCache remain the same)
func initCache() (string, error) {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user cache directory: %w", err)
	}
	cacheDir := filepath.Join(userCacheDir, "depgraph_cache")
	log.LogVf("Using cache directory: %s", cacheDir)
	return cacheDir, os.MkdirAll(cacheDir, 0755)
}
func clearCache(cacheDir string) error {
	if cacheDir == "" {
		return errors.New("cache directory not initialized")
	}
	log.Infof("Clearing cache directory: %s", cacheDir)
	return os.RemoveAll(cacheDir)
}
func getCacheKey(cacheDir string, parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		io.WriteString(h, p)
		io.WriteString(h, "|")
	}
	hash := fmt.Sprintf("%x", h.Sum(nil))
	return filepath.Join(cacheDir, hash+".json")
}
func readCache(key string, target interface{}, useCache bool) (bool, error) {
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
		log.Warnf("Error unmarshaling cache file %s, ignoring cache: %v", key, err)
		return false, nil
	}
	if contentCache, ok := target.(*CachedContentResponse); ok {
		log.LogVf("Cache read successful for %s - Cached Found status: %v", key, contentCache.Found)
	}
	return true, nil
}
func writeCache(key string, data interface{}, useCache bool) error {
	if !useCache {
		return nil
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Errf("Error marshaling data for cache key %s: %v", key, err)
		return fmt.Errorf("failed to marshal data for cache key %s: %w", key, err)
	}
	err = os.WriteFile(key, jsonData, 0644)
	if err != nil {
		log.Errf("Error writing cache file %s: %v", key, err)
		return fmt.Errorf("failed to write cache file %s: %w", key, err)
	}
	log.LogVf("Cache write: %s", key)
	return nil
}

// --- End Cache Handling Functions ---

// --- Cached GitHub API Wrappers ---

func getCachedListByOrg(ctx context.Context, client *github.Client, owner string, opt *github.RepositoryListByOrgOptions, cacheDir string, useCache bool) ([]*github.Repository, *github.Response, error) {
	keyParts := []string{"ListByOrg", owner, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(cacheDir, keyParts...)
	var cachedData CachedListResponse
	hit, readErr := readCache(cacheKey, &cachedData, useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.LogVf("Cache hit for ListByOrg owner=%s page=%d", owner, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}
	log.Infof("Cache miss for ListByOrg owner=%s page=%d, calling API", owner, opt.Page)
	repos, resp, apiErr := client.Repositories.ListByOrg(ctx, owner, opt)
	if apiErr != nil {
		return nil, resp, apiErr
	}
	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache, useCache)
	if writeErr != nil {
		log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
	}
	return repos, resp, nil
}

// Updated getCachedList to use ListByUser and RepositoryListByUserOptions
func getCachedListByUser(ctx context.Context, client *github.Client, user string, opt *github.RepositoryListByUserOptions, cacheDir string, useCache bool) ([]*github.Repository, *github.Response, error) {
	// Update cache key parts based on ListByUserOptions fields used
	keyParts := []string{"ListByUser", user, opt.Type, strconv.Itoa(opt.Page)}
	cacheKey := getCacheKey(cacheDir, keyParts...)
	var cachedData CachedListResponse

	hit, readErr := readCache(cacheKey, &cachedData, useCache)
	if readErr != nil {
		log.Errf("Error reading cache for %v: %v", keyParts, readErr)
	}
	if hit {
		log.LogVf("Cache hit for ListByUser user=%s type=%s page=%d", user, opt.Type, opt.Page)
		resp := &github.Response{NextPage: cachedData.NextPage}
		return cachedData.Repos, resp, nil
	}

	log.Infof("Cache miss for ListByUser user=%s type=%s page=%d, calling API", user, opt.Type, opt.Page)
	// Use the non-deprecated ListByUser method
	repos, resp, apiErr := client.Repositories.ListByUser(ctx, user, opt)
	if apiErr != nil {
		return nil, resp, apiErr
	}

	dataToCache := CachedListResponse{Repos: repos, NextPage: resp.NextPage}
	writeErr := writeCache(cacheKey, dataToCache, useCache)
	if writeErr != nil {
		log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
	}

	return repos, resp, nil
}

func getCachedGetContents(ctx context.Context, client *github.Client, owner, repo, path string, opt *github.RepositoryContentGetOptions, cacheDir string, useCache bool) (*github.RepositoryContent, []*github.RepositoryContent, *github.Response, error) {
	ref := ""
	if opt != nil {
		ref = opt.Ref
	}
	keyParts := []string{"GetContents", owner, repo, path, ref}
	cacheKey := getCacheKey(cacheDir, keyParts...)
	var cachedData CachedContentResponse
	hit, readErr := readCache(cacheKey, &cachedData, useCache)
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
	fileContent, dirContent, resp, apiErr := client.Repositories.GetContents(ctx, owner, repo, path, opt)

	if apiErr != nil {
		if isNotFoundError(apiErr) {
			log.LogVf("API reported Not Found for GetContents repo=%s/%s path=%s ref=%s. Caching result.", owner, repo, path, ref)
			dataToCache := CachedContentResponse{Found: false}
			writeErr := writeCache(cacheKey, dataToCache, useCache)
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
		writeErr := writeCache(cacheKey, dataToCache, useCache)
		if writeErr != nil {
			log.Errf("Error writing cache for %v: %v", keyParts, writeErr)
		}
	} else {
		log.LogVf("Skipping cache write for directory listing: %v", keyParts)
	}

	return fileContent, dirContent, resp, nil
}

// --- End Cached GitHub API Wrappers ---

// main is the entry point, using fortio/cli and containing the application logic
func main() {
	// Define flags locally within main
	noExtFlag := flag.Bool("noext", false, "Exclude external (non-org/user) dependencies from the graph")
	useCacheFlag := flag.Bool("use-cache", true, "Enable filesystem caching for GitHub API calls")
	clearCacheFlag := flag.Bool("clear-cache", false, "Clear the cache directory before running")

	// Configure and run fortio/cli to handle flags and args
	cli.ArgsHelp = "owner1 [owner2...]" // Set custom usage text for arguments
	cli.MinArgs = 1                     // Require at least one owner name
	cli.MaxArgs = -1                    // Allow any number of owner names
	cli.Main()                          // Parses flags, validates args, handles version/help flags

	// --- Start of logic previously in run() ---

	owners := flag.Args() // Get owners from arguments after flag parsing by cli.Main
	// Read flag values into local variables
	noExt := *noExtFlag
	useCache := *useCacheFlag // Local variable, passed down

	// Initialize or clear cache
	cacheDir, err := initCache()
	if err != nil {
		log.Fatalf("Failed to initialize cache: %v", err)
	}
	if *clearCacheFlag {
		if err := clearCache(cacheDir); err != nil {
			log.Fatalf("Failed to clear cache: %v", err)
		}
		cacheDir, err = initCache()
		if err != nil {
			log.Fatalf("Failed to re-initialize cache after clearing: %v", err)
		}
	}

	// Create a map for quick owner index lookup
	ownerIndexMap := make(map[string]int)
	for i, owner := range owners {
		ownerIndexMap[owner] = i
	}

	// --- GitHub Client Setup ---
	token := os.Getenv("GITHUB_TOKEN")
	ctx := context.Background()
	var httpClient *http.Client = nil
	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		httpClient = oauth2.NewClient(ctx, ts)
	} else {
		httpClient = http.DefaultClient
		log.Warnf("GITHUB_TOKEN environment variable not set. Using unauthenticated access (may hit rate limits).")
	}
	client := github.NewClient(httpClient)
	// --- End GitHub Client Setup ---

	// Store module info: map[modulePath]ModuleInfo
	modulesFoundInOwners := make(map[string]*ModuleInfo)
	// Keep track of all unique module paths encountered (sources and dependencies)
	allModulePaths := make(map[string]bool)

	// --- Scan Owners (Orgs or Users) ---
	for i, owner := range owners {
		log.Infof("Processing owner %d: %s", i+1, owner)
		var repos []*github.Repository
		var resp *github.Response
		var err error
		isOrg := true
		var orgOpt *github.RepositoryListByOrgOptions
		var userOpt *github.RepositoryListByUserOptions // Use correct options type

		orgOpt = &github.RepositoryListByOrgOptions{Type: "public", ListOptions: github.ListOptions{PerPage: 100}}
		repos, resp, err = getCachedListByOrg(ctx, client, owner, orgOpt, cacheDir, useCache)
		if err != nil {
			if isNotFoundError(err) {
				log.Infof("    Owner %s not found as an organization, trying as a user...", owner)
				isOrg = false
				// Initialize with correct options type
				userOpt = &github.RepositoryListByUserOptions{Type: "owner", ListOptions: github.ListOptions{PerPage: 100}}
				// Call the updated wrapper function
				repos, resp, err = getCachedListByUser(ctx, client, owner, userOpt, cacheDir, useCache)
			}
			if err != nil {
				log.Errf("Error listing repositories for %s: %v", owner, err)
				continue
			}
		}
		currentPage := 1
		for { // Pagination loop
			if repos == nil {
				log.Warnf("    No repositories found or error occurred for page %d for %s", currentPage, owner)
				break
			}
			log.Infof("    Processing page %d for %s (as %s), %d repos", currentPage, owner, map[bool]string{true: "org", false: "user"}[isOrg], len(repos))
			for _, repo := range repos { // Repo loop
				if repo.GetArchived() {
					continue
				}
				isFork := repo.GetFork()
				repoName := repo.GetName()
				repoOwnerLogin := repo.GetOwner().GetLogin()
				repoPath := fmt.Sprintf("%s/%s", repoOwnerLogin, repoName)
				contentOwner := repoOwnerLogin

				fileContent, _, _, err_content := getCachedGetContents(ctx, client, contentOwner, repoName, "go.mod", nil, cacheDir, useCache)

				if err_content != nil {
					log.Warnf("        Warn: Error checking go.mod for %s: %v", repoPath, err_content)
					continue
				}
				if fileContent == nil {
					continue
				} // Skip repo if go.mod not found

				content, err_decode := fileContent.GetContent()
				if err_decode != nil {
					log.Warnf("        Warn: Error decoding go.mod content for %s: %v", repoPath, err_decode)
					continue
				}
				modFile, err_parse := modfile.Parse(repoPath+"/go.mod", []byte(content), nil)
				if err_parse != nil {
					log.Warnf("        Warn: Error parsing go.mod for %s: %v", repoPath, err_parse)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Warnf("        Warn: Empty module path in go.mod for %s", repoPath)
					continue
				}

				allModulePaths[modulePath] = true
				originalModulePath := ""
				if isFork && repo.GetParent() != nil {
					parent := repo.GetParent()
					parentOwner := parent.GetOwner().GetLogin()
					parentRepo := parent.GetName()
					parentRepoPath := fmt.Sprintf("%s/%s", parentOwner, parentRepo)
					parentFileContent, _, _, err_parent_content := getCachedGetContents(ctx, client, parentOwner, parentRepo, "go.mod", nil, cacheDir, useCache)
					if err_parent_content != nil {
						log.Warnf("            Warn: Error checking parent go.mod for %s: %v", parentRepoPath, err_parent_content)
					} else if parentFileContent != nil {
						parentContent, err_parent_decode := parentFileContent.GetContent()
						if err_parent_decode == nil {
							parentModFile, err_parent_parse := modfile.Parse(parentRepoPath+"/go.mod", []byte(parentContent), nil)
							if err_parent_parse == nil {
								originalModulePath = parentModFile.Module.Mod.Path
							} else {
								log.Warnf("            Warn: Error parsing parent go.mod for %s: %v", parentRepoPath, err_parent_parse)
							}
						} else {
							log.Warnf("            Warn: Error decoding parent go.mod content for %s: %v", parentRepoPath, err_parent_decode)
						}
					}
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

			if resp == nil || resp.NextPage == 0 {
				break
			}
			log.LogVf("    Fetching next page (%d) for %s", resp.NextPage, owner)
			if isOrg {
				// Removed redundant nil check for orgOpt here
				orgOpt.Page = resp.NextPage
				repos, resp, err = getCachedListByOrg(ctx, client, owner, orgOpt, cacheDir, useCache)
			} else {
				if userOpt == nil {
					log.Errf("    Error: userOpt is nil during pagination for user %s", owner)
					break
				} // Keep nil check for userOpt as it's conditionally initialized
				userOpt.Page = resp.NextPage
				// Call the updated wrapper function
				repos, resp, err = getCachedListByUser(ctx, client, owner, userOpt, cacheDir, useCache)
			}
			if err != nil {
				log.Errf("Error fetching next page for %s: %v", owner, err)
				break
			}
			currentPage++
		} // End pagination loop
	} // End loop owners
	// --- End Scan Owners ---

	// --- Determine Nodes to Include in Graph ---
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
	if !noExt {
		for modPath := range allModulePaths {
			_, foundInOwner := modulesFoundInOwners[modPath]
			if !foundInOwner && referencedModules[modPath] {
				nodesToGraph[modPath] = true
			}
		}
	}
	// --- End Determine Nodes to Include in Graph ---

	// --- Generate DOT Output ---
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
		} else if noExt {
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
