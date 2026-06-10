package router

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/velocitykode/velocity/contract"
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
		if _, exists := t.namedRoutes[name]; exists {
			panic(contract.NewRegistrationError("router", fmt.Sprintf("route name %q already registered", name)))
		}
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
	// Remove empty parts (from trailing slash) — reuse the same slice
	n := 0
	for _, p := range parts {
		if p != "" {
			parts[n] = p
			n++
		}
	}
	parts = parts[:n]

	// Pre-allocate matched values buffer with typical capacity
	matchedValues := make([]string, 0, 4)

	return t.root.match(parts, method, matchedValues)
}

// match recursively matches path parts against the tree.
// matchedValues collects parameter values in the order they appear.
// depth tracks the current param depth to allow slice reuse on backtrack.
func (n *Node) match(parts []string, method string, matchedValues []string) *MatchResult {
	if len(parts) == 0 {
		return n.matchLeaf(method, matchedValues)
	}

	part := parts[0]
	remaining := parts[1:]
	depth := len(matchedValues)

	if result := n.matchStatic(part, remaining, method, matchedValues); result != nil {
		return result
	}
	if result := n.matchRegex(part, remaining, method, matchedValues, depth); result != nil {
		return result
	}
	if result := n.matchParam(part, remaining, method, matchedValues, depth); result != nil {
		return result
	}
	return n.matchWildcard(parts, method, matchedValues, depth)
}

// matchLeaf handles the terminal case where all path parts have been
// consumed. Tries the direct handler, then an empty-wildcard handler.
func (n *Node) matchLeaf(method string, matchedValues []string) *MatchResult {
	if n.handlers != nil {
		if result, ok := n.handlers[method]; ok {
			return buildMatchResult(result, matchedValues)
		}
	}
	if n.wildcardChild != nil && n.wildcardChild.handlers != nil {
		if result, ok := n.wildcardChild.handlers[method]; ok {
			return buildMatchResult(result, append(matchedValues, ""))
		}
	}
	return nil
}

// matchStatic tries the static-children map for an exact-string match.
func (n *Node) matchStatic(part string, remaining []string, method string, matchedValues []string) *MatchResult {
	if n.staticChildren == nil {
		return nil
	}
	child, ok := n.staticChildren[part]
	if !ok {
		return nil
	}
	return child.match(remaining, method, matchedValues)
}

// matchRegex walks the regex-constrained children. The constraint is
// evaluated against the encoded wire-form segment; the captured value
// is PathUnescaped so handlers see the decoded form.
func (n *Node) matchRegex(part string, remaining []string, method string, matchedValues []string, depth int) *MatchResult {
	for _, child := range n.regexChildren {
		if !child.segment.Match(part) {
			continue
		}
		newValues := append(matchedValues[:depth:depth], unescapeCapture(part))
		if result := child.match(remaining, method, newValues); result != nil {
			return result
		}
	}
	return nil
}

// matchParam tries the plain-param child, capturing the current part
// PathUnescaped so handlers see the decoded form.
func (n *Node) matchParam(part string, remaining []string, method string, matchedValues []string, depth int) *MatchResult {
	if n.paramChild == nil {
		return nil
	}
	newValues := append(matchedValues[:depth:depth], unescapeCapture(part))
	return n.paramChild.match(remaining, method, newValues)
}

// matchWildcard captures the rest of the path. URL-decodes the captured
// value so handlers see raw path segments instead of percent-encoded ones.
func (n *Node) matchWildcard(parts []string, method string, matchedValues []string, depth int) *MatchResult {
	if n.wildcardChild == nil || n.wildcardChild.handlers == nil {
		return nil
	}
	result, ok := n.wildcardChild.handlers[method]
	if !ok {
		return nil
	}
	wildcardValue := unescapeCapture(strings.Join(parts, "/"))
	newValues := append(matchedValues[:depth:depth], wildcardValue)
	return buildMatchResult(result, newValues)
}

// unescapeCapture decodes a matched segment for delivery to handlers,
// falling back to the encoded form when it is not valid percent-encoding.
func unescapeCapture(part string) string {
	if decoded, err := url.PathUnescape(part); err == nil {
		return decoded
	}
	return part
}

// buildMatchResult constructs a MatchResult with params map built from
// the registered segments and a defensive snapshot of matchedValues.
func buildMatchResult(registered *MatchResult, matchedValues []string) *MatchResult {
	params := buildParams(registered.segments, matchedValues)
	snapshot := make([]string, len(matchedValues))
	copy(snapshot, matchedValues)
	return &MatchResult{
		Handler:       registered.Handler,
		Params:        params,
		Name:          registered.Name,
		Path:          registered.Path,
		segments:      registered.segments,
		matchedValues: snapshot,
	}
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

// CompileStaticRoutes builds a flat map of all fully static routes for O(1) lookup.
// Only routes with no param, regex, or wildcard segments are included.
func (t *Tree) CompileStaticRoutes() map[string]*MatchResult {
	compiled := make(map[string]*MatchResult)
	// Root-level handlers (path "/")
	if t.root.handlers != nil {
		for method, result := range t.root.handlers {
			compiled[method+" /"] = result
		}
	}
	t.root.collectStaticRoutes("/", compiled)
	return compiled
}

// collectStaticRoutes recursively collects static-only routes into the map.
func (n *Node) collectStaticRoutes(prefix string, compiled map[string]*MatchResult) {
	for value, child := range n.staticChildren {
		path := prefix + value
		if child.isLeaf && child.handlers != nil {
			for method, result := range child.handlers {
				compiled[method+" "+path] = result
			}
		}
		// Recurse into static children only
		child.collectStaticRoutes(path+"/", compiled)
	}
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
