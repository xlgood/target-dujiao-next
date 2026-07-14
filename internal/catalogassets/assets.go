package catalogassets

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed *.svg
var files embed.FS

var platforms = []string{"facebook", "instagram", "tiktok", "youtube", "x"}

// Ensure writes a fixed set of category covers into the persistent uploads
// volume. Existing files are left unchanged so deployments are idempotent.
func Ensure(uploadsDir string) error {
	dir := filepath.Join(uploadsDir, "catalog")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create catalog image directory: %w", err)
	}
	for _, platform := range platforms {
		contents, err := files.ReadFile(platform + ".svg")
		if err != nil {
			return fmt.Errorf("read %s catalog image: %w", platform, err)
		}
		path := filepath.Join(dir, platform+".svg")
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s catalog image: %w", platform, err)
		}
		if err := os.WriteFile(path, contents, 0o644); err != nil {
			return fmt.Errorf("write %s catalog image: %w", platform, err)
		}
	}
	return nil
}
