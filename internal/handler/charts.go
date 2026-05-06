package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/kubedoll-heavy-industries/helm-mcp/internal/mcputil"
)

// Default pagination limits
const (
	defaultChartListLimit = 50
)

// Token-based response limits.
// 10K tokens is a practical budget for LLM tool responses; at ~4 bytes/token
// that translates to 40KB of text content.
const (
	MaxResponseTokens = 10_000
	bytesPerToken     = 4
	MaxResponseBytes  = MaxResponseTokens * bytesPerToken
)

// Input/output types for chart tools

type searchChartsInput struct {
	RepositoryURL string `json:"repository_url" jsonschema:"Helm repository URL (e.g. https://charts.bitnami.com/bitnami) or OCI registry (e.g. oci://ghcr.io/traefik/helm)"`
	Keyword       string `json:"keyword,omitempty" jsonschema:"Filter charts by name (case-insensitive substring match, e.g. 'postgres')"`
	Limit         int    `json:"limit,omitempty" jsonschema:"Maximum results (default 50, max 200)"`
}

type searchChartsOutput struct {
	Charts []string `json:"charts" jsonschema:"Chart names"`
	Total  int      `json:"total" jsonschema:"Total matching charts (may exceed returned results if limit applied)"`
}

type getValuesInput struct {
	RepositoryURL   string `json:"repository_url" jsonschema:"Helm repository URL (e.g. https://charts.bitnami.com/bitnami) or OCI registry (e.g. oci://ghcr.io/traefik/helm)"`
	ChartName       string `json:"chart_name" jsonschema:"Chart name (e.g. postgresql)"`
	ChartVersion    string `json:"chart_version,omitempty" jsonschema:"Chart version (defaults to latest)"`
	Path            string `json:"path,omitempty" jsonschema:"YAML path (e.g. .ingress.enabled)"`
	Depth           *int   `json:"depth,omitempty" jsonschema:"Max nesting depth (default 2, 0 for unlimited)"`
	MaxArrayItems   *int   `json:"max_array_items,omitempty" jsonschema:"Max array items before truncation (default 3, 0 for unlimited)"`
	ShowComments    *bool  `json:"show_comments,omitempty" jsonschema:"Preserve YAML comments"`
	ShowDefaults    *bool  `json:"show_defaults,omitempty" jsonschema:"Include default values"`
	IncludeSchema   *bool  `json:"include_schema,omitempty" jsonschema:"Include values.schema.json in response"`
	IncludeExamples *bool  `json:"include_examples,omitempty" jsonschema:"Include nearby commented YAML examples for the selected path"`
	ExampleLimit    *int   `json:"example_limit,omitempty" jsonschema:"Maximum nearby examples to include (default 1, max 3)"`
}

type getValuesOutput struct {
	Version  string          `json:"version" jsonschema:"Resolved chart version (especially useful when chart_version was omitted and latest was used)"`
	Values   string          `json:"values" jsonschema:"Values content (YAML)"`
	Path     string          `json:"path,omitempty" jsonschema:"Extracted path, if specified"`
	Schema   string          `json:"schema,omitempty" jsonschema:"JSON Schema for values (if include_schema=true and schema exists)"`
	Examples []valuesExample `json:"examples,omitempty" jsonschema:"Nearby commented YAML examples, if include_examples=true and examples are found"`
}

type valuesExample struct {
	YAML       string `json:"yaml" jsonschema:"Example YAML"`
	Source     string `json:"source" jsonschema:"Where the example was found"`
	Confidence string `json:"confidence" jsonschema:"Confidence that the commented block is a usable example"`
}

type getDependenciesInput struct {
	RepositoryURL string `json:"repository_url" jsonschema:"Helm repository URL (e.g. https://charts.bitnami.com/bitnami) or OCI registry (e.g. oci://ghcr.io/traefik/helm)"`
	ChartName     string `json:"chart_name" jsonschema:"Chart name (e.g. postgresql)"`
	ChartVersion  string `json:"chart_version,omitempty" jsonschema:"Chart version (defaults to latest)"`
}

type getDependenciesOutput struct {
	Version      string           `json:"version" jsonschema:"Resolved chart version (especially useful when chart_version was omitted and latest was used)"`
	Dependencies []dependencyInfo `json:"dependencies" jsonschema:"Chart dependencies"`
}

type dependencyInfo struct {
	Name       string `json:"name" jsonschema:"Dependency name"`
	Version    string `json:"version" jsonschema:"Version constraint"`
	Repository string `json:"repository,omitempty" jsonschema:"Repository URL (empty for bundled)"`
	Condition  string `json:"condition,omitempty" jsonschema:"Condition for enabling"`
	Alias      string `json:"alias,omitempty" jsonschema:"Alias name"`
}

type getNotesInput struct {
	RepositoryURL string `json:"repository_url" jsonschema:"Helm repository URL (e.g. https://charts.bitnami.com/bitnami) or OCI registry (e.g. oci://ghcr.io/traefik/helm)"`
	ChartName     string `json:"chart_name" jsonschema:"Chart name (e.g. postgresql)"`
	ChartVersion  string `json:"chart_version,omitempty" jsonschema:"Chart version (defaults to latest)"`
}

type getNotesOutput struct {
	Version string `json:"version" jsonschema:"Resolved chart version (especially useful when chart_version was omitted and latest was used)"`
	Notes   string `json:"notes" jsonschema:"Contents of NOTES.txt"`
}

// Handler implementations

func (h *Handler) searchCharts() mcp.ToolHandlerFor[searchChartsInput, searchChartsOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in searchChartsInput) (*mcp.CallToolResult, searchChartsOutput, error) {
		emptyOutput := searchChartsOutput{Charts: []string{}}

		if err := validateRequired(map[string]string{"repository_url": in.RepositoryURL}); err != nil {
			return mcputil.TextError(err.Error()), emptyOutput, nil
		}

		if in.Limit < 0 {
			return mcputil.TextError("limit must be >= 0"), emptyOutput, nil
		}

		mcputil.SessionLogInfo(ctx, req, "Fetching repository index", map[string]any{
			"repository": in.RepositoryURL,
		})

		charts, err := h.svc.ListCharts(ctx, strings.TrimSpace(in.RepositoryURL))
		if err != nil {
			mcputil.SessionLogError(ctx, req, "Failed to fetch repository", map[string]any{
				"repository": in.RepositoryURL,
				"error":      err.Error(),
			})
			return mcputil.HandleError(err), emptyOutput, nil
		}

		mcputil.SessionLogInfo(ctx, req, "Found charts in repository", map[string]any{
			"repository": in.RepositoryURL,
			"count":      len(charts),
		})

		// Apply search filter
		if in.Keyword != "" {
			search := strings.ToLower(in.Keyword)
			filtered := make([]string, 0, len(charts))
			for _, c := range charts {
				if strings.Contains(strings.ToLower(c), search) {
					filtered = append(filtered, c)
				}
			}
			charts = filtered
		}

		total := len(charts)

		// Apply limit (default 50, max 200)
		limit := in.Limit
		if limit == 0 {
			limit = defaultChartListLimit
		}
		if limit > 200 {
			limit = 200
		}

		if limit < total {
			charts = charts[:limit]
		}

		return nil, searchChartsOutput{
			Charts: charts,
			Total:  total,
		}, nil
	}
}

func (h *Handler) getValues() mcp.ToolHandlerFor[getValuesInput, getValuesOutput] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in getValuesInput) (*mcp.CallToolResult, getValuesOutput, error) {
		if err := validateRequired(map[string]string{
			"repository_url": in.RepositoryURL,
			"chart_name":     in.ChartName,
		}); err != nil {
			return mcputil.TextError(err.Error()), getValuesOutput{}, nil
		}

		repo := strings.TrimSpace(in.RepositoryURL)
		chart := strings.TrimSpace(in.ChartName)
		path := strings.TrimSpace(in.Path)
		includeExamples := in.IncludeExamples != nil && *in.IncludeExamples
		if includeExamples && path == "" {
			return mcputil.TextError("include_examples requires path to keep example discovery scoped"), getValuesOutput{}, nil
		}

		version, err := h.resolveVersion(ctx, repo, chart, in.ChartVersion)
		if err != nil {
			return mcputil.HandleOpError("get_values", repo, chart, "", err), getValuesOutput{}, nil
		}

		mcputil.SessionLogInfo(ctx, req, "Downloading chart", map[string]any{
			"repository": repo,
			"chart":      chart,
			"version":    version,
		})

		valuesBytes, err := h.svc.GetValues(ctx, repo, chart, version)
		if err != nil {
			mcputil.SessionLogError(ctx, req, "Failed to get values", map[string]any{
				"repository": repo,
				"chart":      chart,
				"version":    version,
				"error":      err.Error(),
			})
			return mcputil.HandleOpError("get_values", repo, chart, version, err), getValuesOutput{}, nil
		}

		mcputil.SessionLogInfo(ctx, req, "Extracting values", map[string]any{
			"chart":   chart,
			"version": version,
			"size":    len(valuesBytes),
		})

		// Validate non-negative options
		if in.Depth != nil && *in.Depth < 0 {
			return mcputil.TextError("depth must be >= 0"), getValuesOutput{}, nil
		}
		if in.MaxArrayItems != nil && *in.MaxArrayItems < 0 {
			return mcputil.TextError("max_array_items must be >= 0"), getValuesOutput{}, nil
		}
		if in.ExampleLimit != nil && *in.ExampleLimit < 0 {
			return mcputil.TextError("example_limit must be >= 0"), getValuesOutput{}, nil
		}

		// Build collapse options from input
		opts := DefaultCollapseOptions()
		if in.Depth != nil {
			opts.MaxDepth = *in.Depth
		}
		if in.MaxArrayItems != nil {
			opts.MaxArrayItems = *in.MaxArrayItems
		}
		if in.ShowComments != nil {
			opts.ShowComments = *in.ShowComments
		}
		if in.ShowDefaults != nil {
			opts.ShowDefaults = *in.ShowDefaults
		}

		// Fetch schema early so we can account for its size
		var schemaStr string
		if in.IncludeSchema != nil && *in.IncludeSchema {
			schema, present, schemaErr := h.svc.GetValuesSchema(ctx, repo, chart, version)
			if schemaErr != nil {
				mcputil.SessionLogError(ctx, req, "Failed to get schema", map[string]any{
					"repository": repo,
					"chart":      chart,
					"version":    version,
					"error":      schemaErr.Error(),
				})
				// Don't fail the whole request, schema just won't be included
			} else if present {
				schemaStr = string(schema)
			}
		}

		// Apply collapse transformation, auto-reducing depth if output exceeds limit
		result, _, err := CollapseYAMLAtPath(valuesBytes, path, opts)
		if err != nil {
			// Provide actionable error message with chart context
			if strings.Contains(err.Error(), "path not found") {
				return mcputil.TextError(fmt.Sprintf("path %q not found in %s/%s@%s values.yaml (try depth=1 to see available keys)", path, repo, chart, version)), getValuesOutput{}, nil
			}
			if path != "" {
				return mcputil.TextError(fmt.Sprintf("invalid path syntax %q in %s/%s@%s: %v", path, repo, chart, version, err)), getValuesOutput{}, nil
			}
			return mcputil.TextError(fmt.Sprintf("processing values: %v", err)), getValuesOutput{}, nil
		}

		var examples []valuesExample
		var examplesText string
		if includeExamples {
			limit := defaultExampleLimit
			if in.ExampleLimit != nil {
				limit = *in.ExampleLimit
			}
			if limit > maxExampleLimit {
				limit = maxExampleLimit
			}
			nearby, err := extractNearbyExamples(valuesBytes, path, limit)
			if err != nil {
				if strings.Contains(err.Error(), "path not found") {
					return mcputil.TextError(fmt.Sprintf("path %q not found in %s/%s@%s values.yaml (try depth=1 to see available keys)", path, repo, chart, version)), getValuesOutput{}, nil
				}
				return mcputil.TextError(fmt.Sprintf("invalid path syntax %q in %s/%s@%s: %v", path, repo, chart, version, err)), getValuesOutput{}, nil
			}
			examples = make([]valuesExample, 0, len(nearby))
			for _, example := range nearby {
				examples = append(examples, valuesExample(example))
			}
			examplesText = formatExamplesText(examples)
		}

		for len(result)+len(schemaStr)+len(examplesText) > MaxResponseBytes && opts.MaxDepth > 1 {
			opts.MaxDepth--
			result, _, err = CollapseYAMLAtPath(valuesBytes, path, opts)
			if err != nil {
				if strings.Contains(err.Error(), "path not found") {
					return mcputil.TextError(fmt.Sprintf("path %q not found in %s/%s@%s values.yaml (try depth=1 to see available keys)", path, repo, chart, version)), getValuesOutput{}, nil
				}
				if path != "" {
					return mcputil.TextError(fmt.Sprintf("invalid path syntax %q in %s/%s@%s: %v", path, repo, chart, version, err)), getValuesOutput{}, nil
				}
				return mcputil.TextError(fmt.Sprintf("processing values: %v", err)), getValuesOutput{}, nil
			}
		}

		// If output still exceeds limit at minimum depth, return an actionable error
		if len(result)+len(schemaStr)+len(examplesText) > MaxResponseBytes {
			return mcputil.TextError(fmt.Sprintf(
				"values output too large (%d bytes, limit %d) even at depth=%d; use the 'path' parameter to select a subsection (e.g. path=\".ingress\")",
				len(result)+len(schemaStr)+len(examplesText), MaxResponseBytes, opts.MaxDepth,
			)), getValuesOutput{}, nil
		}

		output := getValuesOutput{
			Version:  version,
			Values:   result,
			Path:     path,
			Schema:   schemaStr,
			Examples: examples,
		}

		// Return raw YAML as text so LLMs read it directly, not wrapped in JSON.
		textContent := result
		if examplesText != "" {
			textContent += examplesText
		}
		if schemaStr != "" {
			textContent += "\n\n--- schema ---\n" + schemaStr
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: textContent}},
		}, output, nil
	}
}

func formatExamplesText(examples []valuesExample) string {
	if len(examples) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n--- examples ---")
	for i, example := range examples {
		fmt.Fprintf(&sb, "\n# example %d (%s, %s)\n", i+1, example.Confidence, example.Source)
		sb.WriteString(strings.TrimSpace(example.YAML))
		sb.WriteString("\n")
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

func (h *Handler) getDependencies() mcp.ToolHandlerFor[getDependenciesInput, getDependenciesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getDependenciesInput) (*mcp.CallToolResult, getDependenciesOutput, error) {
		emptyOutput := getDependenciesOutput{Dependencies: []dependencyInfo{}}

		if err := validateRequired(map[string]string{
			"repository_url": in.RepositoryURL,
			"chart_name":     in.ChartName,
		}); err != nil {
			return mcputil.TextError(err.Error()), emptyOutput, nil
		}

		repo := strings.TrimSpace(in.RepositoryURL)
		chart := strings.TrimSpace(in.ChartName)

		version, err := h.resolveVersion(ctx, repo, chart, in.ChartVersion)
		if err != nil {
			return mcputil.HandleOpError("get_dependencies", repo, chart, "", err), emptyOutput, nil
		}

		deps, err := h.svc.GetDependencies(ctx, repo, chart, version)
		if err != nil {
			return mcputil.HandleOpError("get_dependencies", repo, chart, version, err), emptyOutput, nil
		}

		// Convert to output format
		result := make([]dependencyInfo, 0, len(deps))
		for _, d := range deps {
			result = append(result, dependencyInfo{
				Name:       d.Name,
				Version:    d.Version,
				Repository: d.Repository,
				Condition:  d.Condition,
				Alias:      d.Alias,
			})
		}

		return nil, getDependenciesOutput{Version: version, Dependencies: result}, nil
	}
}

func (h *Handler) getNotes() mcp.ToolHandlerFor[getNotesInput, getNotesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in getNotesInput) (*mcp.CallToolResult, getNotesOutput, error) {
		if err := validateRequired(map[string]string{
			"repository_url": in.RepositoryURL,
			"chart_name":     in.ChartName,
		}); err != nil {
			return mcputil.TextError(err.Error()), getNotesOutput{}, nil
		}

		repo := strings.TrimSpace(in.RepositoryURL)
		chart := strings.TrimSpace(in.ChartName)

		version, err := h.resolveVersion(ctx, repo, chart, in.ChartVersion)
		if err != nil {
			return mcputil.HandleOpError("get_notes", repo, chart, "", err), getNotesOutput{}, nil
		}

		notes, present, err := h.svc.GetNotes(ctx, repo, chart, version)
		if err != nil {
			return mcputil.HandleOpError("get_notes", repo, chart, version, err), getNotesOutput{}, nil
		}

		if !present {
			return mcputil.TextError(fmt.Sprintf(
				"%s/%s@%s does not include NOTES.txt",
				repo, chart, version,
			)), getNotesOutput{}, nil
		}

		// Guard against responses that would overwhelm LLM context
		if len(notes) > MaxResponseBytes {
			return mcputil.TextError(fmt.Sprintf(
				"NOTES.txt too large (%d bytes, limit %d)",
				len(notes), MaxResponseBytes,
			)), getNotesOutput{}, nil
		}

		return nil, getNotesOutput{
			Version: version,
			Notes:   string(notes),
		}, nil
	}
}
