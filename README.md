# Go Module Dependency Graph Generator (`depgraph`)

This tool scans specified GitHub organizations for public Go modules, parses their direct dependencies from `go.mod` files, and generates a dependency graph in DOT format. The DOT file can then be visualized using tools like Graphviz.

## Features

* Scans multiple GitHub organizations.
* Identifies public, non-fork, non-archived repositories containing a `go.mod` file at the root.
* Uses the GitHub API to fetch repository information and `go.mod` contents.
* Parses direct dependencies (module path and required version) from `go.mod` files using `golang.org/x/mod/modfile`.
* Generates a graph in DOT format suitable for visualization tools.
* Distinguishes between "internal" modules (belonging to the scanned organizations) and "external" dependencies using node colors in the DOT output.
* Logs progress and warnings to stderr, keeping stdout clean for DOT output.

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
    The script needs a GitHub token to interact with the API and avoid rate limits. Use the `gh` CLI to provide one via an environment variable:
```bash
export GITHUB_TOKEN=$(gh auth token)
```
*(Ensure you have run `gh auth login` previously)*

2.  **Run the tool:**
    Execute the `depgraph` command (installed in the previous step), passing the names of the GitHub organizations you want to scan as command-line arguments. Redirect the standard output (`stdout`) to a `.dot` file. Progress and errors are printed to `stderr`.
```bash
depgraph <org1> [org2]... > dependencies.dot
```

*Example:*
```bash
depgraph fortio grol-io > dependencies.dot
```

3.  **Visualize the Graph (using Graphviz):**
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

## Development Setup (Building from Source)

If you want to modify the code or contribute:

1.  **Clone the repository:**
```bash
git clone https://github.com/ldemailly/depgraph.git
cd depgraph
```

2.  **Build/Run:**
```bash
# Ensure dependencies are downloaded
go mod tidy

# Run directly
go run main.go <org1> [org2]... > dependencies.dot

# Or build the binary
go build
./depgraph <org1> [org2]... > dependencies.dot
```

## How it Works

1.  **Initialization:** Sets up a GitHub API client (using the provided token if available). Defines module prefixes to identify "internal" modules.
2.  **Repository Listing:** Iterates through the specified organizations, listing public repositories via the GitHub API.
3.  **Filtering:** Skips repositories that are forks or archived.
4.  **`go.mod` Fetching:** For each remaining repository, attempts to fetch the content of the `go.mod` file from the root directory using the GitHub API.
5.  **Parsing:** If `go.mod` is found, its content is parsed using `golang.org/x/mod/modfile` to extract the module path and direct dependencies (`require` directives without the `// indirect` comment).
6.  **Data Aggregation:** Stores the dependencies in a map (`map[modulePath]map[dependencyPath]version`) and tracks all unique module paths encountered.
7.  **DOT Generation:**
    * Prints the DOT graph header and default styles.
    * Categorizes all unique modules as internal or external based on path prefixes.
    * Defines nodes with different fill colors for internal (lightblue) and external (lightgrey) modules.
    * Prints the dependency edges (`"source" -> "target"`), adding the required version number as an edge label.
    * Prints the closing graph brace.

## Future Ideas

* Option to include indirect dependencies (would likely require running `go list -m all`).
* Optionally exclude external dependencies from the graph.
* Generate interactive web-based visualizations (e.g., using D3.js, vis.js).
* Handle repositories with multiple Go modules.
* More sophisticated internal module detection.
