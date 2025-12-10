package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/dkaslovsky/nav/internal/sanitize"
)

func (m *model) Init() tea.Cmd {
	return nil
}

func (m *model) View() string {
	var view string
	if m.modeExit {
		if m.modeSubshell || m.exitStr == "" {
			return ""
		}
		return m.exitStr + "\n"
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
		// Exit search mode but keep the search filter active in normal mode.
		m.modeSearch = false
		if m.modeTree {
			m.rebuildVisibleNodes()
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
				m.rebuildVisibleNodes()
			}
			return newActionResult(nil)
		}

		m.saveCursor()

		_, m.search = filepath.Split(m.path)
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

		return newActionResult(nil)

	case key.Matches(msg, keySelect):
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

	default:
		if msg.Type == tea.KeyRunes || key.Matches(msg, keySpace) {
			m.search += string(msg.Runes)
			if m.modeTree {
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
		return newActionResult(nil)
	}

	return newActionResultNoop()
}

func actionModeTree(m *model, msg tea.KeyMsg, esc bool) actionResult {
	switch {

	case key.Matches(msg, keyModeSearch):
		// Enter search mode
		m.modeSearch = true
		return newActionResult(nil)

	case key.Matches(msg, keyUp):
		m.treeMoveUp()
		return newActionResult(nil)

	case key.Matches(msg, keyDown):
		m.treeMoveDown()
		return newActionResult(nil)

	case key.Matches(msg, keyLeft):
		m.treeCollapse()
		return newActionResult(nil)

	case key.Matches(msg, keyRight):
		cmd := m.treeExpand()
		return newActionResult(cmd)

	case key.Matches(msg, keySelect):
		result := m.treeSelectAction()
		return result

	case key.Matches(msg, keyMark):
		if !m.modeSearch {
			m.toggleTreeMark()
			return newActionResult(nil)
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
	node := m.selectedTreeNode()
	if node == nil || node.entry == nil {
		return newActionResult(nil)
	}

	m.saveCursor()

	// For files: return path and quit (same as current behavior)
	if node.entry.hasMode(entryModeFile) {
		m.setExit(sanitize.SanitizeOutputPath(node.fullPath))
		if m.modeSubshell {
			fmt.Print(m.exitStr)
		}
		return newActionResult(tea.Quit)
	}

	// For directories: descend (make this dir the new root)
	if node.entry.hasMode(entryModeDir) {
		m.setPath(node.fullPath)
		if err := m.listTree(); err != nil {
			m.restorePath()
			m.setError(err, err.Error())
		} else {
			m.treeIdx = 0
			m.scrollOffset = 0
		}
		return newActionResult(nil)
	}

	// Handle symlinks
	if node.entry.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, node.entry)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			return newActionResult(nil)
		}
		if !sl.info.IsDir() {
			// The symlink points to a file.
			m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
			if m.modeSubshell {
				fmt.Print(m.exitStr)
			}
			return newActionResult(tea.Quit)
		}
		// The symlink points to a directory.
		m.setPath(sl.absPath)
		if err := m.listTree(); err != nil {
			m.restorePath()
			m.setError(err, err.Error())
		} else {
			m.treeIdx = 0
			m.scrollOffset = 0
		}
		return newActionResult(nil)
	}

	m.setError(
		errors.New("selection is not a file, directory, or symlink"),
		"unexpected file type",
	)
	return newActionResult(nil)
}

func actionModeGeneral(m *model, msg tea.KeyMsg, esc bool) actionResult {
	switch {

	// Normal mode escape
	case esc || key.Matches(msg, keyEsc):
		m.clearSearch()
		return newActionResult(nil)

	// Return

	case key.Matches(msg, keyReturnDirectory):
		m.setExit(sanitize.SanitizeOutputPath(m.path))
		if m.modeSubshell {
			fmt.Print(m.exitStr)
		}
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
				return newActionResult(nil)
			}
			selecteds = append(selecteds, selected)
		}

		for _, selected := range selecteds {
			var path string
			if selected.hasMode(entryModeSymlink) {
				sl, err := followSymlink(m.path, selected)
				if err != nil {
					m.setError(err, "failed to evaluate symlink")
					return newActionResult(nil)
				}
				path = sanitize.SanitizeOutputPath(sl.absPath)
			} else {
				path = sanitize.SanitizeOutputPath(filepath.Join(m.path, selected.Name()))
			}
			paths = append(paths, path)
		}

		m.setExit(strings.Join(paths, " "))
		if m.modeSubshell {
			fmt.Print(m.exitStr)
		}
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

	case key.Matches(msg, keyModeSearch):
		m.modeSearch = true
		m.clearMarks()

	// Toggles

	case key.Matches(msg, keyToggleFollowSymlink):
		m.modeFollowSymlink = !m.modeFollowSymlink

	case key.Matches(msg, keyToggleHidden):
		m.modeHidden = !m.modeHidden
		if m.modeTree {
			m.rebuildVisibleNodes()
		}

	case key.Matches(msg, keyToggleList):
		m.modeList = !m.modeList

	case key.Matches(msg, keyToggleTree):
		m.modeTree = !m.modeTree
		if m.modeTree {
			// Initialize tree mode
			if err := m.listTree(); err != nil {
				m.setError(err, "failed to initialize tree view")
				m.modeTree = false
			} else {
				m.treeIdx = 0
				m.scrollOffset = 0
			}
		} else {
			// Switch back to normal mode
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
