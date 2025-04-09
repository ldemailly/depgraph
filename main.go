package main

import (
	"context"
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
	left2Right := *left2RightFlag // Read left2right flag

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
	// Create client wrapper
	client := NewClientWrapper(ghClient, cacheDir, useCache)
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
		// Use client wrapper methods
		repos, resp, err = client.getCachedListByOrg(ctx, owner, orgOpt)
		if err != nil {
			if isNotFoundError(err) {
				log.Infof("    Owner %s not found as an organization, trying as a user...", owner)
				isOrg = false
				userOpt = &github.RepositoryListByUserOptions{Type: "owner", ListOptions: github.ListOptions{PerPage: 100}}
				repos, resp, err = client.getCachedListByUser(ctx, owner, userOpt) // Use client wrapper method
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

				// Use client wrapper method
				fileContent, _, _, errContent := client.getCachedGetContents(ctx, contentOwner, repoName, "go.mod", nil)

				if errContent != nil {
					log.Warnf("        Warn: Error checking go.mod for %s: %v", repoPath, errContent)
					continue
				}
				if fileContent == nil {
					continue
				} // Skip repo if go.mod not found

				content, errDecode := fileContent.GetContent()
				if errDecode != nil {
					log.Warnf("        Warn: Error decoding go.mod content for %s: %v", repoPath, errDecode)
					continue
				}
				modFile, errParse := modfile.Parse(repoPath+"/go.mod", []byte(content), nil)
				if errParse != nil {
					log.Warnf("        Warn: Error parsing go.mod for %s: %v", repoPath, errParse)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Warnf("        Warn: Empty module path in go.mod for %s", repoPath)
					continue
				}

				allModulePaths[modulePath] = true
				originalModulePath := ""
				// --- Fetch Parent Info for Forks ---
				var parentRepoInfo *github.Repository // To store parent info if fetched
				if isFork {
					log.LogVf("        Repo %s is a fork. Fetching full repo details...", repoPath)
					fullRepo, _, errGet := client.getCachedGetRepo(ctx, repoOwnerLogin, repoName) // Fetch full details
					if errGet != nil {
						log.Warnf("        Warn: Failed to get full repo details for fork %s: %v", repoPath, errGet)
					} else if fullRepo != nil && fullRepo.GetParent() != nil { // Check parent from full details
						parentRepoInfo = fullRepo.GetParent() // Store parent info
						parentOwner := parentRepoInfo.GetOwner().GetLogin()
						parentRepoName := parentRepoInfo.GetName()
						parentRepoPath := fmt.Sprintf("%s/%s", parentOwner, parentRepoName)
						log.LogVf("        Fork parent is %s. Checking for original module path", parentRepoPath)

						parentFileContent, _, _, errParentContent := client.getCachedGetContents(ctx, parentOwner, parentRepoName, "go.mod", nil)
						if errParentContent != nil {
							log.LogVf("            Parent go.mod check error for %s: %v", parentRepoPath, errParentContent)
						} else if parentFileContent != nil {
							parentContent, errParentDecode := parentFileContent.GetContent()
							if errParentDecode == nil {
								parentModFile, errParentParse := modfile.Parse(parentRepoPath+"/go.mod", []byte(parentContent), nil)
								if errParentParse == nil {
									originalModulePath = parentModFile.Module.Mod.Path
									log.LogVf("            Found parent module path: %s", originalModulePath)
								} else {
									log.Warnf("            Warn: Error parsing parent go.mod for %s: %v", parentRepoPath, errParentParse)
								}
							} else {
								log.Warnf("            Warn: Error decoding parent go.mod content for %s: %v", parentRepoPath, errParentDecode)
							}
						} else {
							log.LogVf("            Parent go.mod not found for %s", parentRepoPath)
						}
					} else {
						log.LogVf("        Fork %s has no parent info in full details.", repoPath)
					}
				}
				// --- End Fetch Parent Info ---

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
		} // End pagination loop
	} // End loop owners
	// --- End Scan Owners ---

	// --- Determine Nodes to Include in Graph ---
	nodesToGraph := determineNodesToGraph(modulesFoundInOwners, allModulePaths, noExt)
	// --- End Determine Nodes to Include in Graph ---

	// --- Generate Output ---
	if topoSort {
		performTopologicalSortAndPrint(modulesFoundInOwners, nodesToGraph)
	} else {
		// Pass left2Right flag to DOT generation
		generateDotOutput(modulesFoundInOwners, nodesToGraph, noExt, left2Right)
	}
	// --- End Generate Output ---
}
