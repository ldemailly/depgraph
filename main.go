package main

import (
	"context"
	"flag" // Import flag package
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// ModuleInfo stores details about modules found in the scanned orgs
type ModuleInfo struct {
	Path    string
	IsFork  bool
	Org     string            // Org where the module definition was found
	OrgIdx  int               // Index of the org in the input list (for coloring)
	Deps    map[string]string // path -> version
	Fetched bool              // Indicates if the go.mod was successfully fetched and parsed
}

// Define color palettes
// Ensure these have enough colors or handle cycling gracefully
var orgNonForkColors = []string{"lightblue", "lightgreen", "lightsalmon", "lightgoldenrodyellow", "lightpink"}
var orgForkColors = []string{"steelblue", "darkseagreen", "coral", "darkkhaki", "mediumvioletred"}
var externalColor = "lightgrey"

// Fetches and parses go.mod files for repos in specified GitHub orgs.
// Outputs the dependency graph in DOT format.
func main() {
	// --- Command Line Flags ---
	noExt := flag.Bool("noext", false, "Exclude external (non-org) dependencies from the graph")
	flag.Parse() // Parse flags first

	// Get orgs from remaining arguments
	orgs := flag.Args()
	if len(orgs) < 1 {
		log.Fatalf("Usage: %s [-noext] <org1> [org2]...", os.Args[0])
	}
	// Create a map for quick org index lookup
	orgIndexMap := make(map[string]int)
	for i, org := range orgs {
		orgIndexMap[org] = i
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
	// Includes both forks and non-forks found in the target orgs.
	modulesFoundInOrgs := make(map[string]*ModuleInfo)
	// Keep track of all unique module paths encountered (sources and dependencies)
	allModulePaths := make(map[string]bool)

	// --- Scan Organizations ---
	for i, org := range orgs {
		log.Printf("Processing organization %d: %s\n", i+1, org)
		opt := &github.RepositoryListByOrgOptions{
			Type:        "public", // Adjust if you need private repos
			ListOptions: github.ListOptions{PerPage: 100},
		}

		for {
			repos, resp, err := client.Repositories.ListByOrg(ctx, org, opt)
			if err != nil {
				// Handle rate limiting specifically
				if rlErr, ok := err.(*github.RateLimitError); ok {
					log.Printf("Rate limit hit. Pausing until %v", rlErr.Rate.Reset.Time)
					// Consider adding time.Sleep(time.Until(rlErr.Rate.Reset.Time))
				}
				log.Printf("Error listing repositories for %s: %v", org, err)
				break // Stop processing this org on error
			}

			for _, repo := range repos {
				// Process both forks and non-forks, but skip archived
				if repo.GetArchived() {
					continue
				}

				isFork := repo.GetFork()
				repoName := repo.GetName()
				fullName := fmt.Sprintf("%s/%s", org, repoName)

				// --- Get and Parse go.mod ---
				fileContent, _, _, err := client.Repositories.GetContents(ctx, org, repoName, "go.mod", nil)
				if err != nil {
					// Log only unexpected errors, not 404s
					if _, ok := err.(*github.ErrorResponse); !ok || err.(*github.ErrorResponse).Response.StatusCode != 404 {
						log.Printf("    Warn: Error getting go.mod for %s: %v", fullName, err)
					}
					continue // Skip repo if go.mod fetch fails
				}
				content, err := fileContent.GetContent()
				if err != nil {
					log.Printf("    Warn: Error decoding go.mod content for %s: %v", fullName, err)
					continue
				}
				modFile, err := modfile.Parse(fmt.Sprintf("%s/go.mod", fullName), []byte(content), nil)
				if err != nil {
					log.Printf("    Warn: Error parsing go.mod for %s: %v", fullName, err)
					continue
				}
				modulePath := modFile.Module.Mod.Path
				if modulePath == "" {
					log.Printf("    Warn: Empty module path in go.mod for %s", fullName)
					continue
				}
				// --- End Get and Parse go.mod ---

				log.Printf("    Found module: %s (fork: %v) (from repo %s)\n", modulePath, isFork, fullName)
				allModulePaths[modulePath] = true // Track this module path

				// Store or update module info
				info := &ModuleInfo{
					Path:    modulePath,
					IsFork:  isFork,
					Org:     org,
					OrgIdx:  i,
					Deps:    make(map[string]string),
					Fetched: true,
				}
				modulesFoundInOrgs[modulePath] = info

				// Store direct dependencies
				for _, req := range modFile.Require {
					if !req.Indirect {
						depPath := req.Mod.Path
						depVersion := req.Mod.Version
						info.Deps[depPath] = depVersion
						allModulePaths[depPath] = true // Track the dependency module path as well
					}
				}
			}

			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}
	// --- End Scan Organizations ---

	// --- Determine Nodes to Include in Graph ---
	nodesToGraph := make(map[string]bool)        // Set of module paths to include
	referencedByNonFork := make(map[string]bool) // Set of modules depended on by non-forks

	// First pass: identify dependencies of non-forks
	for modPath, info := range modulesFoundInOrgs {
		if info.Fetched && !info.IsFork {
			nodesToGraph[modPath] = true // Always include non-forks found in orgs
			for depPath := range info.Deps {
				referencedByNonFork[depPath] = true
			}
		}
	}

	// Second pass: add forks *if* they are referenced by a non-fork
	for modPath, info := range modulesFoundInOrgs {
		if info.Fetched && info.IsFork {
			if referencedByNonFork[modPath] {
				nodesToGraph[modPath] = true
				// Also mark dependencies of included forks as referenced
				// (though this doesn't affect fork inclusion itself, it helps ensure external deps are added if needed)
				for depPath := range info.Deps {
					referencedByNonFork[depPath] = true // Mark deps of included forks
				}
			}
		}
	}

	// Third pass: add external dependencies if needed
	if !*noExt {
		for modPath := range allModulePaths {
			// If a module is referenced OR is an included node itself, add it.
			// Check if it's NOT one of the modules we found in orgs (i.e., external)
			// AND if it's referenced by a non-fork (or an included fork).
			_, foundInOrg := modulesFoundInOrgs[modPath]
			if !foundInOrg && referencedByNonFork[modPath] {
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
		if info, found := modulesFoundInOrgs[nodePath]; found {
			// It's an internal module (fork or non-fork)
			orgIdx := info.OrgIdx
			if info.IsFork {
				color = orgForkColors[orgIdx%len(orgForkColors)] // Cycle through fork colors
			} else {
				color = orgNonForkColors[orgIdx%len(orgNonForkColors)] // Cycle through non-fork colors
			}
		} else if *noExt {
			// This should not happen if logic above is correct, but as safety:
			// If it's external and -noext is set, skip definition (it shouldn't be in nodesToGraph anyway)
			continue
		}
		nodeColors[nodePath] = color
		fmt.Printf("  \"%s\" [fillcolor=\"%s\"];\n", nodePath, color)
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Get sorted list of source module paths *that are included in the graph*
	sourceModulesInGraph := []string{}
	for modPath := range modulesFoundInOrgs {
		if nodesToGraph[modPath] { // Only consider sources that are actually in the graph
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

	// Print edges
	for _, sourceModPath := range sourceModulesInGraph {
		info := modulesFoundInOrgs[sourceModPath] // We know it exists
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
