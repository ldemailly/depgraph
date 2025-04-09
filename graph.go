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
// Returns: map[nodePath]bool indicating nodes in cycles, and the initial inDegree map.
func buildReverseGraphAndDetectCycles(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) (map[string]bool, map[string]int, map[string][]string) {
	reverseAdj := make(map[string][]string)
	inDegree := make(map[string]int)
	nodesInSort := []string{} // Nodes potentially involved in the sort/cycle check

	// Initialize in-degrees for all nodes intended for the graph.
	tempModules := make(map[string]*ModuleInfo) // Temporary map including all nodes in graph
	for nodePath := range nodesToGraph {
		inDegree[nodePath] = 0 // Initialize degree for all graph nodes
		nodesInSort = append(nodesInSort, nodePath)
		// Get the corresponding info, needed for dependency lookup below
		if info, exists := modulesFoundInOwners[nodePath]; exists {
			tempModules[nodePath] = info
		} else {
			tempModules[nodePath] = nil // Mark external nodes
		}
	}
	sort.Strings(nodesInSort)

	// Build reversed adjacency list and calculate initial in-degrees
	for sourceMod, sourceInfo := range tempModules {
		// Skip external nodes or nodes marked as external proxies
		// TreatAsExternal flag is not used here, cycle detection works on graph structure
		if sourceInfo == nil {
			continue
		}
		// Ensure source is actually in the graph nodes considered
		if !nodesToGraph[sourceMod] {
			continue
		}

		if _, exists := reverseAdj[sourceMod]; !exists {
			reverseAdj[sourceMod] = []string{}
		}
		depPaths := make([]string, 0, len(sourceInfo.Deps))
		for dep := range sourceInfo.Deps {
			depPaths = append(depPaths, dep)
		}
		sort.Strings(depPaths)

		for _, dep := range depPaths {
			// Only consider dependencies pointing to nodes within our graph set
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
	for node, degree := range inDegree { // Use inDegree calculated above
		tempInDegree[node] = degree
		if degree == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue) // Initial sort

	processedCount := 0
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		processedCount++
		neighbors := reverseAdj[u]
		sort.Strings(neighbors)
		for _, v := range neighbors { // For each node v that depends on u
			tempInDegree[v]--
			if tempInDegree[v] == 0 {
				queue = append(queue, v) // Add newly free node
			}
		}
		sort.Strings(queue) // Keep queue sorted if needed
	}

	// Identify nodes likely in cycles
	nodesInCycles := make(map[string]bool)
	if processedCount < len(nodesInSort) { // Compare against all nodes initially considered
		log.Warnf("Cycle detected in dependencies! Processed %d nodes, expected %d.", processedCount, len(nodesInSort))
		log.Warnf("Nodes likely involved in cycles (remaining in-degree > 0):")
		remainingNodes := []string{}
		for _, node := range nodesInSort {
			if tempInDegree[node] > 0 {
				remainingNodes = append(remainingNodes, node)
				nodesInCycles[node] = true // Add to the map for return
			}
		}
		sort.Strings(remainingNodes)
		for _, node := range remainingNodes {
			log.Warnf("  - %s (remaining reversed in-degree during cycle check: %d)", node, tempInDegree[node])
		}
	}
	return nodesInCycles, inDegree, reverseAdj
}

// isNodeDependedOn returns true if the given node is depended on by any other node
// *within* the set of nodes currently considered to be in cycles.
func isNodeDependedOn(node string, modulesFoundInOwners map[string]*ModuleInfo, currentNodesInCycles map[string]bool) bool {
	for _, info := range modulesFoundInOwners {
		// Skip nodes that aren't relevant
		// TreatAsExternal flag is not relevant for dependency structure check
		if info == nil || !currentNodesInCycles[info.Path] {
			continue
		}
		for dep := range info.Deps {
			if dep == node {
				return true
			}
		}
	}
	return false
}

// filterOutUnusedNodes removes nodes from the cycle set that are not depended upon
// by any *other* node *within the cycle set*.
func filterOutUnusedNodes(nodesInCycles map[string]bool, modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) map[string]bool {
	if len(nodesInCycles) == 0 {
		return nodesInCycles
	}
	log.LogVf("Refining cycle detection: Initial cycle candidates: %d", len(nodesInCycles))
	changed := true
	iteration := 0
	for changed {
		iteration++
		changed = false
		nodesToRemove := []string{}
		for node := range nodesInCycles {
			if !isNodeDependedOn(node, modulesFoundInOwners, nodesInCycles) {
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
// It now also returns a map indicating which forks (by repo path) were included
// *because* they depended on an included non-fork.
func determineNodesToGraph(modulesFoundInOwners map[string]*ModuleInfo, allModulePaths map[string]bool, noExt bool) (map[string]bool, map[string]bool) {
	nodesToGraph := make(map[string]bool)
	referencedModules := make(map[string]bool)         // Modules depended on by included nodes
	forksDependingOnNonFork := make(map[string]bool)   // Forks (by declared module path) that depend on an included non-fork
	forksIncludedByDependency := make(map[string]bool) // Key: Fork RepoPath

	// Pass 1: Add non-forks and collect their initial dependencies
	log.Infof("Determining graph nodes: Pass 1 (Non-forks)")
	for modPath, info := range modulesFoundInOwners {
		if info != nil && info.Fetched && !info.IsFork {
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
		if info != nil && info.Fetched && info.IsFork {
			for depPath := range info.Deps {
				if nodesToGraph[depPath] { // Check if the dependency is an included node
					depInfo, exists := modulesFoundInOwners[depPath]
					if exists && !depInfo.IsFork { // Verify the included node is actually a non-fork
						log.LogVf("  Marking fork '%s' (from %s) as depending on non-fork '%s'", modPath, info.RepoPath, depPath)
						forksDependingOnNonFork[modPath] = true
						forksIncludedByDependency[info.RepoPath] = true
						break
					}
				}
			}
		}
	}

	// Pass 3: Add fork nodes if they depend on non-forks OR (if !noExt) if their declared path is referenced
	log.Infof("Determining graph nodes: Pass 3 (Include qualifying Forks, noExt=%v)", noExt)
	for modPath, info := range modulesFoundInOwners {
		if info != nil && info.Fetched && info.IsFork {
			includeNode := false
			includeReason := ""
			depends := forksDependingOnNonFork[modPath]
			referenced := referencedModules[modPath]

			// *** MODIFIED Logic: Check noExt flag ***
			if depends {
				// Always include if it depends on an internal non-fork
				includeNode = true
				includeReason = "depends on non-fork"
			} else if referenced && !noExt {
				// Include if referenced ONLY if we are allowing external dependencies (-noext=false)
				includeNode = true
				includeReason = "referenced by included module (-noext=false)"
			} else if referenced && noExt {
				// Referenced, but -noext=true, so DO NOT include based on reference alone.
				includeReason = "referenced by included module (but excluded by -noext)"
				// includeNode remains false
			}
			// *** End MODIFIED Logic ***

			if includeNode {
				log.LogVf("  Including fork node for path '%s' (from %s) because: %s", modPath, info.RepoPath, includeReason)
				nodesToGraph[modPath] = true // Add the declared module path
				// Add dependencies of included forks to referenced set
				for depPath := range info.Deps {
					if !referencedModules[depPath] {
						log.LogVf("    Now referencing (from included fork): %s", depPath)
						referencedModules[depPath] = true
					}
				}
			} else if includeReason != "" { // Log if it met criteria but was excluded by -noext
				log.LogVf("  Skipping fork node for path '%s' (from %s) because: %s", modPath, info.RepoPath, includeReason)
			}
		}
	}

	// Pass 4: Add external dependencies if needed (only runs if noExt is false)
	log.Infof("Determining graph nodes: Pass 4 (External dependencies, noExt=%v)", noExt)
	if !noExt {
		for modPath := range allModulePaths {
			// Check if it's truly external (not in map) OR if the entry in map is a fork not included by dependency,
			// AND it's referenced, AND not already added
			info, foundInMap := modulesFoundInOwners[modPath]
			// A path is considered external if not found OR if found but it's a fork not included by dependency
			isConsideredExternal := !foundInMap || (info != nil && info.IsFork && !forksIncludedByDependency[info.RepoPath])

			if isConsideredExternal && referencedModules[modPath] && !nodesToGraph[modPath] {
				log.LogVf("  Including external/proxy node: %s (referenced)", modPath)
				nodesToGraph[modPath] = true
			}
		}
	}
	log.Infof("Total nodes included in graph: %d", len(nodesToGraph))
	return nodesToGraph, forksIncludedByDependency
}

// generateDotOutput generates the DOT graph representation and prints it to stdout
// Accepts forksIncludedByDependency map to control styling.
func generateDotOutput(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool, forksIncludedByDependency map[string]bool, noExt bool, left2Right bool) {
	// --- Detect Cycles ---
	nodesInCyclesSet, _, _ := buildReverseGraphAndDetectCycles(modulesFoundInOwners, nodesToGraph)
	nodesInCyclesSet = filterOutUnusedNodes(nodesInCyclesSet, modulesFoundInOwners, nodesToGraph)
	// --- End Detect Cycles ---

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
		isStyledAsInternal := false // Track if styled as non-external

		info, foundInScannedMap := modulesFoundInOwners[nodePath]

		// Determine attributes based on the info found (or not found)
		if foundInScannedMap && info != nil {
			if !info.IsFork {
				// It's a non-fork found in owners
				isStyledAsInternal = true
				ownerIdx := info.OwnerIdx
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
				// Label remains nodePath for non-forks
			} else {
				// It's a fork found in owners. Style based on inclusion reason.
				if forksIncludedByDependency[info.RepoPath] {
					// Style as internal fork
					isStyledAsInternal = true
					ownerIdx := info.OwnerIdx
					color = orgForkColors[ownerIdx%len(orgForkColors)]
					// Use multi-line fork label using RepoPath
					if info.OriginalModulePath != "" {
						label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						label = fmt.Sprintf("%s\\n(fork)", info.RepoPath)
					}
				} else {
					// Fork info exists, but it wasn't included via dependency. Treat node as external.
					isStyledAsInternal = false
					color = externalColor
					label = nodePath // Use module path label
					log.LogVf("Styling node '%s' as external (fork '%s' included but not via dependency)", nodePath, info.RepoPath)
				}
			}
		}
		// Else (not found in map): Treat as external (label=nodePath, color=externalColor)

		// If -noext is true, we should have already excluded this node in determineNodesToGraph if it ended up external.
		// This check is a safeguard.
		if !isStyledAsInternal && noExt {
			log.Warnf("Node %s styled as external and -noext is true. Skipping node definition.", nodePath)
			continue
		}

		// Escape label for DOT format
		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("label=\"%s\"", escapedLabel))
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("fillcolor=\"%s\"", color))

		// Highlight border if node is part of a refined cycle AND styled as internal
		if isStyledAsInternal && nodesInCyclesSet[nodePath] {
			log.LogVf("Highlighting cycle node in DOT: %s", nodePath)
			nodeAttrs = append(nodeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor)) // Set border color
			nodeAttrs = append(nodeAttrs, "penwidth=2")
		}

		fmt.Printf("  \"%s\" [%s];\n", nodePath, strings.Join(nodeAttrs, ", "))
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Iterate through the modules map to find sources of edges
	sourceModulesInGraph := []string{}
	for modPath, info := range modulesFoundInOwners {
		// Only draw edges *from* nodes that are included in the graph
		// AND are styled as internal (non-fork or fork included by dependency)
		isSourceStyledInternal := false
		if info != nil {
			if !info.IsFork {
				isSourceStyledInternal = true
			} else if forksIncludedByDependency[info.RepoPath] {
				isSourceStyledInternal = true
			}
		}

		if isSourceStyledInternal && nodesToGraph[modPath] {
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

	// Print edges
	for _, sourceModPath := range sourceModulesInGraph {
		info := modulesFoundInOwners[sourceModPath] // Should be non-nil and styled internal
		if info == nil {
			continue // Should not happen
		}

		depPaths := make([]string, 0, len(info.Deps))
		for depPath := range info.Deps {
			depPaths = append(depPaths, depPath)
		}
		sort.Strings(depPaths)

		for _, depPath := range depPaths {
			// Only draw edge if the target module path is included in the graph nodes
			if nodesToGraph[depPath] {
				version := info.Deps[depPath]
				escapedVersion := strings.ReplaceAll(version, "\"", "\\\"")
				edgeAttrs := []string{fmt.Sprintf("label=\"%s\"", escapedVersion)}

				// Highlight edge if both source and target are styled internal and in the cycle set
				targetInfo, _ := modulesFoundInOwners[depPath]
				isTargetStyledInternal := false
				if targetInfo != nil {
					if !targetInfo.IsFork {
						isTargetStyledInternal = true
					} else if forksIncludedByDependency[targetInfo.RepoPath] {
						isTargetStyledInternal = true
					}
				}

				// Source is internal by definition of outer loop
				if isTargetStyledInternal && nodesInCyclesSet[sourceModPath] && nodesInCyclesSet[depPath] {
					edgeAttrs = append(edgeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor))
					edgeAttrs = append(edgeAttrs, "penwidth=1.5")
				}

				// Edge always goes from source module path to target module path
				fmt.Printf("  \"%s\" -> \"%s\" [%s];\n", sourceModPath, depPath, strings.Join(edgeAttrs, ", "))
			}
		}
	}

	fmt.Println("}")
	// --- End Generate DOT Output ---
}

// --- Topological Sort Logic ---

// Helper function to format node output for topo sort (SINGLE LINE format)
// Needs to be updated to reflect the stricter fork styling rule
func formatNodeForTopo(nodePath string, modulesFoundInOwners map[string]*ModuleInfo, forksIncludedByDependency map[string]bool) string {
	// Default output is module path
	outputStr := nodePath
	// Look up info to customize output
	info, exists := modulesFoundInOwners[nodePath]

	if exists && info != nil {
		if !info.IsFork {
			// Non-fork, label is just the module path
			outputStr = info.Path // Use declared path
		} else {
			// Fork - check if it was included by dependency
			if forksIncludedByDependency[info.RepoPath] {
				// Style as internal fork
				outputStr = info.RepoPath // Start with repo path
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
					}
				} else {
					outputStr = fmt.Sprintf("%s (fork)", info.RepoPath)
				}
			} else {
				// Fork exists but not included by dependency - treat as external (label is module path)
				outputStr = nodePath
			}
		}
	}
	// Else (external), label remains nodePath (module path)
	return outputStr
}

// printLevel prints a single level of the topological sort, handling A<->B pairs.
// Needs forksIncludedByDependency map to pass to formatNodeForTopo
func printLevel(levelNodes []string, levelIndex int, indent string, modulesFoundInOwners map[string]*ModuleInfo, forksIncludedByDependency map[string]bool, bidirPairs map[string]string, isBidirNode map[string]bool, processedForOutput map[string]bool, levelName string) {
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
			formattedA := formatNodeForTopo(nodePath, modulesFoundInOwners, forksIncludedByDependency)
			formattedB := formatNodeForTopo(partner, modulesFoundInOwners, forksIncludedByDependency)
			fmt.Printf("%s  - %s <-> %s\n", indent, formattedA, formattedB)
			processedForOutput[nodePath] = true
			processedForOutput[partner] = true
		} else {
			// Print individually using the text-based helper
			marker := ""
			outputStr := formatNodeForTopo(nodePath, modulesFoundInOwners, forksIncludedByDependency) // Format fork/non-fork/external info
			fmt.Printf("%s  - %s%s\n", indent, outputStr, marker)
			processedForOutput[nodePath] = true
		}
	}
}

// performTopologicalSortAndPrint performs Kahn's algorithm on the REVERSE graph
// Needs forksIncludedByDependency map from determineNodesToGraph
func performTopologicalSortAndPrint(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool, forksIncludedByDependency map[string]bool) {
	// --- Initial Setup ---
	log.Infof("Starting topological sort (leaves first)...")

	// Build forward adjacency list needed for bidirectional check
	bidirPairs := make(map[string]string) // Store A -> B if A < B
	isBidirNode := make(map[string]bool)  // Mark nodes involved in any A<->B pair

	// Need to iterate through the actual stored info to check dependencies
	for sourceMod, sourceInfo := range modulesFoundInOwners {
		// Check if source is styled as internal
		isSourceStyledInternal := false
		if sourceInfo != nil {
			if !sourceInfo.IsFork {
				isSourceStyledInternal = true
			} else if forksIncludedByDependency[sourceInfo.RepoPath] {
				isSourceStyledInternal = true
			}
		}
		if !isSourceStyledInternal || !nodesToGraph[sourceMod] {
			continue
		} // Skip sources not styled internal or not in graph

		for dep := range sourceInfo.Deps {
			if nodesToGraph[dep] {
				// Check for bidirectional link (B depends on A)
				depInfo, exists := modulesFoundInOwners[dep]
				// Check if target is styled internal
				isTargetStyledInternal := false
				if exists && depInfo != nil {
					if !depInfo.IsFork {
						isTargetStyledInternal = true
					} else if forksIncludedByDependency[depInfo.RepoPath] {
						isTargetStyledInternal = true
					}
				}

				if isTargetStyledInternal { // Consider link only if dependency is styled internal
					if _, dependsBack := depInfo.Deps[sourceMod]; dependsBack {
						// Found B -> A as well
						isBidirNode[sourceMod] = true
						isBidirNode[dep] = true
						// Store pair consistently
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
	nodesInCycles = filterOutUnusedNodes(nodesInCycles, modulesFoundInOwners, nodesToGraph)
	// --- End Setup ---

	// --- Kahn's Algorithm for Leveling ---
	runningInDegree := make(map[string]int)
	for node, degree := range initialInDegree {
		runningInDegree[node] = degree
	}

	queue := []string{}
	for node, degree := range runningInDegree {
		// Start with nodes that have no dependencies *and* are not part of a detected cycle
		// AND are included in the final graph set
		if nodesToGraph[node] && degree == 0 && !nodesInCycles[node] {
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
			if nodesInCycles[u] {
				log.Warnf("Node %s found in queue but marked as cycle node. Skipping for now.", u)
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true

			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				if nodesToGraph[v] && !processedNodes[v] && !nodesInCycles[v] {
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s", v, u)
					}
				}
			}
		}
		// Pass forksIncludedByDependency to printLevel
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, forksIncludedByDependency, bidirPairs, isBidirNode, processedForOutput, "")
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	}

	// 2. Process the Cycle Level
	log.LogVf("Processing cycle level...")
	cycleNodesList := make([]string, 0, len(nodesInCycles))
	for node := range nodesInCycles {
		if nodesToGraph[node] {
			cycleNodesList = append(cycleNodesList, node)
			processedNodes[node] = true
		} else {
			log.Warnf("Node %s detected in cycle but not included in final graph nodes. Ignoring.", node)
		}
	}
	sort.Strings(cycleNodesList)

	if len(cycleNodesList) > 0 {
		// Pass forksIncludedByDependency to printLevel
		printLevel(cycleNodesList, levelCounter, "", modulesFoundInOwners, forksIncludedByDependency, bidirPairs, isBidirNode, processedForOutput, " (Cycles)")
		queue = []string{} // Reset queue
		for _, cycleNode := range cycleNodesList {
			dependents := reverseAdj[cycleNode]
			sort.Strings(dependents)
			for _, dependent := range dependents {
				if nodesToGraph[dependent] && !processedNodes[dependent] {
					runningInDegree[dependent]--
					if runningInDegree[dependent] == 0 {
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
						log.Warnf("In-degree for %s became negative after processing cycle node %s. Current degree: %d", dependent, cycleNode, runningInDegree[dependent])
					}
				}
			}
		}
		sort.Strings(queue)
		levelCounter++
	} else {
		log.LogVf("No cycle nodes detected or remaining in the graph.")
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
			if processedNodes[u] {
				log.Warnf("Node %s found in post-cycle queue but already processed. Skipping.", u)
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true

			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				if nodesToGraph[v] && !processedNodes[v] {
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s in post-cycle", v, u)
					}
				}
			}
		}
		// Pass forksIncludedByDependency to printLevel
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, forksIncludedByDependency, bidirPairs, isBidirNode, processedForOutput, "")
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	}

	// Final Check
	processedGraphNodesCount := 0
	for node := range nodesToGraph {
		if processedNodes[node] {
			processedGraphNodesCount++
		}
	}
	if processedGraphNodesCount != len(nodesToGraph) {
		log.Warnf("Processed %d nodes, but expected %d graph nodes.", processedGraphNodesCount, len(nodesToGraph))
		unprocessed := []string{}
		for node := range nodesToGraph {
			if !processedNodes[node] {
				unprocessed = append(unprocessed, node)
			}
		}
		sort.Strings(unprocessed)
		log.Warnf("Unprocessed graph nodes: %v", unprocessed)
	} else {
		log.Infof("Topological sort processed all %d graph nodes.", len(nodesToGraph))
	}
}

// --- End Topological Sort Logic ---

// --- End Graph Generation Logic ---
