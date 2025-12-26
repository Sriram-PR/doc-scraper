package utils

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

// testTreeLogger returns a logger that discards output
func testTreeLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

// --- GenerateAndSaveTreeStructure Tests ---

func TestGenerateAndSaveTreeStructure_SingleFile(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create target dir: %v", err)
	}

	// Create a single file
	if err := os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create file: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	// Read and verify output
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)
	if !strings.Contains(output, "file.txt") {
		t.Errorf("Output missing 'file.txt': %s", output)
	}
	if !strings.Contains(output, "└──") {
		t.Errorf("Output missing last-entry prefix '└──': %s", output)
	}
}

func TestGenerateAndSaveTreeStructure_MixedEntries(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create target dir: %v", err)
	}

	// Create directory and files
	subDir := filepath.Join(targetDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "file1.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create file1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "file2.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create file2: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)
	// Directory should come first (before files in sorted order)
	if !strings.Contains(output, "subdir") {
		t.Errorf("Output missing 'subdir': %s", output)
	}
	if !strings.Contains(output, "file1.txt") {
		t.Errorf("Output missing 'file1.txt': %s", output)
	}
	if !strings.Contains(output, "file2.txt") {
		t.Errorf("Output missing 'file2.txt': %s", output)
	}
}

func TestGenerateAndSaveTreeStructure_NestedDirs(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")

	// Create nested structure: target/a/b/c/file.txt
	deepPath := filepath.Join(targetDir, "a", "b", "c")
	if err := os.MkdirAll(deepPath, 0755); err != nil {
		t.Fatalf("Failed to create deep path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(deepPath, "file.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create deep file: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)
	// Should contain all levels
	for _, expected := range []string{"a", "b", "c", "file.txt"} {
		if !strings.Contains(output, expected) {
			t.Errorf("Output missing '%s': %s", expected, output)
		}
	}
}

func TestGenerateAndSaveTreeStructure_SortOrder(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create target dir: %v", err)
	}

	// Create files and dirs in non-alphabetical order
	// Directories: zebra_dir, apple_dir
	// Files: beta.txt, alpha.txt
	if err := os.Mkdir(filepath.Join(targetDir, "zebra_dir"), 0755); err != nil {
		t.Fatalf("Failed to create zebra_dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(targetDir, "apple_dir"), 0755); err != nil {
		t.Fatalf("Failed to create apple_dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "beta.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create beta.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "alpha.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create alpha.txt: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)

	// Expected order: apple_dir, zebra_dir (dirs first, alphabetical), then alpha.txt, beta.txt (files, alphabetical)
	appleIdx := strings.Index(output, "apple_dir")
	zebraIdx := strings.Index(output, "zebra_dir")
	alphaIdx := strings.Index(output, "alpha.txt")
	betaIdx := strings.Index(output, "beta.txt")

	// Directories should come before files
	if appleIdx > alphaIdx || zebraIdx > alphaIdx {
		t.Errorf("Directories should come before files in output:\n%s", output)
	}

	// Within directories, alphabetical order
	if appleIdx > zebraIdx {
		t.Errorf("apple_dir should come before zebra_dir:\n%s", output)
	}

	// Within files, alphabetical order
	if alphaIdx > betaIdx {
		t.Errorf("alpha.txt should come before beta.txt:\n%s", output)
	}
}

func TestGenerateAndSaveTreeStructure_EmptyDir(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create target dir: %v", err)
	}
	// Leave directory empty

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)
	// Should contain header and root name but no entries
	if !strings.Contains(output, "Directory Structure for:") {
		t.Errorf("Output missing header: %s", output)
	}
	if !strings.Contains(output, "target/") {
		t.Errorf("Output missing root directory name: %s", output)
	}
}

func TestGenerateAndSaveTreeStructure_NonExistentTarget(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistent := filepath.Join(tmpDir, "does_not_exist")
	outputFile := filepath.Join(tmpDir, "tree.txt")

	err := GenerateAndSaveTreeStructure(nonExistent, outputFile, testTreeLogger())
	if err == nil {
		t.Error("GenerateAndSaveTreeStructure() expected error for non-existent target, got nil")
	}
}

func TestGenerateAndSaveTreeStructure_TreePrefixes(t *testing.T) {
	tmpDir := t.TempDir()
	targetDir := filepath.Join(tmpDir, "target")
	if err := os.Mkdir(targetDir, 0755); err != nil {
		t.Fatalf("Failed to create target dir: %v", err)
	}

	// Create structure to test prefixes:
	// target/
	// ├── dir1/
	// │   └── nested.txt
	// └── file.txt
	dir1 := filepath.Join(targetDir, "dir1")
	if err := os.Mkdir(dir1, 0755); err != nil {
		t.Fatalf("Failed to create dir1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir1, "nested.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create nested.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "file.txt"), []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create file.txt: %v", err)
	}

	outputFile := filepath.Join(tmpDir, "tree.txt")
	err := GenerateAndSaveTreeStructure(targetDir, outputFile, testTreeLogger())
	if err != nil {
		t.Fatalf("GenerateAndSaveTreeStructure() error = %v", err)
	}

	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("Failed to read output file: %v", err)
	}

	output := string(content)

	// Check for correct tree prefixes
	if !strings.Contains(output, "├──") {
		t.Errorf("Output missing middle-entry prefix '├──': %s", output)
	}
	if !strings.Contains(output, "└──") {
		t.Errorf("Output missing last-entry prefix '└──': %s", output)
	}
	// Vertical line for nested content under non-last directory
	if !strings.Contains(output, "│") {
		t.Errorf("Output missing vertical line '│': %s", output)
	}
}
