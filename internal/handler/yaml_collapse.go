package handler

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// Default values for collapse options.
const (
	defaultMaxDepth      = 2
	defaultMaxArrayItems = 3
	defaultExampleLimit  = 1
	maxExampleLimit      = 3
	maxExampleLines      = 80
	maxExampleBytes      = 4096
)

// CollapseOptions controls how YAML is collapsed for progressive disclosure.
//
// Depth semantics for structure { a: { b: { c: value } } }:
//
//	MaxDepth=0: Full YAML (unlimited)
//	MaxDepth=1: a: object (1 key)
//	MaxDepth=2: a:\n  b: object (1 key)
//	MaxDepth=3: Full expansion to c: value
type CollapseOptions struct {
	// MaxDepth controls how many nesting levels to expand before summarizing.
	// 0 means unlimited (return full YAML).
	MaxDepth int

	// MaxArrayItems limits how many array items to show before truncating.
	// 0 means unlimited. Default is 3.
	MaxArrayItems int

	// ShowDefaults includes actual values. When false, shows types only.
	ShowDefaults bool

	// ShowComments preserves YAML comments in output.
	ShowComments bool
}

// DefaultCollapseOptions returns the default options for collapsing YAML.
func DefaultCollapseOptions() CollapseOptions {
	return CollapseOptions{
		MaxDepth:      defaultMaxDepth,
		MaxArrayItems: defaultMaxArrayItems,
		ShowDefaults:  true,
		ShowComments:  false,
	}
}

// orderedEntry is a key-value pair that preserves insertion order.
type orderedEntry struct {
	key   string
	value interface{}
}

// orderedMap preserves the insertion order of keys, matching the source YAML.
type orderedMap struct {
	entries []orderedEntry
}

// CollapseYAML transforms YAML content with depth limiting for progressive disclosure.
// When depth is limited, nested structures are summarized (e.g., "object (5 keys)").
// Returns the original YAML unchanged if MaxDepth is 0 (unlimited).
//
// The second return value indicates whether any collapsing occurred.
// Returns an error only if the input is not valid YAML.
func CollapseYAML(data []byte, opts CollapseOptions) (string, bool, error) {
	// Unlimited depth - return as-is (possibly with comment stripping)
	if opts.MaxDepth == 0 {
		if !opts.ShowComments {
			return stripComments(data)
		}
		return string(data), false, nil
	}

	// Parse YAML AST to preserve key order and extract comments
	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return "", false, fmt.Errorf("parsing YAML: %w", err)
	}

	// Extract comments keyed by full dotted path
	var comments map[string]string
	if opts.ShowComments {
		comments = extractComments(file, data)
	}

	// Build ordered tree from AST (use first document only; Helm values.yaml is single-document)
	var root interface{}
	if len(file.Docs) > 0 {
		root = astToOrdered(file.Docs[0].Body)
	}
	if root == nil {
		return "", true, nil
	}

	// Build collapsed output
	var sb strings.Builder
	sb.Grow(len(data) / 2)

	renderNode(&sb, root, "", "", 0, opts, comments)

	return strings.TrimSuffix(sb.String(), "\n"), true, nil
}

// CollapseYAMLAtPath transforms either the full YAML document or a selected
// yq-style path such as ".foo.bar[0]". Path extraction uses the parsed YAML AST
// so comments can be preserved when ShowComments is true.
func CollapseYAMLAtPath(data []byte, path string, opts CollapseOptions) (string, bool, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return CollapseYAML(data, opts)
	}

	segments, err := parseYAMLPathSegments(path)
	if err != nil {
		return "", false, err
	}

	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return "", false, fmt.Errorf("parsing YAML: %w", err)
	}
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return "", false, fmt.Errorf("path not found: %q", path)
	}

	node, selectedPath, err := findYAMLPathNode(file.Docs[0].Body, segments, path)
	if err != nil {
		return "", false, err
	}

	var comments map[string]string
	if opts.ShowComments {
		comments = extractComments(file, data)
	}

	// MaxDepth=0 means "unlimited" to callers. The renderer uses MaxDepth as a
	// concrete cutoff, so use a very high cutoff while keeping array behavior.
	renderOpts := opts
	if renderOpts.MaxDepth == 0 {
		renderOpts.MaxDepth = 1 << 30
	}

	root := astToOrdered(node)
	var sb strings.Builder
	sb.Grow(len(data) / 8)

	if opts.ShowComments {
		writePathComment(&sb, comments[selectedPath], "")
	}
	renderFragment(&sb, root, selectedPath, "", 0, renderOpts, comments)

	return strings.TrimSuffix(sb.String(), "\n"), opts.MaxDepth != 0, nil
}

type yamlPathSegmentKind int

const (
	yamlPathSegmentKey yamlPathSegmentKind = iota
	yamlPathSegmentIndex
)

type yamlPathSegment struct {
	kind  yamlPathSegmentKind
	key   string
	index int
}

func parseYAMLPathSegments(path string) ([]yamlPathSegment, error) {
	if path == "" {
		return nil, nil
	}

	var segments []yamlPathSegment
	for len(path) > 0 {
		if path[0] == '.' {
			path = path[1:]
			if path == "" {
				return nil, fmt.Errorf("invalid path: trailing dot")
			}
		}

		if path[0] == '[' {
			index, rest, err := parseYAMLPathIndex(path)
			if err != nil {
				return nil, err
			}
			segments = append(segments, yamlPathSegment{kind: yamlPathSegmentIndex, index: index})
			path = rest
			continue
		}

		end := 0
		for end < len(path) && path[end] != '.' && path[end] != '[' {
			end++
		}
		if end == 0 {
			return nil, fmt.Errorf("invalid path near %q", path)
		}

		segments = append(segments, yamlPathSegment{kind: yamlPathSegmentKey, key: path[:end]})
		path = path[end:]

		for strings.HasPrefix(path, "[") {
			index, rest, err := parseYAMLPathIndex(path)
			if err != nil {
				return nil, err
			}
			segments = append(segments, yamlPathSegment{kind: yamlPathSegmentIndex, index: index})
			path = rest
		}
	}

	return segments, nil
}

func parseYAMLPathIndex(path string) (int, string, error) {
	close := strings.IndexByte(path, ']')
	if close < 0 {
		return 0, "", fmt.Errorf("invalid path: missing closing bracket")
	}
	raw := path[1:close]
	if raw == "" {
		return 0, "", fmt.Errorf("invalid path: empty array index")
	}
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 {
		return 0, "", fmt.Errorf("invalid path: array index %q must be a non-negative integer", raw)
	}
	return index, path[close+1:], nil
}

func findYAMLPathNode(root ast.Node, segments []yamlPathSegment, originalPath string) (ast.Node, string, error) {
	selection, err := findYAMLPathSelection(root, segments, originalPath)
	if err != nil {
		return nil, "", err
	}
	return selection.node, selection.selectedPath, nil
}

type yamlPathSelection struct {
	node         ast.Node
	selectedPath string
	keyLine      int
	keyIndent    int
}

func findYAMLPathSelection(root ast.Node, segments []yamlPathSegment, originalPath string) (yamlPathSelection, error) {
	node := unwrapYAMLPathNode(root)
	selectedPath := ""
	keyLine := 0
	keyIndent := 0

	for _, segment := range segments {
		switch segment.kind {
		case yamlPathSegmentKey:
			mapping, ok := unwrapYAMLPathNode(node).(*ast.MappingNode)
			if !ok {
				return yamlPathSelection{}, fmt.Errorf("path not found: %q", originalPath)
			}

			var found ast.Node
			var keyNode ast.Node
			for _, value := range mapping.Values {
				if value == nil || value.Key == nil {
					continue
				}
				if extractKey(value.Key) == segment.key {
					found = value.Value
					keyNode = value.Key
					break
				}
			}
			if found == nil {
				return yamlPathSelection{}, fmt.Errorf("path not found: %q", originalPath)
			}

			node = unwrapYAMLPathNode(found)
			if token := keyNode.GetToken(); token != nil && token.Position != nil {
				keyLine = token.Position.Line
				keyIndent = token.Position.Column - 1
				if keyIndent < 0 {
					keyIndent = 0
				}
			}
			if selectedPath == "" {
				selectedPath = segment.key
			} else {
				selectedPath += "." + segment.key
			}

		case yamlPathSegmentIndex:
			sequence, ok := unwrapYAMLPathNode(node).(*ast.SequenceNode)
			if !ok || segment.index >= len(sequence.Values) {
				return yamlPathSelection{}, fmt.Errorf("path not found: %q", originalPath)
			}
			node = unwrapYAMLPathNode(sequence.Values[segment.index])
			selectedPath += fmt.Sprintf("[%d]", segment.index)
		}
	}

	return yamlPathSelection{
		node:         node,
		selectedPath: selectedPath,
		keyLine:      keyLine,
		keyIndent:    keyIndent,
	}, nil
}

func unwrapYAMLPathNode(node ast.Node) ast.Node {
	for {
		switch n := node.(type) {
		case *ast.TagNode:
			node = n.Value
		case *ast.AnchorNode:
			node = n.Value
		default:
			return node
		}
	}
}

func renderFragment(sb *strings.Builder, node interface{}, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	switch v := node.(type) {
	case *orderedMap:
		if len(v.entries) == 0 {
			sb.WriteString("{}\n")
			return
		}
		renderMap(sb, v, path, indent, depth, opts, comments)
	case []interface{}:
		if len(v) == 0 {
			sb.WriteString("[]\n")
			return
		}
		renderArray(sb, v, path, indent, depth, opts, comments)
	default:
		renderScalar(sb, v, opts.ShowDefaults)
		sb.WriteString("\n")
	}
}

func writePathComment(sb *strings.Builder, comment string, indent string) {
	if comment == "" {
		return
	}
	sb.WriteString(indent)
	sb.WriteString("# ")
	sb.WriteString(comment)
	sb.WriteString("\n")
}

type nearbyExample struct {
	YAML       string
	Source     string
	Confidence string
}

func extractNearbyExamples(data []byte, path string, limit int) ([]nearbyExample, error) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, ".")
	if path == "" || limit == 0 {
		return nil, nil
	}
	if limit < 0 {
		limit = defaultExampleLimit
	}
	if limit > maxExampleLimit {
		limit = maxExampleLimit
	}

	segments, err := parseYAMLPathSegments(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 || segments[len(segments)-1].kind != yamlPathSegmentKey {
		return nil, nil
	}

	file, err := parser.ParseBytes(data, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parsing YAML: %w", err)
	}
	if len(file.Docs) == 0 || file.Docs[0].Body == nil {
		return nil, fmt.Errorf("path not found: %q", path)
	}

	selection, err := findYAMLPathSelection(file.Docs[0].Body, segments, path)
	if err != nil {
		return nil, err
	}
	if selection.keyLine <= 0 {
		return nil, nil
	}

	lines := strings.Split(string(data), "\n")
	start := selection.keyLine - 1
	if start < 0 || start >= len(lines) {
		return nil, nil
	}

	end := findSelectionEndLine(lines, start, selection.keyIndent)
	blocks := followingCommentBlocks(lines, start+1, end)

	examples := make([]nearbyExample, 0, limit)
	seen := make(map[string]struct{})
	for _, block := range blocks {
		for _, candidate := range exampleCandidatesFromCommentBlock(block) {
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			examples = append(examples, nearbyExample{
				YAML:       candidate,
				Source:     "following_comment_block",
				Confidence: "high",
			})
			if len(examples) >= limit {
				return examples, nil
			}
		}
	}

	return examples, nil
}

func findSelectionEndLine(lines []string, keyLineIndex int, keyIndent int) int {
	for i := keyLineIndex + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if leadingSpaces(lines[i]) <= keyIndent {
			return i
		}
	}
	return len(lines)
}

func followingCommentBlocks(lines []string, start int, end int) [][]string {
	var blocks [][]string
	var current []string
	for i := start; i < end && i < len(lines); i++ {
		line := lines[i]
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			current = append(current, line)
			continue
		}
		if len(current) > 0 {
			blocks = append(blocks, current)
			current = nil
		}
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

func exampleCandidatesFromCommentBlock(lines []string) []string {
	cleaned := make([]string, 0, len(lines))
	for _, line := range lines {
		cleaned = append(cleaned, stripCommentPrefix(line))
	}

	var candidates []string
	for _, part := range splitNonEmptyLineBlocks(cleaned) {
		for start := range part {
			found := false
			for end := len(part); end > start; end-- {
				candidate := normalizeIndent(strings.Join(part[start:end], "\n"))
				if example, ok := normalizeYAMLExample(candidate); ok {
					candidates = append(candidates, example)
					found = true
					break
				}
			}
			if found {
				break
			}
		}
	}
	return candidates
}

func splitNonEmptyLineBlocks(lines []string) [][]string {
	var blocks [][]string
	var current []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	return blocks
}

func stripCommentPrefix(line string) string {
	trimmedLeft := strings.TrimLeft(line, " \t")
	if !strings.HasPrefix(trimmedLeft, "#") {
		return strings.TrimRight(line, " \t")
	}
	withoutHash := strings.TrimLeft(trimmedLeft, "#")
	withoutHash = strings.TrimPrefix(withoutHash, " ")
	return strings.TrimRight(withoutHash, " \t")
}

func normalizeIndent(text string) string {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	minIndent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := leadingSpaces(line)
		if minIndent == -1 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func normalizeYAMLExample(candidate string) (string, bool) {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || len(candidate) > maxExampleBytes {
		return "", false
	}
	lines := strings.Split(candidate, "\n")
	if len(lines) > maxExampleLines {
		lines = lines[:maxExampleLines]
		candidate = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if !looksLikeYAMLExample(candidate) {
		return "", false
	}
	if containsBareProseLine(candidate) {
		return "", false
	}
	if !hasValidBlockIndentation(candidate) {
		return "", false
	}
	if containsProseMappingKey(candidate) {
		return "", false
	}

	var parsed interface{}
	if err := yaml.Unmarshal([]byte(candidate), &parsed); err != nil {
		return "", false
	}
	if !isExampleRoot(parsed) {
		return "", false
	}
	return candidate, true
}

func containsProseMappingKey(candidate string) bool {
	for _, line := range strings.Split(candidate, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "- ") {
			continue
		}
		colon := strings.IndexByte(trimmed, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		if key == "" || strings.HasPrefix(key, "\"") || strings.HasPrefix(key, "'") {
			continue
		}
		if strings.ContainsAny(key, " \t") {
			return true
		}
	}
	return false
}

func hasValidBlockIndentation(candidate string) bool {
	lines := strings.Split(candidate, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !strings.HasSuffix(trimmed, ":") {
			continue
		}
		indent := leadingSpaces(line)
		hasChild := false
		for j := i + 1; j < len(lines); j++ {
			next := strings.TrimSpace(lines[j])
			if next == "" {
				continue
			}
			if leadingSpaces(lines[j]) <= indent {
				return false
			}
			hasChild = true
			break
		}
		if !hasChild {
			return false
		}
	}
	return true
}

func containsBareProseLine(candidate string) bool {
	for _, line := range strings.Split(candidate, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if leadingSpaces(line) > 0 {
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "-") {
			continue
		}
		if strings.Contains(trimmed, ":") {
			continue
		}
		return true
	}
	return false
}

func looksLikeYAMLExample(candidate string) bool {
	for _, line := range strings.Split(candidate, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "- ") || strings.Contains(trimmed, ":") {
			return true
		}
	}
	return false
}

func isExampleRoot(value interface{}) bool {
	switch value.(type) {
	case map[string]interface{}, []interface{}:
		return true
	default:
		return false
	}
}

func leadingSpaces(line string) int {
	count := 0
	for _, r := range line {
		switch r {
		case ' ':
			count++
		case '\t':
			count += 2
		default:
			return count
		}
	}
	return count
}

// extractKey returns the raw key text from an AST key node, without inline
// comments or surrounding quotes. ast.Node.String() includes comments (e.g.
// "image # @schema ..."), so we use the underlying Value when available.
func extractKey(node ast.Node) string {
	if s, ok := node.(*ast.StringNode); ok {
		return s.Value
	}
	return unquoteKey(node.String())
}

// unquoteKey strips surrounding double or single quotes from a YAML key string.
// The AST's Key.String() returns the quoted form for keys like "a.b.c", which
// would render as literal quote characters in the collapsed output.
func unquoteKey(key string) string {
	if len(key) >= 2 {
		if (key[0] == '"' && key[len(key)-1] == '"') ||
			(key[0] == '\'' && key[len(key)-1] == '\'') {
			return key[1 : len(key)-1]
		}
	}
	return key
}

// astToOrdered converts an AST node into an ordered tree structure.
// Maps become *orderedMap, sequences become []interface{}, scalars become Go values.
func astToOrdered(node ast.Node) interface{} {
	if node == nil {
		return nil
	}

	switch n := node.(type) {
	case *ast.MappingNode:
		om := &orderedMap{entries: make([]orderedEntry, 0, len(n.Values))}
		for _, v := range n.Values {
			if v.Key != nil {
				om.entries = append(om.entries, orderedEntry{
					key:   extractKey(v.Key),
					value: astToOrdered(v.Value),
				})
			}
		}
		return om

	case *ast.MappingValueNode:
		// A single mapping value at the document root
		om := &orderedMap{entries: []orderedEntry{{
			key:   extractKey(n.Key),
			value: astToOrdered(n.Value),
		}}}
		return om

	case *ast.SequenceNode:
		arr := make([]interface{}, 0, len(n.Values))
		for _, v := range n.Values {
			arr = append(arr, astToOrdered(v))
		}
		return arr

	case *ast.TagNode:
		return astToOrdered(n.Value)

	case *ast.AnchorNode:
		return astToOrdered(n.Value)

	case *ast.AliasNode:
		return n.String()

	case *ast.NullNode:
		return nil

	case *ast.BoolNode:
		return n.Value

	case *ast.IntegerNode:
		return n.Value

	case *ast.FloatNode:
		return n.Value

	case *ast.StringNode:
		return n.Value

	case *ast.InfinityNode:
		return n.String()

	case *ast.NanNode:
		return n.String()

	default:
		return node.String()
	}
}

// renderNode renders any YAML node with depth tracking.
func renderNode(sb *strings.Builder, node interface{}, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	switch v := node.(type) {
	case *orderedMap:
		renderMap(sb, v, path, indent, depth, opts, comments)
	case []interface{}:
		renderArray(sb, v, path, indent, depth, opts, comments)
	default:
		renderScalar(sb, v, opts.ShowDefaults)
	}
}

// renderMap handles ordered map nodes with depth limiting.
func renderMap(sb *strings.Builder, m *orderedMap, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	for _, entry := range m.entries {
		childPath := entry.key
		if path != "" {
			childPath = path + "." + entry.key
		}

		// Add comment if available, enabled, and the entry won't be immediately collapsed
		if opts.ShowComments && !willCollapse(entry.value, depth+1, opts) {
			if comment, ok := comments[childPath]; ok {
				sb.WriteString(indent)
				sb.WriteString("# ")
				sb.WriteString(comment)
				sb.WriteString("\n")
			}
		}

		sb.WriteString(indent)
		sb.WriteString(entry.key)
		sb.WriteString(": ")

		renderValue(sb, entry.value, childPath, indent+"  ", depth+1, opts, comments)
	}
}

// renderArray handles array nodes with depth limiting and item truncation.
func renderArray(sb *strings.Builder, arr []interface{}, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	maxItems := opts.MaxArrayItems
	if maxItems == 0 {
		maxItems = len(arr) // unlimited
	}

	for i, item := range arr {
		if i >= maxItems {
			remaining := len(arr) - maxItems
			sb.WriteString(indent)
			fmt.Fprintf(sb, "... and %d more items\n", remaining)
			break
		}

		sb.WriteString(indent)
		sb.WriteString("- ")

		renderArrayItem(sb, item, path, indent, depth+1, opts, comments)
	}
}

// renderArrayItem handles a single array item, with special formatting for objects.
func renderArrayItem(sb *strings.Builder, item interface{}, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	switch v := item.(type) {
	case *orderedMap:
		if len(v.entries) == 0 {
			sb.WriteString("object (empty)\n")
			return
		}
		if depth >= opts.MaxDepth {
			sb.WriteString(summarizeOrderedMap(v))
			sb.WriteString("\n")
			return
		}
		renderInlineMap(sb, v, path, indent+"  ", depth, opts, comments)

	case []interface{}:
		if len(v) == 0 {
			sb.WriteString("array (empty)\n")
			return
		}
		if depth >= opts.MaxDepth {
			sb.WriteString(summarizeArray(v))
			sb.WriteString("\n")
			return
		}
		sb.WriteString("\n")
		renderArray(sb, v, path, indent+"  ", depth, opts, comments)

	default:
		renderScalar(sb, v, opts.ShowDefaults)
		sb.WriteString("\n")
	}
}

// renderInlineMap renders a map with the first key on the current line (for array items).
func renderInlineMap(sb *strings.Builder, m *orderedMap, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	first := m.entries[0]
	childPath := first.key
	if path != "" {
		childPath = path + "." + first.key
	}
	sb.WriteString(first.key)
	sb.WriteString(": ")
	renderValue(sb, first.value, childPath, indent, depth+1, opts, comments)

	for _, entry := range m.entries[1:] {
		childPath = entry.key
		if path != "" {
			childPath = path + "." + entry.key
		}
		sb.WriteString(indent)
		sb.WriteString(entry.key)
		sb.WriteString(": ")
		renderValue(sb, entry.value, childPath, indent, depth+1, opts, comments)
	}
}

// renderValue renders a single value with depth checking.
func renderValue(sb *strings.Builder, value interface{}, path string, indent string, depth int, opts CollapseOptions, comments map[string]string) {
	switch v := value.(type) {
	case *orderedMap:
		if len(v.entries) == 0 {
			sb.WriteString("object (empty)\n")
			return
		}
		if depth >= opts.MaxDepth {
			sb.WriteString(summarizeOrderedMap(v))
			sb.WriteString("\n")
			return
		}
		sb.WriteString("\n")
		renderMap(sb, v, path, indent, depth, opts, comments)

	case []interface{}:
		if len(v) == 0 {
			sb.WriteString("array (empty)\n")
			return
		}
		if depth >= opts.MaxDepth {
			sb.WriteString(summarizeArray(v))
			sb.WriteString("\n")
			return
		}
		sb.WriteString("\n")
		renderArray(sb, v, path, indent, depth, opts, comments)

	default:
		renderScalar(sb, v, opts.ShowDefaults)
		sb.WriteString("\n")
	}
}

// renderScalar writes a scalar value to the builder.
func renderScalar(sb *strings.Builder, v interface{}, showDefaults bool) {
	if showDefaults {
		sb.WriteString(formatScalar(v))
	} else {
		sb.WriteString(inferType(v))
	}
}

// summarizeOrderedMap returns a type summary for an ordered map.
func summarizeOrderedMap(m *orderedMap) string {
	switch len(m.entries) {
	case 0:
		return "object (empty)"
	case 1:
		return "object (1 key)"
	default:
		return fmt.Sprintf("object (%d keys)", len(m.entries))
	}
}

// summarizeArray returns a summary for an array.
func summarizeArray(arr []interface{}) string {
	switch len(arr) {
	case 0:
		return "array (empty)"
	case 1:
		return "array (1 item)"
	default:
		return fmt.Sprintf("array (%d items)", len(arr))
	}
}

// inferType returns the type name for a scalar value.
func inferType(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return "number"
	case string:
		return "string"
	default:
		return "unknown"
	}
}

// formatScalar formats a scalar value for YAML output.
func formatScalar(v interface{}) string {
	switch val := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case uint64:
		return strconv.FormatUint(val, 10)
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'g', -1, 64)
	case string:
		if val == "" {
			return `""`
		}
		if needsQuoting(val) {
			return strconv.Quote(val)
		}
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}

// needsQuoting returns true if a string needs YAML quoting.
func needsQuoting(s string) bool {
	if s == "" {
		return true
	}

	if strings.TrimSpace(s) != s {
		return true
	}

	for _, c := range s {
		switch c {
		case ':', '#', '\n', '"', '\'', '[', ']', '{', '}', '&', '*', '!', '|', '>', '%', '@', '`':
			return true
		}
	}

	lower := strings.ToLower(s)
	switch lower {
	case "true", "false", "null", "yes", "no", "on", "off", "~":
		return true
	}

	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}

	return false
}

// extractComments extracts comments from a parsed YAML file.
// Returns a map of full dotted paths to their associated comments.
func extractComments(file *ast.File, source []byte) map[string]string {
	comments := make(map[string]string, 8)
	lines := strings.Split(string(source), "\n")

	if len(file.Docs) > 0 {
		extractCommentsFromNode(file.Docs[0].Body, "", comments, lines)
	}

	return comments
}

// extractCommentsFromNode recursively extracts comments from AST nodes.
// Comments are keyed by full dotted path to avoid collisions.
func extractCommentsFromNode(node ast.Node, path string, comments map[string]string, sourceLines []string) {
	if node == nil {
		return
	}

	switch n := node.(type) {
	case *ast.MappingNode:
		for _, value := range n.Values {
			extractCommentsFromNode(value, path, comments, sourceLines)
		}
	case *ast.MappingValueNode:
		keyNode := n.Key
		if keyNode == nil {
			return
		}

		key := extractKey(keyNode)
		newPath := key
		if path != "" {
			newPath = path + "." + key
		}

		// Extract comment associated with the key, the mapping value node, or
		// the value node itself. goccy/go-yaml attaches preceding-line comments to
		// the MappingValueNode, inline comments on the key node, and value-trailing
		// comments (e.g., `key: value  # comment`) on the value AST node.
		text := extractLeadingCommentLine(sourceLines, keyNode)
		if text == "" {
			var commentNode *ast.CommentGroupNode
			if c := keyNode.GetComment(); c != nil {
				commentNode = c
			} else if c := n.GetComment(); c != nil {
				commentNode = c
			} else if n.Value != nil && isScalarASTNode(n.Value) {
				if c := n.Value.GetComment(); c != nil {
					commentNode = c
				}
			}
			if commentNode != nil {
				text = extractFirstCommentLine(commentNode.String())
			}
		}
		if text != "" {
			comments[newPath] = text
		}

		extractCommentsFromNode(n.Value, newPath, comments, sourceLines)

	case *ast.SequenceNode:
		for i, value := range n.Values {
			extractCommentsFromNode(value, fmt.Sprintf("%s[%d]", path, i), comments, sourceLines)
		}

	case *ast.AnchorNode:
		extractCommentsFromNode(n.Value, path, comments, sourceLines)

	case *ast.TagNode:
		extractCommentsFromNode(n.Value, path, comments, sourceLines)
	}
}

func extractLeadingCommentLine(sourceLines []string, keyNode ast.Node) string {
	token := keyNode.GetToken()
	if token == nil || token.Position == nil || token.Position.Line <= 1 {
		return ""
	}

	lineIndex := token.Position.Line - 2 // previous source line, zero-based
	if lineIndex >= len(sourceLines) {
		lineIndex = len(sourceLines) - 1
	}

	var block []string
	for i := lineIndex; i >= 0; i-- {
		line := strings.TrimSpace(sourceLines[i])
		if line == "" {
			break
		}
		if !strings.HasPrefix(line, "#") {
			break
		}
		block = append([]string{line}, block...)
	}

	if len(block) == 0 {
		return ""
	}
	return extractFirstCommentLine(strings.Join(block, "\n"))
}

func isScalarASTNode(node ast.Node) bool {
	switch unwrapYAMLPathNode(node).(type) {
	case *ast.NullNode,
		*ast.BoolNode,
		*ast.IntegerNode,
		*ast.FloatNode,
		*ast.StringNode,
		*ast.InfinityNode,
		*ast.NanNode:
		return true
	default:
		return false
	}
}

// extractFirstCommentLine processes a raw comment string (potentially multi-line)
// and returns only the first meaningful line. Lines starting with @schema
// (Helm schema annotations) are skipped as they render as garbled output for LLMs.
func extractFirstCommentLine(raw string) string {
	inSchema := false
	var blocks [][]string
	var current []string

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			if len(current) > 0 {
				blocks = append(blocks, current)
				current = nil
			}
			continue
		}
		if strings.HasPrefix(line, "@schema") {
			inSchema = !inSchema
			continue
		}
		if inSchema {
			continue
		}
		// Helm convention: "-- description" prefix
		line = strings.TrimPrefix(line, "-- ")
		current = append(current, line)
	}
	if len(current) > 0 {
		blocks = append(blocks, current)
	}
	if len(blocks) == 0 {
		return ""
	}

	// goccy can attach a commented-out example from the previous key to the
	// next key's leading docs. Helm values usually separate those blocks with
	// a blank comment line, so prefer the first line of the last block.
	last := blocks[len(blocks)-1]
	if len(last) == 0 {
		return ""
	}
	return last[0]
}

// willCollapse returns true if the value would be immediately collapsed
// (summarized) at the given depth with the given options.
func willCollapse(value interface{}, depth int, opts CollapseOptions) bool {
	if opts.MaxDepth == 0 {
		return false
	}
	switch v := value.(type) {
	case *orderedMap:
		return len(v.entries) > 0 && depth >= opts.MaxDepth
	case []interface{}:
		return len(v) > 0 && depth >= opts.MaxDepth
	default:
		return false
	}
}

// stripComments removes comments from YAML while preserving structure.
func stripComments(data []byte) (string, bool, error) {
	var root interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", false, fmt.Errorf("parsing YAML: %w", err)
	}

	out, err := yaml.Marshal(root)
	if err != nil {
		return "", false, fmt.Errorf("marshaling YAML: %w", err)
	}

	return strings.TrimSuffix(string(out), "\n"), false, nil
}
