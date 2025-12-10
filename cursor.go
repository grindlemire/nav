package main

import (
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
)

// position intentionally does not have a constructor to avoid potential inversion of column and row.
// It should always be instantiated explicitly: pos := &position{c: 0, r: 0}
type position struct {
	c int
	r int
}

func newPositionFromIndex(idx int, rows int) *position {
	return &position{
		c: int(float64(idx) / float64(rows)),
		r: idx % rows,
	}
}

func (p *position) index(rows int) int {
	return index(p.c, p.r, rows)
}

func (m *model) resetCursor() {
	m.c = 0
	m.r = 0
}

func (m *model) setCursor(pos *position) {
	m.c = pos.c
	m.r = pos.r
}

func (m *model) saveCursor() {
	pos := &position{c: m.c, r: m.r}
	if cache, ok := m.pathCache[m.path]; ok {
		cache.setPosition(pos)
		return
	}
	m.pathCache[m.path] = newCacheItemWithPosition(pos)
}

func (m *model) moveUp() {
	m.r--
	if m.r < 0 {
		m.r = m.rows - 1
		m.c--
	}
	if m.c < 0 {
		m.r = m.rows - 1 - (m.columns*m.rows - m.displayed)
		m.c = m.columns - 1
	}
}

func (m *model) moveDown() {
	m.r++
	if m.r >= m.rows {
		m.r = 0
		m.c++
	}
	if m.c >= m.columns {
		m.c = 0
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= m.displayed {
		m.r = 0
		m.c = 0
	}
}

func (m *model) moveLeft() {
	m.c--
	if m.c < 0 {
		m.c = m.columns - 1
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= m.displayed {
		m.r = m.rows - 1 - (m.columns*m.rows - m.displayed)
		m.c = m.columns - 1
	}
}

func (m *model) moveRight() {
	m.c++
	if m.c >= m.columns {
		m.c = 0
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= m.displayed {
		m.r = m.rows - 1 - (m.columns*m.rows - m.displayed)
		m.c = m.columns - 1
	}
}

// Tree-mode cursor movements

func (m *model) treeMoveUp() {
	m.treeIdx--
	if m.treeIdx < 0 {
		m.treeIdx = len(m.visibleNodes) - 1 // wrap
	}
	m.adjustScrollOffset()
}

func (m *model) treeMoveDown() {
	m.treeIdx++
	if m.treeIdx >= len(m.visibleNodes) {
		m.treeIdx = 0 // wrap
	}
	m.adjustScrollOffset()
}

// treeCollapse collapses expanded dir OR navigates to parent node OR goes up a level
func (m *model) treeCollapse() {
	node := m.selectedTreeNode()
	if node == nil {
		return
	}

	// If on expanded directory, collapse it
	if node.expanded && node.entry != nil && node.entry.hasMode(entryModeDir) {
		node.expanded = false
		m.rebuildVisibleNodes()
		return
	}

	// If node has a parent within the tree, move cursor to parent
	if node.parent != nil && node.parent != m.treeRoot {
		for i, n := range m.visibleNodes {
			if n == node.parent {
				m.treeIdx = i
				m.adjustScrollOffset()
				return
			}
		}
	}

	// Only at root level: go up to parent directory as new root
	parentPath, err := filepath.Abs(filepath.Join(m.path, ".."))
	if err == nil && parentPath != m.path {
		m.saveCursor()
		m.setPath(parentPath)
		if err := m.listTree(); err != nil {
			m.restorePath()
			m.setError(err, err.Error())
		} else {
			m.treeIdx = 0
			m.scrollOffset = 0
		}
	}
}

// treeExpand expands directory and loads children
// Returns tea.ClearScreen to force full re-render (works around Bubble Tea diff bug)
func (m *model) treeExpand() tea.Cmd {
	node := m.selectedTreeNode()
	if node == nil || node.entry == nil || !node.entry.hasMode(entryModeDir) {
		return nil
	}

	if !node.expanded {
		if err := node.loadChildren(); err != nil {
			m.setError(err, "failed to read directory")
			return nil
		}

		node.expanded = true
		m.rebuildVisibleNodes()
		m.adjustScrollOffset()

		// Return ClearScreen to force full re-render (workaround for Bubble Tea diff bug)
		// Can be disabled with --no-clear-fix flag
		if m.noClearScreenFix {
			return nil
		}
		return tea.ClearScreen
	}

	return nil
}

// adjustScrollOffset keeps cursor in viewport
func (m *model) adjustScrollOffset() {
	viewHeight := m.height - 2 // account for bars
	if viewHeight <= 0 {
		return
	}
	if m.treeIdx < m.scrollOffset {
		m.scrollOffset = m.treeIdx
	} else if m.treeIdx >= m.scrollOffset+viewHeight {
		newOffset := m.treeIdx - viewHeight + 1
		m.scrollOffset = newOffset
	}
}
