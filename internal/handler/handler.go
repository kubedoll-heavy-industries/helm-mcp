// Package handler provides MCP tool handlers for Helm operations.
package handler

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.uber.org/zap"

	"github.com/kubedoll-heavy-industries/helm-mcp/internal/helm"
	"github.com/kubedoll-heavy-industries/helm-mcp/internal/mcputil"
)

// Handler provides MCP tool handlers backed by a Helm service.
type Handler struct {
	svc    helm.ChartService
	logger *zap.Logger
}

// New creates a new Handler.
func New(svc helm.ChartService, logger *zap.Logger) *Handler {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Handler{
		svc:    svc,
		logger: logger,
	}
}

// Register registers all Helm tools with the MCP server.
func (h *Handler) Register(s *mcp.Server) {
	// Search for charts in a repository
	mcputil.RegisterTool(s, mcputil.ToolDef{
		Name:        "search_charts",
		Description: "List or search charts in a Helm repository. Provide a repository_url, then optionally filter by keyword (e.g. keyword='postgres'). Note: OCI registries (oci://) do not support browsing — for OCI you must already know the chart name, then call get_versions or get_values directly with that name.",
		ReadOnly:    true,
		OpenWorld:   true,
	}, h.searchCharts())

	// Get chart versions
	mcputil.RegisterTool(s, mcputil.ToolDef{
		Name:        "get_versions",
		Description: "Get available versions of a chart (newest first). Supports both HTTP/HTTPS repos and OCI registries (oci://). Use this to confirm a chart exists before calling get_values, or to find a specific version to pin.",
		ReadOnly:    true,
		OpenWorld:   true,
	}, h.getVersions())

	// Get chart values with optional schema
	mcputil.RegisterTool(s, mcputil.ToolDef{
		Name:        "get_values",
		Description: "Get chart values.yaml with optional JSON schema. Uses depth limiting (default 2) to show structure without overwhelming context. Use path to drill into specific sections, depth=0 for full YAML. include_examples surfaces nearby commented YAML examples and requires path to be set. Supports both HTTP/HTTPS repos and OCI registries (oci://).",
		ReadOnly:    true,
		OpenWorld:   true,
	}, h.getValues())

	// Get chart dependencies
	mcputil.RegisterTool(s, mcputil.ToolDef{
		Name:        "get_dependencies",
		Description: "Get chart dependencies (sub-charts). Each dependency entry includes the dependency's repository URL — feed that URL back into search_charts, get_versions, or get_values to inspect the dependency. Supports both HTTP/HTTPS repos and OCI registries (oci://).",
		ReadOnly:    true,
		OpenWorld:   true,
	}, h.getDependencies())

	// Get chart NOTES.txt
	mcputil.RegisterTool(s, mcputil.ToolDef{
		Name:        "get_notes",
		Description: "Get chart NOTES.txt (post-install instructions). Supports both HTTP/HTTPS repos and OCI registries (oci://).",
		ReadOnly:    true,
		OpenWorld:   true,
	}, h.getNotes())
}

// resolveVersion returns the given version if non-empty, otherwise fetches the latest.
// Returns an error if version is empty and fetching the latest version fails.
func (h *Handler) resolveVersion(ctx context.Context, repo, chart, version string) (string, error) {
	version = strings.TrimSpace(version)
	if version != "" {
		return version, nil
	}
	return h.svc.GetLatestVersion(ctx, repo, chart)
}

// validateRequired checks that required string fields are non-empty.
// Returns an error describing the first missing field (alphabetically), or nil if all fields are present.
func validateRequired(fields map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if strings.TrimSpace(fields[name]) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}
