package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

const searchBatchSize = 500 // Nodes per batch

type treeNode struct {
	entry    *entry
	parent   *treeNode
	children []*treeNode
	expanded bool
	depth    int
	loaded   bool
	fullPath string
}

func newTreeNode(ent *entry, parent *treeNode, basePath string) *treeNode {
	depth := 0
	if parent != nil {
		depth = parent.depth + 1
	}
	return &treeNode{
		entry:    ent,
		parent:   parent,
		depth:    depth,
		fullPath: filepath.Join(basePath, ent.Name()),
	}
}

// loadChildren populates children lazily when node is expanded
func (n *treeNode) loadChildren() error {
	if n.loaded || !n.entry.hasMode(entryModeDir) {
		return nil
	}

	files, err := os.ReadDir(n.fullPath)
	if err != nil {
		return err
	}

	entries := make([]*entry, 0, len(files))
	for _, f := range files {
		ent, err := newEntry(f)
		if err != nil {
			continue // skip unreadable entries
		}
		entries = append(entries, ent)
	}
	sortEntries(entries)

	n.children = make([]*treeNode, 0, len(entries))
	for _, ent := range entries {
		n.children = append(n.children, newTreeNode(ent, n, n.fullPath))
	}
	n.loaded = true
	return nil
}

// isLastChild returns true if this node is the last visible child of its parent
func (n *treeNode) isLastChild(modeHidden bool) bool {
	if n.parent == nil {
		return false // Root has no parent
	}

	// Find the last visible sibling
	for i := len(n.parent.children) - 1; i >= 0; i-- {
		sibling := n.parent.children[i]
		if sibling.entry == nil {
			continue
		}
		if !modeHidden && sibling.entry.hasMode(entryModeHidden) {
			continue
		}
		return sibling == n
	}
	return false
}

// flatten returns visible nodes in DFS order (only expanded subtrees)
func (n *treeNode) flatten(modeHidden bool) []*treeNode {
	var nodes []*treeNode
	n.flattenInto(&nodes, modeHidden)
	return nodes
}

func (n *treeNode) flattenInto(nodes *[]*treeNode, modeHidden bool) {
	// Skip hidden unless mode is on
	if n.entry != nil && !modeHidden && n.entry.hasMode(entryModeHidden) {
		return
	}

	*nodes = append(*nodes, n)

	if n.expanded && n.loaded {
		for _, child := range n.children {
			child.flattenInto(nodes, modeHidden)
		}
	}
}

// loadAllDescendants recursively loads entire subtree from disk
func (n *treeNode) loadAllDescendants() error {
	if err := n.loadChildren(); err != nil {
		return err
	}
	for _, child := range n.children {
		if child.entry != nil && child.entry.hasMode(entryModeDir) {
			// Ignore errors for unreadable directories to continue searching
			_ = child.loadAllDescendants()
		}
	}
	return nil
}

// collectAllDescendants collects all descendants into a flat list regardless of expanded state
func (n *treeNode) collectAllDescendants(modeHidden bool) []*treeNode {
	if n == nil {
		return nil
	}
	var nodes []*treeNode
	n.collectAllDescendantsInto(&nodes, modeHidden)
	return nodes
}

func (n *treeNode) collectAllDescendantsInto(nodes *[]*treeNode, modeHidden bool) {
	// Skip nil nodes
	if n == nil {
		return
	}

	// Skip hidden unless mode is on
	if n.entry != nil && !modeHidden && n.entry.hasMode(entryModeHidden) {
		return
	}

	// Add this node
	*nodes = append(*nodes, n)

	// Recursively collect all children (must load them first)
	if n.entry != nil && n.entry.hasMode(entryModeDir) {
		// Load children if not already loaded
		if !n.loaded {
			_ = n.loadChildren() // Ignore errors
		}
		if n.children != nil {
			for _, child := range n.children {
				if child != nil {
					child.collectAllDescendantsInto(nodes, modeHidden)
				}
			}
		}
	}
}

// searchSubtree performs recursive substring search in expanded subtrees
func (n *treeNode) searchSubtree(query string, modeHidden bool) []*treeNode {
	var results []*treeNode
	n.searchSubtreeInto(query, modeHidden, &results)
	return results
}

func (n *treeNode) searchSubtreeInto(query string, modeHidden bool, results *[]*treeNode) {
	if n.entry != nil {
		if !modeHidden && n.entry.hasMode(entryModeHidden) {
			return
		}

		if strings.Contains(strings.ToLower(n.entry.Name()), strings.ToLower(query)) {
			*results = append(*results, n)
		}
	}

	// Search expanded children
	if n.expanded && n.loaded {
		for _, child := range n.children {
			child.searchSubtreeInto(query, modeHidden, results)
		}
	}
}

// buildFilteredTree builds a tree showing only branches that lead to matching nodes
// Returns a flattened list of nodes (matches + their ancestors) in DFS order
func buildFilteredTree(root *treeNode, matches []*treeNode, modeHidden bool) []*treeNode {
	if len(matches) == 0 {
		return nil
	}

	// Create a set of nodes to include (matches + all ancestors)
	includeSet := make(map[*treeNode]bool)
	for _, match := range matches {
		// Walk up from match to root, marking all ancestors
		node := match
		for node != nil {
			includeSet[node] = true
			node = node.parent
		}
	}

	// Flatten the tree showing only included nodes
	var result []*treeNode
	buildFilteredTreeFlatten(root, includeSet, modeHidden, &result)
	return result
}

func buildFilteredTreeFlatten(node *treeNode, includeSet map[*treeNode]bool, modeHidden bool, result *[]*treeNode) {
	// Skip if not in include set
	if !includeSet[node] {
		return
	}

	// Skip virtual root node (entry == nil) - it won't render anyway
	if node.entry != nil {
		// Skip hidden unless mode is on
		if !modeHidden && node.entry.hasMode(entryModeHidden) {
			return
		}

		// Add this node
		*result = append(*result, node)
	}

	// Recursively process children that are in the include set
	for _, child := range node.children {
		if includeSet[child] {
			buildFilteredTreeFlatten(child, includeSet, modeHidden, result)
		}
	}
}

// streamDFS performs DFS traversal and sends batches of nodes to the channel.
// It checks ctx.Done() periodically to allow cancellation.
func streamDFS(ctx context.Context, root *treeNode, modeHidden bool, ch chan<- []*treeNode) {
	if root == nil {
		return
	}

	var batch []*treeNode
	stack := []*treeNode{root}

	for len(stack) > 0 {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Pop from stack
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		// Skip nil nodes
		if node == nil {
			continue
		}

		// Skip hidden if needed
		if node.entry != nil && !modeHidden && node.entry.hasMode(entryModeHidden) {
			continue
		}

		// Load children if directory
		if node.entry != nil && node.entry.hasMode(entryModeDir) && !node.loaded {
			_ = node.loadChildren() // Ignore errors
		}

		// Add to batch (skip virtual root)
		if node.entry != nil {
			batch = append(batch, node)
		}

		// Push children onto stack (reverse order for correct DFS)
		if node.children != nil {
			for i := len(node.children) - 1; i >= 0; i-- {
				if node.children[i] != nil {
					stack = append(stack, node.children[i])
				}
			}
		}

		// Send batch when full
		if len(batch) >= searchBatchSize {
			select {
			case <-ctx.Done():
				return
			case ch <- batch:
				batch = nil // Reset batch
			}
		}
	}

	// Send final batch (even if empty, to ensure completion is signaled)
	select {
	case <-ctx.Done():
		return
	case ch <- batch:
	}
}
