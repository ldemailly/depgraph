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

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// ModuleInfo stores details about modules found in the scanned owners (orgs or users)
type ModuleInfo struct {
	Path     string
	IsFork   bool
	Owner    string            // Owner (org or user) where the module definition was found
	OwnerIdx int               // Index of the owner in the input list (for coloring)
	Deps     map[string]string // path -> version
	Fetched  bool              // Indicates if the go.mod was successfully fetched and parsed
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
	modulesFoundInOwners := make(map[string]*ModuleInfo)
	// Keep track of all unique module paths encountered (sources and dependencies)
	allModulePaths := make(map[string]bool)

	// --- Scan Owners (Orgs or Users) ---
	for i, owner := range owners {
		log.Printf("Processing owner %d: %s\n", i+1, owner)

		var repos []*github.Repository
		var resp *github.Response
		var err error
		isOrg := true // Assume org first
		// Declare options structs here to ensure they are in scope for pagination
		var orgOpt *github.RepositoryListByOrgOptions
		var userOpt *github.RepositoryListOptions

		// Try listing as an Organization
		orgOpt = &github.RepositoryListByOrgOptions{
			Type:        "public",
			ListOptions: github.ListOptions{PerPage: 100},
		}
		repos, resp, err = client.Repositories.ListByOrg(ctx, owner, orgOpt)

		if err != nil {
			if isNotFoundError(err) {
				log.Printf("    Owner %s not found as an organization, trying as a user...", owner)
				isOrg = false // It's not an org (or not accessible as one)
				// Try listing as a User
				// Initialize userOpt here (it's already declared above)
				userOpt = &github.RepositoryListOptions{
					Type:        "owner", // Repos owned by the user
					Visibility:  "public",
					ListOptions: github.ListOptions{PerPage: 100},
				}
				repos, resp, err = client.Repositories.List(ctx, owner, userOpt) // Use List for users
			}

			// If there's still an error after trying both (or it wasn't a 404)
			if err != nil {
				log.Printf("Error listing repositories for %s: %v", owner, err)
				continue // Skip this owner
			}
		}

		// --- Paginate and Process Repos ---
		currentPage := 1
		for {
			// Check if repos slice is nil (can happen if initial List call failed but didn't error out completely)
			if repos == nil {
				log.Printf("    No repositories found or error occurred for page %d for %s", currentPage, owner)
				break
			}
			log.Printf("    Processing page %d for %s (as %s), %d repos", currentPage, owner, map[bool]string{true: "org", false: "user"}[isOrg], len(repos))

			for _, repo := range repos {
				// Process both forks and non-forks, but skip archived
				if repo.GetArchived() {
					continue
				}

				isFork := repo.GetFork()
				repoName := repo.GetName()
				// Use repo.Owner.GetLogin() which works for both org and user repos
				repoOwnerLogin := repo.GetOwner().GetLogin()
				// Need owner/repo for GetContents
				// Use the actual owner login from the repo object for GetContents path, more reliable
				contentOwner := repoOwnerLogin

				// --- Get and Parse go.mod ---
				fileContent, _, _, err_content := client.Repositories.GetContents(ctx, contentOwner, repoName, "go.mod", nil)
				if err_content != nil {
					if !isNotFoundError(err_content) { // Log only unexpected errors
						log.Printf("        Warn: Error getting go.mod for %s/%s: %v", contentOwner, repoName, err_content)
					}
					continue // Skip repo if go.mod fetch fails
				}
				content, err_decode := fileContent.GetContent()
				if err_decode != nil {
					log.Printf("        Warn: Error decoding go.mod content for %s/%s: %v", contentOwner, repoName, err_decode)
					continue
				}
				modFile, err_parse := modfile.Parse(fmt.Sprintf("%s/%s/go.mod", contentOwner, repoName), []byte(content), nil)
				if err_parse != nil {
					log.Printf("        Warn: Error parsing go.mod for %s/%s: %v", contentOwner, repoName, err_parse)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Printf("        Warn: Empty module path in go.mod for %s/%s", contentOwner, repoName)
					continue
				}
				// --- End Get and Parse go.mod ---

				log.Printf("        Found module: %s (fork: %v) (from repo %s/%s)\n", modulePath, isFork, repoOwnerLogin, repoName)
				allModulePaths[modulePath] = true // Track this module path

				// Store or update module info
				info := &ModuleInfo{
					Path:     modulePath,
					IsFork:   isFork,
					Owner:    owner, // Use the original input owner name for grouping/coloring
					OwnerIdx: i,
					Deps:     make(map[string]string),
					Fetched:  true,
				}
				modulesFoundInOwners[modulePath] = info

				// Store direct dependencies
				for _, req := range modFile.Require {
					if !req.Indirect {
						depPath := req.Mod.Path
						depVersion := req.Mod.Version
						info.Deps[depPath] = depVersion
						allModulePaths[depPath] = true // Track the dependency module path as well
					}
				}
			} // End processing repos on current page

			// --- Pagination ---
			if resp == nil || resp.NextPage == 0 {
				break // No more pages
			}
			log.Printf("    Fetching next page (%d) for %s", resp.NextPage, owner)
			// Re-fetch based on whether it's org or user
			if isOrg {
				// Ensure orgOpt is not nil (shouldn't be if isOrg is true)
				if orgOpt == nil {
					log.Printf("    Error: orgOpt is nil during pagination for org %s", owner)
					break
				}
				orgOpt.Page = resp.NextPage
				repos, resp, err = client.Repositories.ListByOrg(ctx, owner, orgOpt)
			} else {
				// Ensure userOpt is not nil (it's initialized if isOrg became false)
				if userOpt == nil {
					log.Printf("    Error: userOpt is nil during pagination for user %s", owner)
					break
				}
				userOpt.Page = resp.NextPage
				repos, resp, err = client.Repositories.List(ctx, owner, userOpt)
			}

			if err != nil {
				log.Printf("Error fetching next page for %s: %v", owner, err)
				break // Stop pagination on error
			}
			currentPage++
			// --- End Pagination ---

		} // End pagination loop
		// --- End Paginate and Process Repos ---

	} // End loop through owners
	// --- End Scan Owners ---

	// --- Determine Nodes to Include in Graph ---
	// (This section remains the same)
	nodesToGraph := make(map[string]bool)        // Set of module paths to include
	referencedByNonFork := make(map[string]bool) // Set of modules depended on by non-forks

	// First pass: identify dependencies of non-forks found in owners
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && !info.IsFork {
			nodesToGraph[modPath] = true // Always include non-forks found in owners
			for depPath := range info.Deps {
				referencedByNonFork[depPath] = true
			}
		}
	}

	// Second pass: add forks found in owners *if* they are referenced by a non-fork
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			if referencedByNonFork[modPath] {
				nodesToGraph[modPath] = true
				// Also mark dependencies of included forks as referenced
				for depPath := range info.Deps {
					referencedByNonFork[depPath] = true // Mark deps of included forks
				}
			}
		}
	}

	// Third pass: add external dependencies if needed and not excluded
	if !*noExt {
		for modPath := range allModulePaths {
			// Check if it's NOT one of the modules we found in owners (i.e., external)
			// AND if it's referenced by a non-fork (or an included fork).
			_, foundInOwner := modulesFoundInOwners[modPath]
			if !foundInOwner && referencedByNonFork[modPath] {
				nodesToGraph[modPath] = true
			}
		}
	}
	// --- End Determine Nodes to Include in Graph ---

	// --- Generate DOT Output ---
	// (This section remains the same)
	fmt.Println("digraph dependencies {")
	fmt.Println("  rankdir=\"LR\";")
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];")
	fmt.Println("  edge [fontname=\"Helvetica\", fontsize=10];")

	// Define nodes with appropriate colors
	fmt.Println("\n  // Node Definitions")
	sortedNodes := make([]string, 0, len(nodesToGraph))
	for nodePath := range nodesToGraph {
		sortedNodes = append(sortedNodes, nodePath)
	}
	sort.Strings(sortedNodes)

	nodeColors := make(map[string]string) // Store calculated color for each node

	for _, nodePath := range sortedNodes {
		color := externalColor // Default to external
		if info, found := modulesFoundInOwners[nodePath]; found {
			// It's an internal module (fork or non-fork) from one of the owners
			ownerIdx := info.OwnerIdx
			if info.IsFork {
				color = orgForkColors[ownerIdx%len(orgForkColors)] // Cycle through fork colors
			} else {
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)] // Cycle through non-fork colors
			}
		} else if *noExt {
			// Skip external node definition if -noext is set
			continue
		}
		nodeColors[nodePath] = color
		fmt.Printf("  \"%s\" [fillcolor=\"%s\"];\n", nodePath, color)
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Get sorted list of source module paths *that are included in the graph*
	sourceModulesInGraph := []string{}
	for modPath := range modulesFoundInOwners {
		if nodesToGraph[modPath] { // Only consider sources that are actually in the graph
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

	// Print edges
	for _, sourceModPath := range sourceModulesInGraph {
		info := modulesFoundInOwners[sourceModPath] // We know it exists
		// Get sorted list of dependency paths
		depPaths := make([]string, 0, len(info.Deps))
		for depPath := range info.Deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)

		for _, depPath := range depPaths {
			// Only draw edge if the target node is also included in the graph
			if nodesToGraph[depPath] {
				version := info.Deps[depPath]
				fmt.Printf("  \"%s\" -> \"%s\" [label=\"%s\"];\n", sourceModPath, depPath, version)
			}
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}
