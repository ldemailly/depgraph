// Reverse AISplit to create single context/canvas that can be split by aisplit

package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"fortio.org/cli"
	"fortio.org/log" // Use fortio/log for consistency
)

const (
	headerSeparator = "// ========================================================================"
	headerPrefix    = "// File: "
)

func main() {
	cli.ArgsHelp = "file1 [file2...]" // Set custom usage text for arguments
	cli.MinArgs = 1                   // Require at least one file name
	cli.MaxArgs = -1                  // Allow any number of file names
	cli.Main()                        // Parses flags, validates args, handles version/help flags

	output := bufio.NewWriter(os.Stdout)
	defer output.Flush()

	for _, filename := range flag.Args() {
		log.Infof("Processing file: %s", filename)

		// Write header
		fmt.Fprintln(output, headerSeparator)
		fmt.Fprintf(output, "%s %s\n", headerPrefix, filename)
		fmt.Fprintln(output, headerSeparator)
		fmt.Fprintln(output) // Extra newline for readability

		// Open file for reading
		file, err := os.Open(filename)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed to open file '%s': %v\n", filename, err)
			continue
		}

		// Copy file content to output
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fmt.Fprintln(output, scanner.Text())
		}

		if err := scanner.Err(); err != nil {
			fmt.Fprintf(os.Stderr, "Error: Failed reading file '%s': %v\n", filename, err)
		}
		fmt.Fprintln(output) // extra newline between files
		file.Close()
	}

	log.Infof("Done.")
}
