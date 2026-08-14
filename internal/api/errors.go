package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-faster/jx"
	"github.com/ogen-go/ogen/ogenerrors"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
)

// ErrorHandler renders ogen's pre-handler failures (body decode, parameter
// decode, request validation) as the spec's Error envelope instead of the
// default text/plain, so a client generated from this spec can parse every
// error the spec declares.
func ErrorHandler(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
	// The symbol is the generated enum, not a bare string: adding a code to
	// openapi.yaml is now the only way to introduce one, and a code that is not
	// in the spec fails to compile.
	code, symbol, msg := http.StatusInternalServerError,
		oas.ErrorCodeInternalError,
		"internal server error"

	var (
		decReq   *ogenerrors.DecodeRequestError
		decParam *ogenerrors.DecodeParamsError
	)
	switch {
	case errors.As(err, &decReq):
		// The caller's own request is at fault, so the detail is about their
		// input, not our internals - safe and useful to return.
		code, symbol, msg = http.StatusBadRequest, oas.ErrorCodeInvalidRequestBody, err.Error()
	case errors.As(err, &decParam):
		code, symbol, msg = http.StatusBadRequest, oas.ErrorCodeInvalidParameter, err.Error()
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	e := &oas.Error{Code: symbol, Message: msg}
	enc := new(jx.Encoder)
	e.Encode(enc)
	_, _ = enc.WriteTo(w) //nolint:errcheck // best-effort write to an already-failing response
}
