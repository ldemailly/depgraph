package main

import (
	"fmt"
	"sort"
	"strings"

	"fortio.org/log" // Using fortio log
	"github.com/ldemailly/depgraph/graph"
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
// Returns: map[nodePath]bool indicating nodes in cycles, and the initial inDegree map.
func buildReverseGraphAndDetectCycles(modulesFoundInOwners map[string]*graph.ModuleInfo, nodesToGraph map[string]bool) (map[string]bool, map[string]int, map[string][]string) {
	reverseAdj := make(map[string][]string)
	inDegree := make(map[string]int)
	nodesInSort := []string{}

	// Initialize in-degrees and identify nodes for sorting
	for node := range nodesToGraph {
		inDegree[node] = 0
		nodesInSort = append(nodesInSort, node)
	}
	sort.Strings(nodesInSort) // Sort for deterministic processing later

	// Build reversed adjacency list and calculate in-degrees
	for sourceMod, info := range modulesFoundInOwners {
		if !nodesToGraph[sourceMod] {
			continue
		}
		if _, exists := reverseAdj[sourceMod]; !exists {
			reverseAdj[sourceMod] = []string{}
		}
		// Sort dependencies for deterministic edge processing if needed elsewhere
		depPaths := make([]string, 0, len(info.Deps))
		for dep := range info.Deps {
			depPaths = append(depPaths, dep)
		}
		sort.Strings(depPaths)

		for _, dep := range depPaths {
			if nodesToGraph[dep] {
				if _, exists := reverseAdj[dep]; !exists {
					reverseAdj[dep] = []string{}
				}
				reverseAdj[dep] = append(reverseAdj[dep], sourceMod) // dep -> sourceMod in reverse graph
				inDegree[sourceMod]++
			}
		}
	}

	// --- Kahn's Algorithm for Cycle Detection ---
	queue := []string{}
	tempInDegree := make(map[string]int) // Use a temporary map for cycle detection Kahn's
	for node, degree := range inDegree {
		tempInDegree[node] = degree
		if degree == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue) // Initial sort

	processedCount := 0
	// Process the queue (Kahn's algorithm)
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		processedCount++

		// Sort neighbors for deterministic processing order
		neighbors := reverseAdj[u]
		sort.Strings(neighbors)

		for _, v := range neighbors { // For each node v that depends on u (u -> v in original graph)
			tempInDegree[v]--
			if tempInDegree[v] == 0 {
				queue = append(queue, v) // Add newly free node
			}
		}
		sort.Strings(queue) // Keep queue sorted if needed for deterministic level output (though not strictly necessary for cycle detection itself)
	}

	// Identify nodes likely in cycles (those with remaining tempInDegree > 0)
	nodesInCycles := make(map[string]bool)
	if processedCount < len(nodesInSort) {
		log.Warnf("Cycle detected in dependencies! Processed %d nodes, expected %d.", processedCount, len(nodesInSort))
		log.Warnf("Nodes likely involved in cycles (remaining in-degree > 0):")
		remainingNodes := []string{}
		for _, node := range nodesInSort {
			// Use tempInDegree which was modified by Kahn's
			if tempInDegree[node] > 0 {
				remainingNodes = append(remainingNodes, node)
				nodesInCycles[node] = true // Add to the map for return
			}
		}
		sort.Strings(remainingNodes)
		for _, node := range remainingNodes {
			// Log the remaining degree from the *cycle detection* pass
			log.Warnf("  - %s (remaining reversed in-degree during cycle check: %d)", node, tempInDegree[node])
		}
	}
	// Return the original inDegree map for the main topo sort
	return nodesInCycles, inDegree, reverseAdj
}

// isNodeDependedOn returns true if the given node is depended on by any other node
// *within* the set of nodes currently considered to be in cycles.
func isNodeDependedOn(node string, modulesFoundInOwners map[string]*graph.ModuleInfo, currentNodesInCycles map[string]bool) bool {
	for _, info := range modulesFoundInOwners {
		// Only check dependencies *of* nodes that are *also* in the current cycle set.
		if !currentNodesInCycles[info.Path] {
			continue
		}
		for dep := range info.Deps {
			if dep == node {
				return true // Found a node within the cycle set that depends on 'node'
			}
		}
	}
	return false
}

// filterOutUnusedNodes removes nodes from the cycle set that are not depended upon
// by any *other* node *within the cycle set*. This helps refine the cycle detection
// by removing nodes that might have a non-zero in-degree initially due to dependencies
// from *outside* the cycle, but aren't actually part of a loop structure themselves.
// It iteratively removes such nodes until no more can be removed.
func filterOutUnusedNodes(nodesInCycles map[string]bool, modulesFoundInOwners map[string]*graph.ModuleInfo, nodesToGraph map[string]bool) map[string]bool {
	if len(nodesInCycles) == 0 {
		return nodesInCycles // No cycles detected, nothing to filter
	}
	log.LogVf("Refining cycle detection: Initial cycle candidates: %d", len(nodesInCycles))
	changed := true
	iteration := 0
	for changed {
		iteration++
		changed = false
		nodesToRemove := []string{}
		// Check each node currently marked as potentially in a cycle
		for node := range nodesInCycles {
			// Check if this node is depended on by *any other node* currently in the `nodesInCycles` set
			if !isNodeDependedOn(node, modulesFoundInOwners, nodesInCycles) {
				// If no other node *in the cycle set* depends on this node,
				// it might be a sink within the potential cycle components, or only depended upon from outside.
				// Mark it for removal from the cycle set.
				nodesToRemove = append(nodesToRemove, node)
				changed = true
			}
		}
		if changed {
			log.LogVf("  Iteration %d: Removing %d nodes not depended upon within the cycle set: %v", iteration, len(nodesToRemove), nodesToRemove)
			for _, node := range nodesToRemove {
				delete(nodesInCycles, node)
			}
		} else {
			log.LogVf("  Iteration %d: No nodes removed, cycle set stable.", iteration)
		}
	}
	log.LogVf("Refined cycle detection: Final nodes considered in cycles: %d", len(nodesInCycles))
	return nodesInCycles
}

// determineNodesToGraph calculates the set of nodes to include in the final graph
func determineNodesToGraph(modulesFoundInOwners map[string]*graph.ModuleInfo, allModulePaths map[string]bool, noExt bool) map[string]bool {
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
func generateDotOutput(modulesFoundInOwners map[string]*graph.ModuleInfo, nodesToGraph map[string]bool, noExt bool, left2Right bool) { // Added left2Right flag
	// --- Detect Cycles to Highlight Nodes ---
	nodesInCyclesSet, _, _ := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph)
	// Refine the cycle set before using it for highlighting
	nodesInCyclesSet = filterOutUnusedNodes(nodesInCyclesSet, modulesFoundInOwners, nodesToGraph)

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
		label := nodePath // Default label is the node path (module path)
		color := externalColor
		nodeAttrs := []string{}

		info, foundInScanned := modulesFoundInOwners[nodePath]
		if foundInScanned {
			ownerIdx := info.OwnerIdx
			if !info.IsFork {
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
				// Label remains nodePath
			} else {
				color = orgForkColors[ownerIdx%len(orgForkColors)]
				// *** Fork Labeling Logic for DOT Output (Multi-line using RepoPath) ***
				// Use RepoPath consistently for the first line, based on user feedback/examples.
				// Use \\n in Sprintf format string to produce literal \n in the label for DOT.
				if info.OriginalModulePath != "" {
					label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath)
				} else {
					// Fallback if original path couldn't be found
					label = fmt.Sprintf("%s\\n(fork)", info.RepoPath)
				}
				// *** End Fork Labeling Logic ***
			}
		} else if noExt {
			continue // Skip external nodes if noExt is true
		}

		// Escape label for DOT format AFTER generating it.
		// Only escape double quotes. The \\n from Sprintf should remain as \n.
		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("label=\"%s\"", escapedLabel))
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("fillcolor=\"%s\"", color))

		// Highlight border if node is part of a refined cycle
		if nodesInCyclesSet[nodePath] {
			log.LogVf("Highlighting cycle node in DOT: %s", nodePath)
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

				// Highlight edge if both source and destination are in the refined cycle set
				if nodesInCyclesSet[sourceModPath] && nodesInCyclesSet[depPath] {
					edgeAttrs = append(edgeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor)) // Add red color for cycle edge
					edgeAttrs = append(edgeAttrs, "penwidth=1.5")                          // Slightly thicker edge for cycle
				}

				fmt.Printf("  \"%s\" -> \"%s\" [%s];\n", sourceModPath, depPath, strings.Join(edgeAttrs, ", "))
			}
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}

// --- Topological Sort Logic ---

// Helper function to format node output for topo sort (SINGLE LINE format)
func formatNodeForTopo(nodePath string, modulesFoundInOwners map[string]*graph.ModuleInfo) string {
	// Default output is module path
	outputStr := nodePath
	// Look up info to customize output for forks
	if info, found := modulesFoundInOwners[nodePath]; found && info.IsFork {
		// Always start with the repo path for forks
		outputStr = info.RepoPath
		// Append original module path if it was found and differs from the fork's declared path
		if info.OriginalModulePath != "" {
			if info.Path == info.OriginalModulePath {
				// Path matches original: RepoPath (fork of OriginalPath)
				outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
			} else {
				// Path differs: RepoPath (DeclaredPath fork of OriginalPath)
				outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
			}
		} else {
			// Fork, but couldn't find original module path
			outputStr = fmt.Sprintf("%s (fork)", info.RepoPath)
		}
	}
	return outputStr
}

// printLevel prints a single level of the topological sort, handling A<->B pairs.
func printLevel(levelNodes []string, levelIndex int, indent string, modulesFoundInOwners map[string]*graph.ModuleInfo, bidirPairs map[string]string, isBidirNode map[string]bool, processedForOutput map[string]bool, levelName string) {
	if len(levelNodes) == 0 {
		return // Don't print empty levels
	}
	fmt.Printf("%sLevel %d%s:\n", indent, levelIndex, levelName)
	levelSet := make(map[string]bool)
	for _, node := range levelNodes {
		levelSet[node] = true
	} // For quick lookup

	// Sort level nodes for consistent processing order
	sortedLevelNodes := make([]string, len(levelNodes))
	copy(sortedLevelNodes, levelNodes)
	sort.Strings(sortedLevelNodes)

	for _, nodePath := range sortedLevelNodes {
		if processedForOutput[nodePath] {
			continue // Skip if already printed as part of a pair
		}

		partner, isPairStart := bidirPairs[nodePath] // Is nodePath the 'A' in an A<->B pair (where A<B)?
		_, partnerInLevel := levelSet[partner]       // Is the partner B also in this level?

		if isPairStart && partnerInLevel { // Is it A in A<->B and B is also in this level?
			// Print combined format using the text-based helper
			formattedA := formatNodeForTopo(nodePath, modulesFoundInOwners)
			formattedB := formatNodeForTopo(partner, modulesFoundInOwners)
			fmt.Printf("%s  - %s <-> %s\n", indent, formattedA, formattedB)
			processedForOutput[nodePath] = true
			processedForOutput[partner] = true
		} else {
			// Print individually using the text-based helper
			marker := ""
			outputStr := formatNodeForTopo(nodePath, modulesFoundInOwners) // Format fork info
			fmt.Printf("%s  - %s%s\n", indent, outputStr, marker)
			processedForOutput[nodePath] = true
		}
	}
}

// performTopologicalSortAndPrint performs Kahn's algorithm on the REVERSE graph
// printing levels starting with leaves, grouping cycles into their own level.
func performTopologicalSortAndPrint(modulesFoundInOwners map[string]*graph.ModuleInfo, nodesToGraph map[string]bool) {
	// --- Initial Setup ---
	log.Infof("Starting topological sort (leaves first)...")

	// Build forward adjacency list needed for bidirectional check
	adj := make(map[string]map[string]bool) // adj[source][dest] = true
	bidirPairs := make(map[string]string)   // Store A -> B if A < B
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
				// Check for bidirectional link (B depends on A)
				if otherInfo, ok := modulesFoundInOwners[dep]; ok && nodesToGraph[otherInfo.Path] {
					if _, dependsBack := otherInfo.Deps[sourceMod]; dependsBack {
						// Found B -> A as well
						isBidirNode[sourceMod] = true
						isBidirNode[dep] = true
						// Store pair consistently (e.g., always store A->B where A < B)
						if sourceMod < dep {
							bidirPairs[sourceMod] = dep
						} else {
							bidirPairs[dep] = sourceMod
						}
					}
				}
			}
		}
	}

	// Build reverse graph, get initial in-degrees, and detect cycle nodes
	nodesInCycles, initialInDegree, reverseAdj := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph)
	// Refine the cycle set *before* starting the main topo sort
	nodesInCycles = filterOutUnusedNodes(nodesInCycles, modulesFoundInOwners, nodesToGraph)

	// --- Kahn's Algorithm for Leveling ---
	runningInDegree := make(map[string]int)
	for node, degree := range initialInDegree {
		runningInDegree[node] = degree
	}

	queue := []string{}
	for node, degree := range runningInDegree {
		// Start with nodes that have no dependencies *and* are not part of a detected cycle
		if degree == 0 && !nodesInCycles[node] {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue)

	processedNodes := make(map[string]bool)     // Track processed nodes (acyclic, cycle, post-cycle)
	processedForOutput := make(map[string]bool) // Track nodes printed to avoid duplicates in A<->B pairs
	levelCounter := 0
	fmt.Println("Topological Sort Levels (Leaves First):")

	// 1. Process Acyclic Levels Before Cycles
	log.LogVf("Processing pre-cycle levels...")
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		currentLevelNodes := make([]string, 0, currentLevelSize)
		nextQueue := []string{}

		log.LogVf("  Queue for Level %d: %v", levelCounter, queue)

		for i := 0; i < currentLevelSize; i++ {
			u := queue[i]
			// Double check it's not a cycle node (shouldn't be, based on initial queue population)
			if nodesInCycles[u] {
				log.Warnf("Node %s found in queue but marked as cycle node. Skipping for now.", u)
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true

			// Process nodes that depend on u
			neighbors := reverseAdj[u] // Nodes that depend on u
			sort.Strings(neighbors)
			for _, v := range neighbors {
				// Only decrement degree if the dependent node v hasn't been processed yet
				if !processedNodes[v] && !nodesInCycles[v] { // Don't process nodes already processed or known cycle nodes yet
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s", v, u)
					}
				}
			}
		}

		// Print the completed level
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, bidirPairs, isBidirNode, processedForOutput, "")

		// Prepare for next level
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	} // End of pre-cycle levels loop

	// 2. Process the Cycle Level
	log.LogVf("Processing cycle level...")
	cycleNodesList := make([]string, 0, len(nodesInCycles))
	for node := range nodesInCycles {
		cycleNodesList = append(cycleNodesList, node)
		processedNodes[node] = true // Mark cycle nodes as processed
	}
	sort.Strings(cycleNodesList)

	if len(cycleNodesList) > 0 {
		// Print the cycle level
		printLevel(cycleNodesList, levelCounter, "", modulesFoundInOwners, bidirPairs, isBidirNode, processedForOutput, " (Cycles)")

		// Prepare queue for post-cycle levels:
		// Iterate through cycle nodes and decrement the degrees of their dependents.
		// Add dependents to the queue if their degree becomes 0.
		queue = []string{} // Reset queue
		for _, cycleNode := range cycleNodesList {
			dependents := reverseAdj[cycleNode] // Nodes that depend on this cycleNode
			sort.Strings(dependents)            // Ensure deterministic order
			for _, dependent := range dependents {
				if !processedNodes[dependent] { // If the dependent hasn't been processed
					runningInDegree[dependent]--
					if runningInDegree[dependent] == 0 {
						// Check if it's already queued to avoid duplicates
						alreadyQueued := false
						for _, qn := range queue {
							if qn == dependent {
								alreadyQueued = true
								break
							}
						}
						if !alreadyQueued {
							queue = append(queue, dependent)
						}
					} else if runningInDegree[dependent] < 0 {
						// This might happen if a node depends on multiple cycle nodes.
						// It should have been caught earlier if it depended only on cycle nodes,
						// but log it just in case.
						log.Warnf("In-degree for %s became negative after processing cycle node %s. Current degree: %d", dependent, cycleNode, runningInDegree[dependent])
					}
				}
			}
		}
		sort.Strings(queue)
		levelCounter++ // Increment level counter *after* cycle level
	} else {
		log.LogVf("No cycle nodes detected or remaining.")
	}

	// 3. Process Post-Cycle Levels
	log.LogVf("Processing post-cycle levels...")
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		currentLevelNodes := make([]string, 0, currentLevelSize)
		nextQueue := []string{}

		log.LogVf("  Queue for Level %d: %v", levelCounter, queue)

		for i := 0; i < currentLevelSize; i++ {
			u := queue[i]
			// Ensure it hasn't been processed somehow (e.g., added to queue multiple times)
			if processedNodes[u] {
				log.Warnf("Node %s found in post-cycle queue but already processed. Skipping.", u)
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true

			// Process nodes that depend on u
			neighbors := reverseAdj[u] // Nodes that depend on u
			sort.Strings(neighbors)
			for _, v := range neighbors {
				// Only decrement degree if the dependent node v hasn't been processed yet
				if !processedNodes[v] {
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s in post-cycle", v, u)
					}
				}
			}
		}

		// Print the completed level
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, bidirPairs, isBidirNode, processedForOutput, "")

		// Prepare for next level
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	} // End of post-cycle levels loop

	// Final Check: Ensure all nodes were processed
	if len(processedNodes) != len(nodesToGraph) {
		log.Warnf("Processed %d nodes, but expected %d. Some nodes might be unreachable or part of unhandled graph structures.", len(processedNodes), len(nodesToGraph))
		unprocessed := []string{}
		for node := range nodesToGraph {
			if !processedNodes[node] {
				unprocessed = append(unprocessed, node)
			}
		}
		sort.Strings(unprocessed)
		log.Warnf("Unprocessed nodes: %v", unprocessed)
	} else {
		log.Infof("Topological sort processed all %d nodes.", len(processedNodes))
	}
}

// --- End Topological Sort Logic ---

// --- End Graph Generation Logic ---
