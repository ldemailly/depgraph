package main

import (
	"context"
	"errors" // Import errors package
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings" // Import strings package for label comparison

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

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

// Define color palettes
var orgNonForkColors = []string{"lightblue", "lightgreen", "lightsalmon", "lightgoldenrodyellow", "lightpink"}
var orgForkColors = []string{"steelblue", "darkseagreen", "coral", "darkkhaki", "mediumvioletred"}
var externalColor = "lightgrey"

// isNotFoundError checks if an error is a GitHub API 404 Not Found error
func isNotFoundError(err error) bool {
	var ge *github.ErrorResponse
	if errors.As(err, &ge) {
		return ge.Response.StatusCode == http.StatusNotFound
	}
	return false
}

// Fetches and parses go.mod files for repos in specified GitHub orgs or user accounts.
// Outputs the dependency graph in DOT format.
func main() {
	// --- Command Line Flags ---
	noExt := flag.Bool("noext", false, "Exclude external (non-org/user) dependencies from the graph")
	flag.Parse() // Parse flags first

	// Get owners (orgs or users) from remaining arguments
	owners := flag.Args()
	if len(owners) < 1 {
		log.Fatalf("Usage: %s [-noext] <owner1> [owner2]...", os.Args[0])
	}
	// Create a map for quick owner index lookup
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
	// IMPORTANT: If multiple repos declare the same module path, this map stores the *last one encountered*.
	modulesFoundInOwners := make(map[string]*ModuleInfo)
	// Keep track of all unique module paths encountered (sources and dependencies)
	allModulePaths := make(map[string]bool)

	// --- Scan Owners (Orgs or Users) ---
	// (Scanning logic remains the same as previous version)
	for i, owner := range owners {
		log.Printf("Processing owner %d: %s\n", i+1, owner)
		var repos []*github.Repository
		var resp *github.Response
		var err error
		isOrg := true
		var orgOpt *github.RepositoryListByOrgOptions
		var userOpt *github.RepositoryListOptions
		orgOpt = &github.RepositoryListByOrgOptions{Type: "public", ListOptions: github.ListOptions{PerPage: 100}}
		repos, resp, err = client.Repositories.ListByOrg(ctx, owner, orgOpt)
		if err != nil {
			if isNotFoundError(err) {
				log.Printf("    Owner %s not found as an organization, trying as a user...", owner)
				isOrg = false
				userOpt = &github.RepositoryListOptions{Type: "owner", Visibility: "public", ListOptions: github.ListOptions{PerPage: 100}}
				repos, resp, err = client.Repositories.List(ctx, owner, userOpt)
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
				fileContent, _, _, err_content := client.Repositories.GetContents(ctx, contentOwner, repoName, "go.mod", nil)
				if err_content != nil {
					if !isNotFoundError(err_content) {
						log.Printf("        Warn: Error getting go.mod for %s: %v", repoPath, err_content)
					}
					continue
				}
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
				log.Printf("        Found module: %s (fork: %v) (from repo %s)\n", modulePath, isFork, repoPath)
				allModulePaths[modulePath] = true
				originalModulePath := ""
				if isFork && repo.GetParent() != nil { // Fetch parent go.mod if fork
					parent := repo.GetParent()
					parentOwner := parent.GetOwner().GetLogin()
					parentRepo := parent.GetName()
					parentRepoPath := fmt.Sprintf("%s/%s", parentOwner, parentRepo)
					log.Printf("        Fork detected. Fetching original go.mod from parent: %s (API call)", parentRepoPath)
					parentFileContent, _, _, err_parent_content := client.Repositories.GetContents(ctx, parentOwner, parentRepo, "go.mod", nil)
					if err_parent_content == nil {
						parentContent, err_parent_decode := parentFileContent.GetContent()
						if err_parent_decode == nil {
							parentModFile, err_parent_parse := modfile.Parse(parentRepoPath+"/go.mod", []byte(parentContent), nil)
							if err_parent_parse == nil {
								originalModulePath = parentModFile.Module.Mod.Path
								log.Printf("            Original module path: %s", originalModulePath)
							} else {
								log.Printf("            Warn: Error parsing parent go.mod for %s: %v", parentRepoPath, err_parent_parse)
							}
						} else {
							log.Printf("            Warn: Error decoding parent go.mod content for %s: %v", parentRepoPath, err_parent_decode)
						}
					} else if !isNotFoundError(err_parent_content) {
						log.Printf("            Warn: Error getting go.mod for parent repo %s: %v", parentRepoPath, err_parent_content)
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
			log.Printf("    Fetching next page (%d) for %s", resp.NextPage, owner)
			if isOrg {
				if orgOpt == nil {
					log.Printf("    Error: orgOpt is nil during pagination for org %s", owner)
					break
				}
				orgOpt.Page = resp.NextPage
				repos, resp, err = client.Repositories.ListByOrg(ctx, owner, orgOpt)
			} else {
				if userOpt == nil {
					log.Printf("    Error: userOpt is nil during pagination for user %s", owner)
					break
				}
				userOpt.Page = resp.NextPage
				repos, resp, err = client.Repositories.List(ctx, owner, userOpt)
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
	nodesToGraph := make(map[string]bool)
	referencedModules := make(map[string]bool)       // Modules depended on by included nodes (non-forks or included forks)
	forksDependingOnNonFork := make(map[string]bool) // Forks (by module path) that depend on an included non-fork

	// Pass 1: Add non-forks and collect their initial dependencies
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && !info.IsFork {
			nodesToGraph[modPath] = true
			for depPath := range info.Deps {
				referencedModules[depPath] = true
			}
		}
	}
	// Pass 2: Identify forks that depend on *included* non-forks
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			for depPath := range info.Deps {
				if nodesToGraph[depPath] { // Check if the dep is an included non-fork
					if depInfo, found := modulesFoundInOwners[depPath]; found && !depInfo.IsFork {
						forksDependingOnNonFork[modPath] = true // Mark the fork module path
						break
					}
				}
			}
		}
	}
	// Pass 3: Add forks if they depend on non-forks OR if their module path is referenced by a non-fork initially
	// Collect dependencies from these included forks as well.
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			// Include fork if it depends on a non-fork OR if a non-fork depends on its module path
			if forksDependingOnNonFork[modPath] || referencedModules[modPath] {
				nodesToGraph[modPath] = true
				// Add dependencies of included forks to referenced set for external inclusion check
				for depPath := range info.Deps {
					referencedModules[depPath] = true
				}
			}
		}
	}
	// Pass 4: Add external dependencies if needed
	if !*noExt {
		for modPath := range allModulePaths {
			_, foundInOwner := modulesFoundInOwners[modPath]
			// Add if external and referenced by an included node (non-fork or included fork)
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

	// Define nodes with appropriate colors and labels
	fmt.Println("\n  // Node Definitions")
	sortedNodes := make([]string, 0, len(nodesToGraph))
	for nodePath := range nodesToGraph {
		sortedNodes = append(sortedNodes, nodePath)
	}
	sort.Strings(sortedNodes)

	for _, nodePath := range sortedNodes {
		label := nodePath      // Default label is the module path
		color := externalColor // Default to external

		// Get info about the last scanned repo declaring this module path
		info, foundInScanned := modulesFoundInOwners[nodePath]

		if foundInScanned {
			// This module path was declared by at least one repo in the scanned owners.
			if !info.IsFork {
				// It's a non-fork. Style as internal non-fork.
				ownerIdx := info.OwnerIdx
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
				// Label remains module path
			} else {
				// It's a fork. Style as internal fork (color and label).
				// The inclusion logic in "Determine Nodes" already decided if this fork node should be in the graph.
				// If it's in nodesToGraph, style it as a fork regardless of the reason for inclusion.
				ownerIdx := info.OwnerIdx
				color = orgForkColors[ownerIdx%len(orgForkColors)]
				// --- Fork Labeling Logic ---
				label = info.RepoPath // Primary label is repo path for qualified forks
				if info.OriginalModulePath != "" && info.Path != info.OriginalModulePath {
					label = fmt.Sprintf("%s\\n(module: %s)", info.RepoPath, info.Path)
				}
				// --- End Fork Labeling Logic ---
			}
		} else if *noExt {
			// External node, and flag is set to exclude them. Skip definition.
			continue
		}
		// Else: External node, color is externalColor, label is nodePath.

		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		fmt.Printf("  \"%s\" [label=\"%s\", fillcolor=\"%s\"];\n", nodePath, escapedLabel, color)
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Get sorted list of source module paths *that are included in the graph*
	sourceModulesInGraph := []string{}
	for modPath := range modulesFoundInOwners {
		// Only draw edges FROM nodes that are included in the graph
		if nodesToGraph[modPath] {
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

	// Print edges
	for _, sourceModPath := range sourceModulesInGraph {
		// Use the info for the source module path
		info := modulesFoundInOwners[sourceModPath]
		if info == nil {
			continue
		} // Safety check

		depPaths := make([]string, 0, len(info.Deps))
		for depPath := range info.Deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)

		for _, depPath := range depPaths {
			if nodesToGraph[depPath] { // Only draw edge if target is included
				version := info.Deps[depPath]
				escapedVersion := strings.ReplaceAll(version, "\"", "\\\"")
				fmt.Printf("  \"%s\" -> \"%s\" [label=\"%s\"];\n", sourceModPath, depPath, escapedVersion)
			}
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}
