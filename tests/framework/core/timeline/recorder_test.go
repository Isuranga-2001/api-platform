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

package timeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// --- Recorder ---

func TestRecorderTimelineIsEmptyWithNoBlocks(t *testing.T) {
	r := NewRecorder()
	tl := r.Timeline("gateway-latest", 6)

	require.Equal(t, SchemaVersion, tl.SchemaVersion)
	require.Equal(t, "gateway-latest", tl.Leg)
	require.Equal(t, 6, tl.BlockParallel)
	require.Empty(t, tl.Blocks)
	require.Positive(t, tl.Cores)
	require.GreaterOrEqual(t, tl.WallMs, int64(0))
}

func TestRecorderBlockRecordsLaneNameAndResult(t *testing.T) {
	r := NewRecorder()
	started := time.Now()
	time.Sleep(5 * time.Millisecond)
	r.Block(1, "gateway-core", started, true)
	r.Block(2, "gateway-mtls", started, false)

	tl := r.Timeline("default", 2)
	require.Len(t, tl.Blocks, 2)

	byName := map[string]BlockRun{}
	for _, b := range tl.Blocks {
		byName[b.Name] = b
	}
	require.True(t, byName["gateway-core"].Passed)
	require.Equal(t, 1, byName["gateway-core"].Lane)
	require.False(t, byName["gateway-mtls"].Passed)
	require.Equal(t, 2, byName["gateway-mtls"].Lane)
	require.GreaterOrEqual(t, byName["gateway-core"].DurationMs, int64(0))
}

func TestRecorderTimelineOrdersBlocksByStart(t *testing.T) {
	r := NewRecorder()
	base := time.Now()
	r.Block(1, "second", base.Add(20*time.Millisecond), true)
	r.Block(1, "first", base, true)
	r.Block(1, "third", base.Add(40*time.Millisecond), true)

	tl := r.Timeline("default", 1)
	require.Len(t, tl.Blocks, 3)
	require.Equal(t, []string{"first", "second", "third"}, []string{
		tl.Blocks[0].Name, tl.Blocks[1].Name, tl.Blocks[2].Name,
	})
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r.Block(i%6+1, "block", time.Now(), i%2 == 0)
		}(i)
	}
	wg.Wait()

	tl := r.Timeline("default", 6)
	require.Len(t, tl.Blocks, 50)
}

func TestRecorderTimelineReturnsAnIndependentCopy(t *testing.T) {
	r := NewRecorder()
	r.Block(1, "gateway-core", time.Now(), true)

	first := r.Timeline("default", 1)
	first.Blocks[0].Name = "mutated"

	second := r.Timeline("default", 1)
	require.Equal(t, "gateway-core", second.Blocks[0].Name)
}

// --- Write ---

func TestWriteRejectsAnEmptyPath(t *testing.T) {
	err := Write("   ", Timeline{})
	require.Error(t, err)
}

func TestWriteCreatesParentDirectoriesAndValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "timeline.json")

	tl := Timeline{SchemaVersion: SchemaVersion, Leg: "gateway-1.2.0", BlockParallel: 6, WallMs: 1234, Cores: 8,
		Blocks: []BlockRun{{Lane: 1, Name: "gateway-core", StartMs: 0, DurationMs: 1000, Passed: true}}}
	require.NoError(t, Write(path, tl))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var decoded Timeline
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, tl, decoded)
}

func TestWriteOverwritesAnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "timeline.json")

	require.NoError(t, Write(path, Timeline{SchemaVersion: SchemaVersion, Leg: "first"}))
	require.NoError(t, Write(path, Timeline{SchemaVersion: SchemaVersion, Leg: "second"}))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded Timeline
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, "second", decoded.Leg)
}
