package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/dkaslovsky/nav/internal/sanitize"
)

func (m *model) selectAction() (*model, tea.Cmd) {
	selected, err := m.selected()
	if err != nil {
		m.setError(err, "failed to select entry")
		return m, nil
	}

	m.saveCursor()

	if selected.hasMode(entryModeFile) {
		m.setExit(sanitize.SanitizeOutputPath(filepath.Join(m.path, selected.Name())))
		if m.modeSubshell {
			fmt.Print(m.exitStr)
		}
		return m, tea.Quit
	}
	if selected.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, selected)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			return m, nil
		}
		if !sl.info.IsDir() {
			// The symlink points to a file.
			m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
			if m.modeSubshell {
				fmt.Print(m.exitStr)
			}
			return m, tea.Quit
		}
		m.setPath(sl.absPath)
	} else if selected.hasMode(entryModeDir) {
		path, err := filepath.Abs(filepath.Join(m.path, selected.Name()))
		if err != nil {
			m.setError(err, "failed to evaluate path")
			return m, nil
		}
		m.setPath(path)
	} else {
		m.setError(
			errors.New("selection is not a file, directory, or symlink"),
			"unexpected file type",
		)
		return m, nil
	}

	err = m.list()
	if err != nil {
		m.restorePath()
		m.setError(err, err.Error())
		return m, nil
	}

	m.clearSearch()

	// Return to ensure the cursor is not re-saved using the updated path.
	return m, nil
}

func (m *model) searchSelectAction() (*model, tea.Cmd) {
	// In tree mode, use tree selection logic
	if m.modeTree {
		node := m.selectedTreeNode()
		if node == nil || node.entry == nil {
			m.setError(errors.New("no node selected"), "failed to select entry")
			m.clearSearch()
			return m, nil
		}

		m.saveCursor()

		if node.entry.hasMode(entryModeFile) {
			m.setExit(sanitize.SanitizeOutputPath(node.fullPath))
			if m.modeSubshell {
				fmt.Print(m.exitStr)
			}
			m.clearSearch()
			return m, tea.Quit
		}

		if node.entry.hasMode(entryModeSymlink) {
			sl, err := followSymlink(m.path, node.entry)
			if err != nil {
				m.setError(err, "failed to evaluate symlink")
				m.clearSearch()
				return m, nil
			}
			if !sl.info.IsDir() {
				m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
				if m.modeSubshell {
					fmt.Print(m.exitStr)
				}
				m.clearSearch()
				return m, tea.Quit
			}
			m.setPath(sl.absPath)
		} else if node.entry.hasMode(entryModeDir) {
			m.setPath(node.fullPath)
		} else {
			m.setError(
				errors.New("selection is not a file, directory, or symlink"),
				"unexpected file type",
			)
			m.clearSearch()
			return m, nil
		}

		m.search = ""
		err := m.listTree()
		if err != nil {
			m.restorePath()
			m.setError(err, err.Error())
			m.clearSearch()
		} else {
			m.treeIdx = 0
			m.scrollOffset = 0
		}
		return m, nil
	}

	selected, err := m.selected()
	if err != nil {
		m.setError(err, "failed to select entry")
		m.clearSearch()
		return m, nil
	}

	if selected.hasMode(entryModeFile) {
		m.setExit(sanitize.SanitizeOutputPath(filepath.Join(m.path, selected.Name())))
		if m.modeSubshell {
			fmt.Print(m.exitStr)
		}
		return m, tea.Quit
	}
	if selected.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, selected)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			return m, nil
		}
		if !sl.info.IsDir() {
			// The symlink points to a file.
			m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
			if m.modeSubshell {
				fmt.Print(m.exitStr)
			}
			return m, tea.Quit
		}
		m.setPath(sl.absPath)
	} else if selected.hasMode(entryModeDir) {
		m.setPath(m.path + fileSeparator + selected.Name())
	} else {
		m.setError(
			errors.New("selection is not a file, directory, or symlink"),
			"unexpected file type",
		)
		return m, nil
	}

	// Trim repeated leading file separator characters that occur from searching back
	// to the root directory.
	if strings.HasPrefix(m.path, "//") {
		m.path = m.path[1:]
	}

	m.search = ""
	err = m.list()
	if err != nil {
		m.restorePath()
		m.setError(err, err.Error())
		m.clearSearch()
		return m, nil
	}
	return m, nil
}
