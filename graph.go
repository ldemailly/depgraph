package main

import (
	"fmt"
	"sort"
	"strings"

	"fortio.org/log" // Using fortio log
)

// --- Color Palettes ---
var (
	orgNonForkColors = []string{"lightblue", "lightgreen", "lightsalmon", "lightgoldenrodyellow", "lightpink"}
	orgForkColors    = []string{"steelblue", "darkseagreen", "coral", "darkkhaki", "mediumvioletred"}
	externalColor    = "lightgrey"
)

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
			// Include fork if it depends on a non-fork OR if a non-fork depends on its module path
			if forksDependingOnNonFork[modPath] {
				includeReason = "depends on non-fork"
			} else if referencedModules[modPath] {
				includeReason = "referenced by included module"
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
	fmt.Println("  rankdir=\"TB\";") // Changed from LR to TB
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
				// --- Updated Fork Labeling Logic ---
				label = info.RepoPath // Primary label is repo path for qualified forks
				// Add descriptive suffix if original path was found
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						// Path matches original: RepoPath\n(fork of OriginalPath)
						label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath) // Use \n
					} else {
						// Path differs: RepoPath\n(DeclaredPath fork of OriginalPath)
						label = fmt.Sprintf("%s\\n(%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath) // Use \n
					}
				}
				// --- End Updated Fork Labeling Logic ---
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

// --- Topological Sort Logic ---

// performTopologicalSortAndPrint performs Kahn's algorithm on the REVERSE graph
// to print levels starting with leaves (nodes with no outgoing edges in original graph).
func performTopologicalSortAndPrint(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) {
	reverseAdj := make(map[string][]string) // Adjacency list for the *reversed* graph
	inDegree := make(map[string]int)        // In-degree based on the *reversed* graph
	nodesInSort := []string{}               // Keep track of nodes actually part of the sort

	log.Infof("Building reversed graph for topological sort...")
	// Initialize in-degrees and identify nodes for sorting
	for node := range nodesToGraph {
		inDegree[node] = 0 // Initialize in-degree for all nodes in the graph
		nodesInSort = append(nodesInSort, node)
	}

	// Build *reversed* adjacency list and calculate in-degrees based *only* on edges
	// between nodes included in the final graph (`nodesToGraph`)
	// and originating from modules we scanned (`modulesFoundInOwners`).
	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] { // Skip sources not in the final graph
			continue
		}
		// Ensure sourceMod exists in reverseAdj map even if it has no incoming edges (in reversed graph)
		if _, exists := reverseAdj[sourceMod]; !exists {
			reverseAdj[sourceMod] = []string{}
		}

		for dep := range info.Deps {
			if nodesToGraph[dep] { // Only consider edges pointing to included nodes
				// Add edge from dep -> sourceMod in the reversed graph
				log.LogVf("  Reverse TopoSort Edge: %s -> %s", dep, sourceMod)
				// Ensure dep exists in reverseAdj map
				if _, exists := reverseAdj[dep]; !exists {
					reverseAdj[dep] = []string{}
				}
				reverseAdj[dep] = append(reverseAdj[dep], sourceMod)
				inDegree[sourceMod]++ // Increment in-degree of the *source* in the reversed graph
			}
		}
	}

	// Initialize queue with nodes having in-degree 0 (in the reversed graph)
	// These are the leaves of the original graph.
	queue := []string{}
	for _, node := range nodesInSort {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue) // Sort initial queue for deterministic order

	resultLevels := [][]string{}
	processedCount := 0

	log.Infof("Starting topological sort (leaves first)...")
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		currentLevelNodes := make([]string, 0, currentLevelSize)
		nextQueue := []string{} // Prepare next level's queue candidates

		log.LogVf("  Processing Level %d with %d nodes: %v", len(resultLevels), currentLevelSize, queue)

		// Process nodes at the current level
		for i := 0; i < currentLevelSize; i++ {
			u := queue[i] // u is a node with in-degree 0 in the reversed graph (a leaf in original)
			currentLevelNodes = append(currentLevelNodes, u)
			processedCount++

			// For each neighbor v of u in the *reversed* graph (i.e., nodes that depended on u in original)
			neighbors := reverseAdj[u] // Get neighbors from reversed adjacency list
			sort.Strings(neighbors)    // Process neighbors alphabetically for determinism
			for _, v := range neighbors {
				inDegree[v]-- // Decrement in-degree of node v (which depended on u)
				if inDegree[v] == 0 {
					nextQueue = append(nextQueue, v) // Add v to candidates for next level
				}
			}
		}

		// Add the processed level to results (nodes are sorted alphabetically)
		resultLevels = append(resultLevels, currentLevelNodes)

		// Prepare and sort the queue for the next level
		sort.Strings(nextQueue)
		queue = nextQueue
	}

	// Check for cycles (same logic applies to reversed graph)
	if processedCount < len(nodesInSort) {
		log.Warnf("Cycle detected in dependencies! Processed %d nodes, expected %d.", processedCount, len(nodesInSort))
		log.Warnf("Nodes likely involved in cycles (in-degree > 0 after sort on reversed graph):")
		remainingNodes := []string{}
		for _, node := range nodesInSort {
			if inDegree[node] > 0 {
				remainingNodes = append(remainingNodes, node)
			}
		}
		sort.Strings(remainingNodes)
		for _, node := range remainingNodes {
			log.Warnf("  - %s (remaining reversed in-degree: %d)", node, inDegree[node])
		}
	}

	// Print the sorted levels (Level 0 now contains original leaves)
	fmt.Println("Topological Sort Levels (Leaves First):")
	for i, level := range resultLevels {
		indent := strings.Repeat("  ", i)
		fmt.Printf("%sLevel %d:\n", indent, i)
		for _, nodePath := range level { // nodePath is the module path
			outputStr := nodePath // Default output is module path
			// Look up info to customize output for forks
			if info, found := modulesFoundInOwners[nodePath]; found && info.IsFork {
				outputStr = info.RepoPath // Use repo path for forks
				// Append original module path if it was found
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						// Path matches original: RepoPath (fork of OriginalPath) - single line for text
						outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						// Path differs: RepoPath (DeclaredPath fork of OriginalPath) - single line for text
						outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
					}
				}
			}
			fmt.Printf("%s  - %s\n", indent, outputStr)
		}
	}
}

// --- End Topological Sort Logic ---

// --- End Graph Generation Logic ---
