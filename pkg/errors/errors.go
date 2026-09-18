package errors

import (
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Code 是与传输协议无关的业务错误码。
type Code int

const (
	CodeUnknown Code = iota
	CodeNotFound
	CodeInvalidArgument
	CodeAlreadyExists
	CodeInternal
	CodeUnauthorized
	CodeForbidden
	CodeFailedPrecondition
)

type AppError struct {
	Code    Code
	Message string
	Err     error
}

func (e *AppError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

func (e *AppError) Unwrap() error {
	return e.Err
}

func New(code Code, message string, cause error) *AppError {
	return &AppError{Code: code, Message: message, Err: cause}
}

func NotFound(message string, cause error) *AppError {
	return New(CodeNotFound, message, cause)
}

func InvalidArgument(message string, cause error) *AppError {
	return New(CodeInvalidArgument, message, cause)
}

func AlreadyExists(message string, cause error) *AppError {
	return New(CodeAlreadyExists, message, cause)
}

func Internal(message string, cause error) *AppError {
	return New(CodeInternal, message, cause)
}

func Unauthorized(message string, cause error) *AppError {
	return New(CodeUnauthorized, message, cause)
}

func Forbidden(message string, cause error) *AppError {
	return New(CodeForbidden, message, cause)
}

func FailedPrecondition(message string, cause error) *AppError {
	return New(CodeFailedPrecondition, message, cause)
}

func ToGRPCStatus(err error) error {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if !errors.As(err, &appErr) {
		return status.Error(codes.Internal, "internal error")
	}

	switch appErr.Code {
	case CodeNotFound:
		return status.Error(codes.NotFound, appErr.Message)
	case CodeInvalidArgument:
		return status.Error(codes.InvalidArgument, appErr.Message)
	case CodeAlreadyExists:
		return status.Error(codes.AlreadyExists, appErr.Message)
	case CodeUnauthorized:
		return status.Error(codes.Unauthenticated, appErr.Message)
	case CodeForbidden:
		return status.Error(codes.PermissionDenied, appErr.Message)
	case CodeFailedPrecondition:
		return status.Error(codes.FailedPrecondition, appErr.Message)
	default:
		// 不把 appErr.Err（可能包含 SQL 语句/驱动报错）透传给客户端，
		// 完整信息已经在 service 层落日志，这里只暴露安全的提示文案。
		return status.Error(codes.Internal, "internal error")
	}
}

func ToHTTPStatus(err error) (int, string) {
	st, ok := status.FromError(err)
	if !ok {
		return http.StatusInternalServerError, "internal error"
	}

	switch st.Code() {
	case codes.NotFound:
		return http.StatusNotFound, st.Message()
	case codes.InvalidArgument:
		return http.StatusBadRequest, st.Message()
	case codes.AlreadyExists:
		return http.StatusConflict, st.Message()
	case codes.Unauthenticated:
		return http.StatusUnauthorized, st.Message()
	case codes.PermissionDenied:
		return http.StatusForbidden, st.Message()
	case codes.FailedPrecondition:
		// 库存不足这类"状态冲突"语义，HTTP 世界的对应物是 409。
		return http.StatusConflict, st.Message()
	case codes.DeadlineExceeded:
		// 网关对下游的调用超时（repository 层 defaultCallTimeout）——
		// 504 是"我作为网关/代理，上游没及时回我"的标准语义。
		return http.StatusGatewayTimeout, "downstream service timeout"
	case codes.Unavailable:
		// 下游连不上，或熔断器打开的快速失败（breaker.go 把 ErrOpenState
		// 翻译成 Unavailable）——503 语义是"暂时不可用，可稍后重试"。
		return http.StatusServiceUnavailable, st.Message()
	case codes.OK:
		return http.StatusOK, ""
	default:
		return http.StatusInternalServerError, "internal error"
	}
}
