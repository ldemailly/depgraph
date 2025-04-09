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
	cycleColor       = "red" // Color for node border in cycles
)

// --- End Color Palettes ---

// --- Graph Generation Logic ---

// buildReverseGraphAndDetectCycles builds the reversed graph, runs Kahn's algorithm
// to detect cycles, logs warnings, and returns the set of nodes likely involved in cycles.
// Returns: map[nodePath]bool indicating nodes in cycles.
func buildReverseGraphAndDetectCycles(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) map[string]bool {
	reverseAdj := make(map[string][]string)
	inDegree := make(map[string]int)
	nodesInSort := []string{}

	// Initialize in-degrees and identify nodes for sorting
	for node := range nodesToGraph {
		inDegree[node] = 0
		nodesInSort = append(nodesInSort, node)
	}

	// Build reversed adjacency list and calculate in-degrees
	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] {
			continue
		}
		if _, exists := reverseAdj[sourceMod]; !exists {
			reverseAdj[sourceMod] = []string{}
		}
		for dep := range info.Deps {
			if nodesToGraph[dep] {
				if _, exists := reverseAdj[dep]; !exists {
					reverseAdj[dep] = []string{}
				}
				reverseAdj[dep] = append(reverseAdj[dep], sourceMod)
				inDegree[sourceMod]++
			}
		}
	}

	// Initialize queue with nodes having in-degree 0
	queue := []string{}
	for _, node := range nodesInSort {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue)

	processedCount := 0
	// Process the queue (Kahn's algorithm)
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		nextQueue := []string{}
		for i := 0; i < currentLevelSize; i++ {
			u := queue[i]
			processedCount++
			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				inDegree[v]--
				if inDegree[v] == 0 {
					nextQueue = append(nextQueue, v)
				}
			}
		}
		sort.Strings(nextQueue)
		queue = nextQueue
	}

	// Identify nodes likely in cycles (those with remaining in-degree > 0)
	nodesInCycles := make(map[string]bool) // Changed return type to map
	if processedCount < len(nodesInSort) {
		log.Warnf("Cycle detected in dependencies! Processed %d nodes, expected %d.", processedCount, len(nodesInSort))
		log.Warnf("Nodes likely involved in cycles (remaining in-degree > 0):")
		remainingNodes := []string{}
		for _, node := range nodesInSort {
			if inDegree[node] > 0 {
				remainingNodes = append(remainingNodes, node)
				nodesInCycles[node] = true // Add to the map for return
			}
		}
		sort.Strings(remainingNodes)
		for _, node := range remainingNodes {
			log.Warnf("  - %s (remaining reversed in-degree: %d)", node, inDegree[node])
		}
	}
	return nodesInCycles // Return the map of nodes in cycles
}

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
func generateDotOutput(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool, noExt bool, left2Right bool) { // Added left2Right flag
	// --- Detect Cycles to Highlight Nodes ---
	// Run the cycle detection part of the topo sort
	nodesInCyclesSet := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph) // Correctly gets map[string]bool
	// --- End Detect Cycles ---

	// --- Generate DOT Output ---
	fmt.Println("digraph dependencies {")
	// Set rankdir based on flag
	rankDir := "TB"
	if left2Right {
		rankDir = "LR"
	}
	fmt.Printf("  rankdir=\"%s\";\n", rankDir) // Use flag value
	// Define default node style, potentially overridden later for cycle nodes
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
		nodeAttrs := []string{} // Store attributes like label, fillcolor, color

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
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						// Path matches original: RepoPath\n(fork of OriginalPath)
						label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath) // Use \n
					} else {
						// Path differs: DeclaredPath\n(fork of OriginalPath)
						label = fmt.Sprintf("%s\\n(fork of %s)", info.Path, info.OriginalModulePath) // Use Declared Path
					}
				}
				// --- End Updated Fork Labeling Logic ---
			}
		} else if noExt {
			continue
		} // Skip external node definition if -noext
		// Else: External node, color is externalColor, label is nodePath.

		// Add standard attributes
		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("label=\"%s\"", escapedLabel))
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("fillcolor=\"%s\"", color))

		// Add cycle highlighting if node is in a cycle
		if nodesInCyclesSet[nodePath] { // Check against the map
			log.LogVf("Highlighting cycle node: %s", nodePath)
			nodeAttrs = append(nodeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor)) // Set border color
			nodeAttrs = append(nodeAttrs, "penwidth=2")                            // Make border thicker
		}

		// Print node definition with all attributes
		fmt.Printf("  \"%s\" [%s];\n", nodePath, strings.Join(nodeAttrs, ", "))
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
	// Build reverse graph and get nodes in cycles (warnings printed inside)
	// Corrected: Use blank identifier for the first return value (inDegree map)
	nodesInCycles := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph)

	// Re-build reverse graph and in-degrees again for actual level processing
	reverseAdj := make(map[string][]string)
	inDegree := make(map[string]int) // Re-initialize
	nodesInSort := []string{}

	for node := range nodesToGraph {
		inDegree[node] = 0
		nodesInSort = append(nodesInSort, node)
	}
	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] {
			continue
		}
		if _, exists := reverseAdj[sourceMod]; !exists {
			reverseAdj[sourceMod] = []string{}
		}
		for dep := range info.Deps {
			if nodesToGraph[dep] {
				if _, exists := reverseAdj[dep]; !exists {
					reverseAdj[dep] = []string{}
				}
				reverseAdj[dep] = append(reverseAdj[dep], sourceMod)
				inDegree[sourceMod]++
			}
		}
	}

	// Initialize queue with nodes having in-degree 0
	queue := []string{}
	for _, node := range nodesInSort {
		if inDegree[node] == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue)

	resultLevels := [][]string{}
	processedCount := 0
	processedNodes := make(map[string]bool) // Track processed nodes

	log.Infof("Starting topological sort (leaves first)...")
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		currentLevelNodes := make([]string, 0, currentLevelSize)
		nextQueue := []string{}
		log.LogVf("  Processing Level %d with %d nodes: %v", len(resultLevels), currentLevelSize, queue)
		for i := 0; i < currentLevelSize; i++ {
			u := queue[i]
			currentLevelNodes = append(currentLevelNodes, u)
			processedCount++
			processedNodes[u] = true
			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				inDegree[v]--
				if inDegree[v] == 0 {
					nextQueue = append(nextQueue, v)
				}
			}
		}
		resultLevels = append(resultLevels, currentLevelNodes)
		sort.Strings(nextQueue)
		queue = nextQueue
	}

	// Print the sorted levels
	fmt.Println("Topological Sort Levels (Leaves First):")
	for i, level := range resultLevels {
		indent := strings.Repeat("  ", i)
		fmt.Printf("%sLevel %d:\n", indent, i)
		for _, nodePath := range level {
			outputStr := nodePath
			if info, found := modulesFoundInOwners[nodePath]; found && info.IsFork {
				outputStr = info.RepoPath
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
					}
				}
			}
			fmt.Printf("%s  - %s\n", indent, outputStr)
		}
	}

	// Print nodes involved in cycles (if any)
	// Check if cycles were detected by comparing processed count
	if processedCount < len(nodesInSort) {
		fmt.Println("\nCyclic Dependencies (cannot be ordered):")
		remainingNodes := []string{}
		// Iterate through the map returned by the initial cycle check run
		for node := range nodesInCycles { // Use the map returned earlier
			remainingNodes = append(remainingNodes, node)
		}
		sort.Strings(remainingNodes) // Sort cyclic nodes alphabetically
		for _, nodePath := range remainingNodes {
			// Format output similar to leveled output
			outputStr := nodePath
			if info, found := modulesFoundInOwners[nodePath]; found && info.IsFork {
				outputStr = info.RepoPath
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
					}
				}
			}
			fmt.Printf("  - %s\n", outputStr) // Print with standard indent
		}
	}
}

// --- End Topological Sort Logic ---

// --- End Graph Generation Logic ---
