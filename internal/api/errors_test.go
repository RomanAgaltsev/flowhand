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

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/RomanAgaltsev/flowhand/internal/api"
	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/RomanAgaltsev/flowhand/internal/service/task"
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
		name       string
		method     string
		path       string
		body       string
		wantCode   int
		wantSymbol string
		// wantDetail is a substring the message must carry. Empty means the
		// message must NOT describe internals — see the 5xx cases below.
		wantDetail string
	}{
		{
			name: "malformed json body", method: http.MethodPost, path: "/v1/tasks", body: `{`,
			wantCode: http.StatusBadRequest, wantSymbol: "invalid_request_body", wantDetail: "decode",
		},
		{
			name: "validation failure", method: http.MethodPost, path: "/v1/tasks", body: `{"handler":""}`,
			wantCode: http.StatusBadRequest, wantSymbol: "invalid_request_body", wantDetail: "handler",
		},
		{
			name: "missing required field", method: http.MethodPost, path: "/v1/tasks", body: `{}`,
			wantCode: http.StatusBadRequest, wantSymbol: "invalid_request_body", wantDetail: "handler",
		},
		{
			name: "undecodable path param", method: http.MethodGet, path: "/v1/tasks/not-a-uuid",
			wantCode: http.StatusBadRequest, wantSymbol: "invalid_parameter", wantDetail: "id",
		},
		{
			name: "handler error on create", method: http.MethodPost, path: "/v1/tasks", body: `{"handler":"echo"}`,
			wantCode: http.StatusInternalServerError, wantSymbol: "internal_error",
		},
		{
			name: "handler error on get", method: http.MethodGet, path: "/v1/tasks/0198f0ec-0000-7000-8000-000000000000",
			wantCode: http.StatusInternalServerError, wantSymbol: "internal_error",
		},
		{
			name: "non-ascii idempotency key", method: http.MethodPost, path: "/v1/tasks",
			body:     `{"handler":"echo","idempotency_key":"ключ"}`,
			wantCode: http.StatusBadRequest, wantSymbol: "invalid_request_body", wantDetail: "idempotency_key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequestWithContext(
				context.Background(), tt.method, ts.URL+tt.path, strings.NewReader(tt.body),
			)
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

			// `code` is the stable symbol clients branch on. Asserting it here
			// is what stops someone reverting it to a stringified HTTP status,
			// which would duplicate the status line and say nothing.
			assert.Equal(t, tt.wantSymbol, envelope.Code)
			assert.NotEmpty(t, envelope.Message)

			if tt.wantDetail != "" {
				// 4xx: the fault is the caller's, so the message must name what
				// about *their* request was wrong. A generic "Bad Request" here
				// makes four distinct failures indistinguishable.
				assert.Contains(t, envelope.Message, tt.wantDetail)
				return
			}
			// 5xx: the fault is ours, so the message must stay opaque - no
			// DSNs, table names or wrapped driver errors on the wire.
			assert.Equal(t, "internal server error", envelope.Message)
			assert.NotContains(t, envelope.Message, "db down",
				"5xx message must not leak the underlying error")
		})
	}
}

// TestIdempotencyConflict_OnTheWire pins the 409 the way the table above pins
// every other error: status, JSON content type, the stable symbol, and the
// field only this envelope carries. It needs its own server because the table
// shares one Commander that always fails with a plain error.
func TestIdempotencyConflict_OnTheWire(t *testing.T) {
	existing := uuid.MustParse("0198f0ec-0000-7000-8000-000000000001")
	h := api.NewHandler(
		&fakeCommander{err: &task.ConflictError{ExistingTaskID: existing}},
		&fakeQuerier{}, discardLogger(),
	)
	oasSrv, err := oas.NewServer(h, oas.WithErrorHandler(api.ErrorHandler))
	require.NoError(t, err)
	ts := httptest.NewServer(oasSrv)
	t.Cleanup(ts.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, ts.URL+"/v1/tasks",
		strings.NewReader(`{"handler":"echo","idempotency_key":"k-1","payload":{"a":2}}`))
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

	assert.Equal(t, http.StatusConflict, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	var envelope struct {
		Code           string `json:"code"`
		Message        string `json:"message"`
		ExistingTaskID string `json:"existing_task_id"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), "body was %q", body)
	assert.Equal(t, "idempotency_conflict", envelope.Code)
	assert.NotEmpty(t, envelope.Message)
	assert.Equal(t, existing.String(), envelope.ExistingTaskID)
}
