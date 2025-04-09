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

// buildReverseGraphAndDetectCycles builds the reversed graph for cycle detection.
// Operates on the nodes provided in nodesToGraph.
func buildReverseGraphAndDetectCycles(modulesFoundInOwners map[string]*ModuleInfo, nodesToGraph map[string]bool) (map[string]bool, map[string]int, map[string][]string) {
	reverseAdj := make(map[string][]string)
	inDegree := make(map[string]int)
	nodesInSort := []string{} // Nodes included in this specific graph build

	// Initialize in-degrees only for nodes present in the nodesToGraph map
	for nodePath := range nodesToGraph {
		inDegree[nodePath] = 0
		nodesInSort = append(nodesInSort, nodePath)
	}
	sort.Strings(nodesInSort)

	// Build reversed adjacency list and calculate initial in-degrees based *only* on included nodes
	for sourceMod := range nodesToGraph {
		sourceInfo, exists := modulesFoundInOwners[sourceMod]
		// Skip external nodes when calculating internal graph structure for cycles
		if !exists || sourceInfo == nil {
			continue // Skip truly external nodes as edge sources
		}
		// NOTE: We assume ALL nodes in nodesToGraph (including forks not styled internal)
		// contribute edges for the purpose of cycle detection structure.
		// The styling/filtering happens later.

		if _, existsAdj := reverseAdj[sourceMod]; !existsAdj {
			reverseAdj[sourceMod] = []string{}
		}
		depPaths := make([]string, 0, len(sourceInfo.Deps))
		for dep := range sourceInfo.Deps {
			depPaths = append(depPaths, dep)
		}
		sort.Strings(depPaths)

		for _, dep := range depPaths {
			// Only consider dependencies pointing to nodes also within our graph set
			if nodesToGraph[dep] {
				if _, existsAdjDep := reverseAdj[dep]; !existsAdjDep {
					reverseAdj[dep] = []string{}
				}
				reverseAdj[dep] = append(reverseAdj[dep], sourceMod)
				inDegree[sourceMod]++
			}
		}
	}

	// --- Kahn's Algorithm for Cycle Detection ---
	queue := []string{}
	tempInDegree := make(map[string]int)
	for node, degree := range inDegree { // Use degrees calculated for nodes in nodesToGraph
		tempInDegree[node] = degree
		if degree == 0 {
			queue = append(queue, node)
		}
	}
	sort.Strings(queue)

	processedCount := 0
	for len(queue) > 0 {
		u := queue[0]
		queue = queue[1:]
		processedCount++
		neighbors := reverseAdj[u]
		sort.Strings(neighbors)
		for _, v := range neighbors {
			tempInDegree[v]--
			if tempInDegree[v] == 0 {
				queue = append(queue, v)
			}
		}
		sort.Strings(queue)
	}

	// Identify nodes likely in cycles
	nodesInCycles := make(map[string]bool)
	if processedCount < len(nodesInSort) { // Compare against nodes passed in nodesToGraph
		log.Warnf("Cycle detected in dependencies! Processed %d nodes, expected %d.", processedCount, len(nodesInSort))
		log.Warnf("Nodes likely involved in cycles (remaining in-degree > 0):")
		remainingNodes := []string{}
		for _, node := range nodesInSort {
			if tempInDegree[node] > 0 {
				remainingNodes = append(remainingNodes, node)
				nodesInCycles[node] = true
			}
		}
		sort.Strings(remainingNodes)
		for _, node := range remainingNodes {
			log.Warnf("  - %s (remaining reversed in-degree during cycle check: %d)", node, tempInDegree[node])
		}
	}
	return nodesInCycles, inDegree, reverseAdj
}

// isNodeDependedOn checks dependencies within the cycle set
func isNodeDependedOn(node string, modulesFoundInOwners map[string]*ModuleInfo, currentNodesInCycles map[string]bool) bool {
	for modPath, info := range modulesFoundInOwners {
		// Check only nodes that are part of the current cycle detection
		if info == nil || !currentNodesInCycles[modPath] {
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

// filterOutUnusedNodes refines the cycle set
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
			// Check if this node is depended on by *any other node* currently in the `nodesInCycles` set
			// Pass nodesToGraph for context if needed, though current implementation doesn't use it
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

// determineNodesToGraph calculates the set of nodes potentially included and
// which forks depend *specifically* on internal non-forks.
// It returns the full potential graph (incl. referenced forks/externals if !noExt).
// Filtering for -noext is done in the output functions.
func determineNodesToGraph(modulesFoundInOwners map[string]*ModuleInfo, allModulePaths map[string]bool, noExt bool) (map[string]bool, map[string]bool) {
	initialNodesToGraph := make(map[string]bool)
	referencedModules := make(map[string]bool)
	// Map tracks forks depending specifically on *internal* non-forks found in Pass 1
	forkRepoPathsDependingOnInternalNonFork := make(map[string]bool) // Key: Fork RepoPath

	// Pass 1: Add internal non-forks and collect paths referenced by them.
	log.Infof("Determining graph nodes: Pass 1 (Internal Non-forks)")
	internalNonForkPaths := make(map[string]bool) // Track paths declared by internal non-forks
	for modPath, info := range modulesFoundInOwners {
		if info != nil && info.Fetched && !info.IsFork {
			log.LogVf("  Including internal non-fork: %s", modPath)
			initialNodesToGraph[modPath] = true
			internalNonForkPaths[modPath] = true // Mark this path as provided by an internal non-fork
			for depPath := range info.Deps {
				log.LogVf("    References: %s", depPath)
				referencedModules[depPath] = true
			}
		}
	}

	// Pass 2: Identify forks that depend *specifically* on internal non-forks found in Pass 1.
	log.Infof("Determining graph nodes: Pass 2 (Identify forks depending on internal non-forks)")
	for _, info := range modulesFoundInOwners { // Iterate all found modules
		if info != nil && info.Fetched && info.IsFork { // If it's a fork
			for depPath := range info.Deps { // Check its dependencies
				// If dependency is an internal non-fork path found in Pass 1
				if internalNonForkPaths[depPath] {
					log.LogVf("  Marking fork '%s' (from %s) as depending on internal non-fork '%s'", info.Path, info.RepoPath, depPath)
					forkRepoPathsDependingOnInternalNonFork[info.RepoPath] = true
					break // Mark based on first internal non-fork dependency
				}
			}
		}
	}

	// Pass 3: Add fork nodes if they depend on internal non-forks OR if their declared path is referenced
	log.Infof("Determining graph nodes: Pass 3 (Include qualifying Forks initially)")
	for modPath, info := range modulesFoundInOwners {
		if info != nil && info.Fetched && info.IsFork {
			includeNode := false
			includeReason := ""
			// Check the map calculated in Pass 2
			depends := forkRepoPathsDependingOnInternalNonFork[info.RepoPath]
			referenced := referencedModules[modPath]

			if depends {
				includeNode = true
				includeReason = "depends on internal non-fork"
			} else if referenced {
				includeNode = true
				includeReason = "referenced by included module"
			}

			if includeNode {
				log.LogVf("  Initially including fork node for path '%s' (from %s) because: %s", modPath, info.RepoPath, includeReason)
				initialNodesToGraph[modPath] = true // Add the declared module path
				// Add dependencies of included forks to referenced set
				for depPath := range info.Deps {
					if !referencedModules[depPath] {
						log.LogVf("    Now referencing (from initially included fork): %s", depPath)
						referencedModules[depPath] = true
					}
				}
			}
		}
	}

	// Pass 4: Add external dependencies if needed (always calculate, filter in output)
	log.Infof("Determining graph nodes: Pass 4 (Include External dependencies initially)")
	for modPath := range allModulePaths {
		_, foundInMap := modulesFoundInOwners[modPath]
		isConsideredExternal := !foundInMap // If not in map, it's external
		if isConsideredExternal && referencedModules[modPath] && !initialNodesToGraph[modPath] {
			log.LogVf("  Initially including external node: %s (referenced)", modPath)
			initialNodesToGraph[modPath] = true
		}
	}

	log.Infof("Total nodes determined before filtering: %d", len(initialNodesToGraph))
	// Return the potentially larger set and the CORRECTED dependency map
	return initialNodesToGraph, forkRepoPathsDependingOnInternalNonFork
}

// generateDotOutput generates the DOT graph representation and prints it to stdout
// Applies -noext filtering during generation using the corrected map.
func generateDotOutput(modulesFoundInOwners map[string]*ModuleInfo, initialNodesToGraph map[string]bool, forksIncludedByInternalDependency map[string]bool, noExt bool, left2Right bool) { // Renamed map parameter

	// --- Filter nodes based on noExt ---
	finalNodesToGraph := make(map[string]bool)
	if noExt {
		log.Infof("Applying -noext filter for DOT output...")
		for nodePath := range initialNodesToGraph {
			_, foundInMap := modulesFoundInOwners[nodePath]
			if foundInMap { // Keep only nodes that were found in scanned owners
				finalNodesToGraph[nodePath] = true
			} else {
				log.LogVf("Filtering out external node %s due to -noext", nodePath)
			}
		}
		log.Infof("Nodes included in DOT after -noext filter: %d", len(finalNodesToGraph))
	} else {
		finalNodesToGraph = initialNodesToGraph // Use all nodes if noExt is false
		log.Infof("Nodes included in DOT (-noext=false): %d", len(finalNodesToGraph))
	}
	// --- End Filter ---

	// --- Detect Cycles ---
	// Run cycle detection only on the *final* set of nodes for this output
	nodesInCyclesSet, _, _ := buildReverseGraphAndDetectCycles(modulesFoundInOwners, finalNodesToGraph)
	nodesInCyclesSet = filterOutUnusedNodes(nodesInCyclesSet, modulesFoundInOwners, finalNodesToGraph)
	// --- End Detect Cycles ---

	// --- Generate DOT Output ---
	fmt.Println("digraph dependencies {")
	rankDir := "TB"
	if left2Right {
		rankDir = "LR"
	}
	fmt.Printf("  rankdir=\"%s\";\n", rankDir)
	fmt.Println("  node [shape=box, style=\"rounded,filled\", fontname=\"Helvetica\"];")
	fmt.Println("  edge [fontname=\"Helvetica\", fontsize=10];")

	fmt.Println("\n  // Node Definitions")
	// Iterate final nodes for definitions
	sortedNodes := make([]string, 0, len(finalNodesToGraph))
	for nodePath := range finalNodesToGraph {
		sortedNodes = append(sortedNodes, nodePath)
	}
	sort.Strings(sortedNodes)

	for _, nodePath := range sortedNodes {
		label := nodePath
		color := externalColor
		nodeAttrs := []string{}
		isStyledAsInternal := false

		info, foundInScannedMap := modulesFoundInOwners[nodePath] // Should always be true if filter worked

		// Determine styling based on info
		if foundInScannedMap && info != nil { // Check info just in case
			if !info.IsFork {
				isStyledAsInternal = true
				ownerIdx := info.OwnerIdx
				color = orgNonForkColors[ownerIdx%len(orgNonForkColors)]
			} else { // It's a fork
				// Use the CORRECTED map name here
				if forksIncludedByInternalDependency[info.RepoPath] { // Style as internal fork only if included via *internal* dependency
					isStyledAsInternal = true
					ownerIdx := info.OwnerIdx
					color = orgForkColors[ownerIdx%len(orgForkColors)]
					if info.OriginalModulePath != "" {
						label = fmt.Sprintf("%s\\n(fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						label = fmt.Sprintf("%s\\n(fork)", info.RepoPath)
					}
				} else { // Style as external (grey) because not included via internal dependency
					isStyledAsInternal = false
					color = externalColor
					label = nodePath
					log.LogVf("Styling node '%s' as external (fork '%s' included but not via internal dependency)", nodePath, info.RepoPath)
				}
			}
		} else { // Should not happen if filter worked
			log.Warnf("Node %s in final graph but missing info. Styling external.", nodePath)
			isStyledAsInternal = false
			color = externalColor
			label = nodePath
		}

		// Escape label and add attributes
		escapedLabel := strings.ReplaceAll(label, "\"", "\\\"")
		nodeAttrs = append(nodeAttrs, fmt.Sprintf("label=\"%s\"", escapedLabel), fmt.Sprintf("fillcolor=\"%s\"", color))
		if isStyledAsInternal && nodesInCyclesSet[nodePath] {
			nodeAttrs = append(nodeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor), "penwidth=2")
		}
		fmt.Printf("  \"%s\" [%s];\n", nodePath, strings.Join(nodeAttrs, ", "))
	}

	fmt.Println("\n  // Edges (Dependencies)")
	// Iterate sources that are styled internal AND in the final graph
	sourceModulesInGraph := []string{}
	for modPath, info := range modulesFoundInOwners {
		isSourceStyledInternal := false
		if info != nil {
			if !info.IsFork {
				isSourceStyledInternal = true
				// Use the CORRECTED map name here
			} else if forksIncludedByInternalDependency[info.RepoPath] {
				isSourceStyledInternal = true
			}
		}
		if isSourceStyledInternal && finalNodesInGraph[modPath] { // Check finalNodesInGraph
			sourceModulesInGraph = append(sourceModulesInGraph, modPath)
		}
	}
	sort.Strings(sourceModulesInGraph)

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
			if finalNodesInGraph[depPath] { // Draw edge only if target is in final graph
				version := info.Deps[depPath]
				escapedVersion := strings.ReplaceAll(version, "\"", "\\\"")
				edgeAttrs := []string{fmt.Sprintf("label=\"%s\"", escapedVersion)}
				targetInfo, _ := modulesFoundInOwners[depPath]
				isTargetStyledInternal := false
				if targetInfo != nil {
					if !targetInfo.IsFork {
						isTargetStyledInternal = true
						// Use the CORRECTED map name here
					} else if forksIncludedByInternalDependency[targetInfo.RepoPath] {
						isTargetStyledInternal = true
					}
				}
				// Source is internal by definition
				if isTargetStyledInternal && nodesInCyclesSet[sourceModPath] && nodesInCyclesSet[depPath] {
					edgeAttrs = append(edgeAttrs, fmt.Sprintf("color=\"%s\"", cycleColor), "penwidth=1.5")
				}
				fmt.Printf("  \"%s\" -> \"%s\" [%s];\n", sourceModPath, depPath, strings.Join(edgeAttrs, ", "))
			}
		}
	}
	fmt.Println("}")
}

// --- Topological Sort Logic ---

// Helper function to format node output for topo sort (SINGLE LINE format)
// Needs the corrected map name
func formatNodeForTopo(nodePath string, modulesFoundInOwners map[string]*ModuleInfo, forksIncludedByInternalDependency map[string]bool) string { // Renamed map parameter
	outputStr := nodePath
	info, exists := modulesFoundInOwners[nodePath]
	if exists && info != nil {
		if !info.IsFork {
			outputStr = info.Path
		} else { // Fork
			// Use the CORRECTED map name here
			if forksIncludedByInternalDependency[info.RepoPath] { // Style as internal fork
				outputStr = info.RepoPath
				if info.OriginalModulePath != "" {
					if info.Path == info.OriginalModulePath {
						outputStr = fmt.Sprintf("%s (fork of %s)", info.RepoPath, info.OriginalModulePath)
					} else {
						outputStr = fmt.Sprintf("%s (%s fork of %s)", info.RepoPath, info.Path, info.OriginalModulePath)
					}
				} else {
					outputStr = fmt.Sprintf("%s (fork)", info.RepoPath)
				}
			} // Else: Treat as external, label remains nodePath
		}
	} // Else (external): label remains nodePath
	return outputStr
}

// printLevel needs the corrected map name
func printLevel(levelNodes []string, levelIndex int, indent string, modulesFoundInOwners map[string]*ModuleInfo, forksIncludedByInternalDependency map[string]bool, bidirPairs map[string]string, isBidirNode map[string]bool, processedForOutput map[string]bool, levelName string) { // Renamed map parameter
	if len(levelNodes) == 0 {
		return
	}
	fmt.Printf("%sLevel %d%s:\n", indent, levelIndex, levelName)
	levelSet := make(map[string]bool)
	for _, node := range levelNodes {
		levelSet[node] = true
	}
	sortedLevelNodes := make([]string, len(levelNodes))
	copy(sortedLevelNodes, levelNodes)
	sort.Strings(sortedLevelNodes)
	for _, nodePath := range sortedLevelNodes {
		if processedForOutput[nodePath] {
			continue
		}
		partner, isPairStart := bidirPairs[nodePath]
		_, partnerInLevel := levelSet[partner]
		if isPairStart && partnerInLevel { // A<->B pair
			// Pass CORRECTED map name
			formattedA := formatNodeForTopo(nodePath, modulesFoundInOwners, forksIncludedByInternalDependency)
			formattedB := formatNodeForTopo(partner, modulesFoundInOwners, forksIncludedByInternalDependency)
			fmt.Printf("%s  - %s <-> %s\n", indent, formattedA, formattedB)
			processedForOutput[nodePath] = true
			processedForOutput[partner] = true
		} else { // Print individually
			marker := ""
			outputStr := formatNodeForTopo(nodePath, modulesFoundInOwners, forksIncludedByInternalDependency) // Pass CORRECTED map name
			fmt.Printf("%s  - %s%s\n", indent, outputStr, marker)
			processedForOutput[nodePath] = true
		}
	}
}

// performTopologicalSortAndPrint applies -noext filtering internally.
// Needs the corrected map name
func performTopologicalSortAndPrint(modulesFoundInOwners map[string]*ModuleInfo, initialNodesToGraph map[string]bool, forksIncludedByInternalDependency map[string]bool, noExt bool) { // Renamed map parameter
	log.Infof("Starting topological sort (leaves first)...")

	// --- Filter nodes based on noExt ---
	finalNodesToGraph := make(map[string]bool)
	if noExt {
		log.Infof("Applying -noext filter for topological sort...")
		for nodePath := range initialNodesToGraph {
			info, foundInMap := modulesFoundInOwners[nodePath]
			keepNode := false
			isStyledInternal := false
			if foundInMap && info != nil {
				if !info.IsFork {
					isStyledInternal = true
					// Use the CORRECTED map name here
				} else if forksIncludedByInternalDependency[info.RepoPath] {
					isStyledInternal = true
				}
			}
			if isStyledInternal {
				keepNode = true
			} // Keep only nodes styled internal
			if keepNode {
				finalNodesToGraph[nodePath] = true
			}
		}
		log.Infof("Nodes included in topo sort after -noext filter: %d", len(finalNodesToGraph))
	} else {
		finalNodesToGraph = initialNodesToGraph
		log.Infof("Nodes included in topo sort (-noext=false): %d", len(finalNodesToGraph))
	}
	// --- End Filter ---

	// --- Initial Setup ---
	bidirPairs := make(map[string]string)
	isBidirNode := make(map[string]bool)
	for sourceMod, sourceInfo := range modulesFoundInOwners {
		isSourceStyledInternal := false
		if sourceInfo != nil {
			if !sourceInfo.IsFork {
				isSourceStyledInternal = true
			} else if forksIncludedByInternalDependency[sourceInfo.RepoPath] {
				isSourceStyledInternal = true
			}
		} // Use CORRECTED map name
		if !isSourceStyledInternal || !finalNodesToGraph[sourceMod] {
			continue
		} // Check finalNodesToGraph
		for dep := range sourceInfo.Deps {
			if finalNodesToGraph[dep] { // Check finalNodesToGraph
				depInfo, exists := modulesFoundInOwners[dep]
				isTargetStyledInternal := false
				if exists && depInfo != nil {
					if !depInfo.IsFork {
						isTargetStyledInternal = true
					} else if forksIncludedByInternalDependency[depInfo.RepoPath] {
						isTargetStyledInternal = true
					}
				} // Use CORRECTED map name
				if isTargetStyledInternal {
					if _, dependsBack := depInfo.Deps[sourceMod]; dependsBack {
						isBidirNode[sourceMod] = true
						isBidirNode[dep] = true
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

	nodesInCycles, initialInDegree, reverseAdj := buildReverseGraphAndDetectCycles(modulesFoundInOwners, finalNodesToGraph) // USE filtered nodes
	nodesInCycles = filterOutUnusedNodes(nodesInCycles, modulesFoundInOwners, finalNodesToGraph)                            // USE filtered nodes
	// --- End Setup ---

	// --- Kahn's Algorithm for Leveling ---
	runningInDegree := make(map[string]int)
	for node, degree := range initialInDegree {
		runningInDegree[node] = degree
	} // Use degrees for final nodes
	queue := []string{}
	for node := range finalNodesToGraph {
		degree, ok := runningInDegree[node]
		if ok && degree == 0 && !nodesInCycles[node] {
			queue = append(queue, node)
		} else if !ok && !nodesInCycles[node] && len(finalNodesToGraph) > 0 {
			log.Warnf("Node %s in final graph but missing from initial degree map.", node)
		}
	}
	sort.Strings(queue)
	processedNodes := make(map[string]bool)
	processedForOutput := make(map[string]bool)
	levelCounter := 0
	fmt.Println("Topological Sort Levels (Leaves First):")

	// 1. Process Acyclic Levels
	log.LogVf("Processing pre-cycle levels...")
	for len(queue) > 0 {
		currentLevelSize := len(queue)
		currentLevelNodes := make([]string, 0, currentLevelSize)
		nextQueue := []string{}
		log.LogVf("  Queue for Level %d: %v", levelCounter, queue)
		for i := 0; i < currentLevelSize; i++ {
			u := queue[i]
			if nodesInCycles[u] {
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true
			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				if finalNodesToGraph[v] && !processedNodes[v] && !nodesInCycles[v] {
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s", v, u)
					}
				}
			}
		}
		// Pass CORRECTED map name
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, forksIncludedByInternalDependency, bidirPairs, isBidirNode, processedForOutput, "")
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	}

	// 2. Process Cycle Level
	log.LogVf("Processing cycle level...")
	cycleNodesList := make([]string, 0, len(nodesInCycles))
	for node := range nodesInCycles {
		if finalNodesToGraph[node] {
			cycleNodesList = append(cycleNodesList, node)
			processedNodes[node] = true
		}
	}
	sort.Strings(cycleNodesList)
	if len(cycleNodesList) > 0 {
		// Pass CORRECTED map name
		printLevel(cycleNodesList, levelCounter, "", modulesFoundInOwners, forksIncludedByInternalDependency, bidirPairs, isBidirNode, processedForOutput, " (Cycles)")
		queue = []string{}
		for _, cycleNode := range cycleNodesList {
			dependents := reverseAdj[cycleNode]
			sort.Strings(dependents)
			for _, dependent := range dependents {
				if finalNodesToGraph[dependent] && !processedNodes[dependent] {
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
						log.Warnf("In-degree for %s negative after cycle node %s. Degree: %d", dependent, cycleNode, runningInDegree[dependent])
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
				continue
			}
			currentLevelNodes = append(currentLevelNodes, u)
			processedNodes[u] = true
			neighbors := reverseAdj[u]
			sort.Strings(neighbors)
			for _, v := range neighbors {
				if finalNodesToGraph[v] && !processedNodes[v] {
					runningInDegree[v]--
					if runningInDegree[v] == 0 {
						nextQueue = append(nextQueue, v)
					} else if runningInDegree[v] < 0 {
						log.Errf("BUG: Negative in-degree for %s after processing %s post-cycle", v, u)
					}
				}
			}
		}
		// Pass CORRECTED map name
		printLevel(currentLevelNodes, levelCounter, "", modulesFoundInOwners, forksIncludedByInternalDependency, bidirPairs, isBidirNode, processedForOutput, "")
		sort.Strings(nextQueue)
		queue = nextQueue
		levelCounter++
	}

	// Final Check
	processedGraphNodesCount := 0
	for node := range finalNodesToGraph {
		if processedNodes[node] {
			processedGraphNodesCount++
		}
	} // Check against finalNodesToGraph
	if processedGraphNodesCount != len(finalNodesToGraph) {
		log.Warnf("Processed %d nodes, but expected %d final graph nodes.", processedGraphNodesCount, len(finalNodesToGraph))
		unprocessed := []string{}
		for node := range finalNodesToGraph {
			if !processedNodes[node] {
				unprocessed = append(unprocessed, node)
			}
		}
		sort.Strings(unprocessed)
		log.Warnf("Unprocessed final graph nodes: %v", unprocessed)
	} else {
		log.Infof("Topological sort processed all %d final graph nodes.", len(finalNodesToGraph))
	}
}
