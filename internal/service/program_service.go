package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-education/backend/internal/db"
	"ai-education/backend/internal/docker"
	"ai-education/backend/internal/docker/seccomp"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	sandboxImage            = "sandbox-base:latest"
	sandboxNetworkName      = "sandbox-net"
	sandboxRuntime          = "runsc"
	defaultSandboxCPU       = 0.5        // NanoCPUs = defaultSandboxCPU * 1e9
	defaultSandboxMemMB     = int64(512) // MB
	defaultSandboxPidsLimit = int64(256)

	// アイドルタイムアウト(NextPlan.md フェーズ2「アイドルタイムアウトによる
	// 自動停止ロジック(公開中のコンテナは対象外)」)。CPU/メモリ同様、具体的な
	// 数値は実運用を踏まえて確定する未確定事項のため.envで調整可能にしておく
	// (idle_sandbox_service.goのスイーパーが参照する)。
	defaultSandboxIdleTimeoutMinutes = 30

	// ディスククォータ(NextPlan.md §6, 事前プロビジョニング済みループバックプール方式)。
	// 実際のサイズ上限はホスト側 scripts/setup-sandbox-pool.sh が作成する各スロット
	// (ループバックマウント済みディレクトリ)自体が持つ - Goプロセス自身はmount/losetup
	// を一切行わず、スロットのパス文字列をDocker Engine APIのBindsへ渡すだけ
	// (backendコンテナに特権を与えないための設計、調査結果を参照)。
	defaultSandboxPoolBaseDir = "/var/sandboxes"
	defaultSandboxPoolSize    = 10
	// sandboxWorkspacePath はコンテナ内でプールスロットをbind mountする先。
	// sandbox-base の Dockerfile は root 実行・WORKDIR=/root のため、その配下に置く。
	sandboxWorkspacePath = "/root/workspace"
	// poolSlotAdvisoryLockKey は空きスロット探索+DB行作成を直列化するための
	// Postgresアドバイザリロックキー(固定値、他機能と衝突しないよう適当な大きい数)。
	// 複数リクエストが同時に同じ空きスロットを選んでしまう競合を防ぐ。
	poolSlotAdvisoryLockKey = 810234501

	execWipeTimeout      = 10 * time.Second
	execWipePollInterval = 100 * time.Millisecond
)

// ErrSandboxNotFound is returned by Resume/Stop/Delete when no sandbox
// container exists for the given (user, course) pair.
var ErrSandboxNotFound = errors.New("サンドボックスが見つかりません")

// ErrSandboxAlreadyExists is returned by StartProgramContainer (create) when
// a sandbox already exists for this (user, course) pair - 同じユーザーが
// コンテナ作成を押した場合、既存コンテナを削除するよう促すエラーを返す。
var ErrSandboxAlreadyExists = errors.New("既にコンテナが存在します")

// ErrSandboxPoolExhausted is returned by StartProgramContainer when every
// disk-quota pool slot is currently assigned to another (active) sandbox.
// 呼び出し側(ハンドラー)は「しばらく待ってから再度お試しください」等の
// ユーザー向けメッセージに変換する想定。
var ErrSandboxPoolExhausted = errors.New("ディスク容量プールに空きがありません")

// ErrSandboxPublished is returned by StopProgramContainer/DeleteProgramContainer
// when the sandbox is currently published(単一サブドメインパスベース公開機能、
// NextPlan.md フェーズ7) - 公開URL(https://preview.a-kiis.com/{slug}/)が
// まだ有効な間にコンテナを停止/削除してしまうと、公開中と表示されたまま
// 実体が無い(または応答しない)ページになってしまう。公開停止は生徒自身が
// IDE内のWebプレビューパネル/公開タブから行う操作であり、停止・削除操作の
// 副作用として暗黙に行うべきではないため、先に明示的に公開を止めさせる。
// エラー文言自体は呼び出し元(ハンドラー)が停止/削除それぞれの状況に応じて
// 個別に組み立てる - このエラー値自体は判定用のセンチネルとして扱う。
var ErrSandboxPublished = errors.New("公開中のため実行できません。先にIDE画面で公開を停止してください。")

// ErrSandboxLocked is returned by ResumeProgramContainer when a teacher has
// locked the sandbox against being resumed(教師ダッシュボードの緊急停止/
// 再開ロック、安全対策 - ProgramSandbox.Lockedのコメント参照)。生徒が
// 悪質な操作をした場合に、教師が「緊急停止」(docker kill)した上でロックを
// 掛け、本人が勝手に再開できないようにするためのもの。解除も教師のみ行える。
var ErrSandboxLocked = errors.New("このコンテナはロックされているため再開できません。担当の先生に確認してください。")

const defaultSandboxName = "無題の環境"

// sandboxOpLocks serializes Start/Resume/Stop/Delete/EmergencyStop for the
// same(userID, courseID)sandbox against each other - Start/Stop/Resume/
// Delete/EmergencyStopContainerはどれも「Docker側の現在の状態をfindSandbox
// 等で読む→その数手先でDocker操作+DB更新を行う」という形で、読んでから
// 実際に手を動かすまでの間に複数回のネットワークI/Oを挟む。この間に別の
// goroutine(生徒本人の別タブ、教師ダッシュボード、アイドルタイムアウト
// スイーパー、のいずれか2つ以上)が同じサンドボックスへ同時に操作をかけると、
// 例えば「再開処理が起動を終える直前に、別経路からの停止処理が同じ
// コンテナを掴んで止めてしまい、再開自体はDB上"running"と書き込むのに
// 実体はすぐ後に停止済み」という食い違いが起き得る - このロックは各操作の
// 「読んで→動かす」全体を1つの臨界区間として直列化し、そのクラスの競合を
// 構造的に起こり得なくする。単一プロセス構成(NextPlan.md §3.5)を前提とした
// プロセス内メモリのみのロック(ticketStore等と同じ設計) - 対象は生徒×クラス
// の組ごとに高々1つなので、ticketStoreと違って有効期限による掃除は行わず、
// 一度作られたエントリはプロセス生存中ずっと保持する(問題になるほどの
// 数には現実的にならないため)。
var (
	sandboxOpLocksMu sync.Mutex
	sandboxOpLocks   = map[string]*sync.Mutex{}
)

// lockSandboxOp acquires the per-(userID, courseID)lock and returns a
// function that releases it - callers should acquire it as the very first
// thing in the function and `defer unlock()`immediately, before any
// Docker/DB call.
func lockSandboxOp(userID uuid.UUID, courseID uint) (unlock func()) {
	key := userID.String() + ":" + strconv.FormatUint(uint64(courseID), 10)

	sandboxOpLocksMu.Lock()
	mu, ok := sandboxOpLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		sandboxOpLocks[key] = mu
	}
	sandboxOpLocksMu.Unlock()

	mu.Lock()
	return mu.Unlock
}

// containerName returns the deterministic container name for a user's
// per-course sandbox. 画像分類システムと同様、1ユーザーにつき1クラス1環境
// (複数クラスに所属していれば、クラスごとに別々のサンドボックスを持てる)。
func containerName(userID uuid.UUID, courseID uint) string {
	return fmt.Sprintf("program-%s-%d", userID, courseID)
}

func sandboxLabels(userID uuid.UUID, courseID uint) map[string]string {
	return map[string]string{
		"app":       "program-sandbox",
		"user_id":   userID.String(),
		"course_id": strconv.FormatUint(uint64(courseID), 10),
	}
}

// findSandbox looks up the (at most one) sandbox container for this
// (user, course) pair by label, since there is no DB table tracking the
// association - Docker's own labels are the source of truth.
func findSandbox(ctx context.Context, cli docker.ContainerAPI, userID uuid.UUID, courseID uint) (*container.Summary, error) {
	f := filters.NewArgs(
		filters.Arg("label", "app=program-sandbox"),
		filters.Arg("label", "user_id="+userID.String()),
		filters.Arg("label", "course_id="+strconv.FormatUint(uint64(courseID), 10)),
	)
	list, err := cli.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// resourceLimits reads SANDBOX_CPU_LIMIT (e.g. "0.5" = half a core),
// SANDBOX_MEMORY_LIMIT_MB and SANDBOX_PIDS_LIMIT from the environment,
// falling back to conservative defaults (0.5 CPU / 512MB / 256 PIDs) when
// unset or invalid. Values are intentionally left tunable via .env rather
// than hard-coded (NextPlan.md §16: 具体的な数値はラズパイのRAM実測を踏まえて
// 確定する未確定事項)。PidsLimit is set explicitly (not left at the daemon's
// own default) since a coding sandbox legitimately forks many processes
// (shell, git, language servers, ...) and an unexpectedly low daemon default
// would silently break it - cgroups v2 pids accounting should be a
// deliberate, generous limit, not an implicit one.
func resourceLimits() (nanoCPUs int64, memoryBytes int64, pidsLimit int64) {
	nanoCPUs = int64(defaultSandboxCPU * 1e9)
	memoryBytes = defaultSandboxMemMB * 1024 * 1024
	pidsLimit = defaultSandboxPidsLimit

	if v := os.Getenv("SANDBOX_CPU_LIMIT"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			nanoCPUs = int64(f * 1e9)
		}
	}
	if v := os.Getenv("SANDBOX_MEMORY_LIMIT_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			memoryBytes = n * 1024 * 1024
		}
	}
	if v := os.Getenv("SANDBOX_PIDS_LIMIT"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			pidsLimit = n
		}
	}
	return nanoCPUs, memoryBytes, pidsLimit
}

// idleTimeout reads SANDBOX_IDLE_TIMEOUT_MINUTES from the environment,
// falling back to defaultSandboxIdleTimeoutMinutes when unset or invalid.
// A running, non-published sandbox whose LastActiveAt is older than this
// duration is stopped by the background sweeper (idle_sandbox_service.go).
func idleTimeout() time.Duration {
	minutes := defaultSandboxIdleTimeoutMinutes
	if v := os.Getenv("SANDBOX_IDLE_TIMEOUT_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			minutes = n
		}
	}
	return time.Duration(minutes) * time.Minute
}

// poolConfig reads SANDBOX_POOL_BASE_DIR and SANDBOX_POOL_SIZE from the
// environment, falling back to defaults. baseDir is a HOST path (dockerd
// runs directly on the Raspberry Pi host, not inside the backend's own
// container) - the backend process never reads/writes under baseDir itself,
// it only passes slot path strings to the Docker Engine API's Binds option.
func poolConfig() (baseDir string, size int) {
	baseDir = defaultSandboxPoolBaseDir
	if v := os.Getenv("SANDBOX_POOL_BASE_DIR"); v != "" {
		baseDir = v
	}
	size = defaultSandboxPoolSize
	if v := os.Getenv("SANDBOX_POOL_SIZE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			size = n
		}
	}
	return baseDir, size
}

func poolSlotName(i int) string {
	return fmt.Sprintf("pool-%02d", i)
}

// allocatePoolSlot picks a free disk-quota pool slot ("pool-01", "pool-02",
// ...) and returns its name. Must be called within the same DB transaction
// that goes on to create the ProgramSandbox row, and that transaction must
// commit promptly - the advisory lock is held (pg_advisory_xact_lock, scoped
// to the transaction) until tx commits/rolls back, serializing concurrent
// allocations so two simultaneous requests can't both grab the same slot.
// Returns ErrSandboxPoolExhausted if every slot up to poolSize is in use.
func allocatePoolSlot(tx *gorm.DB) (string, error) {
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", poolSlotAdvisoryLockKey).Error; err != nil {
		return "", err
	}

	used, err := db.ListUsedPoolSlots(tx)
	if err != nil {
		return "", err
	}

	_, poolSize := poolConfig()
	for i := 1; i <= poolSize; i++ {
		slot := poolSlotName(i)
		if !used[slot] {
			return slot, nil
		}
	}
	return "", ErrSandboxPoolExhausted
}

// StartProgramContainer creates AND starts a brand-new sandbox for this
// (user, course) pair. It does NOT resume an existing one - if a sandbox
// (running or stopped) already exists, it returns ErrSandboxAlreadyExists so
// the caller is told to delete the existing one first (use
// ResumeProgramContainer to restart an existing stopped sandbox instead).
// displayName is the student-chosen label shown on their card (falls back to
// defaultSandboxName if blank); it is unrelated to the Docker-level
// container name, which stays server-generated for uniqueness.
func StartProgramContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, userID uuid.UUID, courseID uint, displayName string) (containerID string, err error) {
	defer lockSandboxOp(userID, courseID)()

	existing, err := findSandbox(ctx, cli, userID, courseID)
	if err != nil {
		return "", err
	}
	if existing != nil {
		return "", ErrSandboxAlreadyExists
	}

	if displayName == "" {
		displayName = defaultSandboxName
	}
	dockerName := containerName(userID, courseID)

	nanoCPUs, memoryBytes, pidsLimit := resourceLimits()

	poolBaseDir, _ := poolConfig()
	poolSlot, err := allocatePoolSlot(tx)
	if err != nil {
		return "", err
	}
	workspaceBind := fmt.Sprintf("%s/%s:%s", poolBaseDir, poolSlot, sandboxWorkspacePath)

	// 起動(ContainerCreate+ContainerStart)は一時的なDockerデーモンの不調
	// (混雑・タイムアウト等)であれば自動的にリトライする。ここで返る
	// エラーは、リトライしても無駄な種類も含め既に*StartContainerErrorへ
	// 分類済み(NextPlan.md フェーズ2「コンテナ起動失敗時のリトライ・
	// ユーザー通知フローを設計」、internal/service/container_start_retry.go)。
	containerID, err = createAndStartContainer(
		ctx,
		cli,
		&container.Config{
			Image:  sandboxImage,
			Labels: sandboxLabels(userID, courseID),
		},
		&container.HostConfig{
			Runtime: sandboxRuntime,
			CapDrop: []string{"ALL"},
			// 危険なシステムコール(ptrace, mount等)をseccompプロファイルで
			// 明示的に遮断する(NextPlan.md フェーズ2、internal/docker/seccomp参照)。
			// cap-drop=ALL・gVisor(runsc)に続く3層目の多層防御。
			SecurityOpt: seccomp.SecurityOpt(),
			Resources: container.Resources{
				NanoCPUs:  nanoCPUs,
				Memory:    memoryBytes,
				PidsLimit: &pidsLimit,
			},
			// ディスククォータ: poolSlotはホスト側で事前にループバックマウント
			// 済みの固定サイズディレクトリ(scripts/setup-sandbox-pool.sh)。
			// dockerdがホスト上で直接動くため、このパスはbackendコンテナ自身の
			// ファイルシステムではなくホストのそれとして解決される。
			Binds:       []string{workspaceBind},
			NetworkMode: container.NetworkMode(sandboxNetworkName),
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				sandboxNetworkName: {},
			},
		},
		dockerName,
	)
	if err != nil {
		return "", err
	}
	initWorkspaceGitRepo(ctx, cli, containerID)

	// gVisor(runsc)はコンテナのメモリ管理をSentryプロセス自身が担うため、
	// cgroupのメモリ上限(resourceLimits)に達すると、個別プロセスだけでなく
	// コンテナ全体がまとめてOOM Killされることがある(ホストの空きメモリが
	// 少ない環境、特にラズパイで実際に観測されている - NextPlan.md §16の
	// 「ラズパイのRAM実測を踏まえて確定する未確定事項」参照、
	// initWorkspaceGitRepoのexit=137ログとセットで起きる)。これを見逃すと、
	// DBには"running"として記録したのに実体はすでに終了しているという
	// 食い違いが生じ、直後のファイル一覧/シェル接続が説明の付かない409
	// (ErrSandboxNotRunning)で失敗し続けることになる - ここで一度だけ生死を
	// 確認し、既に終了していれば起動失敗として素直に報告する。
	if running, checkErr := containerIsRunning(ctx, cli, containerID); checkErr != nil {
		log.Printf("[SANDBOX-START] 起動直後の状態確認に失敗しました container=%s err=%v", containerID, checkErr)
	} else if !running {
		log.Printf("[SANDBOX-START] コンテナが起動直後に終了しました(メモリ不足によるOOM Killの可能性があります) container=%s", containerID)
		if rmErr := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[SANDBOX-START] 起動直後に終了したコンテナの削除にも失敗しました(手動確認が必要な可能性があります) container=%s err=%v", containerID, rmErr)
		}
		return "", &StartContainerError{
			Retryable: true,
			Message:   "環境の起動に失敗しました(メモリ不足の可能性があります)。しばらくしてからもう一度お試しください。",
			Cause:     errors.New("container exited immediately after start"),
		}
	}

	if err := db.CreateProgramSandbox(tx, userID, courseID, containerID, dockerName, displayName, "running", poolSlot); err != nil {
		// Dockerコンテナは既に起動済みなのに記録だけ失敗した状態を放置しない。
		// 放置すると: 次回findSandbox()がこの孤立コンテナをDocker側から見つけて
		// 「既に存在します」を返す一方、program_sandboxes行が無いためプール
		// スロットはDB上ずっと空き扱いのまま(=二重割り当てのリスク)になる。
		if rmErr := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); rmErr != nil {
			log.Printf("[SANDBOX-START] DB記録失敗後のコンテナ削除にも失敗しました(手動確認が必要な可能性があります) container=%s err=%v", containerID, rmErr)
		}
		return "", &StartContainerError{Retryable: true, Message: "コンテナの記録に失敗しました。もう一度お試しください。", Cause: err}
	}
	return containerID, nil
}

// containerIsRunning re-checks a just-started container's live state via
// Docker directly (the DB row hasn't been written yet at this point in
// StartProgramContainer) - looks it up by ID via ContainerList rather than
// adding a dedicated ContainerInspect method to ContainerAPI, matching
// findSandbox's existing filter-based lookup style.
func containerIsRunning(ctx context.Context, cli docker.ContainerAPI, containerID string) (bool, error) {
	list, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("id", containerID)),
	})
	if err != nil {
		return false, err
	}
	if len(list) == 0 {
		return false, nil
	}
	return list[0].State == "running", nil
}

// initWorkspaceGitRepo runs `git init` inside the freshly created sandbox's
// workspace so students can start committing right away (NextPlan.md
// フェーズ5「ベースイメージへのgit標準搭載、コンテナ生成時のgit init自動化」。
// identity(user.name/user.email)とinit.defaultBranchはsandbox-baseの
// Dockerfileで既定値をグローバル設定済み)。
// git initは既存の.gitがあっても履歴を消さず安全に再初期化するだけなので
// 事前チェックは不要。失敗してもサンドボックス作成自体は失敗させない
// (gitが無くてもシェル/エディター機能自体は使えるため、生徒は後から自分で
// `git init`し直せる) - ログだけ残す。
func initWorkspaceGitRepo(ctx context.Context, cli docker.ContainerAPI, containerID string) {
	_, stderr, exitCode, err := runContainerCommand(ctx, cli, containerID, []string{"git", "init", sandboxWorkspacePath})
	if err != nil {
		log.Printf("[SANDBOX-START] git initの実行に失敗しました container=%s err=%v", containerID, err)
		return
	}
	if exitCode != 0 {
		hint := ""
		if exitCode == 137 {
			// 137 = 128+9 (SIGKILL)。git init自体が重い処理ではないため、
			// これが起きる時はほぼ確実にcgroupのメモリ上限超過によるOOM Kill
			// (ホスト側のkernelもしくはgVisorのSentryによるもの)であり、
			// git initプロセス単体どころかコンテナ全体が道連れで終了して
			// いることが多い(StartProgramContainerのcontainerIsRunningに
			// よる直後の生死確認を参照)。
			hint = "(exit=137はSIGKILL - メモリ不足によるOOM Killの可能性が高く、コンテナ全体が終了している場合もあります。SANDBOX_MEMORY_LIMIT_MBやホストの空きメモリを確認してください)"
		}
		log.Printf("[SANDBOX-START] git initが失敗しました(exit=%d)%s container=%s stderr=%s", exitCode, hint, containerID, strings.TrimSpace(stderr))
	}
}

// ResumeProgramContainer restarts an EXISTING stopped sandbox for this
// (user, course) pair. Idempotent: calling it while already running just
// returns success. Returns ErrSandboxNotFound if no sandbox exists yet
// (use StartProgramContainer to create one), ErrSandboxLocked if a teacher
// has locked it against being resumed by the student themselves
// (rejectIfLocked - 教師本人の再開はResumeContainerAsTeacherが別途、この
// チェックを経由せずに行える、ErrSandboxLockedのコメント参照)。
func ResumeProgramContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, userID uuid.UUID, courseID uint) (containerID string, alreadyRunning bool, err error) {
	if err := rejectIfLocked(tx, userID, courseID); err != nil {
		return "", false, err
	}
	return resumeProgramContainer(ctx, cli, tx, userID, courseID)
}

// resumeProgramContainer is the actual Docker/DB mechanics shared by
// ResumeProgramContainer(生徒本人、ロック確認あり)and
// ResumeContainerAsTeacher(教師、ロック確認なし - teacher_dashboard_service.go)。
func resumeProgramContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, userID uuid.UUID, courseID uint) (containerID string, alreadyRunning bool, err error) {
	defer lockSandboxOp(userID, courseID)()

	existing, err := findSandbox(ctx, cli, userID, courseID)
	if err != nil {
		return "", false, err
	}
	if existing == nil {
		return "", false, ErrSandboxNotFound
	}

	if existing.State == "running" {
		if err := db.UpdateProgramSandboxStatus(tx, userID, courseID, existing.ID, "running"); err != nil {
			return "", false, err
		}
		return existing.ID, true, nil
	}

	if err := cli.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
		return "", false, err
	}
	if err := db.UpdateProgramSandboxStatus(tx, userID, courseID, existing.ID, "running"); err != nil {
		return "", false, err
	}
	return existing.ID, false, nil
}

// rejectIfPublished returns ErrSandboxPublished if (userID, courseID)'s
// sandbox is currently published - shared by StopProgramContainer/
// DeleteProgramContainer(ErrSandboxPublishedのコメント参照)。Dockerを一切
// 操作する前に、この安価なDB確認だけで弾く。
func rejectIfPublished(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	sandboxRecord, err := db.FindProgramSandbox(tx, userID, courseID)
	if err != nil {
		return err
	}
	if sandboxRecord != nil && sandboxRecord.Published {
		return ErrSandboxPublished
	}
	return nil
}

// rejectIfLocked returns ErrSandboxLocked if (userID, courseID)'s sandbox is
// currently locked by a teacher(緊急停止/再開ロック、ErrSandboxLockedの
// コメント参照) - ResumeProgramContainerがDockerを操作する前にここで弾く。
func rejectIfLocked(tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	sandboxRecord, err := db.FindProgramSandbox(tx, userID, courseID)
	if err != nil {
		return err
	}
	if sandboxRecord != nil && sandboxRecord.Locked {
		return ErrSandboxLocked
	}
	return nil
}

// StopProgramContainer stops the sandbox for this (user, course) pair.
// Returns ErrSandboxNotFound if none exists, ErrSandboxPublished if it's
// currently published(公開中に停止してしまうと、公開URLが応答しない
// ページになってしまうため - 先にIDEで公開を停止させる)。
func StopProgramContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	defer lockSandboxOp(userID, courseID)()

	if err := rejectIfPublished(tx, userID, courseID); err != nil {
		return err
	}

	existing, err := findSandbox(ctx, cli, userID, courseID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSandboxNotFound
	}
	if err := cli.ContainerStop(ctx, existing.ID, container.StopOptions{}); err != nil {
		return err
	}
	return db.UpdateProgramSandboxStatus(tx, userID, courseID, existing.ID, "stopped")
}

// DeleteProgramContainer force-removes the sandbox for this (user, course)
// pair (stopping it first if running, in a single Docker API call). Returns
// ErrSandboxNotFound if none exists.
//
// Before removal it wipes the container's disk-quota pool slot workspace
// (/root/workspace) so the next sandbox assigned that slot (by a different
// student, once this one's ProgramSandbox row is soft-deleted and the slot
// becomes free again in allocatePoolSlot's scan) never inherits this
// student's files. Wiping happens via `exec` inside the container itself,
// which requires the container to be running - if it was stopped, this
// briefly starts it just long enough to run the cleanup command.
func DeleteProgramContainer(ctx context.Context, cli docker.ContainerAPI, tx *gorm.DB, userID uuid.UUID, courseID uint) error {
	defer lockSandboxOp(userID, courseID)()

	if err := rejectIfPublished(tx, userID, courseID); err != nil {
		return err
	}

	existing, err := findSandbox(ctx, cli, userID, courseID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrSandboxNotFound
	}

	if existing.State != "running" {
		if err := cli.ContainerStart(ctx, existing.ID, container.StartOptions{}); err != nil {
			log.Printf("サンドボックス削除: workspace初期化のための起動に失敗しました(削除自体は続行します) container=%s err=%v", existing.ID, err)
		}
	}
	if err := wipeWorkspace(ctx, cli, existing.ID); err != nil {
		// スロットの再利用時に前の生徒のファイルが残るリスクがあるため、
		// 削除自体は止めずに大きく警告を残す(手動での調査・クリーンアップを促す)。
		log.Printf("[要確認] サンドボックス削除: workspaceの初期化に失敗しました。プールスロットに前のデータが残っている可能性があります container=%s err=%v", existing.ID, err)
	}

	if err := cli.ContainerRemove(ctx, existing.ID, container.RemoveOptions{Force: true}); err != nil {
		return err
	}
	return db.DeleteProgramSandbox(tx, userID, courseID)
}

// wipeWorkspace removes every file under sandboxWorkspacePath inside the
// (running) container via `exec`, clearing its disk-quota pool slot for
// reuse. Uses ContainerExecCreate/Start + a short ExecInspect poll loop
// (execWipeTimeout) since the Docker SDK's non-attached ExecStart doesn't
// block until the command finishes.
func wipeWorkspace(ctx context.Context, cli docker.ContainerAPI, containerID string) error {
	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd: []string{"sh", "-c", "rm -rf " + sandboxWorkspacePath + "/* " + sandboxWorkspacePath + "/.[!.]* " + sandboxWorkspacePath + "/..?* 2>/dev/null; true"},
	})
	if err != nil {
		return err
	}
	if err := cli.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return err
	}

	deadline := time.Now().Add(execWipeTimeout)
	for {
		inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return err
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return fmt.Errorf("workspace初期化コマンドが失敗しました(exit=%d)", inspect.ExitCode)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("workspace初期化コマンドがタイムアウトしました(%s)", execWipeTimeout)
		}
		time.Sleep(execWipePollInterval)
	}
}
