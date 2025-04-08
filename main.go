package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"

	// No longer needed: "strings"

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// No longer needed: isInternalModule function

// Fetches and parses go.mod files for non-fork, non-archived repos in specified GitHub orgs.
// Outputs the dependency graph in DOT format.
func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <org1> [org2]...", os.Args[0])
	}
	orgs := os.Args[1:]
	// Removed modulePrefixes definition and related logging

	// --- GitHub Client Setup ---
	token := os.Getenv("GITHUB_TOKEN")
	ctx := context.Background()
	var httpClient *http.Client = nil // Default to unauthenticated

	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		httpClient = oauth2.NewClient(ctx, ts)
	} else {
		httpClient = http.DefaultClient
		log.Println("Warning: GITHUB_TOKEN environment variable not set. Using unauthenticated access (may hit rate limits).")
	}
	client := github.NewClient(httpClient)
	// --- End GitHub Client Setup ---

	// Store dependencies: map[modulePath]map[dependencyPath]version
	// The keys of this map represent the "internal" modules found in the target orgs.
	dependencyMap := make(map[string]map[string]string)
	// Keep track of all unique module paths encountered (both source and deps)
	allModules := make(map[string]bool)

	for _, org := range orgs {
		log.Printf("Processing organization: %s\n", org) // Log to stderr
		opt := &github.RepositoryListByOrgOptions{
			Type:        "public",
			ListOptions: github.ListOptions{PerPage: 100},
		}

		for {
			repos, resp, err := client.Repositories.ListByOrg(ctx, org, opt)
			if err != nil {
				if rlErr, ok := err.(*github.RateLimitError); ok {
					log.Printf("Rate limit hit. Pausing until %v", rlErr.Rate.Reset.Time)
				}
				log.Printf("Error listing repositories for %s: %v", org, err)
				break
			}

			for _, repo := range repos {
				if repo.GetFork() || repo.GetArchived() {
					continue
				}
				repoName := repo.GetName()
				fullName := fmt.Sprintf("%s/%s", org, repoName)

				fileContent, _, _, err := client.Repositories.GetContents(ctx, org, repoName, "go.mod", nil)
				if err != nil {
					if _, ok := err.(*github.ErrorResponse); !ok || err.(*github.ErrorResponse).Response.StatusCode != 404 {
						log.Printf("    Warn: Error getting go.mod for %s: %v", fullName, err)
					}
					continue
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
				log.Printf("    Found module: %s (from repo %s)\n", modulePath, fullName)
				allModules[modulePath] = true // Track this module

				// Initialize map for this module if not present (ensures it's marked internal)
				if _, exists := dependencyMap[modulePath]; !exists {
					dependencyMap[modulePath] = make(map[string]string)
				}

				// Store direct dependencies
				for _, req := range modFile.Require {
					if !req.Indirect {
						depPath := req.Mod.Path
						depVersion := req.Mod.Version
						dependencyMap[modulePath][depPath] = depVersion
						allModules[depPath] = true // Track the dependency module as well
					}
				}
			}

			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage
		}
	}

	// --- Generate DOT Output ---
	fmt.Println("digraph dependencies {")
	fmt.Println("  rankdir=\"LR\";")                                                     // Layout left-to-right
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];") // Default node style
	fmt.Println("  edge [fontname=\"Helvetica\", fontsize=10];")                         // Default edge style

	// Define node styles (internal vs external)
	fmt.Println("  node [fillcolor=\"lightblue\"]; // Style for internal modules")
	internalNodes := []string{}
	externalNodes := []string{}

	// Iterate over *all* modules found (sources and dependencies)
	for modPath := range allModules {
		// A module is internal if its definition was found (i.e., it's a key in dependencyMap)
		if _, isInternal := dependencyMap[modPath]; isInternal {
			internalNodes = append(internalNodes, modPath)
		} else {
			externalNodes = append(externalNodes, modPath)
		}
	}
	sort.Strings(internalNodes)
	sort.Strings(externalNodes)

	// Print internal node definitions
	for _, modPath := range internalNodes {
		fmt.Printf("  \"%s\";\n", modPath) // Apply default internal style
	}

	// Change default style for external nodes
	fmt.Println("\n  node [fillcolor=\"lightgrey\"]; // Style for external modules")
	// Print external node definitions
	for _, modPath := range externalNodes {
		fmt.Printf("  \"%s\";\n", modPath) // Apply external style
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Get sorted list of source module paths (which are the internal modules) for consistent output
	sourceModulePaths := make([]string, 0, len(dependencyMap))
	for modPath := range dependencyMap {
		sourceModulePaths = append(sourceModulePaths, modPath)
	}
	sort.Strings(sourceModulePaths)

	// Print edges
	for _, sourceModPath := range sourceModulePaths {
		deps := dependencyMap[sourceModPath]
		// Get sorted list of dependency paths
		depPaths := make([]string, 0, len(deps))
		for depPath := range deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)

		for _, depPath := range depPaths {
			version := deps[depPath]
			// Add version as edge label
			fmt.Printf("  \"%s\" -> \"%s\" [label=\"%s\"];\n", sourceModPath, depPath, version)
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}
