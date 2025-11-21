// Package web holds the embedded dashboard (plain ES modules, no build step).
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"
)

// FS contains index.html, the PWA manifest and service worker, and the
// assets directory.
//
//go:embed index.html manifest.webmanifest sw.js assets
var FS embed.FS

// Build is a short hash of every embedded file. The service worker uses it
// to name its cache, so each release replaces the cached app shell.
var Build = sync.OnceValue(func() string {
	h := sha256.New()
	_ = fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := FS.ReadFile(p)
		if err != nil {
			return err
		}
		_, _ = io.WriteString(h, p)
		_, _ = h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
})
