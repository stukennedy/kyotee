package embedded

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed defaults/*
var defaults embed.FS

// Install copies embedded default files to the target directory
func Install(targetDir string) error {
	return fs.WalkDir(defaults, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the root "defaults" directory itself
		if path == "defaults" {
			return nil
		}

		// Get relative path (strip "defaults/" prefix)
		relPath, err := filepath.Rel("defaults", path)
		if err != nil {
			return err
		}

		targetPath := filepath.Join(targetDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		// Read embedded file
		data, err := defaults.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read embedded file %s: %w", path, err)
		}

		// Write to target (don't overwrite existing files)
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			if err := os.WriteFile(targetPath, data, 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", targetPath, err)
			}
		}

		return nil
	})
}
