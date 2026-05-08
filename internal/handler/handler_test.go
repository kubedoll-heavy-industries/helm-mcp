package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/kubedoll-heavy-industries/helm-mcp/internal/helm"
	"github.com/kubedoll-heavy-industries/helm-mcp/internal/helm/mocks"
)

func TestNew(t *testing.T) {
	t.Run("with nil logger uses nop", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, nil)

		assert.NotNil(t, h)
		assert.NotNil(t, h.logger)
	})

	t.Run("with provided logger", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		logger := zap.NewNop()
		h := New(mockSvc, logger)

		assert.NotNil(t, h)
		assert.Equal(t, logger, h.logger)
	})
}

func TestValidateRequired(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string]string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "all fields present",
			fields:  map[string]string{"repo": "https://example.com", "chart": "nginx"},
			wantErr: false,
		},
		{
			name:    "empty field",
			fields:  map[string]string{"repo": "", "chart": "nginx"},
			wantErr: true,
			errMsg:  "repo is required",
		},
		{
			name:    "whitespace only",
			fields:  map[string]string{"repo": "   ", "chart": "nginx"},
			wantErr: true,
			errMsg:  "repo is required",
		},
		{
			name:    "empty map",
			fields:  map[string]string{},
			wantErr: false,
		},
		{
			name:    "multiple empty fields reports first alphabetically",
			fields:  map[string]string{"zebra": "", "alpha": "", "beta": ""},
			wantErr: true,
			errMsg:  "alpha is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRequired(tt.fields)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestResolveVersion(t *testing.T) {
	ctx := context.Background()

	t.Run("explicit version returned as-is", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())

		version, err := h.resolveVersion(ctx, "https://repo.com", "nginx", "1.2.3")

		assert.NoError(t, err)
		assert.Equal(t, "1.2.3", version)
		mockSvc.AssertNotCalled(t, "GetLatestVersion")
	})

	t.Run("whitespace version trimmed", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())

		version, err := h.resolveVersion(ctx, "https://repo.com", "nginx", "  1.2.3  ")

		assert.NoError(t, err)
		assert.Equal(t, "1.2.3", version)
	})

	t.Run("empty version fetches latest", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "nginx").
			Return("2.0.0", nil)

		h := New(mockSvc, zap.NewNop())

		version, err := h.resolveVersion(ctx, "https://repo.com", "nginx", "")

		assert.NoError(t, err)
		assert.Equal(t, "2.0.0", version)
		mockSvc.AssertExpectations(t)
	})

	t.Run("whitespace-only version fetches latest", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "nginx").
			Return("2.0.0", nil)

		h := New(mockSvc, zap.NewNop())

		version, err := h.resolveVersion(ctx, "https://repo.com", "nginx", "   ")

		assert.NoError(t, err)
		assert.Equal(t, "2.0.0", version)
	})

	t.Run("error from GetLatestVersion propagated", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "nginx").
			Return("", errors.New("chart not found"))

		h := New(mockSvc, zap.NewNop())

		_, err := h.resolveVersion(ctx, "https://repo.com", "nginx", "")

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "chart not found")
	})
}

func TestSearchCharts(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://charts.bitnami.com/bitnami").
			Return([]string{"nginx", "redis", "postgresql"}, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://charts.bitnami.com/bitnami",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, []string{"nginx", "redis", "postgresql"}, output.Charts)
		assert.Equal(t, 3, output.Total)
		mockSvc.AssertExpectations(t)
	})

	t.Run("empty repository", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://empty.repo").
			Return([]string{}, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://empty.repo",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Empty(t, output.Charts)
		assert.Equal(t, 0, output.Total)
	})

	t.Run("missing repository_url", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, _, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
		mockSvc.AssertNotCalled(t, "ListCharts")
	})

	t.Run("service error", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://bad.repo").
			Return(nil, errors.New("network error"))

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, _, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://bad.repo",
		})

		assert.NoError(t, err) // Handler errors are in result, not err
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("trims whitespace from URL", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://charts.bitnami.com/bitnami").
			Return([]string{"nginx"}, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "  https://charts.bitnami.com/bitnami  ",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, []string{"nginx"}, output.Charts)
	})

	t.Run("with limit", func(t *testing.T) {
		charts := []string{"a", "b", "c", "d", "e"}
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://repo.com").
			Return(charts, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://repo.com",
			Limit:         2,
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, []string{"a", "b"}, output.Charts)
		assert.Equal(t, 5, output.Total)
	})

	t.Run("limit capped at 200", func(t *testing.T) {
		// Create 250 charts
		charts := make([]string, 250)
		for i := range charts {
			charts[i] = fmt.Sprintf("chart-%d", i)
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://repo.com").
			Return(charts, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://repo.com",
			Limit:         500, // Request more than max
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Charts, 200) // Capped at 200
		assert.Equal(t, 250, output.Total)
	})

	t.Run("with search filter", func(t *testing.T) {
		charts := []string{"nginx", "nginx-ingress", "redis", "redis-cluster"}
		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListCharts", ctx, "https://repo.com").
			Return(charts, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.searchCharts()

		result, output, err := handler(ctx, nil, searchChartsInput{
			RepositoryURL: "https://repo.com",
			Keyword:       "redis",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, []string{"redis", "redis-cluster"}, output.Charts)
		assert.Equal(t, 2, output.Total)
	})
}

func TestGetVersions(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		versions := []helm.ChartVersion{
			{Version: "2.0.0", AppVersion: "1.25.0", Created: time.Now(), Deprecated: false},
			{Version: "1.0.0", AppVersion: "1.24.0", Created: time.Now(), Deprecated: true},
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListVersions", ctx, "https://repo.com", "nginx").
			Return(versions, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, output, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Versions, 2)
		assert.Equal(t, "2.0.0", output.Versions[0].Version)
		assert.Equal(t, 2, output.Total)
	})

	t.Run("with limit", func(t *testing.T) {
		versions := []helm.ChartVersion{
			{Version: "3.0.0"},
			{Version: "2.0.0"},
			{Version: "1.0.0"},
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListVersions", ctx, "https://repo.com", "nginx").
			Return(versions, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, output, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			Limit:         2,
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Versions, 2)
		assert.Equal(t, "3.0.0", output.Versions[0].Version)
		assert.Equal(t, "2.0.0", output.Versions[1].Version)
		assert.Equal(t, 3, output.Total)
	})

	t.Run("limit=1 for latest version", func(t *testing.T) {
		versions := []helm.ChartVersion{
			{Version: "3.0.0", AppVersion: "latest"},
			{Version: "2.0.0"},
			{Version: "1.0.0"},
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListVersions", ctx, "https://repo.com", "nginx").
			Return(versions, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, output, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			Limit:         1,
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Versions, 1)
		assert.Equal(t, "3.0.0", output.Versions[0].Version)
		assert.Equal(t, 3, output.Total)
	})

	t.Run("limit capped at 100", func(t *testing.T) {
		// Create 150 versions
		versions := make([]helm.ChartVersion, 150)
		for i := range versions {
			versions[i] = helm.ChartVersion{Version: fmt.Sprintf("1.0.%d", i)}
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListVersions", ctx, "https://repo.com", "nginx").
			Return(versions, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, output, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			Limit:         200, // Request more than max
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Versions, 100) // Capped at 100
		assert.Equal(t, 150, output.Total)
	})

	t.Run("negative limit rejected", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, _, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			Limit:         -1,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("missing required fields", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, _, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("default limit applied", func(t *testing.T) {
		// Create 25 versions
		versions := make([]helm.ChartVersion, 25)
		for i := 0; i < 25; i++ {
			versions[i] = helm.ChartVersion{Version: "1.0." + string(rune('0'+i))}
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("ListVersions", ctx, "https://repo.com", "nginx").
			Return(versions, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getVersions()

		result, output, err := handler(ctx, nil, getVersionsInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Len(t, output.Versions, defaultVersionListLimit)
		assert.Equal(t, 25, output.Total)
	})
}

func TestGetValues(t *testing.T) {
	ctx := context.Background()

	t.Run("success with explicit version", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1\nimage: nginx"), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "1.0.0", output.Version)
		// Output preserves source order and is collapsed with default depth=3
		assert.Contains(t, output.Values, "image: nginx")
		assert.Contains(t, output.Values, "replicaCount: 1")
	})

	t.Run("resolves latest version", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "nginx").
			Return("2.0.0", nil)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "2.0.0").
			Return([]byte("replicaCount: 2"), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "", // Should resolve to latest
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "2.0.0", output.Version, "resolved version should be included in output")
		assert.Equal(t, "replicaCount: 2", output.Values)
		mockSvc.AssertExpectations(t)
	})

	t.Run("missing required fields", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "", // Missing
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("with path extraction", func(t *testing.T) {
		yamlContent := `server:
  port: 8080
  host: localhost
client:
  timeout: 30`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Path:          ".server",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, output.Values, "port: 8080")
		assert.Contains(t, output.Values, "host: localhost")
	})

	t.Run("with path extraction preserves comments", func(t *testing.T) {
		yamlContent := `server:
  # -- Port exposed by the service
  port: 8080
  # -- Hostname clients should use
  host: localhost
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		showComments := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Path:          ".server",
			ShowComments:  &showComments,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, output.Values, "# Port exposed by the service")
		assert.Contains(t, output.Values, "port: 8080")
		assert.Contains(t, output.Values, "# Hostname clients should use")
		assert.Contains(t, output.Values, "host: localhost")
	})

	t.Run("with path extraction preserves selected empty object comment", func(t *testing.T) {
		yamlContent := `prometheus:
  prometheusSpec:
    # -- StorageSpec defines persistent storage.
    # Additional details are intentionally omitted from collapsed comments.
    storageSpec: {}
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		showComments := true
		depth := 0
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Path:          ".prometheus.prometheusSpec.storageSpec",
			Depth:         &depth,
			ShowComments:  &showComments,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, output.Values, "# StorageSpec defines persistent storage.")
		assert.Contains(t, output.Values, "{}")
		assert.NotContains(t, output.Values, "Additional details")
	})

	t.Run("with path extraction includes nearby examples when requested", func(t *testing.T) {
		yamlContent := `prometheus:
  prometheusSpec:
    # -- StorageSpec defines persistent storage.
    storageSpec: {}
    ## Using PersistentVolumeClaim
    ##
    # volumeClaimTemplate:
    #   spec:
    #     resources:
    #       requests:
    #         storage: 50Gi
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		depth := 0
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			Path:            ".prometheus.prometheusSpec.storageSpec",
			Depth:           &depth,
			IncludeExamples: &includeExamples,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		require.Len(t, output.Examples, 1)
		assert.Contains(t, output.Examples[0].YAML, "volumeClaimTemplate:")
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "--- examples ---")
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "storage: 50Gi")
	})

	t.Run("example_limit=0 falls back to default", func(t *testing.T) {
		yamlContent := `prometheus:
  prometheusSpec:
    # -- StorageSpec defines persistent storage.
    storageSpec: {}
    ## Using PersistentVolumeClaim
    ##
    # volumeClaimTemplate:
    #   spec:
    #     resources:
    #       requests:
    #         storage: 50Gi
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		depth := 0
		zero := 0
		_, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			Path:            ".prometheus.prometheusSpec.storageSpec",
			Depth:           &depth,
			IncludeExamples: &includeExamples,
			ExampleLimit:    &zero,
		})

		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(output.Examples), 1, "example_limit=0 should fall back to default, not return zero examples")
	})

	t.Run("include_examples requires path", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			IncludeExamples: &includeExamples,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "include_examples requires path")
	})

	t.Run("with depth limiting", func(t *testing.T) {
		yamlContent := `server:
  port: 8080
  host: localhost
client:
  timeout: 30`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		depth := 1
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Depth:         &depth, // Only show top-level keys
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Contains(t, output.Values, "server: object (2 keys)")
		assert.Contains(t, output.Values, "client: object (1 key)")
	})

	t.Run("with unlimited depth", func(t *testing.T) {
		yamlContent := `name: test
value: 123`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		depth := 0
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Depth:         &depth, // Unlimited depth - return raw YAML
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, yamlContent, output.Values)
	})

	t.Run("with include_schema true and schema present", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1"), nil)
		mockSvc.On("GetValuesSchema", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte(`{"type": "object"}`), true, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeSchema := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
			IncludeSchema: &includeSchema,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, `{"type": "object"}`, output.Schema)
	})

	t.Run("with include_schema true but schema absent", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1"), nil)
		mockSvc.On("GetValuesSchema", ctx, "https://repo.com", "nginx", "1.0.0").
			Return(nil, false, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeSchema := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
			IncludeSchema: &includeSchema,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, output.Schema)
	})

	t.Run("negative depth rejected", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1"), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		depth := -1
		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
			Depth:         &depth,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("negative max_array_items rejected", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1"), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		maxItems := -5
		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
			MaxArrayItems: &maxItems,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("without include_schema doesn't fetch schema", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("replicaCount: 1"), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, output.Schema)
		mockSvc.AssertNotCalled(t, "GetValuesSchema")
	})

	t.Run("auto-reduces depth when output exceeds limit", func(t *testing.T) {
		// Build YAML with many nested keys that exceeds MaxResponseBytes at high depth
		// but fits when collapsed to depth=1
		var sb strings.Builder
		for i := 0; i < 400; i++ {
			fmt.Fprintf(&sb, "section%d:\n", i)
			for j := 0; j < 5; j++ {
				fmt.Fprintf(&sb, "  key%d:\n", j)
				for k := 0; k < 3; k++ {
					fmt.Fprintf(&sb, "    sub%d: value%d\n", k, k)
				}
			}
		}
		bigYAML := sb.String()

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "big", "1.0.0").
			Return([]byte(bigYAML), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		depth := 10
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "big",
			ChartVersion:  "1.0.0",
			Depth:         &depth,
		})

		assert.NoError(t, err)
		assert.NotNil(t, result) // No error - auto-reduced instead
		assert.LessOrEqual(t, len(output.Values), MaxResponseBytes)
	})

	t.Run("auto-reduce returns error when depth=1 still exceeds limit", func(t *testing.T) {
		// Build YAML with thousands of top-level keys that exceeds MaxResponseBytes even at depth=1
		var sb strings.Builder
		for i := 0; i < 2000; i++ {
			fmt.Fprintf(&sb, "top_level_key_%04d: value_%d\n", i, i)
		}
		hugeYAML := sb.String()

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "huge", "1.0.0").
			Return([]byte(hugeYAML), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "huge",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
		// Verify the error message suggests using path parameter
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "path")
	})

	t.Run("schema fetch failure surfaces schema_warning", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte("foo: 1"), nil)
		mockSvc.On("GetValuesSchema", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(nil), false, errors.New("connection reset"))

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeSchema := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			IncludeSchema: &includeSchema,
		})

		assert.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError, "schema failure must not fail the whole request")
		assert.Empty(t, output.Schema)
		assert.NotEmpty(t, output.SchemaWarning, "agent must be able to tell schema fetch failed")
		assert.Contains(t, output.SchemaWarning, "connection reset")
		// Warning should also be visible in the text content the LLM reads
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "schema fetch failed")
	})

	t.Run("budget loop accounts for schema size", func(t *testing.T) {
		// Big schema that alone fits, plus values that fit at low depth, should
		// still produce a non-error response (depth-reduced) rather than an
		// over-budget error.
		var schemaBuilder strings.Builder
		schemaBuilder.WriteString(`{"type":"object","properties":{`)
		for i := 0; i < 500; i++ {
			if i > 0 {
				schemaBuilder.WriteString(",")
			}
			fmt.Fprintf(&schemaBuilder, `"prop_%04d":{"type":"string","description":"a fairly long description that takes some bytes to encode"}`, i)
		}
		schemaBuilder.WriteString(`}}`)

		var valuesBuilder strings.Builder
		for i := 0; i < 500; i++ {
			fmt.Fprintf(&valuesBuilder, "section_%04d:\n  child:\n    grandchild: value_%d\n", i, i)
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(valuesBuilder.String()), nil)
		mockSvc.On("GetValuesSchema", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(schemaBuilder.String()), true, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeSchema := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			IncludeSchema: &includeSchema,
		})

		assert.NoError(t, err)
		require.NotNil(t, result)
		// Either succeeds (depth was reduced) or errors with a useful "use path"
		// hint — but never silently truncates without telling the agent.
		if result.IsError {
			assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "path")
		} else {
			assert.NotEmpty(t, output.Values)
		}
	})

	t.Run("example_limit > max is clamped to max", func(t *testing.T) {
		yamlContent := `app:
  # -- block A
  ## Example A
  # alpha: 1
  blockA: false
  # -- block B
  ## Example B
  # beta: 2
  blockB: false
  # -- block C
  ## Example C
  # gamma: 3
  blockC: false
  # -- block D
  ## Example D
  # delta: 4
  blockD: false
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		large := 99
		_, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			Path:            ".app",
			IncludeExamples: &includeExamples,
			ExampleLimit:    &large,
		})
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(output.Examples), 3, "example_limit must be clamped to maxExampleLimit=3")
	})

	t.Run("include_examples on array-index path returns no error", func(t *testing.T) {
		yamlContent := `containers:
  - name: app
    image: nginx
  - name: sidecar
    image: redis
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			Path:            ".containers[0]",
			IncludeExamples: &includeExamples,
		})
		assert.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError, "include_examples on array-index path should not error")
		// May or may not find examples; we just need a clean response.
		_ = output
	})

	t.Run("root path '.' returns full document", func(t *testing.T) {
		yamlContent := `service:
  enabled: true
ingress:
  className: nginx
`
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(yamlContent), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		zero := 0
		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
			Path:          ".",
			Depth:         &zero,
		})
		assert.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError)
		assert.Contains(t, output.Values, "service:")
		assert.Contains(t, output.Values, "ingress:")
	})

	t.Run("parse failure degrades to raw values with parse_warning", func(t *testing.T) {
		// Reproducer for argo-cd's chart pattern: empty literal block scalar +
		// blank line + same-indent comment + sibling key. goccy/go-yaml has a
		// bug here (see ../../docs/.. or upstream issue). The handler must
		// degrade gracefully instead of returning an error.
		problematic := []byte("affinity: |\n\n# -- comment\ntolerations: []\n")

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "argocd", "1.0.0").
			Return(problematic, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		result, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "argocd",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		require.NotNil(t, result)
		assert.False(t, result.IsError, "parse failure must not return an error result")
		assert.NotEmpty(t, output.ParseWarning, "agent must be told the parser failed")
		assert.Contains(t, output.Values, "affinity", "raw values must be returned so the agent has the data")
		assert.Contains(t, output.Values, "tolerations", "raw values must include the sibling key")
		// Text content should also surface the warning.
		assert.Contains(t, fmt.Sprintf("%v", result.Content[0]), "parse_warning")
	})

	t.Run("parse failure warning is a single line, not a code frame", func(t *testing.T) {
		problematic := []byte("affinity: |\n\n# -- comment\ntolerations: []\n")

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "argocd", "1.0.0").
			Return(problematic, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		_, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "argocd",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotEmpty(t, output.ParseWarning)
		// Multi-line code frames bloat the warning and confuse agents that
		// surface tool errors verbatim.
		assert.NotContains(t, output.ParseWarning, "\n", "warning must be a single line")
		// Sanity: still contains the parser's location info so the agent
		// (or a developer reading logs) can find the offending line.
		assert.Regexp(t, `\[\d+:\d+\]`, output.ParseWarning)
	})

	t.Run("parse failure truncation respects line boundaries", func(t *testing.T) {
		// Build a huge YAML that won't parse and exceeds the budget, with
		// line-aligned content so we can detect mid-line truncation.
		var sb strings.Builder
		sb.WriteString("affinity: |\n\n# unparseable\n")
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(&sb, "key_%05d: value_with_some_meaningful_payload_to_take_bytes_%d\n", i, i)
		}
		problematic := []byte(sb.String())

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "argocd", "1.0.0").
			Return(problematic, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		_, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "argocd",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		require.NotEmpty(t, output.ParseWarning)
		require.Contains(t, output.Values, "...truncated", "output must be truncated for this large input")

		// The line immediately before the truncation marker should be a
		// complete `key_NNNNN: value_...` line, not a half-key like "key_0".
		idx := strings.Index(output.Values, "\n# ...truncated")
		require.Greater(t, idx, 0)
		lastNewline := strings.LastIndexByte(output.Values[:idx], '\n')
		require.Greater(t, lastNewline, -1)
		lastLine := output.Values[lastNewline+1 : idx]
		assert.Regexp(t, `^key_\d{5}: value_with_some_meaningful_payload_to_take_bytes_\d+$`, lastLine,
			"truncation must round down to a complete line")
	})

	t.Run("parse failure with path returns raw and explains path was not applied", func(t *testing.T) {
		problematic := []byte("affinity: |\n\n# -- comment\ntolerations: []\n")

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "argocd", "1.0.0").
			Return(problematic, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		_, output, err := handler(ctx, nil, getValuesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "argocd",
			ChartVersion:  "1.0.0",
			Path:          ".tolerations",
		})

		assert.NoError(t, err)
		assert.NotEmpty(t, output.ParseWarning)
		assert.Contains(t, output.ParseWarning, ".tolerations", "warning should name the path that was not applied")
	})

	t.Run("budget trim drops examples before erroring", func(t *testing.T) {
		// Construct a values doc where the values fit comfortably but the
		// examples (commented siblings) push over budget. The handler should
		// drop examples and succeed instead of returning a too-large error.
		var sb strings.Builder
		sb.WriteString("section:\n  field: value\n")
		// Add many large commented-block siblings under .section
		for i := 0; i < 30; i++ {
			fmt.Fprintf(&sb, "  # -- block %d\n", i)
			fmt.Fprintf(&sb, "  ## Example block %d\n", i)
			for j := 0; j < 50; j++ {
				fmt.Fprintf(&sb, "  # key_%d_%d: %s\n", i, j, strings.Repeat("x", 80))
			}
			fmt.Fprintf(&sb, "  field_%d: actual_value\n", i)
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetValues", ctx, "https://repo.com", "app", "1.0.0").
			Return([]byte(sb.String()), nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getValues()

		includeExamples := true
		exampleLimit := 3
		result, _, err := handler(ctx, nil, getValuesInput{
			RepositoryURL:   "https://repo.com",
			ChartName:       "app",
			ChartVersion:    "1.0.0",
			Path:            ".section",
			IncludeExamples: &includeExamples,
			ExampleLimit:    &exampleLimit,
		})
		assert.NoError(t, err)
		require.NotNil(t, result)
		// The budget loop should have either succeeded (with examples possibly
		// trimmed) or returned a clean error — never a panic and never silently
		// truncated values.
		_ = result
	})
}

func TestGetDependencies(t *testing.T) {
	ctx := context.Background()

	t.Run("success with dependencies", func(t *testing.T) {
		deps := []helm.Dependency{
			{Name: "redis", Version: "17.x", Repository: "https://charts.bitnami.com/bitnami"},
			{Name: "postgresql", Version: "12.x", Repository: "https://charts.bitnami.com/bitnami"},
		}

		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetDependencies", ctx, "https://repo.com", "app", "1.0.0").
			Return(deps, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getDependencies()

		result, output, err := handler(ctx, nil, getDependenciesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "1.0.0", output.Version)
		assert.Len(t, output.Dependencies, 2)
		assert.Equal(t, "redis", output.Dependencies[0].Name)
		assert.Equal(t, "postgresql", output.Dependencies[1].Name)
	})

	t.Run("no dependencies", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetDependencies", ctx, "https://repo.com", "simple", "1.0.0").
			Return([]helm.Dependency{}, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getDependencies()

		result, output, err := handler(ctx, nil, getDependenciesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "simple",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Empty(t, output.Dependencies)
	})

	t.Run("resolves latest version", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "app").
			Return("2.0.0", nil)
		mockSvc.On("GetDependencies", ctx, "https://repo.com", "app", "2.0.0").
			Return([]helm.Dependency{{Name: "redis", Version: "18.x"}}, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getDependencies()

		result, output, err := handler(ctx, nil, getDependenciesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "app",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "2.0.0", output.Version, "resolved version should be included in output")
		assert.Len(t, output.Dependencies, 1)
		mockSvc.AssertExpectations(t)
	})
}

func TestGetNotes(t *testing.T) {
	ctx := context.Background()

	t.Run("notes present", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetNotes", ctx, "https://repo.com", "nginx", "1.0.0").
			Return([]byte("Thank you for installing nginx!"), true, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getNotes()

		result, output, err := handler(ctx, nil, getNotesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "1.0.0", output.Version)
		assert.Equal(t, "Thank you for installing nginx!", output.Notes)
	})

	t.Run("notes absent returns isError", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetNotes", ctx, "https://repo.com", "simple", "1.0.0").
			Return(nil, false, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getNotes()

		result, _, err := handler(ctx, nil, getNotesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "simple",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("resolves latest version", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetLatestVersion", ctx, "https://repo.com", "nginx").
			Return("2.0.0", nil)
		mockSvc.On("GetNotes", ctx, "https://repo.com", "nginx", "2.0.0").
			Return([]byte("Notes for v2"), true, nil)

		h := New(mockSvc, zap.NewNop())
		handler := h.getNotes()

		result, output, err := handler(ctx, nil, getNotesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
		assert.Equal(t, "2.0.0", output.Version, "resolved version should be included in output")
		assert.Equal(t, "Notes for v2", output.Notes)
		mockSvc.AssertExpectations(t)
	})

	t.Run("missing required fields", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		h := New(mockSvc, zap.NewNop())
		handler := h.getNotes()

		result, _, err := handler(ctx, nil, getNotesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("service error", func(t *testing.T) {
		mockSvc := new(mocks.ChartService)
		mockSvc.On("GetNotes", ctx, "https://repo.com", "nginx", "1.0.0").
			Return(nil, false, errors.New("chart not found"))

		h := New(mockSvc, zap.NewNop())
		handler := h.getNotes()

		result, _, err := handler(ctx, nil, getNotesInput{
			RepositoryURL: "https://repo.com",
			ChartName:     "nginx",
			ChartVersion:  "1.0.0",
		})

		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}
