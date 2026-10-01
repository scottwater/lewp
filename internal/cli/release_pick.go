package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/scottwater/lewp/internal/control"
	"github.com/scottwater/lewp/internal/identity"
	"golang.org/x/term"
)

// pickEntry is one selectable directory. Picking it releases every route,
// alias, and bare port registered at that path, like "lewp release --path".
// Rows show only its main host and folder; the ports and aliases released with
// it are counted in the plan totals rather than listed.
type pickEntry struct {
	path    string
	display string
	state   string
	host    string
}

// Picker and preview rows keep hosts and folders to these widths so a row fits
// on one terminal line; the end of a folder is the part that identifies it.
const (
	pickHostWidth   = 50
	pickFolderWidth = 40
)

var errPickCancelled = errors.New("selection cancelled")

// executePickRelease lets a person choose several registered paths from a list
// and releases them as one batch: every selected path is planned first, the
// combined plan is previewed and confirmed, and only then are the plans applied.
func executePickRelease(cfg Config, opts releaseOptions, interactive bool) int {
	if !interactive {
		fmt.Fprintln(cfg.Stderr, "lewp release: --pick requires an interactive terminal")
		fmt.Fprintln(cfg.Stderr, "Use --path <path> [--recursive] for scripted releases")
		return 2
	}
	// --forget also deletes released history, so offer those paths as well.
	listed, err := call(cfg, control.Request{Command: "list", All: opts.forget})
	if err != nil {
		return daemonError(cfg, err)
	}
	entries := groupPickEntries(listed.Entries, homeDir())
	if len(entries) == 0 {
		fmt.Fprintln(cfg.Stdout, "nothing to release")
		return 0
	}

	// One buffered reader serves both the picker and the confirmation prompt so
	// input typed ahead of the prompt is not lost between them.
	reader := bufio.NewReader(cfg.Stdin)
	selected, err := pickPaths(cfg, reader, entries)
	if errors.Is(err, errPickCancelled) || err == nil && len(selected) == 0 {
		fmt.Fprintln(cfg.Stdout, "release aborted")
		return 1
	}
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "lewp release: %v\n", err)
		return 2
	}
	cfg.Stdin = reader

	byPath := make(map[string]pickEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.path] = entry
	}
	var chosen []pickEntry
	var releasedCount, forgottenCount int
	plans := make([]*control.ReleasePlanReference, 0, len(selected))
	for _, path := range selected {
		pathOpts := opts
		pathOpts.path, pathOpts.pathSet = path, true
		planned, err := call(cfg, control.Request{Command: "release-plan", Release: releaseRequest(cfg, pathOpts)})
		if err != nil {
			return daemonError(cfg, err)
		}
		if planned.Release == nil || planned.ReleasePlan == nil {
			fmt.Fprintln(cfg.Stderr, "malformed daemon response: missing release result or plan")
			return 1
		}
		if len(planned.Release.Items) == 0 {
			continue
		}
		chosen = append(chosen, byPath[path])
		releasedCount += planned.Release.Released
		forgottenCount += planned.Release.Forgotten
		plans = append(plans, planned.ReleasePlan)
	}
	if len(plans) == 0 {
		fmt.Fprintln(cfg.Stdout, "nothing to release")
		return 0
	}
	if err := writePickPreview(cfg.Stdout, chosen, releasedCount, forgottenCount); err != nil {
		return releaseOutputError(cfg, err, false)
	}
	if opts.dryRun {
		return 0
	}
	confirmed, err := confirmRelease(cfg, opts.assumeYes, true)
	if err != nil {
		return releaseOutputError(cfg, err, false)
	}
	if !confirmed {
		return 1
	}

	var totals control.ReleaseResponse
	for i, plan := range plans {
		applied, err := call(cfg, control.Request{Command: "release-apply", ReleasePlan: plan})
		if err == nil && applied.Release == nil {
			err = errors.New("malformed daemon response: missing release result")
		}
		if err != nil {
			code := daemonError(cfg, err)
			if control.IsTransportError(err) {
				fmt.Fprintln(cfg.Stderr, "release outcome unknown; inspect current state before retrying")
			}
			fmt.Fprintf(cfg.Stderr, "released %d of %d selected paths before the failure\n", i, len(plans))
			return code
		}
		totals.Released += applied.Release.Released
		totals.Forgotten += applied.Release.Forgotten
	}
	if err := writeReleaseTotals(cfg.Stdout, totals); err != nil {
		return releaseOutputError(cfg, err, true)
	}
	return 0
}

// groupPickEntries collapses list rows into one entry per path, sorted by path,
// keeping the path's main host and whether it needs cleaning up.
func groupPickEntries(entries []control.ListEntry, home string) []pickEntry {
	byPath := map[string]*pickEntry{}
	var paths []string
	for _, entry := range entries {
		pick, seen := byPath[entry.Path]
		if !seen {
			display := entry.Path
			if home != "" && (entry.Path == home || strings.HasPrefix(entry.Path, home+"/")) {
				display = "~" + strings.TrimPrefix(entry.Path, home)
			}
			pick = &pickEntry{path: entry.Path, display: display}
			byPath[entry.Path] = pick
			paths = append(paths, entry.Path)
		}
		// Only flag paths worth cleaning up; up/down health is noise here.
		// Staleness is per path, so any stale row marks the whole path.
		switch {
		case entry.State == "stale":
			pick.state = "stale"
		case entry.State == "released" && pick.state == "":
			pick.state = "released"
		}
		// The route's primary host is the main URL; fall back to an alias, then
		// to a bare port name for paths that only lease ports.
		switch {
		case entry.Kind == identity.KindRoute:
			pick.host = entry.Host
		case entry.Kind == control.KindAlias && pick.host == "":
			pick.host = entry.Host
		case pick.host == "":
			pick.host = "port " + dashIfEmpty(entry.Name)
		}
	}
	sort.Strings(paths)
	picks := make([]pickEntry, 0, len(paths))
	for _, path := range paths {
		picks = append(picks, *byPath[path])
	}
	return picks
}

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// pickPaths uses the arrow-key checklist when both ends are a real terminal and
// falls back to a numbered prompt otherwise.
func pickPaths(cfg Config, reader *bufio.Reader, entries []pickEntry) ([]string, error) {
	in, inOK := cfg.Stdin.(*os.File)
	out, outOK := cfg.Stdout.(*os.File)
	if !inOK || !outOK || !term.IsTerminal(int(in.Fd())) || !term.IsTerminal(int(out.Fd())) {
		return pickPathsNumbered(reader, cfg.Stdout, entries)
	}
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return pickPathsNumbered(reader, cfg.Stdout, entries)
	}
	defer func() { _ = term.Restore(int(in.Fd()), state) }()
	// Some pseudo-terminals report 0x0; treat that like an unknown size.
	width, height, err := term.GetSize(int(out.Fd()))
	if err != nil || width <= 0 || height <= 0 {
		width, height = 80, 24
	}
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h")
	return runPicker(reader, out, entries, width, height)
}

func pickPathsNumbered(reader *bufio.Reader, out io.Writer, entries []pickEntry) ([]string, error) {
	fmt.Fprintln(out, "Choose paths to release:")
	hostWidth, stateWidth := pickHostColumn(entries), pickStateWidth(entries)
	for i, entry := range entries {
		fmt.Fprintf(out, "  %2d) %s\n", i+1, pickRow(entry, hostWidth, stateWidth))
	}
	fmt.Fprint(out, "Enter numbers (e.g. 1 3 5-7), \"all\", or blank to cancel: ")
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return nil, errPickCancelled
	}
	indexes, err := parsePickSelection(line, len(entries))
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(indexes))
	for _, i := range indexes {
		paths = append(paths, entries[i].path)
	}
	return paths, nil
}

// parsePickSelection parses 1-based numbers and ranges separated by spaces or
// commas into sorted, de-duplicated 0-based indexes.
func parsePickSelection(input string, count int) ([]int, error) {
	input = strings.ToLower(strings.TrimSpace(input))
	if input == "" {
		return nil, nil
	}
	chosen := make([]bool, count)
	if input == "all" {
		for i := range chosen {
			chosen[i] = true
		}
	} else {
		for _, token := range strings.FieldsFunc(input, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			lo, hi, isRange := strings.Cut(token, "-")
			if !isRange {
				hi = lo
			}
			start, err1 := strconv.Atoi(lo)
			end, err2 := strconv.Atoi(hi)
			if err1 != nil || err2 != nil || start < 1 || end > count || start > end {
				return nil, fmt.Errorf("invalid selection %q; choose numbers between 1 and %d", token, count)
			}
			for n := start; n <= end; n++ {
				chosen[n-1] = true
			}
		}
	}
	var indexes []int
	for i, ok := range chosen {
		if ok {
			indexes = append(indexes, i)
		}
	}
	return indexes, nil
}

// runPicker drives the raw-mode checklist. It is independent of terminal setup
// so key handling can be tested with plain byte input.
func runPicker(reader *bufio.Reader, out io.Writer, entries []pickEntry, width, height int) ([]string, error) {
	cursor, offset := 0, 0
	checked := make([]bool, len(entries))
	visible := min(len(entries), max(height-3, 1))
	drawn := 0
	hostWidth, stateWidth := pickHostColumn(entries), pickStateWidth(entries)

	render := func() {
		if cursor < offset {
			offset = cursor
		} else if cursor >= offset+visible {
			offset = cursor - visible + 1
		}
		var b strings.Builder
		if drawn > 0 {
			fmt.Fprintf(&b, "\x1b[%dF", drawn)
		}
		count := 0
		for _, ok := range checked {
			if ok {
				count++
			}
		}
		lines := []string{fmt.Sprintf("Release which paths? %d selected (↑/↓ move, space toggle, a all, enter confirm, q cancel)", count)}
		for i := offset; i < offset+visible; i++ {
			pointer, box := "  ", "[ ]"
			if checked[i] {
				box = "[x]"
			}
			line := box + " " + pickRow(entries[i], hostWidth, stateWidth)
			if i == cursor {
				pointer = "❯ "
			}
			lines = append(lines, pointer+truncateRunes(line, width-3))
		}
		if len(entries) > visible {
			lines = append(lines, fmt.Sprintf("  (%d-%d of %d)", offset+1, offset+visible, len(entries)))
		}
		for i, line := range lines {
			if i == cursor-offset+1 {
				line = "\x1b[1;36m" + line + "\x1b[0m"
			}
			b.WriteString("\x1b[2K" + line + "\r\n")
		}
		drawn = len(lines)
		fmt.Fprint(out, b.String())
	}
	erase := func() {
		if drawn > 0 {
			fmt.Fprintf(out, "\x1b[%dF\x1b[J", drawn)
		}
	}

	render()
	for {
		key, err := reader.ReadByte()
		if err != nil {
			erase()
			return nil, errPickCancelled
		}
		switch key {
		case '\r', '\n':
			erase()
			var paths []string
			for i, ok := range checked {
				if ok {
					paths = append(paths, entries[i].path)
				}
			}
			return paths, nil
		case ' ', 'x':
			checked[cursor] = !checked[cursor]
		case 'a':
			all := true
			for _, ok := range checked {
				all = all && ok
			}
			for i := range checked {
				checked[i] = !all
			}
		case 'k':
			cursor = max(cursor-1, 0)
		case 'j':
			cursor = min(cursor+1, len(entries)-1)
		case 'q', 3:
			erase()
			return nil, errPickCancelled
		case 27:
			// A bare Esc cancels; arrow keys arrive as one ESC [ A/B burst.
			if reader.Buffered() == 0 {
				erase()
				return nil, errPickCancelled
			}
			if next, _ := reader.ReadByte(); next != '[' {
				continue
			}
			switch dir, _ := reader.ReadByte(); dir {
			case 'A':
				cursor = max(cursor-1, 0)
			case 'B':
				cursor = min(cursor+1, len(entries)-1)
			}
		default:
			continue
		}
		render()
	}
}

// writePickPreview prints one line per selected path followed by the plan
// totals, which still count every route, alias, and bare port being released.
func writePickPreview(w io.Writer, entries []pickEntry, released, forgotten int) error {
	if _, err := fmt.Fprintln(w, "Release plan:"); err != nil {
		return err
	}
	hostWidth, stateWidth := pickHostColumn(entries), pickStateWidth(entries)
	for _, entry := range entries {
		if _, err := fmt.Fprintf(w, "  %s\n", pickRow(entry, hostWidth, stateWidth)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "Planned releases: %d\nPlanned forgets: %d\n", released, forgotten)
	return err
}

// pickRow formats an entry's main host, cleanup state, and folder tail as
// aligned columns.
func pickRow(entry pickEntry, hostWidth, stateWidth int) string {
	row := fmt.Sprintf("%-*s  ", hostWidth, truncateRunes(entry.host, hostWidth))
	if stateWidth > 0 {
		row += fmt.Sprintf("%-*s  ", stateWidth, entry.state)
	}
	return row + folderTail(entry.display, pickFolderWidth)
}

func pickStateWidth(entries []pickEntry) int {
	width := 0
	for _, entry := range entries {
		width = max(width, len(entry.state))
	}
	return width
}

func pickHostColumn(entries []pickEntry) int {
	width := 0
	for _, entry := range entries {
		width = max(width, utf8.RuneCountInString(entry.host))
	}
	return min(width, pickHostWidth)
}

// folderTail keeps the end of a path, which is the part that tells sibling
// worktrees apart.
func folderTail(path string, width int) string {
	runes := []rune(path)
	if width <= 1 || len(runes) <= width {
		return path
	}
	return "…" + string(runes[len(runes)-width+1:])
}

func truncateRunes(s string, width int) string {
	if width <= 1 || utf8.RuneCountInString(s) <= width {
		return s
	}
	runes := []rune(s)
	return string(runes[:width-1]) + "…"
}
