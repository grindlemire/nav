package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sahilm/fuzzy"
)

var fileSeparator = string(filepath.Separator)

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

	// Fix flags
	noClearScreenFix bool

	// Tree mode fields
	treeRoot     *treeNode
	visibleNodes []*treeNode
	treeIdx      int
	scrollOffset int
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

		treeIdx:      0,
		scrollOffset: 0,
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
}

func index(c int, r int, rows int) int {
	return r + (c * rows)
}

// listTree builds tree structure from current path
func (m *model) listTree() error {
	files, err := os.ReadDir(m.path)
	if err != nil {
		return err
	}

	entries := make([]*entry, 0, len(files))
	for _, f := range files {
		ent, err := newEntry(f)
		if err != nil {
			return err
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
	return nil
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
func (m *model) rebuildVisibleNodesWithSearch() {
	m.visibleNodes = nil
	if m.treeRoot == nil || m.search == "" {
		m.displayed = len(m.visibleNodes)
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = max(0, len(m.visibleNodes)-1)
		}
		return
	}

	// 1. Recursively load all descendants from root
	for _, child := range m.treeRoot.children {
		if child.entry != nil && child.entry.hasMode(entryModeDir) {
			_ = child.loadAllDescendants() // Ignore errors for unreadable dirs
		}
	}

	// 2. Collect all nodes into flat list
	allNodes := make([]*treeNode, 0)
	for _, child := range m.treeRoot.children {
		descendants := child.collectAllDescendants(m.modeHidden)
		allNodes = append(allNodes, descendants...)
	}

	if len(allNodes) == 0 {
		m.displayed = 0
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = 0
		}
		return
	}

	// 3. Extract node names for fuzzy matching
	nodeNames := make([]string, len(allNodes))
	for i, node := range allNodes {
		if node.entry != nil {
			nodeNames[i] = node.entry.Name()
		} else {
			nodeNames[i] = ""
		}
	}

	// 4. Run fuzzy.Find to get matches
	fuzzyMatches := fuzzy.Find(m.search, nodeNames)

	if len(fuzzyMatches) == 0 {
		m.displayed = 0
		if m.treeIdx >= len(m.visibleNodes) {
			m.treeIdx = 0
		}
		return
	}

	// 5. Get matching nodes from fuzzy results
	matchingNodes := make([]*treeNode, 0, len(fuzzyMatches))
	for _, match := range fuzzyMatches {
		if match.Index < len(allNodes) {
			matchingNodes = append(matchingNodes, allNodes[match.Index])
		}
	}

	// 6. Build filtered tree showing only branches to matches
	m.visibleNodes = buildFilteredTree(m.treeRoot, matchingNodes, m.modeHidden)

	m.displayed = len(m.visibleNodes)

	// Clamp cursor
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
