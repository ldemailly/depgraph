# Go Module Dependency Graph Generator (`depgraph`)

This tool scans specified GitHub organizations or user accounts for public Go modules, parses their direct dependencies from `go.mod` files, and generates a dependency graph in DOT format or a topological sort order. The DOT file can then be visualized using tools like Graphviz.

## Features

* Scans multiple GitHub organizations and/or user accounts.
* Identifies public, non-fork, non-archived repositories containing a `go.mod` file at the root. Also processes forks found within those accounts.
* Uses the GitHub API to fetch repository information and `go.mod` contents (with optional filesystem caching).
* Parses direct dependencies (module path and required version) from `go.mod` files using `golang.org/x/mod/modfile`.
* **Cycle Detection:** Detects dependency cycles using Kahn's algorithm on the reversed graph. It refines the detection to identify only the nodes truly part of the cycles.
* Generates a graph in DOT format suitable for visualization tools, **highlighting cycles**.
* Generates a topological sort order (leaves first), **grouping cyclic dependencies** into a dedicated level.
* Distinguishes between internal modules (non-forks, included forks) and external dependencies using node colors.
* Provides options to exclude external dependencies, control graph layout, and manage the API cache.
* Logs progress and warnings to stderr, keeping stdout clean for DOT or topological sort output.

## Prerequisites

* **Go:** Version 1.18 or later. Needed to install and run the tool.
* **`gh` CLI:** The official GitHub CLI, used for authentication (getting a token). Install it from [cli.github.com](https://cli.github.com/). Required for the tool to access the GitHub API.
* **Graphviz (Optional):** Needed only if you want to convert the DOT output into an image (e.g., `png`, `svg`). Install it from [graphviz.org](https://graphviz.org/download/).

## Installation (Recommended)

Ensure you have Go installed and configured correctly (including `$GOPATH/bin` or `$HOME/go/bin` in your `PATH`). Then, run:

```bash
go install github.com/ldemailly/depgraph@latest
```

This will download the source code, compile it, and place the `depgraph` executable in your Go binary directory.

## Usage

1.  **Authenticate with GitHub:**
    The tool needs a GitHub token to interact with the API and avoid rate limits. Use the `gh` CLI to provide one via an environment variable:
    ```bash
    export GITHUB_TOKEN=$(gh auth token)
    ```
    *(Ensure you have run `gh auth login` previously)*

2.  **Run the tool:**
    Execute the `depgraph` command, optionally providing flags, followed by the names of the GitHub organizations or user accounts you want to scan.
    * **For DOT output:** Redirect the standard output (`stdout`) to a `.dot` file.
        ```bash
        depgraph [flags] <owner1> [owner2]... > dependencies.dot
        ```
        *Example:*
        ```bash
        depgraph -noext -clear-cache fortio grol-io ldemailly > dependencies.dot
        ```
    * **For Topological Sort output:** Use the `-topo-sort` flag. Output is printed directly to `stdout`.
        ```bash
        depgraph -topo-sort [flags] <owner1> [owner2]...
        ```
        *Example:*
        ```bash
        depgraph -topo-sort -noext golang
        ```

3.  **Visualize the Graph (using Graphviz, for DOT output):**
    Use the `dot` command (from Graphviz) to convert the generated `dependencies.dot` file into an image format like PNG or SVG.
    * **Generate PNG:**
        ```bash
        dot -Tpng dependencies.dot -o dependencies.png
        ```
    * **Generate SVG:**
        ```bash
        dot -Tsvg dependencies.dot -o dependencies.svg
        ```
    You can then open the generated image file.

### Command-Line Flags

* `-noext`: (Boolean, default `false`) If set, excludes external dependencies (modules not found in the specified owners) from the graph/output.
* `-left2right`: (Boolean, default `false`) If set (and not using `-topo-sort`), generates the DOT graph with a left-to-right layout (`rankdir=LR`) instead of the default top-to-bottom layout (`rankdir=TB`).
* `-topo-sort`: (Boolean, default `false`) If set, outputs the dependency order as text grouped by topological sort levels (leaves first) to standard output, instead of generating DOT graph output. **Cycles are grouped into a specific level.**
* `-use-cache`: (Boolean, default `true`) Enables the use of a local filesystem cache for GitHub API calls to speed up subsequent runs. Cache is stored in the user's cache directory (e.g., `~/.cache/depgraph_cache`). Disable with `-use-cache=false`.
* `-clear-cache`: (Boolean, default `false`) If set, removes the cache directory before running. Useful if you suspect the cache is stale.

## Example DOT Output (Visualized)

Example graph generated by running the tool with my `fortio`, `grol-io`, and `ldemailly` accounts:
```bash
depgraph  -left2right -noext fortio grol-io ldemailly > dependencies.dot
dot -Tsvg dependencies.dot -o dependencies.svg; open dependencies.svg
```

![Dependency Graph](dependencies.svg)

**Cycle Highlighting in DOT:**
* Nodes identified as part of a dependency cycle will have a **red border**.
* Edges *between* two nodes that are both part of a cycle will be drawn in **red** and slightly thicker.

![Golang's Graph](dependencies_golang.svg)

## Example Topological Sort Output

The `-topo-sort` flag indicates the order in which modules can be updated/built, processing dependencies first. Modules at the same level can potentially be processed in parallel.

```bash
depgraph -noext -topo-sort fortio grol-io ldemailly
```

Outputs (example without cycles):
```md
Topological Sort Levels (Leaves First):
Level 0:
  - fortio.org/assert
  - fortio.org/otel-sample-app
  - fortio.org/progressbar
  - fortio.org/safecast
  - fortio.org/struct2env
  - fortio/term (fortio.org/term fork of golang.org/x/term)
  - fortio/testscript (fortio.org/testscript fork of github.com/rogpeppe/go-internal)
  - fortio.org/version
  - github.com/fortio/h2demo
  - gitlab.com/ldemailly/go-flag-bug
Level 1:
  - fortio.org/log
  - fortio.org/sets
Level 2:
  - fortio.org/cli
  - fortio.org/dflag
  - github.com/ldemailly/advent24
  - github.com/ldemailly/panic-linter
Level 3:
  - fortio.org/multicurl
  - fortio.org/scli
  - fortio.org/terminal
  - github.com/fortio/delta
  - github.com/fortio/h2cli
  - github.com/ldemailly/depgraph
  - github.com/ldemailly/lll-fixer
  - github.com/ldemailly/quartiles
  - github.com/ldemailly/rate
Level 4:
  - fortio.org/logc
  - github.com/fortio/logger-bench
  - github.com/fortio/workflows
  - grol.io/grol
Level 5:
  - fortio.org/fortio
  - grol.io/grol-discord-bot
Level 6:
  - fortio.org/dnsping
  - fortio.org/fortiotel
  - fortio.org/memstore
  - fortio.org/proxy
  - fortio.org/slack-proxy
  - fortio.org/terminal/fps
  - github.com/fortio/semtest/v2
  - github.com/ldemailly/go-scratch
  - github.com/ldemailly/gohook_sample
```

**Cycle Handling in Topological Sort:**
* If cycles are detected, the sort proceeds through the acyclic levels first.
* All nodes identified as being part of cycles are then grouped together into a single level, labeled like `Level N (Cycles):`.
    * Within this level, simple bidirectional dependencies (A <-> B) are printed on a single line like `- A <-> B`.
* Subsequent levels contain nodes that depend on the preceding acyclic levels and/or the cycle level.

Example with cycle

```bash
depgraph -noext -topo-sort golang
```

```md
Topological Sort Levels (Leaves First):
Level 0:
  - github.com/golang/geo
  - github.com/golang/glog
  - github.com/golang/protobuf
  - golang.org/dl
  - golang.org/x/arch
  - golang.org/x/oauth2
  - golang.org/x/review
  - golang.org/x/sync
  - golang.org/x/sys
  - golang.org/x/time
  - golang.org/x/tour
  - golang.org/x/vgo
  - golang.org/x/xerrors
Level 1:
  - github.com/golang/groupcache
  - golang.org/x/benchmarks
  - golang.org/x/debug
  - golang.org/x/term
Level 2 (Cycles):
  - golang.org/x/crypto <-> golang.org/x/net
  - golang.org/x/mod <-> golang.org/x/tools
  - golang.org/x/telemetry
  - golang.org/x/text
Level 3:
  - github.com/golang/vscode-go
  - golang.org/x/example
  - golang.org/x/exp
  - golang.org/x/image
  - golang.org/x/oscar
  - golang.org/x/pkgsite
  - golang.org/x/vuln
  - google.golang.org/appengine
Level 4:
  - golang.org/x/mobile
  - golang.org/x/perf
  - golang.org/x/pkgsite-metrics
  - golang.org/x/vulndb
  - google.golang.org/open2opaque
Level 5:
  - golang.org/x/build
Level 6:
  - golang.org/x/playground
  - golang.org/x/website
```

## Graph Legend (DOT Output)

* **Nodes:** Represent Go modules.
* **Edges:** Represent direct dependencies (from `require` directives in `go.mod`). The label shows the required version.
* **Cycle Highlighting:** Nodes in cycles have red borders; edges between cycle nodes are red.

### Node Colors

Node colors indicate the origin and type of the module:

* **Light Blue / Light Green / Light Salmon / ...:** A non-fork module whose `go.mod` was found in the 1st / 2nd / 3rd / ... owner (org or user) specified on the command line. The colors cycle through a predefined palette.
* **Dark Blue / Dark Green / Dark Orange / ...:** A fork module whose `go.mod` was found in the 1st / 2nd / 3rd / ... owner. Forks are included in the graph only if:
    1.  They are depended upon by an included non-fork module, **OR**
    2.  They themselves depend on an included non-fork module.
* **Light Grey:** An external module (a dependency whose defining `go.mod` was not found in any of the specified owners). These nodes are hidden if the `-noext` flag is used.

### Node Labels

* **Non-Fork / External:** Labeled with the Go module path declared in `go.mod` (e.g., `fortio.org/log`, `golang.org/x/net`).
* **Included Fork:** Labeled to clearly indicate it's a fork:
    * If the fork's declared module path matches the parent's: `repo/path (fork of original/module/path)`
    * If the fork's declared module path differs: `repo/path (fork/declared/path fork of original/module/path)`
    * If the parent's module path couldn't be determined: `repo/path (fork)`

## Development Setup (Building from Source)

If you want to modify the code or contribute:

1.  **Clone the repository:**
    ```bash
    git clone https://github.com/ldemailly/depgraph.git
    cd depgraph
    ```

2.  **Build/Run:**
    ```bash
    # Run directly (uses the module context)
    go run . [flags] <owner1> [owner2]...

    # Or build the binary
    go build
    ./depgraph [flags] <owner1> [owner2]...

    # Check the effect of changes using
    make # see Makefile
    git diff
    ```

## How it Works

1.  **Initialization:** Parses flags, sets up GitHub client, initializes or clears the cache.
2.  **Repository Listing:** Lists public repositories for each owner (org/user), using caching.
3.  **Filtering & `go.mod` Fetching:** Fetches `go.mod` for non-archived repos (including forks), using caching.
4.  **Parent `go.mod` Fetching (Forks):** Fetches parent repo details and `go.mod` (cached) to find the original module path for better fork labeling.
5.  **Parsing:** Parses `go.mod` files for module path and direct dependencies.
6.  **Node Inclusion Logic:** Determines the final set of nodes (`nodesToGraph`) based on fetched data and the `-noext` flag (non-forks, qualifying forks, optional external).
7.  **Cycle Detection & Refinement:** Builds a reverse dependency graph and uses Kahn's algorithm to find nodes with remaining dependencies (potential cycles). Iteratively refines this set to include only nodes that are depended upon by other nodes within the set, identifying the core cycle members.
8.  **Output Generation:**
    * If `-topo-sort` is **true**: Performs Kahn's algorithm on the reversed graph. Prints acyclic levels first. Groups all refined cycle nodes into a single `Level N (Cycles):`. Continues Kahn's for remaining nodes depending on previous levels or the cycle level.
    * If `-topo-sort` is **false** (default): Generates DOT output. Nodes are colored by origin/type. Nodes in refined cycles get red borders. Edges between cycle nodes are red and thicker.

## Future Ideas

* Option to include indirect dependencies (would likely require running `go list -m all`).
* More sophisticated internal module detection (e.g., handling vanity URLs better).
* Alternative graph output formats (JSON, GML).
* Interactive web-based visualizations (e.g., using D3.js, vis.js).
* Handle repositories with multiple Go modules.

## About this

All the code in 0.1.0 was generated through many iterations/prompts of Gemini 2.5 pro

The driving and idea and need and "QA"ing is my own (ldemailly)

While I was lucky at first, it became increasingly frustrating to use AI for that, fixing bugs and (re)introducing others and explaining what is wrong etc...
Will try manual fix or localized or some other AI than Gemini as I just wasted another 2h going in a loop with respect to forks

And other AI failed too so... This is now in the process of being manually updated/redone.
