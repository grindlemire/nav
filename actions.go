package main

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"

	"github.com/dkaslovsky/nav/internal/sanitize"
)

func (m *model) Init() tea.Cmd {
	// If indexing is already active (e.g., started via -t flag), return polling command
	if m.searchIndexLoading && m.searchIndexChan != nil {
		return m.pollSearchIndexCmd()
	}
	return nil
}

func (m *model) View() string {
	var view string
	if m.modeExit {
		return ""
	}
	if m.modeHelp {
		view = commands()
	} else if m.modeTree {
		view = m.treeView()
	} else {
		view = m.normalView()
	}

	if m.hideStatusBar {
		return view
	}
	return strings.Join([]string{view, m.statusBar()}, "\n")
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	esc := false

	switch msg := msg.(type) {

	case fuzzySearchResultMsg:
		// Ignore stale results from old workers
		if msg.generation != m.searchWorkerGeneration {
			return m, nil // Don't continue polling - wrong generation
		}
		// Ignore stale results where query changed
		if msg.query != m.search {
			return m, m.pollSearchResultCmd()
		}
		// Build tree on main thread (fast relative to fuzzy)
		m.rebuildVisibleNodesFromMatches(msg.matches)
		return m, m.pollSearchResultCmd()

	case searchIndexBatchMsg:
		// Ignore stale batches from old indexers
		if msg.generation != m.searchIndexGeneration {
			return m, nil // Don't continue polling - wrong generation
		}

		// Capture index length before appending (for incremental matching)
		startIdx := len(m.searchIndexNodes)

		// Append new nodes to index
		for _, node := range msg.nodes {
			m.searchIndexNodes = append(m.searchIndexNodes, node)
			if node.entry != nil {
				m.searchIndexNames = append(m.searchIndexNames, node.entry.Name())
			} else {
				m.searchIndexNames = append(m.searchIndexNames, "")
			}
		}

		// Incremental fuzzy matching: only search new nodes, then merge results
		if m.search != "" && len(msg.nodes) > 0 {
			// Only fuzzy search the NEW names
			newNames := m.searchIndexNames[startIdx:]
			newMatches := fuzzy.Find(m.search, newNames)

			// Adjust indices to be absolute (add startIdx offset)
			for i := range newMatches {
				newMatches[i].Index += startIdx
			}

			// Merge into pending matches (maintain score order)
			m.searchPendingMatches = mergeMatchesByScore(m.searchPendingMatches, newMatches)

			// Rebuild tree from merged matches
			m.rebuildVisibleNodesFromMatches(m.searchPendingMatches)
		}

		// Continue polling if not done
		if !msg.done {
			return m, m.pollSearchIndexCmd()
		}
		// Indexing complete - clear loading state and channel reference
		m.searchIndexLoading = false
		m.searchIndexChan = nil
		// Clear pending matches since indexing is done
		m.searchPendingMatches = nil
		// If we're in search mode and worker isn't running, start it now that we have an index
		if m.modeSearch && m.modeTree && m.searchQueryChan == nil && len(m.searchIndexNodes) > 0 {
			return m, m.startSearchWorker()
		}
		return m, nil

	case tea.WindowSizeMsg:
		if result := actionWindowResize(m, msg, esc); !result.noop {
			return m, result.cmd
		}

	case tea.KeyMsg:

		// Remapped escape logic
		if key.Matches(msg, m.esc.key) {
			if m.esc.triggered() {
				esc = true
			}
		} else {
			m.esc.reset()
		}

		if result := actionQuit(m, msg, esc); !result.noop {
			return m, result.cmd
		}

		if m.modeError {
			if result := actionModeError(m, msg, esc); !result.noop {
				return m, result.cmd
			}
		}

		if m.modeHelp {
			if result := actionModeHelp(m, msg, esc); !result.noop {
				return m, result.cmd
			}
		}

		if m.modeSearch {
			if result := actionModeSearch(m, msg, esc); !result.noop {
				return m, result.cmd
			}
		}

		if m.modeMarks {
			if result := actionModeMarks(m, msg, esc); !result.noop {
				return m, result.cmd
			}
		}

		if m.modeTree {
			if result := actionModeTree(m, msg, esc); !result.noop {
				return m, result.cmd
			}
		}

		if result := actionModeGeneral(m, msg, esc); !result.noop {
			return m, result.cmd
		}

	}

	m.saveCursor()

	// Auto-start polling if indexing is active but no command is running
	if m.searchIndexLoading && m.searchIndexChan != nil {
		return m, m.pollSearchIndexCmd()
	}

	return m, nil
}

type actionResult struct {
	noop bool
	cmd  tea.Cmd
}

func newActionResult(cmd tea.Cmd) actionResult {
	return actionResult{
		noop: false,
		cmd:  cmd,
	}
}

func newActionResultNoop() actionResult {
	return actionResult{
		noop: true,
		cmd:  nil,
	}
}

func actionWindowResize(m *model, msg tea.WindowSizeMsg, esc bool) actionResult {
	m.width = msg.Width
	m.height = msg.Height
	return newActionResult(nil)
}

func actionQuit(m *model, msg tea.KeyMsg, esc bool) actionResult {
	if key.Matches(msg, keyQuit) {
		m.setExitWithCode("", 2)
		return newActionResult(tea.Quit)
	}

	return newActionResultNoop()
}

func actionModeError(m *model, msg tea.KeyMsg, esc bool) actionResult {
	if key.Matches(msg, keyDismissError) {
		m.clearError()
	}

	return newActionResult(nil)
}

func actionModeHelp(m *model, msg tea.KeyMsg, esc bool) actionResult {
	if esc || key.Matches(msg, keyEsc) || key.Matches(msg, keyModeHelp) {
		m.modeHelp = false
	}

	// Unconditional return to disable all other functionality.
	return newActionResult(nil)
}

func actionModeSearch(m *model, msg tea.KeyMsg, esc bool) actionResult {
	if esc || key.Matches(msg, keyEsc) {
		// Exit search mode - in tree mode, restore cursor to currently selected node
		m.modeSearch = false
		if m.modeTree {
			// Save the currently selected node's path before clearing search
			selectedNode := m.selectedTreeNode()
			var savedPath string
			if selectedNode != nil {
				savedPath = selectedNode.fullPath
			}
			m.treeSearchStartNode = nil
			m.searchMatchNodes = nil
			m.search = ""                // Clear search to unfilter
			m.searchPendingMatches = nil // Clear pending matches
			m.stopSearchWorker()         // Stop background worker
			m.rebuildVisibleNodes()
			// Find and position cursor on the saved node
			if savedPath != "" {
				for i, node := range m.visibleNodes {
					if node.fullPath == savedPath {
						m.treeIdx = i
						m.adjustScrollOffset()
						break
					}
				}
			}
		}
		return newActionResult(nil)
	}

	switch {

	// Do not allow remapped escape key character as part of the search.
	case key.Matches(msg, m.esc.key):
		return newActionResult(nil)

	case key.Matches(msg, keyBack):
		if len(m.search) > 0 {
			m.search = m.search[:len(m.search)-1]
			if m.modeTree {
				// Dispatch to background worker if active, otherwise rebuild synchronously
				if m.searchQueryChan != nil {
					select {
					case m.searchQueryChan <- m.search:
					default:
					}
					return newActionResult(m.pollSearchResultCmd())
				}
				m.rebuildVisibleNodes()
			}
			return newActionResult(nil)
		}

		// Empty search - do nothing (don't navigate up directory)
		return newActionResult(nil)

	case key.Matches(msg, keySelect):
		// In tree mode, Enter exits search mode but keeps filtered view
		if m.modeTree {
			// Exit search mode but keep the filter
			m.modeSearch = false
			// Keep m.search set to maintain filtered view
			// Keep m.treeSearchStartNode to maintain search scope
			m.searchPendingMatches = nil // Clear pending matches
			m.stopSearchWorker()         // Stop background worker
			m.rebuildVisibleNodes()
			// Set cursor to first item in filtered view
			m.treeIdx = 0
			m.scrollOffset = 0
			m.adjustScrollOffset()
			return newActionResult(nil)
		}
		_, cmd := m.searchSelectAction()
		return newActionResult(cmd)

	case key.Matches(msg, keyTab):
		if m.displayed != 1 {
			return newActionResult(nil)
		}
		_, cmd := m.searchSelectAction()
		return newActionResult(cmd)

	case key.Matches(msg, keyFileSeparator):
		if m.displayed != 1 {
			m.search += keyString(keyFileSeparator)
			return newActionResult(nil)
		}
		if selected, err := m.selected(); err == nil && selected.hasMode(entryModeFile) {
			m.search += keyString(keyFileSeparator)
			return newActionResult(nil)
		}
		_, cmd := m.searchSelectAction()
		return newActionResult(cmd)

	case key.Matches(msg, keySearchSlash):
		// "/" in search mode adds "/" to search string
		// (On Unix, keyFileSeparator handles this, but this case handles it on other systems)
		m.search += "/"
		if m.modeTree {
			// Dispatch to background worker if active, otherwise rebuild synchronously
			if m.searchQueryChan != nil {
				select {
				case m.searchQueryChan <- m.search:
				default:
				}
				return newActionResult(m.pollSearchResultCmd())
			}
			m.rebuildVisibleNodes()
		}
		return newActionResult(nil)

	default:
		if msg.Type == tea.KeyRunes || key.Matches(msg, keySpace) {
			m.search += string(msg.Runes)
			if m.modeTree {
				// Dispatch to background worker if active, otherwise rebuild synchronously
				if m.searchQueryChan != nil {
					select {
					case m.searchQueryChan <- m.search:
					default:
					}
					return newActionResult(m.pollSearchResultCmd())
				}
				m.rebuildVisibleNodes()
			}
			return newActionResult(nil)
		}

	}

	return newActionResultNoop()
}

func actionModeMarks(m *model, msg tea.KeyMsg, esc bool) actionResult {
	if key.Matches(msg, keyMarkAll) {
		err := m.toggleMarkAll()
		if err != nil {
			m.setError(err, "failed to update marks")
		}
		// Continue polling if in tree mode with active indexing
		return newActionResult(m.indexingCmd())
	}

	return newActionResultNoop()
}

func actionModeTree(m *model, msg tea.KeyMsg, esc bool) actionResult {
	// Reset gPressed if any key other than 'g' is pressed
	if !key.Matches(msg, keyGotoTop) {
		m.gPressed = false
	}

	switch {

	case key.Matches(msg, keyGotoBottom):
		m.treeMoveToBottom()
		return newActionResult(m.indexingCmd())

	case key.Matches(msg, keyGotoTop):
		if m.gPressed {
			// Second 'g' press - jump to top
			m.treeMoveToTop()
			m.gPressed = false
		} else {
			// First 'g' press - wait for second
			m.gPressed = true
		}
		return newActionResult(m.indexingCmd())

	case key.Matches(msg, keyModeSearch), key.Matches(msg, keySearchSlash):
		// Enter search mode - save parent of current node as search start
		// "/" also starts search in normal mode
		if !m.modeSearch {
			m.modeSearch = true
			selectedNode := m.selectedTreeNode()
			// Search from parent of selected node, or root if no parent
			if selectedNode != nil && selectedNode.parent != nil {
				m.treeSearchStartNode = selectedNode.parent
			} else {
				// No parent (at root level), use root
				m.treeSearchStartNode = m.treeRoot
			}
			// Start background search worker if we have an index
			var cmd tea.Cmd
			if len(m.searchIndexNodes) > 0 {
				cmd = m.startSearchWorker()
				// If there's already a search query, trigger search immediately
				if m.search != "" {
					select {
					case m.searchQueryChan <- m.search:
					default:
					}
					if cmd == nil {
						cmd = m.pollSearchResultCmd()
					}
				}
			} else {
				// No index yet, use fallback search
				if m.search != "" {
					m.rebuildVisibleNodesWithSearch()
				}
			}
			return newActionResult(cmd)
		}
		// If already in search mode, "/" should be handled by search handler
		return newActionResultNoop()

	case key.Matches(msg, keyUp):
		m.treeMoveUp()
		return newActionResult(m.indexingCmd())

	case key.Matches(msg, keyDown):
		m.treeMoveDown()
		return newActionResult(m.indexingCmd())

	case key.Matches(msg, keyLeft):
		m.treeCollapse()
		// treeCollapse may navigate to parent dir which restarts indexing
		return newActionResult(m.indexingCmd())

	case key.Matches(msg, keyRight):
		cmd := m.treeExpand()
		// Ensure polling continues even if treeExpand returns nil
		return newActionResult(tea.Batch(cmd, m.indexingCmd()))

	case key.Matches(msg, keyToggleExpand):
		if !m.modeSearch {
			cmd := m.treeToggleExpand()
			// Ensure polling continues even if treeToggleExpand returns nil
			return newActionResult(tea.Batch(cmd, m.indexingCmd()))
		}

	case key.Matches(msg, keySelect):
		result := m.treeSelectAction()
		return result

	case key.Matches(msg, keyMark):
		if !m.modeSearch {
			m.toggleTreeMark()
			return newActionResult(m.indexingCmd())
		}

	case key.Matches(msg, keyBack):
		// Backspace only active in search mode for tree
		if m.modeSearch {
			return newActionResultNoop() // Let search handler deal with it
		}
		return newActionResultNoop() // No-op in tree mode

	}

	return newActionResultNoop()
}

func (m *model) treeSelectAction() actionResult {
	// If in normal mode with filtered view, return all fuzzy match results
	if !m.modeSearch && m.search != "" {
		if len(m.searchMatchNodes) == 0 {
			return newActionResult(m.indexingCmd())
		}
		var paths []string
		for _, node := range m.searchMatchNodes {
			if node.entry == nil {
				continue
			}
			var path string
			if node.entry.hasMode(entryModeSymlink) {
				// Use parent directory for symlink resolution
				parentPath := filepath.Dir(node.fullPath)
				sl, err := followSymlink(parentPath, node.entry)
				if err != nil {
					// Skip symlinks that can't be resolved
					continue
				}
				path = sanitize.SanitizeOutputPath(sl.absPath)
			} else {
				path = sanitize.SanitizeOutputPath(node.fullPath)
			}
			paths = append(paths, path)
		}
		if len(paths) > 0 {
			// Output one path per line
			m.setExit(strings.Join(paths, "\n"))
			m.clearSearch()
			return newActionResult(tea.Sequence(tea.ClearScreen, tea.Quit))
		}
		return newActionResult(m.indexingCmd())
	}

	node := m.selectedTreeNode()
	if node == nil || node.entry == nil {
		return newActionResult(m.indexingCmd())
	}

	m.saveCursor()

	// For files: return path and quit
	if node.entry.hasMode(entryModeFile) {
		m.setExit(sanitize.SanitizeOutputPath(node.fullPath))
		// Clear screen if exiting from search
		if m.search != "" {
			return newActionResult(tea.Sequence(tea.ClearScreen, tea.Quit))
		}
		return newActionResult(tea.Quit)
	}

	// For directories: return path and quit (same as files)
	if node.entry.hasMode(entryModeDir) {
		m.setExit(sanitize.SanitizeOutputPath(node.fullPath))
		// Clear screen if exiting from search
		if m.search != "" {
			return newActionResult(tea.Sequence(tea.ClearScreen, tea.Quit))
		}
		return newActionResult(tea.Quit)
	}

	// Handle symlinks
	if node.entry.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, node.entry)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			return newActionResult(m.indexingCmd())
		}
		if !sl.info.IsDir() {
			// The symlink points to a file.
			m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
			// Clear screen if exiting from search
			if m.search != "" {
				return newActionResult(tea.Sequence(tea.ClearScreen, tea.Quit))
			}
			return newActionResult(tea.Quit)
		}
		// The symlink points to a directory: return path and quit
		m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
		// Clear screen if exiting from search
		if m.search != "" {
			return newActionResult(tea.Sequence(tea.ClearScreen, tea.Quit))
		}
		return newActionResult(tea.Quit)
	}

	m.setError(
		errors.New("selection is not a file, directory, or symlink"),
		"unexpected file type",
	)
	return newActionResult(m.indexingCmd())
}

func actionModeGeneral(m *model, msg tea.KeyMsg, esc bool) actionResult {
	switch {

	// Normal mode escape
	case esc || key.Matches(msg, keyEsc):
		m.clearSearch()
		return newActionResult(m.indexingCmd())

	// Return

	case key.Matches(msg, keyReturnDirectory):
		m.setExit(sanitize.SanitizeOutputPath(m.path))
		return newActionResult(tea.Quit)

	case key.Matches(msg, keyReturnSelected):
		selecteds := []*entry{}
		paths := []string{}

		if m.modeMarks {
			for _, entryIdx := range m.marks {
				if entryIdx < len(m.entries) {
					selecteds = append(selecteds, m.entries[entryIdx])
				}
			}
			sortEntries(selecteds)
		} else {
			selected, err := m.selected()
			if err != nil {
				m.setError(err, "failed to select entry")
				return newActionResult(m.indexingCmd())
			}
			selecteds = append(selecteds, selected)
		}

		for _, selected := range selecteds {
			var path string
			if selected.hasMode(entryModeSymlink) {
				sl, err := followSymlink(m.path, selected)
				if err != nil {
					m.setError(err, "failed to evaluate symlink")
					return newActionResult(m.indexingCmd())
				}
				path = sanitize.SanitizeOutputPath(sl.absPath)
			} else {
				path = sanitize.SanitizeOutputPath(filepath.Join(m.path, selected.Name()))
			}
			paths = append(paths, path)
		}

		m.setExit(strings.Join(paths, " "))
		return newActionResult(tea.Quit)

	// Cursor

	case key.Matches(msg, keyUp):
		m.moveUp()

	case key.Matches(msg, keyDown):
		m.moveDown()

	case key.Matches(msg, keyLeft):
		m.moveLeft()

	case key.Matches(msg, keyRight):
		m.moveRight()

	// Selectors

	case key.Matches(msg, keySelect):
		m.clearMarks()
		_, cmd := m.selectAction()
		return newActionResult(cmd)

	case key.Matches(msg, keyBack):
		// Backspace is a no-op in tree mode normal mode
		if m.modeTree {
			return newActionResultNoop()
		}

		m.saveCursor()

		path, err := filepath.Abs(filepath.Join(m.path, ".."))
		if err != nil {
			m.setError(err, "failed to evaluate path")
			return newActionResult(nil)
		}
		m.setPath(path)

		err = m.list()
		if err != nil {
			m.restorePath()
			m.setError(err, err.Error())
			return newActionResult(nil)
		}

		m.clearSearch()
		m.clearMarks()

		// Return to ensure the cursor is not re-saved using the updated path.
		return newActionResult(nil)

	case key.Matches(msg, keyMark):
		if m.normalMode() {
			err := m.toggleMark()
			if err != nil {
				m.setError(err, "failed to update mark")
			}
			return newActionResult(nil)
		}

	case key.Matches(msg, keyMarkAll):
		if m.normalMode() {
			err := m.markAll()
			if err != nil {
				m.setError(err, "failed to mark all entries")
				return newActionResult(nil)
			}
			return newActionResult(nil)
		}

	// Change modes

	case key.Matches(msg, keyModeHelp):
		m.modeHelp = true
		return newActionResult(tea.ClearScreen)

	case key.Matches(msg, keyModeSearch):
		m.modeSearch = true
		m.clearMarks()
		// If in tree mode with index, start search worker
		if m.modeTree && len(m.searchIndexNodes) > 0 {
			return newActionResult(m.startSearchWorker())
		}

	// Toggles

	case key.Matches(msg, keyToggleFollowSymlink):
		m.modeFollowSymlink = !m.modeFollowSymlink

	case key.Matches(msg, keyToggleHidden):
		m.modeHidden = !m.modeHidden
		if m.modeTree {
			m.rebuildVisibleNodes()
			return newActionResult(m.indexingCmd())
		}

	case key.Matches(msg, keyToggleList):
		m.modeList = !m.modeList

	case key.Matches(msg, keyToggleTree):
		m.modeTree = !m.modeTree
		if m.modeTree {
			// Initialize tree mode
			err, cmd := m.listTree()
			if err != nil {
				m.setError(err, "failed to initialize tree view")
				m.modeTree = false
				return newActionResult(nil)
			} else {
				m.treeIdx = 0
				m.scrollOffset = 0
				return newActionResult(cmd)
			}
		} else {
			// Switch back to normal mode - stop indexing and clear cache
			m.stopSearchIndexLoader()
			m.stopSearchWorker() // Stop search worker
			m.searchIndexNodes = nil
			m.searchIndexNames = nil
			m.searchIndexRoot = nil
			m.searchPendingMatches = nil
			if err := m.list(); err != nil {
				m.setError(err, "failed to switch to normal view")
				m.modeTree = true
			} else {
				m.resetCursor()
			}
		}

	}

	return newActionResultNoop()
}
