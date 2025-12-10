package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sahilm/fuzzy"
)

var fileSeparator = string(filepath.Separator)

// searchIndexBatchMsg delivers a batch of discovered nodes
type searchIndexBatchMsg struct {
	nodes      []*treeNode
	done       bool  // true when DFS traversal is complete
	generation int64 // generation counter to detect stale messages
}

// fuzzySearchResultMsg delivers fuzzy search results from background worker
type fuzzySearchResultMsg struct {
	query      string        // Query this result is for (detect stale)
	matches    []fuzzy.Match // Raw fuzzy matches with scores
	generation int64         // generation counter to detect stale messages
}

type model struct {
	path      string
	prevPath  string
	entries   []*entry
	displayed int
	exitCode  int
	exitStr   string
	error     error
	errorStr  string
	esc       *remappedEscKey
	search    string
	pathCache map[string]*cacheItem // Map path to cached state.
	marks     map[int]int           // Map display index to entry index for marked entries.

	c       int // Cursor column position.
	r       int // Cursor row position.
	columns int // Displayed columns.
	rows    int // Displayed columns.
	width   int // Terminal width.
	height  int // Terminal height.

	modeColor         bool
	modeError         bool
	modeExit          bool
	modeFollowSymlink bool
	modeHelp          bool
	modeHidden        bool
	modeList          bool
	modeMarks         bool
	modeSearch        bool
	modeSubshell      bool
	modeTrailing      bool
	modeTree          bool

	hideStatusBar bool

	// Tree mode fields
	treeRoot     *treeNode
	visibleNodes []*treeNode
	treeIdx      int
	scrollOffset int
	// treeLastChild maps parent directory path to the name of the last selected child.
	// Used to restore cursor position when re-expanding a previously collapsed directory.
	// Path-based (not pointer-based) so it survives tree node recreation during navigation.
	treeLastChild map[string]string
	// treeSearchStartNode is the node at cursor position when search mode is entered.
	// Search will be scoped to this node's subtree (or its parent's subtree if it's a file).
	treeSearchStartNode *treeNode
	// searchMatchNodes stores the actual fuzzy match results (not ancestors) for returning on Enter
	searchMatchNodes []*treeNode

	// Search index streaming fields
	searchIndexNodes     []*treeNode      // Accumulated nodes for fuzzy matching
	searchIndexNames     []string         // Cached names (parallel to searchIndexNodes)
	searchIndexLoading   bool             // True while background loader is running
	searchIndexChan      chan []*treeNode // Channel for receiving batches from goroutine
	searchIndexCancel    func()           // Cancel function to stop the background goroutine
	searchIndexRoot      *treeNode        // Root node being indexed (for reuse detection)
	searchPendingMatches []fuzzy.Match    // Accumulated matches during indexing (for incremental matching)

	// Background fuzzy search worker fields
	searchQueryChan        chan string               // Send queries to background worker
	searchResultChan       chan fuzzySearchResultMsg // Receive results
	searchWorkerCancel     func()                    // Cancel the worker goroutine
	searchIndexGeneration  int64                     // Generation counter for index loader (to detect stale messages)
	searchWorkerGeneration int64                     // Generation counter for search worker (to detect stale messages)

	// gPressed tracks whether 'g' was pressed for the 'gg' command to jump to top
	gPressed bool
}

func newModel() *model {
	return &model{
		width:     80,
		height:    60,
		esc:       defaultEscRemapKey(),
		pathCache: make(map[string]*cacheItem),
		marks:     make(map[int]int),

		modeColor:         true,
		modeError:         false,
		modeExit:          false,
		modeFollowSymlink: false,
		modeHelp:          false,
		modeHidden:        false,
		modeList:          false,
		modeMarks:         false,
		modeSearch:        false,
		modeSubshell:      false,
		modeTrailing:      true,
		modeTree:          false,

		hideStatusBar: false,

		treeIdx:             0,
		scrollOffset:        0,
		treeLastChild:       make(map[string]string),
		treeSearchStartNode: nil,
	}
}

func (m *model) normalMode() bool {
	return !(m.modeSearch || m.modeHelp)
}

func (m *model) list() error {
	files, err := os.ReadDir(m.path)
	if err != nil {
		return err
	}

	m.entries = []*entry{}
	for _, file := range files {
		ent, err := newEntry(file)
		if err != nil {
			return err
		}
		m.entries = append(m.entries, ent)
	}
	sortEntries(m.entries)

	return nil
}

func (m *model) selected() (*entry, error) {
	cache, ok := m.pathCache[m.path]
	if !ok {
		return nil, fmt.Errorf("cache item not found for %s", m.path)
	}
	idx, found := cache.lookupEntryIndex(m.displayIndex())
	if !found {
		return nil, errors.New("failed to map to valid entry index")
	}
	if idx > len(m.entries) {
		return nil, fmt.Errorf("invalid index %d for entries with length %d", idx, len(m.entries))
	}
	return m.entries[idx], nil
}

func (m *model) location() string {
	location := m.path
	if userHomeDir, err := os.UserHomeDir(); err == nil {
		location = strings.Replace(m.path, userHomeDir, "~", 1)
	}
	if runtime.GOOS == "windows" {
		location = strings.ReplaceAll(strings.Replace(location, "\\/", fileSeparator, 1), "/", fileSeparator)
	}
	return location
}

func (m *model) displayNameOpts() []displayNameOption {
	opts := []displayNameOption{}
	if m.modeColor {
		opts = append(opts, displayNameWithColor())
	}
	if m.modeFollowSymlink {
		opts = append(opts, displayNameWithFollowSymlink(m.path))
	}
	if m.modeList {
		opts = append(opts, displayNameWithList())
	}
	if m.modeTrailing {
		opts = append(opts, displayNameWithTrailing())
	}
	return opts
}

func (m *model) displayIndex() int {
	return index(m.c, m.r, m.rows)
}

func (m *model) setPath(path string) {
	m.prevPath = m.path
	m.path = path
}

func (m *model) restorePath() {
	if m.prevPath != "" {
		m.path = m.prevPath
		m.prevPath = ""
	}
}

func (m *model) setError(err error, status string) {
	m.modeError = true
	m.errorStr = status
	m.error = err
}

func (m *model) clearError() {
	m.modeError = false
	m.errorStr = ""
	m.error = nil
}

func (m *model) setExit(exitStr string) {
	m.setExitWithCode(exitStr, 0)
}

func (m *model) setExitWithCode(exitStr string, exitCode int) {
	m.modeExit = true
	m.exitStr = exitStr
	m.exitCode = exitCode
}

func (m *model) clearSearch() {
	m.modeSearch = false
	m.search = ""
	m.treeSearchStartNode = nil
	m.searchMatchNodes = nil
	m.searchPendingMatches = nil
	m.stopSearchWorker()
	// Note: searchIndexNodes/Names are kept for reuse
}

// stopSearchIndexLoader cancels the background indexing goroutine and cleans up
func (m *model) stopSearchIndexLoader() {
	if m.searchIndexCancel != nil {
		m.searchIndexCancel()
		m.searchIndexCancel = nil
	}
	if m.searchIndexChan != nil {
		// Drain channel to prevent goroutine leak
		ch := m.searchIndexChan
		m.searchIndexChan = nil
		go func() {
			for range ch {
			}
		}()
	}
	m.searchIndexLoading = false
}

// startSearchIndexLoader starts background indexing of the tree root
func (m *model) startSearchIndexLoader(root *treeNode) tea.Cmd {
	// Stop any existing loader
	m.stopSearchIndexLoader()

	if root == nil {
		m.searchIndexLoading = false
		return nil
	}

	m.searchIndexLoading = true
	m.searchIndexRoot = root
	m.searchIndexNodes = nil
	m.searchIndexNames = nil
	m.searchPendingMatches = nil // Clear pending matches when starting new index
	m.searchIndexGeneration++    // Increment generation to invalidate old messages

	ctx, cancel := context.WithCancel(context.Background())
	m.searchIndexCancel = cancel
	m.searchIndexChan = make(chan []*treeNode, 10)

	go func() {
		defer close(m.searchIndexChan)
		streamDFS(ctx, root, m.modeHidden, m.searchIndexChan)
	}()

	return m.pollSearchIndexCmd()
}

// pollSearchIndexCmd returns a command that reads the next batch from the channel
func (m *model) pollSearchIndexCmd() tea.Cmd {
	// Capture current generation to detect stale messages
	gen := m.searchIndexGeneration
	ch := m.searchIndexChan
	return func() tea.Msg {
		if ch == nil {
			return searchIndexBatchMsg{done: true, generation: gen}
		}
		batch, ok := <-ch
		if !ok {
			return searchIndexBatchMsg{done: true, generation: gen}
		}
		return searchIndexBatchMsg{nodes: batch, done: false, generation: gen}
	}
}

// indexingCmd returns the polling command if indexing is active, otherwise nil.
// Use this in action handlers that may trigger indexing to ensure polling starts.
func (m *model) indexingCmd() tea.Cmd {
	if m.searchIndexLoading && m.searchIndexChan != nil {
		return m.pollSearchIndexCmd()
	}
	return nil
}

// stopSearchWorker cancels the background fuzzy search worker and cleans up
func (m *model) stopSearchWorker() {
	if m.searchWorkerCancel != nil {
		m.searchWorkerCancel()
		m.searchWorkerCancel = nil
	}
	if m.searchQueryChan != nil {
		// Capture and drain channel to prevent goroutine leak
		ch := m.searchQueryChan
		m.searchQueryChan = nil
		go func() {
			for range ch {
			}
		}()
	}
	if m.searchResultChan != nil {
		// Capture and drain channel to prevent goroutine leak
		ch := m.searchResultChan
		m.searchResultChan = nil
		go func() {
			for range ch {
			}
		}()
	}
}

// startSearchWorker starts a background goroutine that processes search queries
func (m *model) startSearchWorker() tea.Cmd {
	// Stop any existing worker
	m.stopSearchWorker()

	if !m.modeTree {
		return nil
	}

	m.searchWorkerGeneration++ // Increment generation to invalidate old messages
	gen := m.searchWorkerGeneration

	ctx, cancel := context.WithCancel(context.Background())
	m.searchWorkerCancel = cancel
	m.searchQueryChan = make(chan string, 1)
	m.searchResultChan = make(chan fuzzySearchResultMsg, 1)

	go func() {
		defer close(m.searchResultChan)
		for {
			select {
			case <-ctx.Done():
				return
			case query, ok := <-m.searchQueryChan:
				if !ok {
					return
				}
				// Snapshot the current index length and slice for safe concurrent access
				// We capture the length first, then the slice up to that length
				indexLen := len(m.searchIndexNames)
				if indexLen == 0 {
					// Send empty result
					select {
					case <-ctx.Done():
						return
					case m.searchResultChan <- fuzzySearchResultMsg{query: query, matches: nil, generation: gen}:
					}
					continue
				}

				// Create a snapshot of names up to current length
				indexNames := make([]string, indexLen)
				copy(indexNames, m.searchIndexNames[:indexLen])

				// Run fuzzy search in background
				matches := fuzzy.Find(query, indexNames)

				// Send result (non-blocking)
				select {
				case <-ctx.Done():
					return
				case m.searchResultChan <- fuzzySearchResultMsg{query: query, matches: matches, generation: gen}:
				}
			}
		}
	}()

	return m.pollSearchResultCmd()
}

// pollSearchResultCmd returns a command that reads the next result from the channel
func (m *model) pollSearchResultCmd() tea.Cmd {
	// Capture current generation and channel to detect stale messages
	gen := m.searchWorkerGeneration
	ch := m.searchResultChan
	return func() tea.Msg {
		if ch == nil {
			return fuzzySearchResultMsg{query: "", matches: nil, generation: gen}
		}
		result, ok := <-ch
		if !ok {
			return fuzzySearchResultMsg{query: "", matches: nil, generation: gen}
		}
		return result
	}
}

// mergeMatchesByScore merges two sorted match slices maintaining score order
func mergeMatchesByScore(a, b []fuzzy.Match) []fuzzy.Match {
	result := make([]fuzzy.Match, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i].Score >= b[j].Score {
			result = append(result, a[i])
			i++
		} else {
			result = append(result, b[j])
			j++
		}
	}
	result = append(result, a[i:]...)
	result = append(result, b[j:]...)
	return result
}

// rebuildVisibleNodesFromMatches builds visible nodes from fuzzy match results
func (m *model) rebuildVisibleNodesFromMatches(fuzzyMatches []fuzzy.Match) {
	if len(fuzzyMatches) == 0 {
		m.visibleNodes = nil
		m.displayed = 0
		m.searchMatchNodes = nil
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = max(0, len(m.visibleNodes)-1)
		}
		return
	}

	// Determine search root
	searchRoot := m.treeSearchStartNode
	if searchRoot == nil {
		searchRoot = m.treeRoot
	}

	// Filter matches to only include nodes under the search root
	// This is needed because the index may contain nodes from parent directories
	searchRootPrefix := searchRoot.fullPath + string(filepath.Separator)
	matchingNodes := make([]*treeNode, 0, len(fuzzyMatches))
	for _, match := range fuzzyMatches {
		if match.Index < len(m.searchIndexNodes) {
			node := m.searchIndexNodes[match.Index]
			// Only include if node is under search root (or is the search root itself)
			if node.fullPath == searchRoot.fullPath ||
				strings.HasPrefix(node.fullPath, searchRootPrefix) {
				matchingNodes = append(matchingNodes, node)
			}
		}
	}

	m.searchMatchNodes = matchingNodes

	m.visibleNodes = buildFilteredTree(searchRoot, matchingNodes, m.modeHidden)
	m.displayed = len(m.visibleNodes)

	if m.treeIdx >= len(m.visibleNodes) {
		m.treeIdx = max(0, len(m.visibleNodes)-1)
	}
}

// rebuildVisibleNodesFromIndex filters visible nodes using the cached search index
func (m *model) rebuildVisibleNodesFromIndex() {
	if len(m.searchIndexNodes) == 0 || m.search == "" {
		m.visibleNodes = nil
		m.displayed = 0
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = max(0, len(m.visibleNodes)-1)
		}
		return
	}

	// Run fuzzy matching on accumulated index
	fuzzyMatches := fuzzy.Find(m.search, m.searchIndexNames)
	m.rebuildVisibleNodesFromMatches(fuzzyMatches)
}

// formatAbbreviatedCount formats a count as abbreviated (e.g., 5132 -> "5K")
func formatAbbreviatedCount(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	if count < 1000000 {
		return fmt.Sprintf("%dK", count/1000)
	}
	return fmt.Sprintf("%dM", count/1000000)
}

// handleRootChange handles root directory changes by reusing index where possible
func (m *model) handleRootChange(newRoot *treeNode) tea.Cmd {
	if newRoot == nil {
		return nil
	}

	oldRoot := m.searchIndexRoot
	if oldRoot == nil {
		// No previous index, start fresh
		return m.startSearchIndexLoader(newRoot)
	}

	// Check if new root is descendant of old root (navigated DOWN)
	if strings.HasPrefix(newRoot.fullPath+string(filepath.Separator), oldRoot.fullPath+string(filepath.Separator)) {
		// Filter existing index to nodes under new root
		filteredNodes := make([]*treeNode, 0)
		filteredNames := make([]string, 0)
		newRootPrefix := newRoot.fullPath + string(filepath.Separator)

		for i, node := range m.searchIndexNodes {
			if node.fullPath == newRoot.fullPath || strings.HasPrefix(node.fullPath+string(filepath.Separator), newRootPrefix) {
				filteredNodes = append(filteredNodes, node)
				filteredNames = append(filteredNames, m.searchIndexNames[i])
			}
		}

		m.searchIndexNodes = filteredNodes
		m.searchIndexNames = filteredNames
		m.searchIndexRoot = newRoot
		m.searchPendingMatches = nil // Clear pending matches when filtering index
		// No need to restart indexing - we have what we need
		return nil
	}

	// Check if old root is descendant of new root (navigated UP)
	if strings.HasPrefix(oldRoot.fullPath+string(filepath.Separator), newRoot.fullPath+string(filepath.Separator)) {
		// Keep existing nodes (they're still valid), but need to index new siblings
		// For simplicity, restart indexing from new root (reuse will happen naturally)
		return m.startSearchIndexLoader(newRoot)
	}

	// Completely different directory - start fresh
	return m.startSearchIndexLoader(newRoot)
}

func index(c int, r int, rows int) int {
	return r + (c * rows)
}

// listTree builds tree structure from current path
// Returns error and a command to start background indexing
func (m *model) listTree() (error, tea.Cmd) {
	files, err := os.ReadDir(m.path)
	if err != nil {
		return err, nil
	}

	entries := make([]*entry, 0, len(files))
	for _, f := range files {
		ent, err := newEntry(f)
		if err != nil {
			return err, nil
		}
		entries = append(entries, ent)
	}
	sortEntries(entries)

	// Create virtual root node (current directory contents are roots)
	m.treeRoot = &treeNode{
		entry:    nil, // virtual root
		fullPath: m.path,
		expanded: true,
		loaded:   true,
	}

	for _, ent := range entries {
		m.treeRoot.children = append(m.treeRoot.children,
			newTreeNode(ent, m.treeRoot, m.path))
	}

	m.rebuildVisibleNodes()

	// Start background indexing of the tree root
	// This will be used for fast search when search mode is entered
	return nil, m.startSearchIndexLoader(m.treeRoot)
}

// rebuildVisibleNodes flattens expanded tree into visible nodes list
func (m *model) rebuildVisibleNodes() {
	if m.search != "" {
		m.rebuildVisibleNodesWithSearch()
		return
	}

	m.visibleNodes = nil
	if m.treeRoot != nil {
		for _, child := range m.treeRoot.children {
			child.flattenInto(&m.visibleNodes, m.modeHidden)
		}
	}
	m.displayed = len(m.visibleNodes)

	// Clamp cursor
	if m.treeIdx >= len(m.visibleNodes) {
		m.treeIdx = max(0, len(m.visibleNodes)-1)
	}
}

// rebuildVisibleNodesWithSearch filters visible nodes by fuzzy search query
// Uses the cached search index if available, otherwise falls back to collecting nodes on-demand
func (m *model) rebuildVisibleNodesWithSearch() {
	m.visibleNodes = nil
	if m.treeRoot == nil || m.search == "" {
		m.displayed = len(m.visibleNodes)
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = max(0, len(m.visibleNodes)-1)
		}
		return
	}

	// Use cached index if available (preferred - faster)
	if len(m.searchIndexNodes) > 0 {
		m.rebuildVisibleNodesFromIndex()
		return
	}

	// Fallback: collect nodes on-demand (for backward compatibility or if indexing hasn't started)
	searchRoot := m.treeSearchStartNode
	if searchRoot == nil {
		searchRoot = m.treeRoot
	}

	allNodes := make([]*treeNode, 0)
	if searchRoot == nil {
		m.displayed = 0
		m.treeIdx = 0
		return
	}
	if searchRoot == m.treeRoot {
		if searchRoot.children != nil {
			for _, child := range searchRoot.children {
				if child != nil {
					descendants := child.collectAllDescendants(m.modeHidden)
					allNodes = append(allNodes, descendants...)
				}
			}
		}
	} else {
		descendants := searchRoot.collectAllDescendants(m.modeHidden)
		allNodes = append(allNodes, descendants...)
	}

	if len(allNodes) == 0 {
		m.displayed = 0
		m.treeIdx = 0
		return
	}

	nodeNames := make([]string, len(allNodes))
	for i, node := range allNodes {
		if node.entry != nil {
			nodeNames[i] = node.entry.Name()
		} else {
			nodeNames[i] = ""
		}
	}

	fuzzyMatches := fuzzy.Find(m.search, nodeNames)
	if len(fuzzyMatches) == 0 {
		m.displayed = 0
		m.treeIdx = 0
		return
	}

	matchingNodes := make([]*treeNode, 0, len(fuzzyMatches))
	for _, match := range fuzzyMatches {
		if match.Index < len(allNodes) {
			matchingNodes = append(matchingNodes, allNodes[match.Index])
		}
	}

	m.searchMatchNodes = matchingNodes
	m.visibleNodes = buildFilteredTree(searchRoot, matchingNodes, m.modeHidden)
	m.displayed = len(m.visibleNodes)

	if m.treeIdx >= len(m.visibleNodes) {
		m.treeIdx = max(0, len(m.visibleNodes)-1)
	}
}

// selectedTreeNode returns the currently selected tree node
func (m *model) selectedTreeNode() *treeNode {
	if m.treeIdx >= 0 && m.treeIdx < len(m.visibleNodes) {
		return m.visibleNodes[m.treeIdx]
	}
	return nil
}

func min(i, j int) int {
	if i < j {
		return i
	}
	return j
}

// toggleTreeMark toggles mark on current tree node
func (m *model) toggleTreeMark() {
	if m.markedTreeNode(m.treeIdx) {
		delete(m.marks, m.treeIdx)
		m.modeMarks = len(m.marks) != 0
	} else {
		m.marks[m.treeIdx] = m.treeIdx // In tree mode, displayIdx == entryIdx conceptually
		m.modeMarks = true
	}
}
