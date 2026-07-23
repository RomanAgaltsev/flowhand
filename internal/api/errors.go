package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/RomanAgaltsev/flowhand/internal/api/oas"
	"github.com/go-faster/jx"
	"github.com/ogen-go/ogen/ogenerrors"
)

// ErrorHandler renders ogen's pre-handler failures (body decode, parameter
// decode, request validation) as the spec's Error envelope instead of the
// default text/plain, so a client generated from this spec can parse every
// error the spec declares.
func ErrorHandler(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
	code := http.StatusInternalServerError
	var (
		decReq   *ogenerrors.DecodeRequestError
		decParam *ogenerrors.DecodeParamsError
	)
	if errors.As(err, &decReq) || errors.As(err, &decParam) {
		code = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	e := &oas.Error{
		Code:    strconv.Itoa(code),
		Message: http.StatusText(code),
	}
	enc := new(jx.Encoder)
	e.Encode(enc)
	_, _ = enc.WriteTo(w) //nolint:errcheck // best-effort write to an already-failing response
}
