package router

import (
	"net/url"
	"strings"
)

// Tree is a radix tree for route matching
type Tree struct {
	root        *Node
	namedRoutes map[string]*MatchResult // lookup by route name
}

// Node represents a node in the radix tree
type Node struct {
	// segment info
	segment Segment

	// children organized by type for priority matching
	staticChildren map[string]*Node // static path segments
	regexChildren  []*Node          // regex constrained params
	paramChild     *Node            // plain param {id}
	wildcardChild  *Node            // wildcard {path:.*}

	// handlers per HTTP method
	handlers map[string]*MatchResult

	// is this a route endpoint
	isLeaf bool
}

// MatchResult contains the matched route info
type MatchResult struct {
	Handler       HandlerFunc
	Params        map[string]string
	Name          string
	Path          string
	segments      []Segment // internal: for param name extraction
	matchedValues []string  // internal: raw matched values for pool-based context init
}

// NewTree creates a new routing tree
func NewTree() *Tree {
	return &Tree{
		root: &Node{
			staticChildren: make(map[string]*Node),
		},
		namedRoutes: make(map[string]*MatchResult),
	}
}

// Insert adds a route to the tree
func (t *Tree) Insert(method, path string, handler HandlerFunc) error {
	return t.InsertWithName(method, path, handler, "")
}

// InsertWithName adds a named route to the tree
func (t *Tree) InsertWithName(method, path string, handler HandlerFunc, name string) error {
	segments, err := ParseSegments(path)
	if err != nil {
		return err
	}

	node := t.root
	for _, seg := range segments {
		node = node.getOrCreateChild(seg)
	}

	// Store handler at leaf
	if node.handlers == nil {
		node.handlers = make(map[string]*MatchResult)
	}

	result := &MatchResult{
		Handler:  handler,
		Name:     name,
		Path:     path,
		segments: segments, // Store segments for param name extraction
	}
	node.handlers[method] = result
	node.isLeaf = true

	// Store in named routes lookup if name provided
	if name != "" {
		t.namedRoutes[name] = result
	}

	return nil
}

// getOrCreateChild gets or creates a child node for the segment
func (n *Node) getOrCreateChild(seg Segment) *Node {
	switch seg.Type {
	case SegmentStatic:
		if n.staticChildren == nil {
			n.staticChildren = make(map[string]*Node)
		}
		if child, ok := n.staticChildren[seg.Value]; ok {
			return child
		}
		child := &Node{segment: seg}
		n.staticChildren[seg.Value] = child
		return child

	case SegmentRegex:
		// Check for existing regex with same pattern
		for _, child := range n.regexChildren {
			if child.segment.RawPattern == seg.RawPattern {
				return child
			}
		}
		child := &Node{segment: seg}
		n.regexChildren = append(n.regexChildren, child)
		return child

	case SegmentParam:
		if n.paramChild == nil {
			n.paramChild = &Node{segment: seg}
		}
		return n.paramChild

	case SegmentWildcard:
		if n.wildcardChild == nil {
			n.wildcardChild = &Node{segment: seg}
		}
		return n.wildcardChild
	}

	return nil
}

// Match finds a route for the given method and path
func (t *Tree) Match(method, path string) *MatchResult {
	// Normalize path
	path = strings.Trim(path, "/")

	// Handle root path
	if path == "" {
		if t.root.handlers != nil {
			if result, ok := t.root.handlers[method]; ok {
				return &MatchResult{
					Handler:  result.Handler,
					Params:   make(map[string]string),
					Name:     result.Name,
					Path:     result.Path,
					segments: result.segments,
				}
			}
		}
		return nil
	}

	parts := strings.Split(path, "/")
	// Remove empty parts (from trailing slash)
	cleanParts := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			cleanParts = append(cleanParts, p)
		}
	}

	// Store matched values in order (will be mapped to param names at the end)
	matchedValues := make([]string, 0)

	return t.root.match(cleanParts, method, matchedValues)
}

// match recursively matches path parts against the tree
// matchedValues collects parameter values in the order they appear
func (n *Node) match(parts []string, method string, matchedValues []string) *MatchResult {
	if len(parts) == 0 {
		// At potential leaf
		if n.handlers != nil {
			if result, ok := n.handlers[method]; ok {
				// Build params map using stored segments for param names
				params := buildParams(result.segments, matchedValues)
				return &MatchResult{
					Handler:       result.Handler,
					Params:        params,
					Name:          result.Name,
					Path:          result.Path,
					segments:      result.segments,
					matchedValues: matchedValues,
				}
			}
		}

		// Check if wildcard child can match empty
		if n.wildcardChild != nil && n.wildcardChild.handlers != nil {
			if result, ok := n.wildcardChild.handlers[method]; ok {
				newValues := append(copyValues(matchedValues), "") // empty wildcard
				params := buildParams(result.segments, newValues)
				return &MatchResult{
					Handler:       result.Handler,
					Params:        params,
					Name:          result.Name,
					Path:          result.Path,
					segments:      result.segments,
					matchedValues: newValues,
				}
			}
		}

		return nil
	}

	part := parts[0]
	remaining := parts[1:]

	// Priority 1: Static children (most specific)
	if n.staticChildren != nil {
		if child, ok := n.staticChildren[part]; ok {
			if result := child.match(remaining, method, matchedValues); result != nil {
				return result
			}
		}
	}

	// Priority 2: Regex constrained params (more specific than plain param)
	for _, child := range n.regexChildren {
		if child.segment.Match(part) {
			newValues := append(copyValues(matchedValues), part)
			if result := child.match(remaining, method, newValues); result != nil {
				return result
			}
		}
	}

	// Priority 3: Plain param
	if n.paramChild != nil {
		newValues := append(copyValues(matchedValues), part)
		if result := n.paramChild.match(remaining, method, newValues); result != nil {
			return result
		}
	}

	// Priority 4: Wildcard (consumes rest of path)
	if n.wildcardChild != nil {
		// Wildcard captures everything including the current part.
		// URL-decode the value since URL path segments may be percent-encoded.
		wildcardValue := strings.Join(parts, "/")
		if decoded, err := url.PathUnescape(wildcardValue); err == nil {
			wildcardValue = decoded
		}
		newValues := append(copyValues(matchedValues), wildcardValue)

		if n.wildcardChild.handlers != nil {
			if result, ok := n.wildcardChild.handlers[method]; ok {
				params := buildParams(result.segments, newValues)
				return &MatchResult{
					Handler:       result.Handler,
					Params:        params,
					Name:          result.Name,
					Path:          result.Path,
					segments:      result.segments,
					matchedValues: newValues,
				}
			}
		}
	}

	return nil
}

// buildParams creates a params map from segments and matched values
func buildParams(segments []Segment, values []string) map[string]string {
	params := make(map[string]string)
	valueIdx := 0

	for _, seg := range segments {
		if seg.Type == SegmentParam || seg.Type == SegmentRegex || seg.Type == SegmentWildcard {
			if valueIdx < len(values) {
				params[seg.Value] = values[valueIdx]
				valueIdx++
			}
		}
	}

	return params
}

// copyValues creates a copy of the values slice
func copyValues(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

// AllowedMethods returns all HTTP methods registered for a path
func (t *Tree) AllowedMethods(path string) []string {
	path = strings.Trim(path, "/")

	var node *Node

	if path == "" {
		node = t.root
	} else {
		parts := strings.Split(path, "/")
		node = t.root.findNode(parts)
	}

	if node == nil || node.handlers == nil {
		return nil
	}

	methods := make([]string, 0, len(node.handlers))
	for method := range node.handlers {
		methods = append(methods, method)
	}

	return methods
}

// findNode finds the node matching the path (for AllowedMethods)
func (n *Node) findNode(parts []string) *Node {
	if len(parts) == 0 {
		return n
	}

	part := parts[0]
	remaining := parts[1:]

	// Try static
	if n.staticChildren != nil {
		if child, ok := n.staticChildren[part]; ok {
			return child.findNode(remaining)
		}
	}

	// Try regex
	for _, child := range n.regexChildren {
		if child.segment.Match(part) {
			return child.findNode(remaining)
		}
	}

	// Try param
	if n.paramChild != nil {
		return n.paramChild.findNode(remaining)
	}

	// Try wildcard
	if n.wildcardChild != nil {
		return n.wildcardChild
	}

	return nil
}
