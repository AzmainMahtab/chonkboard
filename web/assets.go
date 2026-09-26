// Package web serves the built front-end from inside the binary. Embedding is
// what makes the deploy a single file: no asset directory to mount, no chance of
// HTML and CSS drifting apart between releases.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

//go:embed static
var staticFS embed.FS

// Static returns the embedded asset tree rooted so that paths look like
// "css/app.css" rather than "static/css/app.css".
func Static() fs.FS {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("web: static assets missing from the binary: " + err.Error())
	}
	return sub
}

// AssetSuffix is a cache-busting query derived from the content of every
// embedded asset. Because it changes only when an asset changes, /static can be
// served immutable and cached for a year.
func AssetSuffix() string {
	sum := sha256.New()
	assets := Static()

	var paths []string
	_ = fs.WalkDir(assets, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	sort.Strings(paths) // WalkDir is ordered, but do not depend on it

	for _, p := range paths {
		f, err := assets.Open(p)
		if err != nil {
			continue
		}
		sum.Write([]byte(p))
		_, _ = io.Copy(sum, f)
		_ = f.Close()
	}
	return "?v=" + hex.EncodeToString(sum.Sum(nil))[:12]
}

// StaticHandler serves the embedded assets under prefix with immutable caching.
// It refuses directory listings: an index of the asset tree is not a feature.
func StaticHandler(prefix string) http.Handler {
	files := http.FileServerFS(Static())
	return http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	}))
}
