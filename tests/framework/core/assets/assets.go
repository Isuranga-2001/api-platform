/*
 * Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
 *
 * WSO2 LLC. licenses this file to you under the Apache License,
 * Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	framework "github.com/wso2/api-platform/tests/framework"
)

const (
	// modulePrefix is where the framework module sits in the repository.
	modulePrefix = "tests/framework/"
	// snapshotDir holds embedded copies of files that live elsewhere in the repository.
	snapshotDir = "snapshots"
	// completeMarker is written last, so a half-extracted directory is never reused.
	completeMarker = ".complete"
	// cacheDirName is the directory under the user cache that holds extracted assets.
	cacheDirName = "apip-framework-assets"
)

// Store resolves repository-relative paths against a workspace and an embedded file tree.
type Store struct {
	fsys fs.FS

	mu  sync.Mutex
	dir string
	err error
}

// NewStore returns a Store backed by fsys, which uses the layout of the framework's
// embedded tree: module files at their module-relative path, other repository files under
// snapshots/.
func NewStore(fsys fs.FS) *Store {
	return &Store{fsys: fsys}
}

var defaultStore = NewStore(framework.Assets)

// Resolve maps a repository-relative path to a path on disk using the framework's embedded
// tree. See Store.Resolve.
func Resolve(workspaceRoot, rel string) string { return defaultStore.Resolve(workspaceRoot, rel) }

// ReadEmbedded returns the embedded copy of a repository-relative file, ignoring any
// workspace. See Store.ReadEmbedded.
func ReadEmbedded(rel string) ([]byte, error) { return defaultStore.ReadEmbedded(rel) }

// Within reports whether p lies inside the directory the embedded tree was extracted to.
func Within(p string) bool { return defaultStore.Within(p) }

// Resolve maps a repository-relative path to a path on disk. A file or directory that exists
// beneath workspaceRoot is used as is. Otherwise the embedded copy is extracted once and its
// path returned. A path that is empty, absolute or unknown is returned beneath workspaceRoot
// unchanged, so the caller's own error reporting names the file the way it always did.
func (s *Store) Resolve(workspaceRoot, rel string) string {
	if rel == "" || filepath.IsAbs(rel) {
		return rel
	}
	candidate := rel
	if workspaceRoot != "" {
		candidate = filepath.Join(workspaceRoot, rel)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	clean, ok := repositoryPath(rel)
	if !ok || !s.has(clean) {
		return candidate
	}
	dir, err := s.extract()
	if err != nil {
		return candidate
	}
	return filepath.Join(dir, filepath.FromSlash(clean))
}

// ReadEmbedded returns the embedded copy of a repository-relative file. It does not consult
// a workspace, so every caller reads the same bytes whatever checkout it runs in.
func (s *Store) ReadEmbedded(rel string) ([]byte, error) {
	clean, ok := repositoryPath(rel)
	if !ok {
		return nil, fmt.Errorf("assets: %q is not a repository-relative path", rel)
	}
	embedded, _ := embeddedPath(clean)
	content, err := fs.ReadFile(s.fsys, embedded)
	if err != nil {
		return nil, fmt.Errorf("assets: no embedded copy of %q: %w", rel, err)
	}
	return content, nil
}

// Within reports whether p lies inside the directory the embedded tree was extracted to.
// It is false until the first extraction.
func (s *Store) Within(p string) bool {
	s.mu.Lock()
	dir := s.dir
	s.mu.Unlock()
	if dir == "" || p == "" {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return abs == root || strings.HasPrefix(abs, root+string(filepath.Separator))
}

func (s *Store) has(clean string) bool {
	embedded, _ := embeddedPath(clean)
	_, err := fs.Stat(s.fsys, embedded)
	return err == nil
}

// repositoryPath normalizes rel to a slash-separated path that stays inside the repository.
func repositoryPath(rel string) (string, bool) {
	clean := path.Clean(filepath.ToSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", false
	}
	return clean, true
}

// embeddedPath maps a repository-relative path to its place in the embedded tree.
func embeddedPath(clean string) (string, bool) {
	if strings.HasPrefix(clean, modulePrefix) {
		return strings.TrimPrefix(clean, modulePrefix), true
	}
	return path.Join(snapshotDir, clean), false
}

// repositoryPathOf is the inverse of embeddedPath.
func repositoryPathOf(embedded string) string {
	if strings.HasPrefix(embedded, snapshotDir+"/") {
		return strings.TrimPrefix(embedded, snapshotDir+"/")
	}
	return modulePrefix + embedded
}

// extract writes the embedded tree to a content-addressed directory and returns it. It is
// repeated only if the directory has disappeared. The directory mirrors the repository layout, so a repository-relative path
// joined onto it names the embedded file. Concurrent processes extract to separate
// directories and the first rename wins.
func (s *Store) extract() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dir != "" {
		// A cache cleaner may have removed the directory since the last call.
		if _, err := os.Stat(filepath.Join(s.dir, completeMarker)); err == nil {
			return s.dir, nil
		}
	}
	s.dir, s.err = s.materialize()
	return s.dir, s.err
}

func (s *Store) materialize() (string, error) {
	files, err := s.listFiles()
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, name := range files {
		content, err := fs.ReadFile(s.fsys, name)
		if err != nil {
			return "", fmt.Errorf("assets: reading embedded %q: %w", name, err)
		}
		fmt.Fprintf(digest, "%s\x00%d\x00", name, len(content))
		digest.Write(content)
	}
	base, err := cacheRoot()
	if err != nil {
		return "", err
	}
	target := filepath.Join(base, hex.EncodeToString(digest.Sum(nil))[:16])
	if _, err := os.Stat(filepath.Join(target, completeMarker)); err == nil {
		return target, nil
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("assets: creating %q: %w", base, err)
	}
	staging, err := os.MkdirTemp(base, "extract-")
	if err != nil {
		return "", fmt.Errorf("assets: creating a staging directory: %w", err)
	}
	for _, name := range files {
		content, err := fs.ReadFile(s.fsys, name)
		if err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("assets: reading embedded %q: %w", name, err)
		}
		dest := filepath.Join(staging, filepath.FromSlash(repositoryPathOf(name)))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("assets: creating %q: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			_ = os.RemoveAll(staging)
			return "", fmt.Errorf("assets: writing %q: %w", dest, err)
		}
	}
	if err := os.WriteFile(filepath.Join(staging, completeMarker), nil, 0o644); err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("assets: marking %q complete: %w", staging, err)
	}
	if err := os.Rename(staging, target); err != nil {
		_ = os.RemoveAll(staging)
		if _, statErr := os.Stat(filepath.Join(target, completeMarker)); statErr == nil {
			return target, nil
		}
		return "", fmt.Errorf("assets: publishing %q: %w", target, err)
	}
	return target, nil
}

func (s *Store) listFiles() ([]string, error) {
	var files []string
	err := fs.WalkDir(s.fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, name)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("assets: listing the embedded tree: %w", err)
	}
	return files, nil
}

func cacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	if base == "" {
		return "", fmt.Errorf("assets: no cache directory is available")
	}
	return filepath.Join(base, cacheDirName), nil
}
