package graph

// ModuleInfo stores details about modules found in the scanned owners (orgs or users).
type ModuleInfo struct {
	Path               string // Module path from go.mod
	RepoPath           string // Repository path (owner/repo) where it was found
	IsFork             bool
	OriginalModulePath string            // Module path from the parent repo's go.mod (if fork)
	Owner              string            // Owner (org or user) where the module definition was found
	OwnerIdx           int               // Index of the owner in the input list (for coloring)
	Deps               map[string]string // path -> version
	Fetched            bool              // Indicates if the go.mod was successfully fetched and parsed
}

type Node struct {
	path       string
	module     *ModuleInfo // nil for (ext) dependencies
	partOfLoop bool
	setId      int // 0 for first owner/org, 1 for second, etc. - determines the color (with the fork attribute of the module)
}

type Edge struct {
	from *Node // never nil.
	to   *Node
	// The version of the dependency
	version string
}

type Graph struct {
	nodes  map[string]*Node // path -> Node
	edges  []Edge           // Edges
	cycles []Cycle          // Cycles as they are discovered
}

type Cycle struct {
	// Nodes in the cycle
	nodes []*Node
}
