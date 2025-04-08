package main

import (
	"context"
	"fmt"
	"log"
	"net/http" // Added import
	"os"
	"sort" // Added for sorting dependencies

	"github.com/google/go-github/v62/github"
	"golang.org/x/mod/modfile"
	"golang.org/x/oauth2"
)

// Fetches and parses go.mod files for non-fork, non-archived repos in specified GitHub orgs.
func main() {
	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <org1> [org2]...", os.Args[0])
	}
	orgs := os.Args[1:]

	// --- GitHub Client Setup ---
	token := os.Getenv("GITHUB_TOKEN")
	ctx := context.Background()
	var httpClient *http.Client = nil // Default to unauthenticated

	if token != "" {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		httpClient = oauth2.NewClient(ctx, ts)
		fmt.Println("Using authenticated GitHub API access.")
	} else {
		// Use default http client for unauthenticated access
		httpClient = http.DefaultClient
		log.Println("Warning: GITHUB_TOKEN environment variable not set. Using unauthenticated access (may hit rate limits).")
	}
	client := github.NewClient(httpClient)
	// --- End GitHub Client Setup ---


	// Store dependencies: map[modulePath][]dependencyString (e.g., "path@version")
	// Using a nested map to store module path and its dependencies map[depPath]depVersion
	// This makes it easier later to check for internal vs external deps.
	// dependencyMap := make(map[string][]string)
	dependencyMap := make(map[string]map[string]string) // Key: module path, Value: map[dependencyPath]version


	for _, org := range orgs {
		fmt.Printf("Processing organization: %s\n", org)
		opt := &github.RepositoryListByOrgOptions{
			Type:        "public", // Consider only public repos, adjust if needed
			ListOptions: github.ListOptions{PerPage: 100},
		}

		// Paginate through repositories
		for {
			repos, resp, err := client.Repositories.ListByOrg(ctx, org, opt)
			if err != nil {
				// Handle rate limiting specifically
				if rlErr, ok := err.(*github.RateLimitError); ok {
					log.Printf("Rate limit hit. Pausing until %v", rlErr.Rate.Reset.Time)
					// A more robust solution would wait until the reset time.
					// time.Sleep(time.Until(rlErr.Rate.Reset.Time))
					// For simplicity here, we just log and break. Consider adding a wait.
					log.Printf("Error listing repositories for %s (rate limit): %v", org, err)
				} else {
					log.Printf("Error listing repositories for %s: %v", org, err)
				}
				break // Stop processing this org on error
			}

			for _, repo := range repos {
				// Skip forks and archived repos
				if repo.GetFork() || repo.GetArchived() {
					continue
				}

				repoName := repo.GetName()
				fullName := fmt.Sprintf("%s/%s", org, repoName)
				// fmt.Printf("  Checking repo: %s\n", fullName) // Reduce verbosity

				// --- Get go.mod Content ---
				fileContent, _, _, err := client.Repositories.GetContents(ctx, org, repoName, "go.mod", nil)
				if err != nil {
					// Log only unexpected errors, not 404s
					if _, ok := err.(*github.ErrorResponse); !ok || err.(*github.ErrorResponse).Response.StatusCode != 404 {
						log.Printf("    Warn: Error getting go.mod for %s: %v", fullName, err)
					}
					continue // Skip repo if go.mod fetch fails
				}

				content, err := fileContent.GetContent() // Gets base64 content and decodes it
				if err != nil {
					log.Printf("    Warn: Error decoding go.mod content for %s: %v", fullName, err)
					continue
				}
				// --- End Get go.mod Content ---

				// --- Parse go.mod ---
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
				fmt.Printf("    Found module: %s (from repo %s)\n", modulePath, fullName)

				// Initialize map for this module if not present
				if _, exists := dependencyMap[modulePath]; !exists {
					dependencyMap[modulePath] = make(map[string]string)
				}

				// Store direct dependencies
				for _, req := range modFile.Require {
					if !req.Indirect {
						dependencyMap[modulePath][req.Mod.Path] = req.Mod.Version
					}
				}
				// --- End Parse go.mod ---
			}

			// Break loop if no more pages
			if resp.NextPage == 0 {
				break
			}
			opt.Page = resp.NextPage // Move to the next page
		}
	}

	// --- Print Results ---
	fmt.Println("\n--- Dependencies Found ---")
	// Get sorted list of module paths for consistent output
	modulePaths := make([]string, 0, len(dependencyMap))
	for modPath := range dependencyMap {
		modulePaths = append(modulePaths, modPath)
	}
	sort.Strings(modulePaths)

	for _, modPath := range modulePaths {
		deps := dependencyMap[modPath]
		fmt.Printf("\nModule: %s\n", modPath)

		if len(deps) > 0 {
			fmt.Println("  Direct Dependencies:")
			// Get sorted list of dependency paths
			depPaths := make([]string, 0, len(deps))
			for depPath := range deps {
				depPaths = append(depPaths, depPath)
			}
			sort.Strings(depPaths)

			for _, depPath := range depPaths {
				fmt.Printf("    - %s@%s\n", depPath, deps[depPath])
			}
		} else {
			fmt.Println("  No direct dependencies found.")
		}
	}
	fmt.Println("\n--- End ---")
	// --- End Print Results ---
}

