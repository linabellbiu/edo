package repository

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// IsTransientGitError 只分类可恢复的传输错误，不按文本猜测认证、配置或协议错误。
func IsTransientGitError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var permanent *plumbing.PermanentError
	if errors.As(err, &permanent) {
		return false
	}
	// go-git v5 的 UnexpectedError 没有 Unwrap，需要显式保留其原始错误语义。
	var unexpected *plumbing.UnexpectedError
	if errors.As(err, &unexpected) {
		return IsTransientGitError(unexpected.Err)
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var responseError *githttp.Err
	if errors.As(err, &responseError) && responseError.Response != nil {
		switch responseError.Response.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
			http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		}
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func gitRetryAfter(err error) time.Duration {
	var unexpected *plumbing.UnexpectedError
	if errors.As(err, &unexpected) {
		return gitRetryAfter(unexpected.Err)
	}
	var responseError *githttp.Err
	if !errors.As(err, &responseError) || responseError.Response == nil {
		return 0
	}
	value := responseError.Response.Header.Get("Retry-After")
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil {
		return time.Until(until)
	}
	return 0
}
