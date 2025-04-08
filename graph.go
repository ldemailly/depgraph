package main

import (
	"fmt"
	"sort"
	"strings"

	"fortio.org/log" // Using fortio log (Removed explicit name)
)

// --- Color Palettes ---
var orgNonForkColors = []string{"lightblue", "lightgreen", "lightsalmon", "lightgoldenrodyellow", "lightpink"}
var orgForkColors = []string{"steelblue", "darkseagreen", "coral", "darkkhaki", "mediumvioletred"}
var externalColor = "lightgrey"

// --- End Color Palettes ---

// --- Graph Generation Logic ---

// determineNodesToGraph calculates the set of nodes to include in the final graph
func determineNodesToGraph(modulesFoundInOwners map[string]*ModuleInfo, allModulePaths map[string]bool, noExt bool) map[string]bool {
	nodesToGraph := make(map[string]bool)
	referencedModules := make(map[string]bool)       // Modules depended on by included nodes (non-forks or included forks)
	forksDependingOnNonFork := make(map[string]bool) // Forks (by module path) that depend on an included non-fork

	// Pass 1: Add non-forks and collect their initial dependencies
	log.Infof("Determining graph nodes: Pass 1 (Non-forks)")
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && !info.IsFork {
			log.LogVf("  Including non-fork: %s", modPath)
			nodesToGraph[modPath] = true
			for depPath := range info.Deps {
				log.LogVf("    References: %s", depPath)
				referencedModules[depPath] = true
			}
		}
	}
	// Pass 2: Identify forks that depend on *included* non-forks
	log.Infof("Determining graph nodes: Pass 2 (Forks depending on Non-forks)")
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			for depPath := range info.Deps {
				if nodesToGraph[depPath] { // Check if the dep is an included non-fork
					if depInfo, found := modulesFoundInOwners[depPath]; found && !depInfo.IsFork {
						log.LogVf("  Marking fork '%s' (from %s) as depending on non-fork '%s'", modPath, info.RepoPath, depPath)
						forksDependingOnNonFork[modPath] = true // Mark the fork module path
						break
					}
				}
			}
		}
	}
	// Pass 3: Add forks if they depend on non-forks OR if their module path is referenced by a non-fork initially
	log.Infof("Determining graph nodes: Pass 3 (Include qualifying Forks)")
	for modPath, info := range modulesFoundInOwners {
		if info.Fetched && info.IsFork {
			includeReason := ""
			if forksDependingOnNonFork[modPath] {
				includeReason = "depends on non-fork"
			} else if referencedModules[modPath] {
				includeReason = "referenced by non-fork"
			}

			if includeReason != "" {
				log.LogVf("  Including fork '%s' (from %s) because: %s", modPath, info.RepoPath, includeReason)
				nodesToGraph[modPath] = true
				// Add dependencies of included forks to referenced set for external inclusion check
				for depPath := range info.Deps {
					if !referencedModules[depPath] {
						log.LogVf("    Now referencing (from included fork): %s", depPath)
						referencedModules[depPath] = true
					}
				}
			}
		}
	}
	// Pass 4: Add external dependencies if needed
	log.Infof("Determining graph nodes: Pass 4 (External dependencies, noExt=%v)", noExt)
	if !noExt {
		for modPath := range allModulePaths {
			_, foundInOwner := modulesFoundInOwners[modPath]
			// Add if external and referenced by an included node (non-fork or included fork)
			if !foundInOwner && referencedModules[modPath] {
				if !nodesToGraph[modPath] { // Avoid logging duplicates if somehow already added
					log.LogVf("  Including external: %s (referenced)", modPath)
					nodesToGraph[modPath] = true
				}
			}
		}
	}
	log.Infof("Total nodes included in graph: %d", len(nodesToGraph))
	return nodesToGraph
}

// generateDotOutput generates the DOT graph representation and prints it to stdout
func generateDotOutput(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool, noExt bool) {
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
		label := nodePath
		color := externalColor
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
				ownerIdx := info.OwnerIdx
				color = orgForkColors[ownerIdx%len(orgForkColors)]
				// --- Fork Labeling Logic ---
				label = info.RepoPath // Primary label is repo path for qualified forks
				if info.OriginalModulePath != "" && info.Path != info.OriginalModulePath {
					label = fmt.Sprintf("%s\\n(module: %s)", info.RepoPath, info.Path)
				}
				// --- End Fork Labeling Logic ---
			}
		} else if noExt {
			continue
		} // Skip external node definition if -noext
		// Else: External node, color is externalColor, label is nodePath.

		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		fmt.Printf("  \"%s\" [label=\"%s\", fillcolor=\"%s\"];\n", nodePath, escapedLabel, color)
	}

	fmt.Println("\n  // Edges (Dependencies)")
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

// --- End Graph Generation Logic ---
