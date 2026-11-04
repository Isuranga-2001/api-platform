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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// EnvOut overrides the default timeline output file.
const EnvOut = "IT_TIMELINE_OUT"

// SchemaVersion is the version of the Timeline JSON layout.
const SchemaVersion = 1

// Timeline is the record of one suite run: how long each block took, which lane ran it, and
// how many lanes the run allowed running at once.
type Timeline struct {
	SchemaVersion int        `json:"schemaVersion"`
	Leg           string     `json:"leg"`
	BlockParallel int        `json:"blockParallel"`
	WallMs        int64      `json:"wallMs"`
	Cores         int        `json:"cores"`
	Blocks        []BlockRun `json:"blocks"`
}

// BlockRun is one block of the suite and how long it ran, and the lane (counted from 1) that
// ran it. A block's time runs from the moment it acquired its lane to the moment every one of
// its runners and its teardown finished, so it includes booting the block's topology.
type BlockRun struct {
	Lane       int    `json:"lane"`
	Name       string `json:"name"`
	StartMs    int64  `json:"startMs"`
	DurationMs int64  `json:"durationMs"`
	Passed     bool   `json:"passed"`
}

// Recorder collects the block timings of a run. It is safe for concurrent use. The zero value
// is not ready to use; create one with NewRecorder.
type Recorder struct {
	mu     sync.Mutex
	start  time.Time
	blocks []BlockRun
}

// NewRecorder starts the clock of a run.
func NewRecorder() *Recorder {
	return &Recorder{start: time.Now()}
}

// Block records a block that ran on lane, which began at started and has just finished with the
// given result. lane is counted from 1.
func (r *Recorder) Block(lane int, name string, started time.Time, passed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocks = append(r.blocks, BlockRun{
		Lane:       lane,
		Name:       name,
		StartMs:    started.Sub(r.start).Milliseconds(),
		DurationMs: time.Since(started).Milliseconds(),
		Passed:     passed,
	})
}

// Timeline returns what the run has recorded so far for leg, a run bounded to blockParallel
// concurrent lanes, with the blocks in the order they started.
func (r *Recorder) Timeline(leg string, blockParallel int) Timeline {
	r.mu.Lock()
	defer r.mu.Unlock()
	blocks := append([]BlockRun{}, r.blocks...)
	sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].StartMs < blocks[j].StartMs })
	return Timeline{
		SchemaVersion: SchemaVersion,
		Leg:           leg,
		BlockParallel: blockParallel,
		WallMs:        time.Since(r.start).Milliseconds(),
		Cores:         runtime.NumCPU(),
		Blocks:        blocks,
	}
}

// Write writes t to path as indented JSON, creating its parent directory if needed.
func Write(path string, t Timeline) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("timeline: an output path is required")
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("timeline: encoding: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("timeline: creating %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("timeline: writing %q: %w", path, err)
	}
	return nil
}
