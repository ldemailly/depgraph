// Allows to copy paste content of single "multi-file" as written by gemini and write each file
// to a separate file. The files are separated by a line starting with "// File: "
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec" // Import os/exec
	"strings"

	"fortio.org/log" // Use fortio/log for consistency
)

const (
	fileMarkerPrefix = "// File: "
	separatorPrefix  = "// =====" // Re-define separator to skip it
)

// runGoFumpt executes "gofumpt -w" on the specified file
func runGoFumpt(filename string) {
	if filename == "" {
		return // Safety check
	}
	log.Infof("  Running gofumpt on %s...", filename)
	cmd := exec.Command("gofumpt", "-w", filename)
	// Capture combined output (stdout + stderr)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Log error and output if gofumpt fails
		log.Warnf("  gofumpt failed for %s: %v\nOutput:\n%s", filename, err, string(output))
	} else if len(output) > 0 {
		// Log output even on success if gofumpt printed anything (e.g., warnings)
		log.LogVf("  gofumpt output for %s:\n%s", filename, string(output))
	}
}

func main() {
	// --- Use Stdin as Input ---
	fmt.Fprintln(os.Stderr, "Reading from stdin... Paste combined code and signal EOF (Ctrl+D).")
	fmt.Fprintln(os.Stderr, "Processing...")

	// --- Scan and Split ---
	scanner := bufio.NewScanner(os.Stdin)
	var currentFile *os.File
	var currentFilename string
	writing := false // Only true when actively writing to a file section
	linesInCurrentFile := 0

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		trimmedLine := strings.TrimSpace(line)

		// Check for File Marker
		if strings.HasPrefix(trimmedLine, fileMarkerPrefix) {
			filenameToFormat := "" // Store filename before resetting
			// Close previous file if open
			if currentFile != nil {
				filenameToFormat = currentFilename // Remember filename for gofumpt
				fmt.Fprintf(os.Stderr, "  Finished %s (%d lines written)\n", currentFilename, linesInCurrentFile)
				if err := currentFile.Close(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: Failed to close previous file '%s': %v\n", currentFilename, err)
					filenameToFormat = "" // Don't format if close failed? Or format anyway? Let's format.
				}
				currentFile = nil
			}
			// Run gofumpt on the completed file *after* closing it
			runGoFumpt(filenameToFormat)

			// Extract new filename
			filename := strings.TrimSpace(strings.TrimPrefix(line, fileMarkerPrefix))
			if filename == "" {
				fmt.Fprintf(os.Stderr, "Warning: Found marker with empty filename on line %d: %s\n", lineNum, line)
				writing = false // Stop writing until a valid marker is found
				continue
			}

			// Create/Truncate new output file
			fmt.Fprintf(os.Stderr, "  Extracting %s...\n", filename)
			outFile, err := os.Create(filename)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Failed to create output file '%s': %v\n", filename, err)
				writing = false // Stop writing if file creation fails
				currentFile = nil
				currentFilename = ""
				continue
			}

			// Update state
			currentFile = outFile
			currentFilename = filename
			writing = true
			linesInCurrentFile = 0 // Reset line counter
			continue               // Don't write the marker line itself
		}

		// Check for Separator and skip writing it
		if strings.HasPrefix(trimmedLine, separatorPrefix) {
			// Don't close the file here, just skip writing the separator line
			continue
		}

		// If NOT a marker or separator, and we have successfully opened a file, write the line
		if writing && currentFile != nil {
			_, err := fmt.Fprintln(currentFile, line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: Failed to write line %d to file '%s': %v\n", lineNum, currentFilename, err)
				writing = false
				currentFile.Close()
				currentFile = nil
				currentFilename = "" // Stop writing on error
				continue
			}
			// Sync removed as likely unnecessary
			linesInCurrentFile++
		}
	} // End scanner loop

	filenameToFormat := "" // Store filename before resetting
	// Close the last opened file
	if currentFile != nil {
		filenameToFormat = currentFilename // Remember filename
		fmt.Fprintf(os.Stderr, "  Finished %s (%d lines written) (at EOF)\n", currentFilename, linesInCurrentFile)
		if err := currentFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: Failed to close last file '%s': %v\n", currentFilename, err)
			filenameToFormat = "" // Don't format if close failed? Let's format.
		}
	}
	// Run gofumpt on the very last file *after* closing it
	runGoFumpt(filenameToFormat)

	// Check for scanner errors
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed reading input from stdin: %v\n", err)
		os.Exit(1)
	}

	fmt.Fprintln(os.Stderr, "Done.")
}
