package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

func (m *model) treeView() string {
	if len(m.visibleNodes) == 0 {
		return m.treeLocationBar() + "\n\n\t(no entries)\n"
	}

	viewHeight := m.height - 3 // Account for location bar (1) and status bar (2)
	displayNameOpts := m.displayNameOpts()

	var output []string
	output = append(output, m.treeLocationBar())

	// Check if we'll need scroll indicators and account for their space
	hasTopIndicator := m.scrollOffset > 0
	if hasTopIndicator {
		viewHeight-- // Reserve space for top indicator
	}

	// Check if we'll need bottom indicator (before accounting for it)
	// We need to check if there will be content below after showing viewHeight items
	willHaveBottomIndicator := m.scrollOffset+viewHeight < len(m.visibleNodes)
	if willHaveBottomIndicator {
		viewHeight-- // Reserve space for bottom indicator
	}

	// Add scroll indicator at top if scrolled down
	if hasTopIndicator {
		remaining := m.scrollOffset
		indicator := barRendererScrollIndicator.Render(fmt.Sprintf(" ↑ %d more", remaining))
		output = append(output, indicator)
	}

	// Render visible slice based on scroll offset
	endIdx := min(m.scrollOffset+viewHeight, len(m.visibleNodes))
	startIdx := m.scrollOffset

	for i := startIdx; i < endIdx; i++ {
		node := m.visibleNodes[i]
		rawLine := m.renderTreeNode(node, i, displayNameOpts)

		// Pad line to full terminal width to ensure consistent diff rendering
		lineWidth := lipgloss.Width(rawLine)
		paddedLine := rawLine
		if lineWidth < m.width {
			paddedLine = rawLine + strings.Repeat(" ", m.width-lineWidth)
		}

		var finalLine string
		if i == m.treeIdx && !m.modeSearch {
			if m.markedTreeNode(i) {
				finalLine = cursorRendererSelectedMarked.Render(paddedLine)
			} else {
				finalLine = cursorRendererSelected.Render(paddedLine)
			}
		} else {
			if m.markedTreeNode(i) {
				finalLine = cursorRendererMarked.Render(paddedLine)
			} else {
				finalLine = cursorRendererNormal.Render(paddedLine)
			}
		}

		output = append(output, finalLine)
	}

	// Add scroll indicator at bottom if more content below
	if willHaveBottomIndicator {
		remaining := len(m.visibleNodes) - endIdx
		indicator := barRendererScrollIndicator.Render(fmt.Sprintf(" ↓ %d more", remaining))
		output = append(output, indicator)
	}

	// Pad output to fill viewport height (prevents ghost lines from previous renders)
	emptyLine := strings.Repeat(" ", m.width)
	for len(output) < m.height-2 { // -2 for 2-line status bar
		output = append(output, cursorRendererNormal.Render(emptyLine))
	}

	return strings.Join(output, "\n")
}

func (m *model) renderTreeNode(node *treeNode, idx int, opts []displayNameOption) string {
	if node.entry == nil {
		// Virtual root - shouldn't happen in normal rendering
		return ""
	}

	// Helper to check if there are more visible siblings at a given depth level
	// In DFS order, if we see another node at depth d before going back up (depth < d),
	// and they share the same parent at depth d-1, they're siblings
	hasMoreSiblingsAtDepth := func(depth int) bool {
		if depth == 0 {
			// Root level - check if there are more root-level nodes
			for i := idx + 1; i < len(m.visibleNodes); i++ {
				if m.visibleNodes[i].depth == 0 {
					return true
				}
			}
			return false
		}

		// For non-root levels, find the parent at depth-1
		parentAtDepthMinus1 := node
		for parentAtDepthMinus1 != nil && parentAtDepthMinus1.depth >= depth {
			parentAtDepthMinus1 = parentAtDepthMinus1.parent
		}

		// Look ahead for siblings (same parent at depth-1, same depth d)
		for i := idx + 1; i < len(m.visibleNodes); i++ {
			sibling := m.visibleNodes[i]
			if sibling.depth < depth {
				// Gone back up, no more siblings at this depth
				break
			}
			if sibling.depth == depth {
				// Found a node at same depth - check if it shares the same parent
				siblingParent := sibling
				for siblingParent != nil && siblingParent.depth >= depth {
					siblingParent = siblingParent.parent
				}
				if siblingParent == parentAtDepthMinus1 {
					return true
				}
			}
		}
		return false
	}

	// Build tree line prefix for each depth level
	var prefix strings.Builder
	for d := 0; d < node.depth; d++ {
		if hasMoreSiblingsAtDepth(d) {
			prefix.WriteString("│ ")
		} else {
			prefix.WriteString("  ")
		}
	}

	// Determine connector for this node - check if there are more siblings at same depth
	hasMoreSiblings := false
	if node.depth == 0 {
		// Root level - check for more root nodes
		for i := idx + 1; i < len(m.visibleNodes); i++ {
			if m.visibleNodes[i].depth == 0 {
				hasMoreSiblings = true
				break
			}
		}
	} else if node.parent != nil {
		// Look ahead for siblings with same parent
		for i := idx + 1; i < len(m.visibleNodes); i++ {
			sibling := m.visibleNodes[i]
			if sibling.depth < node.depth {
				// Gone back up, no more siblings
				break
			}
			if sibling.depth == node.depth && sibling.parent == node.parent {
				hasMoreSiblings = true
				break
			}
		}
	}

	var connector string
	if node.depth > 0 {
		if hasMoreSiblings {
			connector = "├─"
		} else {
			connector = "└─"
		}
	}

	// Expand/collapse indicator
	var indicator string
	if node.entry.hasMode(entryModeDir) {
		if node.expanded {
			indicator = "▼ "
		} else {
			indicator = "▶ "
		}
	} else {
		indicator = "  " // align with dirs
	}

	name := newDisplayName(node.entry, opts...)
	return prefix.String() + connector + indicator + name.String()
}

func (m *model) markedTreeNode(idx int) bool {
	// In tree mode, marks are based on visible node index
	_, marked := m.marks[idx]
	return marked
}

func (m *model) normalView() string {
	var (
		updateCache     = newCacheItem() // Cache for storing the current state as it is constructed.
		displayNames    = []*displayName{}
		displayNameOpts = m.displayNameOpts()
		displayed       = 0
		validEntries    = 0
	)

	// Construct display names from filtered entries and populate a new cache mapping between them.
	for entryIdx, ent := range m.entries {
		// Filter hidden files.
		if !m.modeHidden && ent.hasMode(entryModeHidden) {
			continue
		}

		validEntries++

		// Filter for search.
		if m.search != "" {
			if !strings.HasPrefix(ent.Name(), m.search) {
				continue
			}
		}

		displayNames = append(displayNames, newDisplayName(ent, displayNameOpts...))
		updateCache.addIndexPair(&indexPair{entry: entryIdx, display: displayed})
		displayed++
	}

	if validEntries == 0 {
		return m.locationBar() + "\n\n\t(no entries)\n"
	}

	if m.modeSearch || m.search != "" {
		if displayed == 0 && validEntries > 0 {
			return m.locationBar() + "\n\n\t(no matching entries)\n"
		}
	}

	// Grid layout for display.
	var (
		width     = m.width
		height    = m.height - 2 // Account for location and status bars.
		gridNames [][]string
		layout    gridLayout
	)
	if m.modeList {
		gridNames, layout = gridSingleColumn(displayNames, width, height)
	} else {
		gridNames, layout = gridMultiColumn(displayNames, width, height)
	}

	// Retrieve cached cursor position and index mappings to set cursor position for current state.
	updateCursorPosition := &position{c: 0, r: 0}
	if cache, found := m.pathCache[m.path]; found && cache.hasIndexes() {
		// Lookup the entry index using the cached cursor (display) position.
		if entryIdx, entryFound := cache.lookupEntryIndex(cache.cursorIndex()); entryFound {
			// Use the entry index to get the current display index.
			if dispIdx, dispFound := updateCache.lookupDisplayIndex(entryIdx); dispFound {
				// Set the cursor position using the current display index and layout.
				updateCursorPosition = newPositionFromIndex(dispIdx, layout.rows)
			}
		}
	}

	// Update the cache.
	updateCache.setPosition(updateCursorPosition)
	updateCache.setColumns(layout.columns)
	updateCache.setRows(layout.rows)

	// Update the model.
	m.pathCache[m.path] = updateCache
	m.displayed = displayed
	m.columns = layout.columns
	m.rows = layout.rows
	m.setCursor(updateCursorPosition)
	if m.c >= m.columns || m.r > m.rows {
		m.resetCursor()
	}
	if err := m.reloadMarks(); err != nil {
		m.setError(err, "failed to update marks")
	}

	// Render entry names in grid.
	gridOutput := make([]string, layout.rows)
	for row := 0; row < layout.rows; row++ {
		for col := 0; col < layout.columns; col++ {
			if col == m.c && row == m.r {
				if m.marked() {
					gridOutput[row] += cursorRendererSelectedMarked.Render(gridNames[col][row])
				} else {
					gridOutput[row] += cursorRendererSelected.Render(gridNames[col][row])
				}
			} else {
				if m.markedIndex(index(col, row, layout.rows)) {
					gridOutput[row] += cursorRendererMarked.Render(gridNames[col][row])
				} else {
					gridOutput[row] += cursorRendererNormal.Render(gridNames[col][row])
				}
			}
		}
	}

	// Construct the final view.
	output := []string{m.locationBar()}
	output = append(output, gridOutput...)
	return strings.Join(output, "\n")
}

type statusBarItem string

func (s statusBarItem) String() string { return string(s) }
func (s statusBarItem) Len() int       { return len(s) }

func (m *model) statusBar() string {
	const rows = 2

	var (
		mode string
		cmds []statusBarItem
	)

	if m.modeSearch {
		mode = "SEARCH"
		cmds = []statusBarItem{
			statusBarItem(fmt.Sprintf(`"%s": complete`, keyString(keyTab))),
			statusBarItem(fmt.Sprintf(`"%s": normal mode`, keyString(keyEsc))),
		}
	} else if m.modeHelp {
		mode = "HELP"
		cmds = []statusBarItem{
			statusBarItem(fmt.Sprintf(`"%s": normal mode`, keyString(keyEsc))),
		}
	} else {
		mode = "NORMAL"
		cmds = []statusBarItem{
			statusBarItem(fmt.Sprintf(`"%s": search`, keyString(keyModeSearch))),
			statusBarItem(fmt.Sprintf(`"%s": help`, keyString(keyModeHelp))),
			statusBarItem(fmt.Sprintf(`"%s": multiselect`, keyString(keyMark))),
		}
	}

	globalCmds := []statusBarItem{
		statusBarItem(fmt.Sprintf(`"%s": quit`, keyString(keyQuit))),
		statusBarItem(fmt.Sprintf(`"%s": return dir`, keyString(keyReturnDirectory))),
	}
	if m.modeMarks {
		globalCmds = append(globalCmds, statusBarItem(fmt.Sprintf(`"%s": return marked`, keyString(keyReturnSelected))))
	} else {
		globalCmds = append(globalCmds, statusBarItem(fmt.Sprintf(`"%s": return cursor`, keyString(keyReturnSelected))))
	}

	columns := max(len(cmds), len(globalCmds))
	for len(cmds) < columns {
		cmds = append(cmds, statusBarItem(""))
	}
	for len(globalCmds) < columns {
		globalCmds = append(globalCmds, statusBarItem(""))
	}
	cmds = append(cmds, globalCmds...)
	gridItems := gridRowMajorFixedLayout(cmds, columns, rows)

	nameAndMode := fmt.Sprintf(" %s   %s MODE  |", name, mode)
	output := strings.Join([]string{
		barRendererStatus.Render(
			fmt.Sprintf("%s\t%s\t",
				nameAndMode,
				strings.Join(gridItems[0], "\t\t"),
			),
		),
		barRendererStatus.Render(
			fmt.Sprintf("%s|\t%s\t",
				strings.Repeat(" ", len(nameAndMode)-1),
				strings.Join(gridItems[1], "\t\t"),
			),
		),
	}, "\n")

	return output
}

func (m *model) locationBar() string {
	err := ""
	if m.modeError {
		err = fmt.Sprintf(
			"\tERROR (\"%s\": dismiss): %s",
			keyString(keyDismissError),
			m.errorStr,
		)
		return barRendererError.Render(err + "\t\t")
	}

	locationBar := barRendererLocation.Render(m.location())
	if m.modeSearch || m.search != "" {
		if m.path != fileSeparator {
			locationBar += barRendererSearch.Render(fileSeparator + m.search)
		}
	}
	return locationBar
}

func (m *model) treeLocationBar() string {
	// Error mode: show error bar instead of breadcrumb
	if m.modeError {
		err := fmt.Sprintf(
			"\tERROR (\"%s\": dismiss): %s",
			keyString(keyDismissError),
			m.errorStr,
		)
		return barRendererError.Render(err + "\t\t")
	}

	// In search mode, show parent context + search query instead of full path breadcrumb
	if m.modeSearch || m.search != "" {
		return m.treeSearchLocationBar()
	}

	// Show indexing status if indexing is in progress
	// Only show if we actually have a channel (indexing started) and it's still loading
	if m.searchIndexLoading && m.searchIndexChan != nil {
		path := m.path
		if node := m.selectedTreeNode(); node != nil {
			path = node.fullPath
		}
		path = substituteHomeDir(path)
		breadcrumb := barRendererBreadcrumb.Render(path)
		count := formatAbbreviatedCount(len(m.searchIndexNodes))
		breadcrumb += barRendererSearchCount.Render(fmt.Sprintf(" (indexing %s files...)", count))
		return barRendererLocation.Render(breadcrumb)
	}

	// Get the selected node's full path for breadcrumb, fallback to m.path
	path := m.path
	if node := m.selectedTreeNode(); node != nil {
		path = node.fullPath
	}
	path = substituteHomeDir(path)
	if runtime.GOOS == "windows" {
		path = strings.ReplaceAll(strings.Replace(path, "\\/", fileSeparator, 1), "/", fileSeparator)
	}

	// Split path into components
	var components []string
	if path == fileSeparator || path == "~" {
		components = []string{path}
	} else {
		// Handle both absolute paths and paths starting with ~
		if strings.HasPrefix(path, "~"+fileSeparator) {
			components = append([]string{"~"}, strings.Split(strings.TrimPrefix(path, "~"+fileSeparator), fileSeparator)...)
		} else if strings.HasPrefix(path, fileSeparator) {
			components = strings.Split(strings.TrimPrefix(path, fileSeparator), fileSeparator)
			// Prepend root separator for absolute paths
			components = append([]string{fileSeparator}, components...)
		} else {
			components = strings.Split(path, fileSeparator)
		}
	}

	// Filter out empty components
	var cleanComponents []string
	for _, comp := range components {
		if comp != "" {
			cleanComponents = append(cleanComponents, comp)
		}
	}
	if len(cleanComponents) == 0 {
		cleanComponents = []string{fileSeparator}
	}

	// Build breadcrumb string
	var breadcrumbParts []string
	for i, comp := range cleanComponents {
		if i > 0 {
			separator := barRendererBreadcrumbSeparator.Render("/")
			breadcrumbParts = append(breadcrumbParts, separator)
		}

		// Last component (current directory) gets highlighted
		if i == len(cleanComponents)-1 {
			breadcrumbParts = append(breadcrumbParts, barRendererBreadcrumbCurrent.Render(comp))
		} else {
			breadcrumbParts = append(breadcrumbParts, barRendererBreadcrumb.Render(comp))
		}
	}

	breadcrumb := strings.Join(breadcrumbParts, "")

	// Render with location bar background
	return barRendererLocation.Render(breadcrumb)
}

// treeSearchLocationBar renders the location bar during tree search mode
// Shows: parent - search_query (X matched files)
func (m *model) treeSearchLocationBar() string {
	// Get the parent directory name being searched
	parentName := ""
	if m.treeSearchStartNode != nil {
		if m.treeSearchStartNode == m.treeRoot {
			parentName = substituteHomeDir(m.path)
		} else if m.treeSearchStartNode.entry != nil {
			parentName = substituteHomeDir(m.treeSearchStartNode.fullPath)
		}
	}
	if parentName == "" {
		parentName = m.path
	}

	// Build the location bar: parent - search_query
	breadcrumb := barRendererBreadcrumb.Render(parentName)
	breadcrumb += barRendererBreadcrumbSeparator.Render(" - ")
	breadcrumb += barRendererSearch.Render(m.search)

	// Show indexing status if still loading
	if m.searchIndexLoading {
		count := formatAbbreviatedCount(len(m.searchIndexNodes))
		breadcrumb += barRendererSearchCount.Render(fmt.Sprintf(" (indexing %s files...)", count))
	} else if m.search != "" && len(m.searchMatchNodes) > 0 {
		// Count matched files (non-directory leaves only)
		matchedFiles := 0
		for _, node := range m.searchMatchNodes {
			if node.entry != nil && !node.entry.hasMode(entryModeDir) {
				matchedFiles++
			}
		}
		if matchedFiles > 0 {
			breadcrumb += barRendererSearchCount.Render(fmt.Sprintf(" (%d matched files)", matchedFiles))
		}
	}

	return barRendererLocation.Render(breadcrumb)
}

func keyString(key key.Binding) string {
	return key.Keys()[0]
}

func max(i, j int) int {
	if i > j {
		return i
	}
	return j
}

// substituteHomeDir replaces the user's home directory with ~ in a path
func substituteHomeDir(path string) string {
	if userHomeDir, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, userHomeDir) {
		return strings.Replace(path, userHomeDir, "~", 1)
	}
	return path
}
