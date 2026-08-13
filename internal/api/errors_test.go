package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
)

// TestErrorEnvelopes_OnTheWire pins the contract api/openapi.yaml publishes:
// every error response is the Error schema as application/json. Handler-level
// tests cannot prove this - decode and validation failures never reach a
// handler method, and a handler that returns a raw error only becomes JSON
// because api.ErrorHandler is installed. Dropping WithErrorHandler in
// internal/server would silently revert every case below to text/plain.
func TestErrorEnvelopes_OnTheWire(t *testing.T) {
	h := api.NewHandler(
		&fakeCommander{err: errors.New("db down")},
		&fakeQuerier{err: errors.New("db down")},
		discardLogger(),
	)
	oasSrv, err := oas.NewServer(h, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)

	ts := httptest.NewServer(oasSrv)
	t.Cleanup(ts.Close)

	tests := []struct {
		name     string
		method   string
		path     string
		body     string
		wantCode int
	}{
		{"malformed json body", http.MethodPost, "/v1/tasks", `{`, http.StatusBadRequest},
		{"validation failure", http.MethodPost, "/v1/tasks", `{"handler":""}`, http.StatusBadRequest},
		{"missing required field", http.MethodPost, "/v1/tasks", `{}`, http.StatusBadRequest},
		{"undecodable path param", http.MethodGet, "/v1/tasks/not-a-uuid", "", http.StatusBadRequest},
		{"handler error on create", http.MethodPost, "/v1/tasks", `{"handler":"echo"}`, http.StatusInternalServerError},
		{"handler error on get", http.MethodGet, "/v1/tasks/0198f0ec-0000-7000-8000-000000000000", "", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(
				context.Background(), tt.method, ts.URL+tt.path, strings.NewReader(tt.body))
			require.NoError(t, err)
			req.Header.Set("Content-Type", "application/json")

			resp, err := ts.Client().Do(req)
			require.NoError(t, err)
			defer func() {
				if err := resp.Body.Close(); err != nil {
					t.Logf("close response body: %v", err)
				}
			}()

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, tt.wantCode, resp.StatusCode)
			assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

			// The body must decode as the spec's Error schema, with both
			// required fields populated - a generated client parses exactly
			// this, so an empty envelope is as broken as text/plain.
			var envelope struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			require.NoError(t, json.Unmarshal(body, &envelope), "body was %q", body)
			assert.Equal(t, http.StatusText(tt.wantCode), envelope.Message)
			assert.NotEmpty(t, envelope.Code)
		})
	}
}
