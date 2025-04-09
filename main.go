package main

import (
	"context"
	"errors" // Ensure errors is imported
	"flag"
	"fmt"
	"net/http"
	"os"

	"fortio.org/cli" // Import fortio cli
	"fortio.org/log" // Import fortio log
	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// main is the entry point, using fortio/cli and containing the application logic
func main() {
	// Define flags locally within main
	noExtFlag := flag.Bool("noext", false, "Exclude external (non-org/user) dependencies from the graph")
	useCacheFlag := flag.Bool("use-cache", true, "Enable filesystem caching for GitHub API calls")
	clearCacheFlag := flag.Bool("clear-cache", false, "Clear the cache directory before running")
	topoSortFlag := flag.Bool("topo-sort", false, "Output dependencies in topological sort order by level (text format, disables DOT output)")
	left2RightFlag := flag.Bool("left2right", false, "Generate graph left-to-right instead of top-to-bottom (default)") // New flag

	// Configure and run fortio/cli to handle flags and args
	cli.ArgsHelp = "owner1 [owner2...]" // Set custom usage text for arguments
	cli.MinArgs = 1                     // Require at least one owner name
	cli.MaxArgs = -1                    // Allow any number of owner names
	cli.Main()                          // Parses flags, validates args, handles version/help flags

	// --- Start of application logic ---

	owners := flag.Args() // Get owners from arguments after flag parsing by cli.Main
	// Read flag values into local variables
	noExt := *noExtFlag
	useCache := *useCacheFlag     // Local variable, passed down
	topoSort := *topoSortFlag     // Read topo-sort flag
	left2Right := *left2RightFlag // Read left2Right flag

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
	ghClient := github.NewClient(httpClient)
	client := NewClientWrapper(ghClient, cacheDir, useCache)
	// --- End GitHub Client Setup ---

	modulesFoundInOwners := make(map[string]*ModuleInfo)
	// allScannedModules := []*ModuleInfo{} // Removed as not strictly needed for current logic
	allModulePaths := make(map[string]bool)

	// --- Scan Owners (Orgs or Users) ---
	for i, owner := range owners {
		log.Infof("Processing owner %d: %s", i+1, owner)
		var repos []*github.Repository
		var resp *github.Response
		var err error
		isOrg := true
		var orgOpt *github.RepositoryListByOrgOptions
		var userOpt *github.RepositoryListByUserOptions

		orgOpt = &github.RepositoryListByOrgOptions{Type: "public", ListOptions: github.ListOptions{PerPage: 100}}
		repos, resp, err = client.getCachedListByOrg(ctx, owner, orgOpt)
		if err != nil {
			var errResp *github.ErrorResponse
			if errors.As(err, &errResp) && (errResp.Response.StatusCode == http.StatusNotFound || errResp.Response.StatusCode == http.StatusForbidden) {
				log.Infof("  Owner %s not found as an organization (or access denied), trying as a user...", owner)
				isOrg = false
				userOpt = &github.RepositoryListByUserOptions{Type: "owner", ListOptions: github.ListOptions{PerPage: 100}}
				repos, resp, err = client.getCachedListByUser(ctx, owner, userOpt)
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

				fileContent, _, _, errContent := client.getCachedGetContents(ctx, contentOwner, repoName, "go.mod", nil)
				if errContent != nil {
					if !isNotFoundError(errContent) {
						log.Warnf("      Warn: Error checking go.mod for %s: %v", repoPath, errContent)
					}
					continue
				}
				if fileContent == nil {
					continue
				} // No go.mod found

				content, errDecode := fileContent.GetContent()
				if errDecode != nil {
					log.Warnf("      Warn: Error decoding go.mod content for %s: %v", repoPath, errDecode)
					continue
				}
				modFile, errParse := modfile.Parse(repoPath+"/go.mod", []byte(content), nil)
				if errParse != nil {
					log.Warnf("      Warn: Error parsing go.mod for %s: %v", repoPath, errParse)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Warnf("      Warn: Empty module path in go.mod for %s", repoPath)
					continue
				}

				allModulePaths[modulePath] = true
				for _, req := range modFile.Require {
					if !req.Indirect {
						allModulePaths[req.Mod.Path] = true
					}
				}

				// --- Fetch Parent Info for Forks ---
				originalModulePath := ""
				if isFork {
					// ... (fetching parent logic remains the same) ...
					log.LogVf("      Repo %s is a fork. Fetching full repo details...", repoPath)
					fullRepo, _, errGet := client.getCachedGetRepo(ctx, repoOwnerLogin, repoName)
					if errGet != nil {
						log.Warnf("      Warn: Failed to get full repo details for fork %s: %v", repoPath, errGet)
					} else if fullRepo != nil && fullRepo.GetParent() != nil {
						parentRepoInfo := fullRepo.GetParent()
						parentOwner := parentRepoInfo.GetOwner().GetLogin()
						parentRepoName := parentRepoInfo.GetName()
						parentRepoPath := fmt.Sprintf("%s/%s", parentOwner, parentRepoName)
						log.LogVf("      Fork parent is %s. Checking for original module path", parentRepoPath)
						parentFileContent, _, _, errParentContent := client.getCachedGetContents(ctx, parentOwner, parentRepoName, "go.mod", nil)
						if errParentContent != nil && !isNotFoundError(errParentContent) {
							log.LogVf("        Parent go.mod check error for %s: %v", parentRepoPath, errParentContent)
						} else if parentFileContent != nil {
							parentContent, errParentDecode := parentFileContent.GetContent()
							if errParentDecode == nil {
								parentModFile, errParentParse := modfile.Parse(parentRepoPath+"/go.mod", []byte(parentContent), nil)
								if errParentParse == nil {
									originalModulePath = parentModFile.Module.Mod.Path
									log.LogVf("          Found parent module path: %s", originalModulePath)
								} else {
									log.Warnf("      Warn: Error parsing parent go.mod for %s: %v", parentRepoPath, errParentParse)
								}
							} else {
								log.Warnf("      Warn: Error decoding parent go.mod content for %s: %v", parentRepoPath, errParentDecode)
							}
						} else {
							log.LogVf("        Parent go.mod not found for %s", parentRepoPath)
						}
					} else {
						log.LogVf("      Fork %s has no parent info in full details.", repoPath)
					}
				}
				// --- End Fetch Parent Info ---

				// --- Store Module Info and Handle Collisions ---
				currentInfo := &ModuleInfo{Path: modulePath, RepoPath: repoPath, IsFork: isFork, OriginalModulePath: originalModulePath, Owner: owner, OwnerIdx: i, Deps: make(map[string]string), Fetched: true /* TreatAsExternal removed */}
				for _, req := range modFile.Require {
					if !req.Indirect {
						currentInfo.Deps[req.Mod.Path] = req.Mod.Version
					}
				}

				existingInfo, exists := modulesFoundInOwners[modulePath]
				if !exists {
					log.LogVf("      Storing info for new module path '%s' from repo '%s'", modulePath, repoPath)
					modulesFoundInOwners[modulePath] = currentInfo
				} else {
					log.LogVf("      Module path '%s' collision: Existing repo '%s' (IsFork: %t), Current repo '%s' (IsFork: %t)", modulePath, existingInfo.RepoPath, existingInfo.IsFork, currentInfo.RepoPath, currentInfo.IsFork)
					if !existingInfo.IsFork && currentInfo.IsFork {
						log.LogVf("        Keeping non-fork '%s', discarding fork '%s'", existingInfo.RepoPath, currentInfo.RepoPath)
					} else if existingInfo.IsFork && !currentInfo.IsFork {
						log.LogVf("        Replacing fork '%s' with non-fork '%s'", existingInfo.RepoPath, currentInfo.RepoPath)
						modulesFoundInOwners[modulePath] = currentInfo
					} else {
						log.Warnf("        Collision between same types: Keeping first encountered ('%s').", existingInfo.RepoPath)
					}
				}
				// --- End Handle Module Path Collisions ---

			} // End repo loop

			// --- Pagination Logic ---
			if resp == nil || resp.NextPage == 0 {
				break
			}
			log.LogVf("    Fetching next page (%d) for %s", resp.NextPage, owner)
			if isOrg {
				orgOpt.Page = resp.NextPage
				repos, resp, err = client.getCachedListByOrg(ctx, owner, orgOpt)
			} else {
				if userOpt == nil {
					log.Errf("    Error: userOpt is nil during pagination for user %s", owner)
					break
				}
				userOpt.Page = resp.NextPage
				repos, resp, err = client.getCachedListByUser(ctx, owner, userOpt)
			}
			if err != nil {
				log.Errf("Error fetching next page for %s: %v", owner, err)
				break
			}
			currentPage++
			// --- End Pagination Logic ---
		} // End pagination loop
	} // End loop owners
	// --- End Scan Owners ---

	// --- Determine Nodes to Include in Graph ---
	// Use the RENAMED function determineNodesAndForkDeps
	initialNodesToGraph, forkDependsOnAnyInternal := determineNodesAndForkDeps(modulesFoundInOwners, allModulePaths) // MODIFIED
	// --- End Determine Nodes to Include in Graph ---

	// --- Generate Output ---
	if topoSort {
		// Pass the results AND the noExt flag
		performTopologicalSortAndPrint(modulesFoundInOwners, initialNodesToGraph, forkDependsOnAnyInternal, noExt) // MODIFIED: Added noExt
	} else {
		// Pass the results AND the noExt flag
		generateDotOutput(modulesFoundInOwners, initialNodesToGraph, forkDependsOnAnyInternal, noExt, left2Right) // MODIFIED: Renamed map var
	}
	// --- End Generate Output ---
}
