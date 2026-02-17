package utils

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
)

const (
	indentPrefix    = "    "
	entryPrefix     = "├── "
	lastEntryPrefix = "└── "
	verticalLine    = "│   "
)

// GenerateAndSaveTreeStructure walks the targetDir and writes a text-based directory tree structure to the specified outputFilePath
func GenerateAndSaveTreeStructure(targetDir, outputFilePath string, log *logrus.Entry) error {
	log.Debugf("Starting tree generation for target: %s", targetDir)
	// Ensure target directory exists
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		return fmt.Errorf("target directory '%s' does not exist: %w", targetDir, err)
	} else if err != nil {
		return fmt.Errorf("error checking target directory '%s': %w", targetDir, err)
	}

	// Create or truncate the output file
	file, err := os.Create(outputFilePath)
	if err != nil {
		return fmt.Errorf("failed to create output file '%s': %w", outputFilePath, err)
	}
	defer file.Close() // Ensure file is closed

	writer := bufio.NewWriter(file)
	defer writer.Flush() // Ensure buffer is flushed

	// Write header
	_, err = fmt.Fprintf(writer, "Directory Structure for: %s\n", targetDir)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "%s\n\n", strings.Repeat("=", 25+len(targetDir)))
	if err != nil {
		return err
	}

	// Write the root directory name itself
	rootName := filepath.Base(targetDir)
	_, err = fmt.Fprintf(writer, "%s/\n", rootName)
	if err != nil {
		return err
	}

	// Start the recursive walk from the target directory, passing the logger
	log.Debugf("Initiating recursive walk from: %s", targetDir)
	err = walkDirRecursive(writer, targetDir, "", log)
	if err != nil {
		// os.Remove(outputFilePath)
		log.Errorf("Error occurred during recursive walk for '%s': %v", targetDir, err)
		return fmt.Errorf("error generating tree structure for '%s': %w", targetDir, err)
	}
	log.Debugf("Finished recursive walk for: %s", targetDir)

	return nil // Success
}

// walkDirRecursive performs the recursive directory walk and writes entries
func walkDirRecursive(writer io.Writer, dirPath string, currentIndent string, log *logrus.Entry) error {
	log.Debugf("Walking directory: %s", dirPath)
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		// Log the error at warning level as we are handling it by returning
		log.Warnf("Failed to read directory '%s': %v", dirPath, err)
		return fmt.Errorf("failed to read directory '%s': %w", dirPath, err)
	}
	log.Debugf("Found %d entries in %s", len(entries), dirPath)

	// Sort entries: directories first, then alphabetically by name
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		aIsDir := a.IsDir()
		bIsDir := b.IsDir()
		if aIsDir && !bIsDir {
			return -1 // a (dir) comes before b (file)
		}
		if !aIsDir && bIsDir {
			return 1 // b (dir) comes before a (file)
		}
		// Both are dirs or both are files, sort by name
		return strings.Compare(strings.ToLower(a.Name()), strings.ToLower(b.Name()))
	})

	for i, entry := range entries {
		isLast := (i == len(entries)-1) // Check if this is the last entry at this level

		// Determine connector prefix
		connector := entryPrefix
		if isLast {
			connector = lastEntryPrefix
		}

		// Log the entry being written
		log.Debugf("Writing entry: %s%s%s", currentIndent, connector, entry.Name())

		// Write the current entry line
		_, writeErr := fmt.Fprintf(writer, "%s%s%s\n", currentIndent, connector, entry.Name())
		if writeErr != nil {
			log.Errorf("Error writing entry '%s' to output file: %v", entry.Name(), writeErr)
			return writeErr // Stop processing on write error
		}

		// If it's a directory, recurse
		if entry.IsDir() {
			// Determine the prefix for the next level's indentation
			nextIndent := currentIndent
			if isLast {
				nextIndent += indentPrefix // No vertical line needed after last entry
			} else {
				nextIndent += verticalLine // Add vertical line for non-last entries
			}

			// Recursive call
			subDirPath := filepath.Join(dirPath, entry.Name())
			log.Debugf("Recursing into directory: %s", subDirPath)
			err := walkDirRecursive(writer, subDirPath, nextIndent, log)
			if err != nil {
				return err // Propagate error up
			}
			log.Debugf("Finished recursion for directory: %s", subDirPath)
		}
	}
	log.Debugf("Finished processing directory: %s", dirPath)
	return nil // Success for this directory level
}
