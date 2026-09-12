package docker

import (
	"context"
	"net/http"
	"os"

	"github.com/docker/cli/cli/connhelper"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// ContainerAPI is the minimal subset of *client.Client that the program
// sandbox feature depends on. Handlers/services depend on this interface
// (not the concrete SDK client) so tests can substitute a hand-written mock.
type ContainerAPI interface {
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, platform *ocispec.Platform, containerName string) (container.CreateResponse, error)
	ContainerStart(ctx context.Context, containerID string, options container.StartOptions) error
	ContainerStop(ctx context.Context, containerID string, options container.StopOptions) error
	// ContainerKill は教師ダッシュボードの「緊急停止」(EmergencyStopContainer、
	// teacher_dashboard_service.go)専用 - ContainerStopと違いSIGTERM+猶予時間を
	// 待たず、即座にSIGKILL相当で強制終了する(生徒がコンテナ内で悪質な処理を
	// 実行している場合に、シャットダウン猶予すら与えず止めるため)。
	ContainerKill(ctx context.Context, containerID, signal string) error
	ContainerRemove(ctx context.Context, containerID string, options container.RemoveOptions) error
	ContainerList(ctx context.Context, options container.ListOptions) ([]container.Summary, error)
	// Exec* はディスククォータプールのスロット解放時に、次の割り当て相手へ前の
	// 生徒のファイルが引き継がれないよう workspace を一括削除するために使う
	// (program_service.go の wipeWorkspace 参照)。
	ContainerExecCreate(ctx context.Context, containerID string, options container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecStart(ctx context.Context, execID string, config container.ExecStartOptions) error
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
	// ContainerExecAttach/ContainerExecResize はシェル機能(NextPlan.md フェーズ3)の
	// 対話的exec+WebSocketストリーミングで使う。ExecAttachは標準入出力を生のまま
	// やり取りできるハイジャック済み接続を返す(ContainerExecStartは非対話用で
	// wipeWorkspace等のfire-and-forgetコマンドにのみ使う、上のコメント参照)。
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecResize(ctx context.Context, execID string, options container.ResizeOptions) error
	// ContainerStats はリソース使用状況ダッシュボード(metrics_service.go)の
	// コンテナ個別統計(CPU%/メモリ使用量)取得に使う。stream=falseで呼ぶと
	// Docker Engine自身が内部で短い間隔を空けた2サンプルをcpu_stats/
	// precpu_statsに詰めて返してくれるため、呼び出し側で独自にリトライ/
	// 間隔待ちをする必要が無い(`docker stats --no-stream`と同じ動作)。
	ContainerStats(ctx context.Context, containerID string, stream bool) (container.StatsResponseReader, error)
}

// NewClient builds a Docker Engine API client for whatever DOCKER_HOST points
// at. NextPlan.md §3.5: the connection target moves from a local unix socket
// (研究運用段階) to a remote ssh:// host (物理分離後) via env var alone -
// application code depends only on the ContainerAPI interface above, so
// nothing else needs to change when that switch happens.
func NewClient() (*client.Client, error) {
	host := os.Getenv("DOCKER_HOST")

	// ssh:// hosts need a dialer injected via connhelper; everything else
	// (unix://, tcp://, or unset) falls through to the normal FromEnv path.
	// GetConnectionHelper returns (nil, nil) for non-ssh schemes.
	if helper, err := connhelper.GetConnectionHelper(host); err != nil {
		return nil, err
	} else if helper != nil {
		httpClient := &http.Client{
			Transport: &http.Transport{
				DialContext: helper.Dialer,
			},
		}
		return client.NewClientWithOpts(
			client.WithHTTPClient(httpClient),
			client.WithHost(helper.Host),
			client.WithAPIVersionNegotiation(),
		)
	}

	return client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
}
