package dockerengine

import (
	"context"
	"errors"
	"io"
	"iter"
	"strings"

	"github.com/containerd/errdefs/pkg/errhttp"
	"github.com/moby/moby/api/types/jsonstream"
	"github.com/moby/moby/client/pkg/jsonmessage"
)

type imageTransferStream interface {
	JSONMessages(context.Context) iter.Seq2[jsonstream.Message, error]
	Wait(context.Context) error
}

func imageTransferOutput(outputs []io.Writer, index int) io.Writer {
	if index < len(outputs) {
		return outputs[index]
	}
	return nil
}

// 使用 Docker SDK 解码和格式化真实传输流；日志按行保留分层进度，不依赖终端光标覆盖。
func waitImageTransfer(ctx context.Context, stream imageTransferStream, output io.Writer) error {
	if output == nil {
		return stream.Wait(ctx)
	}
	var responseError *jsonstream.Error
	messages := func(yield func(jsonstream.Message, error) bool) {
		for message, err := range stream.JSONMessages(ctx) {
			if message.Error != nil {
				responseError = message.Error
			}
			if message.Progress != nil {
				message.Status = strings.TrimSpace(message.Status + " " + jsonmessage.RenderTUIProgress(*message.Progress, 100))
				message.Progress = nil
			}
			if !yield(message, err) {
				return
			}
		}
	}
	err := jsonmessage.DisplayJSONMessages(messages, output, 0, false, nil)
	if responseError != nil {
		return &imageTransferResponseError{cause: responseError}
	}
	return err
}

// 保留 SDK Wait 的 HTTP 错误分类及原始错误文本，调用方的重试判断不因日志展示改变。
type imageTransferResponseError struct{ cause *jsonstream.Error }

func (e *imageTransferResponseError) Error() string { return e.cause.Error() }
func (e *imageTransferResponseError) Unwrap() error { return e.cause }
func (e *imageTransferResponseError) Is(target error) bool {
	return errors.Is(errhttp.ToNative(e.cause.Code), target)
}
