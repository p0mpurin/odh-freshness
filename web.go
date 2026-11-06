// SPDX-FileCopyrightText: 2026 The odh-freshness contributors
//
// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
)

// The static page (HTML, CSS, JS, vendored Leaflet and font) is compiled into
// the binary and copied next to data.json on every run.
//
//go:embed web
var webFS embed.FS

func copyWeb(dir string) error {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return err
	}
	return fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := fs.ReadFile(sub, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
