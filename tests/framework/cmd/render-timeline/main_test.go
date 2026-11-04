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

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wso2/api-platform/tests/framework/core/timeline"
)

func writeTimeline(t *testing.T, tl timeline.Timeline) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timeline.json")
	data, err := json.Marshal(tl)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

// --- run ---

func TestRunRequiresInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "-input is required")
}

func TestRunRejectsAnUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-nope"}, &stdout, &stderr)
	require.Equal(t, 2, code)
}

func TestRunPrintsANoteWhenTheFileIsMissing(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-input", filepath.Join(t.TempDir(), "missing.json")}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "No timing is available")
}

func TestRunPrintsANoteOnMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.json")
	require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o644))

	var stdout, stderr bytes.Buffer
	code := run([]string{"-input", path}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "No timing is available")
}

func TestRunPrintsANoteOnAnUnsupportedSchemaVersion(t *testing.T) {
	path := writeTimeline(t, timeline.Timeline{SchemaVersion: timeline.SchemaVersion + 1})

	var stdout, stderr bytes.Buffer
	code := run([]string{"-input", path}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "No timing is available")
	require.Contains(t, stdout.String(), "unsupported timeline schema")
}

func TestRunRendersAnEmptyTimelineAsNoBlockRan(t *testing.T) {
	path := writeTimeline(t, timeline.Timeline{SchemaVersion: timeline.SchemaVersion, Leg: "gateway-latest"})

	var stdout, stderr bytes.Buffer
	code := run([]string{"-input", path}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Contains(t, stdout.String(), "execution map")
	require.Contains(t, stdout.String(), "No block ran.")
}

func TestRunRendersAFullTimelineToStdout(t *testing.T) {
	tl := timeline.Timeline{
		SchemaVersion: timeline.SchemaVersion,
		Leg:           "gateway-1.2.0",
		BlockParallel: 2,
		WallMs:        10_000,
		Cores:         8,
		Blocks: []timeline.BlockRun{
			{Lane: 1, Name: "gateway-core", StartMs: 0, DurationMs: 6_000, Passed: true},
			{Lane: 1, Name: "gateway-restart", StartMs: 6_000, DurationMs: 2_000, Passed: false},
			{Lane: 2, Name: "api-portal-core", StartMs: 0, DurationMs: 9_000, Passed: true},
		},
	}
	path := writeTimeline(t, tl)

	var stdout, stderr bytes.Buffer
	code := run([]string{"-input", path}, &stdout, &stderr)
	require.Equal(t, 0, code)
	require.Empty(t, stderr.String())

	got := stdout.String()
	require.Contains(t, got, "gateway-1.2.0")
	require.Contains(t, got, "```mermaid")
	require.Contains(t, got, "gateway-core")
	require.Contains(t, got, "gateway-restart")
	require.Contains(t, got, "api-portal-core")
	require.Contains(t, got, "section Lane 1")
	require.Contains(t, got, "section Lane 2")
	require.Contains(t, got, ": done,")
	require.Contains(t, got, ": crit,")
	require.Contains(t, got, "doneTaskBorderColor': '#90ee90'")
	require.Contains(t, got, "critBorderColor': '#cf222e'")
	require.Contains(t, got, "3 (2 passed, 1 failed)")
	require.Contains(t, got, "#### Time per lane")
	require.Contains(t, got, "<summary>Time per block</summary>")
	require.Contains(t, got, "✅")
	require.Contains(t, got, "❌")
}

func TestLoadTimelineRejectsAFileOverTheSizeLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timeline.json")
	oversized := bytes.Repeat([]byte("a"), maxInputBytes+1)
	require.NoError(t, os.WriteFile(path, oversized, 0o644))

	_, err := loadTimeline(path)
	require.ErrorContains(t, err, "exceeds")
}

// --- duration ---

func TestDuration(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{0, "0s"},
		{999, "0s"},
		{45_000, "45s"},
		{65_000, "1m 5s"},
		{3_665_000, "1h 1m 5s"},
		{-5_000, "0s"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, duration(c.ms), "duration(%d)", c.ms)
	}
}

// --- ganttLabel ---

func TestGanttLabelEscapesStructurallySignificantCharacters(t *testing.T) {
	require.Equal(t, "gateway-core - sqlite", ganttLabel("gateway-core : sqlite"))
	require.Equal(t, "a b", ganttLabel("a\nb"))
	require.Equal(t, "a b", ganttLabel("a\rb"))
}

// --- ganttDate ---

func TestGanttDateIsMonotonicWithOffset(t *testing.T) {
	require.Equal(t, "2024-01-01 00:00:00", ganttDate(0))
	require.Equal(t, "2024-01-01 00:01:05", ganttDate(65_000))
}

// --- lanesOf ---

func TestLanesOfGroupsAndOrders(t *testing.T) {
	tl := timeline.Timeline{Blocks: []timeline.BlockRun{
		{Lane: 2, Name: "second-lane-first", StartMs: 100},
		{Lane: 1, Name: "first-lane-second", StartMs: 50},
		{Lane: 1, Name: "first-lane-first", StartMs: 0},
	}}

	lanes := lanesOf(tl)
	require.Len(t, lanes, 2)
	require.Equal(t, 1, lanes[0].Number)
	require.Equal(t, []string{"first-lane-first", "first-lane-second"},
		[]string{lanes[0].Blocks[0].Name, lanes[0].Blocks[1].Name})
	require.Equal(t, 2, lanes[1].Number)
	require.Equal(t, "second-lane-first", lanes[1].Blocks[0].Name)
}

func TestLanesOfReturnsNoLanesWithoutBlocks(t *testing.T) {
	require.Empty(t, lanesOf(timeline.Timeline{}))
}

// --- laneTotals ---

func TestLaneTotalsSumsBusyTimeAndTakesTheLatestFinish(t *testing.T) {
	busy, finished := laneTotals([]timeline.BlockRun{
		{StartMs: 0, DurationMs: 1_000},
		{StartMs: 1_000, DurationMs: 2_000},
	})
	require.Equal(t, int64(3_000), busy)
	require.Equal(t, int64(3_000), finished)
}

func TestLaneTotalsOfNoBlocksIsZero(t *testing.T) {
	busy, finished := laneTotals(nil)
	require.Zero(t, busy)
	require.Zero(t, finished)
}

// --- gantt ---

func TestGanttFloorsAZeroDurationBlockToANonZeroBar(t *testing.T) {
	tl := timeline.Timeline{Leg: "default", Blocks: []timeline.BlockRun{
		{Lane: 1, Name: "instant", StartMs: 0, DurationMs: 0, Passed: true},
	}}
	out := gantt(tl, lanesOf(tl))
	// The rendered end date must differ from the start date, or Mermaid will not draw a bar.
	require.Contains(t, out, "2024-01-01 00:00:00, 2024-01-01 00:00:01")
}

func TestGanttTitlesTheDiagramWithTheLeg(t *testing.T) {
	tl := timeline.Timeline{Leg: "gateway-1.1.0", Blocks: []timeline.BlockRun{
		{Lane: 1, Name: "gateway-core", StartMs: 0, DurationMs: 1_000, Passed: true},
	}}
	out := gantt(tl, lanesOf(tl))
	require.True(t, strings.Contains(out, "title Integration suite on gateway-1.1.0"))
}
