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
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"

	framework "github.com/wso2/api-platform/tests/framework"
)

// isolateCache points the user cache at a temporary directory so extraction never touches
// the developer's real cache.
func isolateCache(t *testing.T) {
	t.Helper()
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
}

func testTree() fstest.MapFS {
	return fstest.MapFS{
		"core/catalog/overlays/logging.toml": {Data: []byte("level = 'debug'\n")},
		"snapshots/gateway/schema.sql":       {Data: []byte("CREATE TABLE t;\n")},
		"snapshots/gateway/certs/a.crt":      {Data: []byte("crt")},
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(content)
}

func TestResolvePrefersTheWorkspaceCopy(t *testing.T) {
	isolateCache(t)
	workspace := t.TempDir()
	live := filepath.Join(workspace, "gateway", "schema.sql")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o755))
	require.NoError(t, os.WriteFile(live, []byte("live"), 0o644))

	got := NewStore(testTree()).Resolve(workspace, "gateway/schema.sql")

	require.Equal(t, live, got)
	require.Equal(t, "live", readFile(t, got))
}

func TestResolveFallsBackToTheEmbeddedCopy(t *testing.T) {
	isolateCache(t)
	store := NewStore(testTree())
	workspace := t.TempDir()

	snapshot := store.Resolve(workspace, "gateway/schema.sql")
	module := store.Resolve(workspace, "tests/framework/core/catalog/overlays/logging.toml")
	directory := store.Resolve(workspace, "gateway/certs")

	require.Equal(t, "CREATE TABLE t;\n", readFile(t, snapshot))
	require.Equal(t, "level = 'debug'\n", readFile(t, module))
	require.Equal(t, "crt", readFile(t, filepath.Join(directory, "a.crt")))
	require.True(t, store.Within(snapshot))
	require.False(t, store.Within(workspace))
}

func TestResolveWithoutAWorkspaceUsesTheEmbeddedCopy(t *testing.T) {
	isolateCache(t)

	got := NewStore(testTree()).Resolve("", "gateway/schema.sql")

	require.Equal(t, "CREATE TABLE t;\n", readFile(t, got))
}

func TestResolveReturnsTheWorkspacePathForAnUnknownFile(t *testing.T) {
	isolateCache(t)
	store := NewStore(testTree())
	workspace := t.TempDir()

	require.Equal(t, filepath.Join(workspace, "gateway", "missing.sql"), store.Resolve(workspace, "gateway/missing.sql"))
	require.Equal(t, "gateway/missing.sql", store.Resolve("", "gateway/missing.sql"))
	require.False(t, store.Within(filepath.Join(workspace, "gateway", "missing.sql")))
}

func TestResolveLeavesEmptyAndAbsolutePathsUnchanged(t *testing.T) {
	store := NewStore(testTree())
	absolute := filepath.Join(t.TempDir(), "file")

	require.Equal(t, "", store.Resolve("/ws", ""))
	require.Equal(t, absolute, store.Resolve("/ws", absolute))
}

func TestResolveRefusesToLeaveTheRepository(t *testing.T) {
	isolateCache(t)
	store := NewStore(fstest.MapFS{"snapshots/secret": {Data: []byte("x")}})
	workspace := t.TempDir()

	got := store.Resolve(workspace, "../secret")

	require.Equal(t, filepath.Join(filepath.Dir(workspace), "secret"), got)
	require.False(t, store.Within(got))
}

func TestReadEmbeddedIgnoresTheWorkspace(t *testing.T) {
	store := NewStore(testTree())

	content, err := store.ReadEmbedded("gateway/schema.sql")
	require.NoError(t, err)
	require.Equal(t, "CREATE TABLE t;\n", string(content))

	_, err = store.ReadEmbedded("gateway/missing.sql")
	require.Error(t, err)
	_, err = store.ReadEmbedded("../escape")
	require.Error(t, err)
	_, err = store.ReadEmbedded("")
	require.Error(t, err)
}

func TestEmbeddedPathMappingRoundTrips(t *testing.T) {
	for _, clean := range []string{
		"tests/framework/core/catalog/overlays/logging.toml",
		"gateway/gateway-controller/listener-certs/default-listener.crt",
	} {
		embedded, _ := embeddedPath(clean)
		require.Equal(t, clean, repositoryPathOf(embedded))
	}
	embedded, isModule := embeddedPath("tests/framework/core/x")
	require.Equal(t, "core/x", embedded)
	require.True(t, isModule)
	embedded, isModule = embeddedPath("gateway/x")
	require.Equal(t, "snapshots/gateway/x", embedded)
	require.False(t, isModule)
}

func TestExtractionIsSharedByStoresWithTheSameContent(t *testing.T) {
	isolateCache(t)

	first := NewStore(testTree()).Resolve("", "gateway/schema.sql")
	second := NewStore(testTree()).Resolve("", "gateway/schema.sql")
	changed := NewStore(fstest.MapFS{"snapshots/gateway/schema.sql": {Data: []byte("other")}}).Resolve("", "gateway/schema.sql")

	require.Equal(t, first, second)
	require.NotEqual(t, first, changed)
}

func TestResolveExtractsAgainWhenTheCacheDirectoryDisappears(t *testing.T) {
	isolateCache(t)
	store := NewStore(testTree())
	first := store.Resolve("", "gateway/schema.sql")
	require.NoError(t, os.RemoveAll(filepath.Dir(filepath.Dir(first))))

	second := store.Resolve("", "gateway/schema.sql")

	require.Equal(t, "CREATE TABLE t;\n", readFile(t, second))
}

func TestResolveIsSafeForConcurrentUse(t *testing.T) {
	isolateCache(t)
	store := NewStore(testTree())
	var wg sync.WaitGroup
	paths := make([]string, 16)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i] = store.Resolve("", "gateway/schema.sql")
			_ = store.Within(paths[i])
		}()
	}
	wg.Wait()

	for _, path := range paths {
		require.Equal(t, paths[0], path)
	}
	require.Equal(t, "CREATE TABLE t;\n", readFile(t, paths[0]))
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

func TestSyncSnapshotsReportsAndRepairsDrift(t *testing.T) {
	original := SnapshotSources
	t.Cleanup(func() { SnapshotSources = original })
	SnapshotSources = []string{"gateway/certs", "gateway/one.bin"}
	repo, module := t.TempDir(), t.TempDir()
	writeTree(t, repo, map[string]string{
		"gateway/certs/a.crt": "new",
		"gateway/certs/b.crt": "added",
		"gateway/one.bin":     "bin",
	})
	writeTree(t, module, map[string]string{
		"snapshots/gateway/certs/a.crt":    "old",
		"snapshots/gateway/certs/gone.crt": "stale",
	})

	drifted, err := SyncSnapshots(repo, module, false)
	require.NoError(t, err)
	require.Equal(t, []string{
		"gateway/certs/a.crt",
		"gateway/certs/b.crt",
		"gateway/certs/gone.crt",
		"gateway/one.bin",
	}, drifted)
	require.Equal(t, "old", readFile(t, filepath.Join(module, "snapshots", "gateway", "certs", "a.crt")))

	drifted, err = SyncSnapshots(repo, module, true)
	require.NoError(t, err)
	require.Len(t, drifted, 4)
	require.Equal(t, "new", readFile(t, filepath.Join(module, "snapshots", "gateway", "certs", "a.crt")))
	require.NoFileExists(t, filepath.Join(module, "snapshots", "gateway", "certs", "gone.crt"))

	drifted, err = SyncSnapshots(repo, module, false)
	require.NoError(t, err)
	require.Empty(t, drifted)
}

// TestEmbeddedTreeCarriesEverySnapshotSource guards the module against a snapshot that was
// listed but never added to the repository, which would only fail for a consumer.
func TestEmbeddedTreeCarriesEverySnapshotSource(t *testing.T) {
	store := NewStore(framework.Assets)
	for _, source := range SnapshotSources {
		clean, ok := repositoryPath(source)
		require.True(t, ok, source)
		require.True(t, store.has(clean), "no embedded copy of %s", source)
	}
}

func TestEmbeddedTreeCarriesTheCatalogFilesTheGatewayReads(t *testing.T) {
	store := NewStore(framework.Assets)
	for _, name := range []string{
		"tests/framework/core/catalog/platformgateway/docker-compose.yaml",
		"tests/framework/core/catalog/platformgateway/resources/1.1.0/config.toml",
		"tests/framework/core/catalog/platformgateway/resources/1.1.0/gateway-controller-storage.toml",
		"tests/framework/core/catalog/platformgateway/resources/1.2.0/config.toml",
		"tests/framework/core/catalog/platformgateway/resources/1.2.0/gateway-controller-storage.toml",
		"tests/framework/core/catalog/overlays/gateway-controller-storage.toml",
		"tests/framework/core/catalog/overlays/gateway-analytics.toml",
	} {
		require.True(t, store.has(name), "no embedded copy of %s", name)
	}
}

// TestSnapshotsMatchTheRepositorySource keeps each embedded copy equal to the file the
// gateway ships. It only runs inside an api-platform checkout.
func TestSnapshotsMatchTheRepositorySource(t *testing.T) {
	repo, ok := findRepository()
	if !ok {
		t.Skip("not running inside an api-platform checkout")
	}
	_, file, _, _ := runtime.Caller(0)
	moduleRoot := filepath.Join(filepath.Dir(file), "..", "..")

	drifted, err := SyncSnapshots(repo, moduleRoot, false)

	require.NoError(t, err)
	require.Empty(t, drifted, "snapshots are stale; run: go run ./cmd/syncassets")
}

func findRepository() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "gateway", "VERSION")); err == nil {
			return dir, true
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", false
		}
	}
}
