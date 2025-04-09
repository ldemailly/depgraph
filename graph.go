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
	nodesInCyclesSet := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph)
	// --- End Detect Cycles ---

	// --- Build Forward Adjacency List for Bidirectional Edge Check ---
	adj := make(map[string]map[string]bool) // adj[source][dest] = true
	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] {
			continue
		}
		if adj[sourceMod] == nil {
			adj[sourceMod] = make(map[string]bool)
		}
		for dep := range info.Deps {
			if nodesToGraph[dep] {
				adj[sourceMod][dep] = true
			}
		}
	}
	// --- End Build Forward Adjacency List ---

	// --- Generate DOT Output ---
	fmt.Println("digraph dependencies {")
	rankDir := "TB"
	if left2Right {
		rankDir = "LR"
	}
	fmt.Printf("  rankdir=\"%s\";\n", rankDir)
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];")
	fmt.Println("  edge [fontname=\"Helvetica\", fontsize=10];") // Default edge style

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
		nodeAttrs := []string{}

		info, foundInScanned := modulesFoundInOwners[nodePath]
		if foundInScanned {
			if !info.IsFork {
				ownerIdx := info.OwnerIdx
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
			} else {
				ownerIdx := info.OwnerIdx
				color = orgForkColors[ownerIdx%len(orgForkColors)]
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						label = fmt.Sprintf("%s\\n(fork of %s)", info.Path, info.OriginalModulePath) // Fixed Sprintf
					}
				} else {
					label = info.RepoPath
				}
			}
		} else if noExt {
			continue
		}

		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("label=\"%s\"", escapedLabel))
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("fillcolor=\"%s\"", color))

		if nodesInCyclesSet[nodePath] {
			log.LogVf("Highlighting cycle node: %s", nodePath)
			nodeAttrs = append(nodeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor)) // Set border color
			nodeAttrs = append(nodeAttrs, "penwidth=2")
		}

		fmt.Printf("  \"%s\" [%s];\n", nodePath, strings.Join(nodeAttrs, ", "))
	}

	fmt.Println("\n  // Edges (Dependencies)")
	sourceModulesInGraph := []string{}
	for modPath := range modulesFoundInOwners {
		if nodesToGraph[modPath] {
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

	// Print edges
	for _, sourceModPath := range sourceModulesInGraph {
		info := modulesFoundInOwners[sourceModPath]
		if info == nil {
			continue
		}

		depPaths := make([]string, 0, len(info.Deps))
		for depPath := range info.Deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)

		for _, depPath := range depPaths {
			if nodesToGraph[depPath] { // Only draw edge if target is included
				version := info.Deps[depPath]
				escapedVersion := strings.ReplaceAll(version, "\"", "\\\"")
				edgeAttrs := []string{fmt.Sprintf("label=\"%s\"", escapedVersion)} // Start with label attribute

				// Check for bidirectional edge
				if adj[depPath] != nil && adj[depPath][sourceModPath] {
					log.LogVf("Highlighting bidirectional edge: %s <-> %s", sourceModPath, depPath)
					edgeAttrs = append(edgeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor)) // Add red color for bidirectional edge
					edgeAttrs = append(edgeAttrs, "penwidth=1.5")                          // Slightly thicker edge for bidir?
				}

				fmt.Printf("  \"%s\" -> \"%s\" [%s];\n", sourceModPath, depPath, strings.Join(edgeAttrs, ", "))
			}
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}

// --- Topological Sort Logic ---

// Helper function to format node output for topo sort
func formatNodeForTopo(nodePath string, modulesFoundInOwners map[string]*ModuleInfo) string {
	// Default output is module path
	outputStr := nodePath
	// Look up info to customize output for forks
	if info, found := modulesFoundInOwners[nodePath]; found && info.IsFork {
		outputStr = info.RepoPath // Use repo path for forks
		// Append original module path if it was found
		if info.OriginalModulePath != "" {
			if info.Path == info.OriginalModulePath {
				// Path matches original: RepoPath (fork of OriginalPath)
				outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
			} else {
				// Path differs: RepoPath (DeclaredPath fork of OriginalPath)
				outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
			}
		}
	}
	return outputStr
}

// performTopologicalSortAndPrint performs Kahn's algorithm on the REVERSE graph
// to print levels starting with leaves (nodes with no outgoing edges in original graph).
// It also attempts to group simple A<->B cycles on the same line.
func performTopologicalSortAndPrint(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) {
	// Build forward adjacency list needed for bidirectional check
	adj := make(map[string]map[string]bool) // adj[source][dest] = true
	bidirPairs := make(map[string]string)   // Store A -> B if A <-> B and A < B
	isBidirNode := make(map[string]bool)    // Mark nodes involved in any A<->B pair

	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] {
			continue
		}
		if adj[sourceMod] == nil {
			adj[sourceMod] = make(map[string]bool)
		}
		for dep := range info.Deps {
			if nodesToGraph[dep] {
				adj[sourceMod][dep] = true
				// Check for bidirectional link
				if otherInfo, ok := modulesFoundInOwners[dep]; ok && nodesToGraph[otherInfo.Path] {
					for otherDep := range otherInfo.Deps {
						if otherDep == sourceMod { // Found B -> A
							isBidirNode[sourceMod] = true
							isBidirNode[dep] = true
							// Store pair consistently (e.g., always store A->B where A < B)
							if sourceMod < dep {
								bidirPairs[sourceMod] = dep
							} else {
								bidirPairs[dep] = sourceMod
							}
							break // Found reverse link, no need to check further deps of B
						}
					}
				}
			}
		}
	}

	// Build reverse graph and get nodes in cycles (warnings printed inside)
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

	// Print the sorted levels, attempting to combine A<->B pairs
	fmt.Println("Topological Sort Levels (Leaves First):")
	processedForOutput := make(map[string]bool) // Track nodes already printed in combined format

	for i, level := range resultLevels {
		indent := strings.Repeat("  ", i)
		fmt.Printf("%sLevel %d:\n", indent, i)
		levelSet := make(map[string]bool)
		for _, node := range level {
			levelSet[node] = true
		} // For quick lookup

		// Sort level nodes for consistent processing order
		sortedLevelNodes := make([]string, len(level))
		copy(sortedLevelNodes, level)
		sort.Strings(sortedLevelNodes)

		for _, nodePath := range sortedLevelNodes {
			if processedForOutput[nodePath] {
				continue
			} // Skip if already printed as part of a pair

			partner, isPairStart := bidirPairs[nodePath] // Is nodePath the 'A' in an A<->B pair (where A<B)?
			// Removed unused isLoneBidir variable

			if isPairStart && levelSet[partner] { // Is it A in A<->B and B is also in this level?
				// Print combined format
				formattedA := formatNodeForTopo(nodePath, modulesFoundInOwners) // No marker needed here
				formattedB := formatNodeForTopo(partner, modulesFoundInOwners)  // No marker needed here
				fmt.Printf("%s  - %s <-> %s\n", indent, formattedA, formattedB)
				processedForOutput[nodePath] = true
				processedForOutput[partner] = true
			} else {
				// Print individually
				marker := ""
				if isBidirNode[nodePath] {
					marker = " (*)"
				} // Mark if part of any bidir pair, even if partner not in level
				outputStr := formatNodeForTopo(nodePath, modulesFoundInOwners) // Format fork info
				fmt.Printf("%s  - %s%s\n", indent, outputStr, marker)          // Append marker
				processedForOutput[nodePath] = true
			}
		}
	}

	// Print nodes involved in cycles (if any)
	if processedCount < len(nodesInSort) {
		fmt.Println("\nCyclic Dependencies (cannot be ordered):")
		remainingNodes := []string{}
		for node := range nodesInCycles {
			remainingNodes = append(remainingNodes, node)
		}
		sort.Strings(remainingNodes) // Sort cyclic nodes alphabetically

		processedForOutput := make(map[string]bool) // Track nodes already printed in combined format within cycles

		for _, nodePath := range remainingNodes {
			if processedForOutput[nodePath] {
				continue
			} // Skip if already printed as part of a pair

			partner, isPairStart := bidirPairs[nodePath]
			_, partnerInCycle := nodesInCycles[partner] // Check if partner is also in the cycle list

			if isPairStart && partnerInCycle { // Is it A in A<->B and B is also in the cycle list?
				// Print combined format
				formattedA := formatNodeForTopo(nodePath, modulesFoundInOwners)
				formattedB := formatNodeForTopo(partner, modulesFoundInOwners)
				fmt.Printf("  - %s <-> %s\n", formattedA, formattedB)
				processedForOutput[nodePath] = true
				processedForOutput[partner] = true
			} else {
				// Print individually
				marker := " (*)" // Mark all cyclic nodes
				outputStr := formatNodeForTopo(nodePath, modulesFoundInOwners)
				fmt.Printf("  - %s%s\n", outputStr, marker) // Print with standard indent and marker
				processedForOutput[nodePath] = true
			}
		}
	}
}

// --- End Topological Sort Logic ---

// --- End Graph Generation Logic ---
