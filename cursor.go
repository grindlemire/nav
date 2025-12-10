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

	// If node has a parent within the tree, collapse parent and move up
	if node.parent != nil && node.parent != m.treeRoot {
		// Remember this child for re-expansion using path-based tracking.
		// We use path/name instead of pointer because tree nodes get recreated
		// when navigating up directories, searching, or when the filesystem changes.
		if node.entry != nil {
			m.treeLastChild[node.parent.fullPath] = node.entry.Name()
		}

		// Collapse the parent so pressing 'l' will expand and restore position
		node.parent.expanded = false

		// Move cursor to parent and rebuild
		m.rebuildVisibleNodes()
		for i, n := range m.visibleNodes {
			if n == node.parent {
				m.treeIdx = i
				m.adjustScrollOffset()
				return
			}
		}
		return
	}

	// At root level: save current child before going up to parent directory
	if node.entry != nil {
		m.treeLastChild[m.path] = node.entry.Name()
	}

	// Only at root level: go up to parent directory as new root
	parentPath, err := filepath.Abs(filepath.Join(m.path, ".."))
	if err == nil && parentPath != m.path {
		m.saveCursor()
		// Save the name of the directory we're leaving to position cursor on it after going up
		_, childDirName := filepath.Split(m.path)
		m.setPath(parentPath)
		if err := m.listTree(); err != nil {
			m.restorePath()
			m.setError(err, err.Error())
		} else {
			// Find the child directory we came from and position cursor on it
			m.treeIdx = 0
			m.scrollOffset = 0
			for i, n := range m.visibleNodes {
				if n.entry != nil && n.entry.Name() == childDirName {
					m.treeIdx = i
					m.adjustScrollOffset()
					break
				}
			}
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

		// Position cursor on last selected child if exists, otherwise first child
		found := false
		if childName, ok := m.treeLastChild[node.fullPath]; ok {
			for i, n := range m.visibleNodes {
				if n.entry != nil && n.entry.Name() == childName && n.parent == node {
					m.treeIdx = i
					found = true
					break
				}
			}
		}

		// Fallback: jump to first child
		if !found && len(node.children) > 0 {
			for i, n := range m.visibleNodes {
				if n == node {
					// First child is at i+1 (if it exists)
					if i+1 < len(m.visibleNodes) {
						m.treeIdx = i + 1
					}
					break
				}
			}
		}
		m.adjustScrollOffset()

		return nil
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
