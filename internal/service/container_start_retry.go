package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"ai-education/backend/internal/docker"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/errdefs"
)

// NextPlan.md フェーズ2: 「コンテナ起動失敗時のリトライ・ユーザー通知フローを
// 設計(エラーメッセージの分かりやすい表示)」。
//
// containerStartMaxAttempts is how many times createAndStartContainer tries
// ContainerCreate+ContainerStart before giving up. Only errors that look
// like a transient Docker daemon hiccup (isRetryableDockerError) burn an
// automatic attempt - a persistent config problem (missing image, invalid
// parameters, ...) would just fail identically three times in a row, adding
// latency without helping anyone.
const (
	containerStartMaxAttempts    = 3
	containerStartRetryBaseDelay = 500 * time.Millisecond
)

// StartContainerError is returned by StartProgramContainer when the actual
// Docker launch (or the DB write recording it) fails, after any automatic
// retries are exhausted. It carries a student-facing Japanese message plus
// whether trying again has a realistic chance of succeeding, so the handler
// can hand the frontend enough to drive a proper retry/notification UI
// instead of a bare "failed" string.
type StartContainerError struct {
	Retryable bool
	Message   string
	Cause     error
}

func (e *StartContainerError) Error() string {
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *StartContainerError) Unwrap() error { return e.Cause }

// isRetryableDockerError reports whether err looks like a transient
// infrastructure hiccup (daemon momentarily unavailable/overloaded, a
// request that timed out) worth an automatic retry, as opposed to a
// persistent configuration problem (missing image, invalid parameters,
// permission) that will fail again immediately.
func isRetryableDockerError(err error) bool {
	if err == nil {
		return false
	}
	return errdefs.IsUnavailable(err) || errdefs.IsSystem(err) || errdefs.IsDeadline(err) || errors.Is(err, context.DeadlineExceeded)
}

// classifyStartError turns the final Docker error (after automatic retries,
// if any, were exhausted) into a StartContainerError with a message the
// frontend's error modal can show directly. Everything not positively
// identified as a persistent configuration problem defaults to
// Retryable:true - a manual "もう一度試す" click from the student costs
// nothing, so it's safer to offer it than to wrongly tell someone a
// recoverable failure is final.
func classifyStartError(err error) *StartContainerError {
	switch {
	case errdefs.IsNotFound(err):
		return &StartContainerError{Retryable: false, Message: "環境の準備に失敗しました(必要なイメージが見つかりません)。先生に連絡してください。", Cause: err}
	case errdefs.IsInvalidParameter(err):
		return &StartContainerError{Retryable: false, Message: "環境の設定に問題があり、起動できませんでした。先生に連絡してください。", Cause: err}
	case errdefs.IsForbidden(err):
		return &StartContainerError{Retryable: false, Message: "権限の問題で環境を起動できませんでした。先生に連絡してください。", Cause: err}
	case isRetryableDockerError(err):
		return &StartContainerError{Retryable: true, Message: "サーバーが混み合っているか、一時的に応答がありませんでした。もう一度お試しください。", Cause: err}
	default:
		return &StartContainerError{Retryable: true, Message: "コンテナの起動に失敗しました。もう一度お試しください。", Cause: err}
	}
}

// createAndStartContainer runs ContainerCreate+ContainerStart, automatically
// retrying (with exponential backoff) only for isRetryableDockerError
// failures. A container left behind by a failed Start is always removed
// before the next attempt so retries never leak a stray Docker container
// under the deterministic per-student name (which would otherwise make the
// next attempt's ContainerCreate fail with a name conflict too).
func createAndStartContainer(ctx context.Context, cli docker.ContainerAPI, cfg *container.Config, hostCfg *container.HostConfig, netCfg *network.NetworkingConfig, name string) (containerID string, err error) {
	var lastErr error

	for attempt := 1; attempt <= containerStartMaxAttempts; attempt++ {
		resp, createErr := cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
		if createErr != nil {
			lastErr = createErr
			if isRetryableDockerError(createErr) && attempt < containerStartMaxAttempts {
				if !sleepBeforeRetry(ctx, attempt) {
					break
				}
				continue
			}
			break
		}

		if startErr := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); startErr != nil {
			if rmErr := cli.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); rmErr != nil {
				log.Printf("[SANDBOX-START] 起動失敗後のコンテナ削除にも失敗しました(手動確認が必要な可能性があります) container=%s err=%v", resp.ID, rmErr)
			}
			lastErr = startErr
			if isRetryableDockerError(startErr) && attempt < containerStartMaxAttempts {
				if !sleepBeforeRetry(ctx, attempt) {
					break
				}
				continue
			}
			break
		}

		return resp.ID, nil
	}

	return "", classifyStartError(lastErr)
}

// sleepBeforeRetry waits an exponential backoff delay (500ms, 1s, ...)
// before the next attempt, returning false without waiting out the rest of
// the delay if ctx is cancelled/expired first - there's no point scheduling
// a retry the caller has already given up on.
func sleepBeforeRetry(ctx context.Context, attempt int) bool {
	delay := containerStartRetryBaseDelay * time.Duration(uint(1)<<uint(attempt-1))
	select {
	case <-ctx.Done():
		return false
	case <-time.After(delay):
		return true
	}
}
