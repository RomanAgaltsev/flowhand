package tasks

import (
	"errors"
	"fmt"
	"testing"

	domaintasks "github.com/RomanAgaltsev/flowhand/internal/domain/tasks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func TestTranslate(t *testing.T) {
	tests := []struct {
		name string
		in   error
		want error
	}{
		{"no rows becomes domain not-found", pgx.ErrNoRows, domaintasks.ErrNotFound},
		{"wrapped no rows still matches", fmt.Errorf("q: %w", pgx.ErrNoRows), domaintasks.ErrNotFound},
		{"other errors pass through", errors.New("conn refused"), nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := translate(tt.in)
			if tt.want != nil {
				require.ErrorIs(t, got, tt.want)
				return
			}
			require.NotErrorIs(t, got, domaintasks.ErrNotFound)
		})
	}
}

func TestIsUniqueViolation(t *testing.T) {
	// errors.As needs a real *pgconn.PgError; only Code is read.
	require.True(t, isUniqueViolation(&pgconn.PgError{Code: "23505"}))
	require.True(t, isUniqueViolation(fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"})))

	// 23503 is foreign_key_violation — a different fault that must NOT be
	// mistaken for an idempotency replay.
	require.False(t, isUniqueViolation(&pgconn.PgError{Code: "23503"}))
	require.False(t, isUniqueViolation(errors.New("boom")))
}
