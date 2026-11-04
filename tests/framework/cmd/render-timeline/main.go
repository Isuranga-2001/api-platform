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

// Command render-timeline prints the execution map of one integration suite run, as GitHub job
// summary markdown. Its input is the timeline JSON core/timeline's Recorder writes (schema
// version 1): a run keeps BlockParallel lanes busy, each lane running one block at a time and
// taking the next waiting block as soon as it finishes the last one. The map draws every lane as
// a Mermaid Gantt row, each block a bar whose length is its own running time, labelled with its
// name and how long it took. A passed block's bar has a light green border, a failed block's a
// red one.
//
// A missing, unreadable or malformed timeline prints a short note instead of the map and exits
// zero, so a reporting problem never fails the CI job that is uploading real test results.
//
// Usage: go run ./cmd/render-timeline -input it-timeline-1.2.0.json >> "$GITHUB_STEP_SUMMARY"
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wso2/api-platform/tests/framework/core/timeline"
)

// maxInputBytes bounds how much of the timeline file is read, so a malformed or truncated file
// from a broken run can never make this reporting step read an unbounded amount of input.
const maxInputBytes = 10 << 20 // 10 MiB

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("render-timeline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "the timeline JSON written by the suite's -timeline flag")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*input) == "" {
		fmt.Fprintln(stderr, "render-timeline: -input is required")
		return 2
	}

	tl, err := loadTimeline(*input)
	if err != nil {
		fmt.Fprintf(stdout, "### Integration suite: execution map\n\nNo timing is available: %v\n", err)
		return 0
	}
	fmt.Fprintln(stdout, render(tl))
	return 0
}

// loadTimeline reads and decodes the timeline JSON at path.
func loadTimeline(path string) (timeline.Timeline, error) {
	f, err := os.Open(path)
	if err != nil {
		return timeline.Timeline{}, err
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxInputBytes+1))
	if err != nil {
		return timeline.Timeline{}, err
	}
	if len(data) > maxInputBytes {
		return timeline.Timeline{}, fmt.Errorf("timeline file exceeds %d bytes", maxInputBytes)
	}

	var tl timeline.Timeline
	if err := json.Unmarshal(data, &tl); err != nil {
		return timeline.Timeline{}, err
	}
	if tl.SchemaVersion != timeline.SchemaVersion {
		return timeline.Timeline{}, fmt.Errorf("unsupported timeline schema %d", tl.SchemaVersion)
	}
	return tl, nil
}

// lane is one lane of a run, with the blocks it ran in the order it ran them.
type lane struct {
	Number int
	Blocks []timeline.BlockRun
}

// lanesOf groups a timeline's blocks by the lane that ran them, each lane's blocks ordered by
// when they started, and the lanes themselves ordered by number.
func lanesOf(tl timeline.Timeline) []lane {
	byNumber := map[int][]timeline.BlockRun{}
	for _, b := range tl.Blocks {
		byNumber[b.Lane] = append(byNumber[b.Lane], b)
	}
	numbers := make([]int, 0, len(byNumber))
	for n := range byNumber {
		numbers = append(numbers, n)
	}
	sort.Ints(numbers)

	lanes := make([]lane, 0, len(numbers))
	for _, n := range numbers {
		blocks := byNumber[n]
		sort.SliceStable(blocks, func(i, j int) bool { return blocks[i].StartMs < blocks[j].StartMs })
		lanes = append(lanes, lane{Number: n, Blocks: blocks})
	}
	return lanes
}

func endOf(b timeline.BlockRun) int64 { return b.StartMs + b.DurationMs }

// laneTotals returns how long a lane was busy running blocks and when it finished its last one.
func laneTotals(blocks []timeline.BlockRun) (busyMs, finishedMs int64) {
	for _, b := range blocks {
		busyMs += b.DurationMs
		if end := endOf(b); end > finishedMs {
			finishedMs = end
		}
	}
	return busyMs, finishedMs
}

// duration formats milliseconds as "1h 2m 3s", leaving out the leading units that are zero.
func duration(ms int64) string {
	seconds := ms / 1000
	if seconds < 0 {
		seconds = 0
	}
	hours := seconds / 3600
	seconds -= hours * 3600
	minutes := seconds / 60
	seconds -= minutes * 60
	switch {
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// ganttEpoch anchors every block's relative offset to a date Mermaid's Gantt chart can parse.
// Only elapsed time (the offset from this moment) is ever shown; the calendar date itself is
// never displayed.
var ganttEpoch = time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)

// ganttDate renders a block's offset, in milliseconds from the start of the run, as a Gantt
// chart date-time. Sub-second precision is not representable in the classic Mermaid Gantt date
// format, which is an acceptable rounding for bars measuring whole suite blocks.
func ganttDate(ms int64) string {
	return ganttEpoch.Add(time.Duration(ms) * time.Millisecond).Format("2006-01-02 15:04:05")
}

// ganttLabel escapes a block name for use as a Gantt task title: a literal colon would be read
// as the title/metadata separator, and a newline would start a new (unparsable) line.
func ganttLabel(name string) string {
	r := strings.NewReplacer(":", "-", "\n", " ", "\r", " ")
	return r.Replace(name)
}

// ganttThemeInit sets the Gantt task colors so a passed block's bar has a light green border
// and a failed block's a red one, matching the "done"/"crit" tags render assigns below.
const ganttThemeInit = `%%{init: {'theme': 'base', 'themeVariables': {` +
	`'taskBkgColor': '#ffffff', 'taskBorderColor': '#d1d9e0', 'taskTextColor': '#1f2328', ` +
	`'taskTextOutsideColor': '#1f2328', ` +
	`'doneTaskBkgColor': '#ffffff', 'doneTaskBorderColor': '#90ee90', 'doneTaskTextColor': '#1f2328', ` +
	`'critBkgColor': '#ffebe9', 'critBorderColor': '#cf222e', 'critTaskTextColor': '#82071e', ` +
	`'sectionBkgColor': '#f6f8fa', 'sectionBkgColor2': '#ffffff', 'altSectionBkgColor': '#ffffff'` +
	`}}}%%`

// gantt returns the fenced Mermaid Gantt diagram of the timeline's lanes: one section per lane,
// its blocks in the order it ran them, each bar's length its own running time.
func gantt(tl timeline.Timeline, lanes []lane) string {
	var b strings.Builder
	b.WriteString("```mermaid\n")
	b.WriteString(ganttThemeInit)
	b.WriteString("\ngantt\n")
	fmt.Fprintf(&b, "    title Integration suite on %s\n", ganttLabel(tl.Leg))
	b.WriteString("    dateFormat YYYY-MM-DD HH:mm:ss\n")
	b.WriteString("    axisFormat %H:%M:%S\n")
	b.WriteString("    todayMarker off\n")

	id := 0
	for _, l := range lanes {
		fmt.Fprintf(&b, "    section Lane %d\n", l.Number)
		for _, blk := range l.Blocks {
			tag := "done"
			if !blk.Passed {
				tag = "crit"
			}
			end := endOf(blk)
			if end-blk.StartMs < 1000 {
				// ganttDate only has one-second resolution: anything shorter would format to
				// the same start and end date-time, and Mermaid will not draw a zero-length bar.
				end = blk.StartMs + 1000
			}
			fmt.Fprintf(&b, "    %s (%s) : %s, b%d, %s, %s\n",
				ganttLabel(blk.Name), duration(blk.DurationMs), tag, id, ganttDate(blk.StartMs), ganttDate(end))
			id++
		}
	}
	b.WriteString("```\n")
	return b.String()
}

// render returns the whole job summary for one timeline.
func render(tl timeline.Timeline) string {
	var out strings.Builder
	fmt.Fprintf(&out, "### Integration suite on %s: execution map\n\n", tl.Leg)
	if len(tl.Blocks) == 0 {
		out.WriteString("No block ran.\n")
		return out.String()
	}

	lanes := lanesOf(tl)
	failed := 0
	blockMs := int64(0)
	for _, b := range tl.Blocks {
		blockMs += b.DurationMs
		if !b.Passed {
			failed++
		}
	}

	fmt.Fprintf(&out, "| | |\n| --- | --- |\n"+
		"| Blocks | %d (%d passed, %d failed) |\n"+
		"| Lanes (blocks at the same time, at most) | %d (%d used) |\n"+
		"| Time running the suite | %s |\n"+
		"| Time of all blocks added up | %s |\n\n",
		len(tl.Blocks), len(tl.Blocks)-failed, failed,
		tl.BlockParallel, len(lanes),
		duration(tl.WallMs), duration(blockMs))

	out.WriteString("Each lane runs one block at a time and takes the next waiting block as soon as it " +
		"finishes the last one. A bar starts where its block actually started, counted from the start of " +
		"the run, and its length is how long the block ran — booting its topology, every one of its " +
		"runners, and its teardown. A light green border marks a passed block, a red border a failed " +
		"one.\n\n")

	out.WriteString(gantt(tl, lanes))
	out.WriteString("\n")

	out.WriteString("#### Time per lane\n\n| Lane | Blocks | Busy | Finished at | Busy share |\n" +
		"| ---: | ---: | ---: | ---: | ---: |\n")
	for _, l := range lanes {
		busy, finished := laneTotals(l.Blocks)
		share := ""
		if finished > 0 {
			share = fmt.Sprintf("%d%%", (100*busy+finished/2)/finished)
		}
		fmt.Fprintf(&out, "| %d | %d | %s | %s | %s |\n", l.Number, len(l.Blocks), duration(busy), duration(finished), share)
	}

	out.WriteString("\n<details>\n<summary>Time per block</summary>\n\n" +
		"| Block | Lane | Result | Time |\n| --- | ---: | --- | ---: |\n")
	slowest := append([]timeline.BlockRun{}, tl.Blocks...)
	sort.SliceStable(slowest, func(i, j int) bool { return slowest[i].DurationMs > slowest[j].DurationMs })
	for _, b := range slowest {
		result := "✅"
		if !b.Passed {
			result = "❌"
		}
		fmt.Fprintf(&out, "| %s | %d | %s | %s |\n", b.Name, b.Lane, result, duration(b.DurationMs))
	}
	out.WriteString("\n</details>\n")

	return out.String()
}
