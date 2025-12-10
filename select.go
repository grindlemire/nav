package main

import (
	"errors"
	"path/filepath"

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
		return m, tea.Quit
	}
	if selected.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, selected)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			return m, nil
		}
		// Return path for both files and directories
		m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
		return m, tea.Quit
	}
	if selected.hasMode(entryModeDir) {
		path, err := filepath.Abs(filepath.Join(m.path, selected.Name()))
		if err != nil {
			m.setError(err, "failed to evaluate path")
			return m, nil
		}
		m.setExit(sanitize.SanitizeOutputPath(path))
		return m, tea.Quit
	}

	m.setError(
		errors.New("selection is not a file, directory, or symlink"),
		"unexpected file type",
	)
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
			m.clearSearch()
			return m, tea.Sequence(tea.ClearScreen, tea.Quit)
		}

		if node.entry.hasMode(entryModeSymlink) {
			sl, err := followSymlink(m.path, node.entry)
			if err != nil {
				m.setError(err, "failed to evaluate symlink")
				m.clearSearch()
				return m, nil
			}
			// Return path for both files and directories
			m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
			m.clearSearch()
			return m, tea.Sequence(tea.ClearScreen, tea.Quit)
		}
		if node.entry.hasMode(entryModeDir) {
			m.setExit(sanitize.SanitizeOutputPath(node.fullPath))
			m.clearSearch()
			return m, tea.Sequence(tea.ClearScreen, tea.Quit)
		}

		m.setError(
			errors.New("selection is not a file, directory, or symlink"),
			"unexpected file type",
		)
		m.clearSearch()
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
		return m, tea.Sequence(tea.ClearScreen, tea.Quit)
	}
	if selected.hasMode(entryModeSymlink) {
		sl, err := followSymlink(m.path, selected)
		if err != nil {
			m.setError(err, "failed to evaluate symlink")
			m.clearSearch()
			return m, nil
		}
		// Return path for both files and directories
		m.setExit(sanitize.SanitizeOutputPath(sl.absPath))
		m.clearSearch()
		return m, tea.Sequence(tea.ClearScreen, tea.Quit)
	}
	if selected.hasMode(entryModeDir) {
		path, err := filepath.Abs(filepath.Join(m.path, selected.Name()))
		if err != nil {
			m.setError(err, "failed to evaluate path")
			m.clearSearch()
			return m, nil
		}
		m.setExit(sanitize.SanitizeOutputPath(path))
		m.clearSearch()
		return m, tea.Sequence(tea.ClearScreen, tea.Quit)
	}

	m.setError(
		errors.New("selection is not a file, directory, or symlink"),
		"unexpected file type",
	)
	m.clearSearch()
	return m, nil
}
