<!-- template_id: plan; template_version: 1.3.0 -->
# 项目文件读写的符号链接边界 实施计划

> 状态：Draft 0.1 / 已批准（2026-11-10，用户：「计划批准」）
> issue：gofer-i72n

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude（gofer 监督会话） | 初稿 |

> 仅语义变化递增版本；纯 identity/provenance/元数据纠正沿用原版本，并在 Git/进度记录中留痕。

思考模式：RIGOROUS。核心目标：`/file` 预览不能经项目内链接读到白名单外的文件；传输与 job 附带上传的读写不能经链接到达项目根外。Scope freeze：只实施设计 Draft 0.1（已批准，Q1–Q3 按推荐项）与下文“对设计的细化”。Review budget：一轮合并轴评审，有阻塞项才继续。停止条件：评审无 `CORE_BLOCKING`，停在计划 Gate。

## 目标与完成定义

- `ReadKeyFile`：解析后的真实文件名不在白名单 → 404；响应 `name` 取请求名。
- server 本机传输 get / put、worker 传输 get / put、job 附带上传（hub 与 worker 两侧）、本机附件目录，读写全部经 `os.Root`；目标或中间目录链到根外时失败，根外不产生任何文件。
- 根内相对链接仍可用；行为、临时文件命名、校验顺序、错误语义其余不变。
- 每个入口有出根反例与根内正例；反向验证全部按预期失败；gofer-verify 四路通过；合并、关 issue。

## 范围、排除项与授权

范围：设计“范围”所列四类入口；`internal/project` 新增 `os.Root` 辅助；`job.XferBridge.FetchUpload` 签名调整（见 R2）；`skills/gofer-usage/references/` 中传输说明一句；CHANGELOG「未发布」。

排除：设计“非目标”全部（job `cwd` / `read_dirs` / 会话恢复 / 结果目录解析）；job 产物收集（worker 把 job 目录里的文件上传给 hub：读者是 job 自己的 agent，本来就能读写这些位置）；`xfer_handler.go:628` 的早期校验（保持字面 `SafeJoin`）。

授权：本计划批准后，按功能点本地提交、建 worktree、派子 agent、跑主机 job 与临时 server；不 push、不发版（Q3=A）。

## 输入与批准证据

- 设计：`docs/design/2026-11-10-project-file-symlink-boundary-design.md` Draft 0.1。用户 2026-11-10（web）：「按推荐」——Q1=A、Q2=A、Q3=A。
- Windows 实测：`tmp/i72n-probe/`（工作区，不入库），结论见设计“已确认事实”。

## Capability Discovery

### Capability decisions

| capability_id | required_capability | searched_candidates | direct_reuse | thin_adapter_or_owner_extension | decision | proven_gap | duplication_and_lifecycle_risk |
|---|---|---|---|---|---|---|---|
| CAP-01 | 不出根的读写 | `project.SafeJoin`、`project.SafeJoinReal`、标准库 `os.Root` | 是 | `internal/project/root.go` 薄封装 `os.Root` | OWNER_EXTENSION | `SafeJoinReal` 有检查与打开之间的竞态，且不覆盖写方向 | 现有三份「MkdirAll + 临时文件 + Rename」（`xfer/local.go`、`worker/xfer.go`、`core/job_xfer.go`）收敛为一处 |
| CAP-02 | 真实目标复核 | `ReadDoc` 的 `docRealPathAllowed` | 是 | `ReadKeyFile` 按同一写法加复核 | OWNER_EXTENSION | none | 同包两处，写法一致 |

### New module candidates

| candidate_id | capability_id | proposed_module | searched_candidates | direct_reuse_gap | thin_adapter_or_owner_extension_gap | proven_gap | unique_owner_and_lifecycle | deletion_or_merge_handling |
|---|---|---|---|---|---|---|---|---|
| NONE | none | none（`root.go` 是 `internal/project` 包内新文件，不是新包） | none | none | none | none | none | none |

### Rejected new tools

| rejected_candidate | capability_id | deletion_test_and_reason |
|---|---|---|
| NONE | none | none |

## 对设计的细化

| # | 问题 | 定案 |
|---|---|---|
| R1 | 辅助函数的形状 | `internal/project/root.go`：`OpenInRoot(root, rel string) (*os.File, error)`（读）；`PlaceInRoot(root, rel, tmpSuffix string) (*Placement, error)`：在根内 `MkdirAll(父目录)` 并打开 `rel + ".gofer-tmp-" + tmpSuffix`；`Placement` 暴露 `File() *os.File`（调用方写入并自己算 size / sha256）、`Commit() error`（关闭后 `Rename` 到 `rel`，失败删临时文件）、`Abort()`（关闭并删临时文件，可重复调用）；`ExistsInRoot(root, rel string) (bool, error)` 用 `Lstat`。每次调用自行 `os.OpenRoot` 并在返回前或 `Commit`/`Abort` 时关闭。 |
| R2 | job 附带上传只拿到绝对 `dst` | `job.XferBridge.FetchUpload(ctx, xferID, projectKey, dst)` 改为 `FetchUpload(ctx, xferID, projectKey, root, rel string)`；`job/xfer.go` 传 `root`（workDir 或 resultDir）与 `up.Dest`，仍先做字面 `SafeJoin` 预检；落位后的 `os.Stat` 改用 `SafeJoin` 结果（只读大小，不改语义）。实现方 `core.hubJobXfer`、`worker.jobXfer`、测试桩同步改。 |
| R3 | 「已存在」判断 | put 前的存在检查由 `os.Stat(dst)` 改为 `ExistsInRoot`（`Lstat`）：目标是任意链接（含悬空、根外）都算已存在，需 `force` 才覆盖；`force` 时 `Rename` 替换链接本身。中间目录出根时返回错误而不是「不存在」。 |
| R4 | 出根时的错误 | 直接返回 `os.Root` 的错误（只含相对名，不含解析后的路径），外面按现有格式包一层（如 `open source: …`、`create destination directory: …`）。get 时 `os.IsNotExist` 仍映射到「source not found」；出根不映射为不存在。 |
| R5 | 绝对路径回报（协议 v25） | put 回报的绝对落位路径仍取字面 `SafeJoin` 结果，不变。 |
| R6 | Windows 测试 | 建符号链接失败时按 `internal/config/worker_roots_test.go:161` 的 `mustSymlink` 惯例 `t.Skip`；另在 Windows 上用 `mklink /J` 建联接的用例不写（`os.Root` 行为已由实测确认，单测不重复）。 |
| R7 | `ReadKeyFile` 复核 | `SafeJoinReal` 后取 `filepath.Base(abs)` 判 `keyFileAllowlist`，不通过返回 `fs.ErrNotExist`（handler 已映射 404）；`FileContent.Name` 改为 `filepath.Base(rel)`。 |

## 前置检查与 fail-closed 条件

workspace baseline：主机建 worktree `.worktrees/i72n`（分支 `fix/i72n-symlink-boundary`），`.git` 改相对 gitdir；Go 命令带 `GOWORK=off`，`TMPDIR` 在任何 git 仓库之外。

fail-closed（出现即停，回报监督会话）：

- 基线 `go test ./internal/project ./internal/xfer ./internal/worker ./internal/job ./internal/core` 在 worktree 未改动时就失败。
- 发现除 R2 外还需要改跨包接口或协议字段。
- `os.Root` 在 Linux 容器内对根内相对链接的行为与设计不符。

## 波次与依赖

```
W0  T0 前置核对（Claude）
W1  T1 ReadKeyFile 复核 → T2 root.go 辅助 → T3 server 侧接入 → T4 worker 侧接入（sonnet 子 agent，顺序）
W2  T5 文档与 CHANGELOG（suga，gpt）
W3  T6 真实验收（Claude）→ T7 反向验证、全量与收尾（Claude）
```

## 任务

### T0 前置核对

- 执行方：Claude。
- 动作：建 worktree；跑上面的基线测试；`grep -rn "SafeJoin(" internal` 复核调用点与设计表一致。
- 完成标准：基线绿；调用点无遗漏。

### T1 `ReadKeyFile` 真实目标复核

- 执行方：sonnet 子 agent（安全）。
- 文件：`internal/project/browse.go`、`internal/project/browse_test.go`（或现有对应测试文件）、`internal/httpapi/project_browse_handler_test.go`。
- 动作：按 R7。
- 验证：`go test ./internal/project ./internal/httpapi -run 'KeyFile|GetFile' -count=1`。用例：`README.md -> .env`（根内）→ 404 且响应体不含 `.env`；`README.md -> docs/README.md`（根内，白名单名）→ 200 且 `name` 为 `README.md`；原有根外链接 404 用例仍过。

### T2 `internal/project/root.go`

- 执行方：sonnet 子 agent。
- 文件：`internal/project/root.go`（新）、`internal/project/root_test.go`（新）。
- 动作：按 R1、R3、R4。
- 验证：`go test ./internal/project -run 'InRoot|Placement' -count=1`。用例：正常读；目标链到根外读失败；中间目录链到根外读失败；根内相对链接可读；`PlaceInRoot` 正常落位、父目录自动创建；中间目录链到根外时 `PlaceInRoot` 失败且根外目录为空；`Abort` 后无临时文件；`Commit` 覆盖已存在目标；`ExistsInRoot` 对悬空链接返回 true、对中间目录出根返回错误。

### T3 server 侧接入

- 执行方：sonnet 子 agent。
- 文件：`internal/xfer/local.go`、`internal/xfer/attachments.go`（如需）、`internal/core/job_xfer.go`、`internal/job/xfer.go`、`internal/job/xfer_files_test.go`、`internal/xfer/local_test.go`、`internal/core` 对应测试。
- 动作：`runPut` / `runGet` / `runOwnAttachment` / `writeVerified` 改用 R1 辅助（`runOwnAttachment` 的根是 `OwnAttachmentsDir()`）；`FetchUpload` 按 R2 改签名。删除被替换的重复片段，保留原有注释中仍成立的说明。
- 验证：`go test ./internal/xfer ./internal/core ./internal/job -count=1`（`job` 包全量较慢，可先 `-run 'Xfer|Upload'` 再全量一次）。新增用例：server 本机 get 经根外链接失败；put 经中间目录链接失败且根外无文件；job 附带上传的 `Dest` 经中间目录链接失败并记 `EventJobUploadFailed`。

### T4 worker 侧接入

- 执行方：sonnet 子 agent。
- 文件：`internal/worker/xfer.go`、`internal/worker/xfer_job.go`、`internal/worker` 对应测试。
- 动作：`runFileXfer` 传 `run.root` 与 `fr.Path`；`xferFetch` / `xferSend` 改用 R1 辅助；`jobXfer.FetchUpload` 按 R2。
- 验证：`go test ./internal/worker -run 'Xfer' -count=1`，再 `go test ./internal/worker -count=1`。新增用例：worker get 经根外链接失败；put 经中间目录链接失败。
- 提交前：`go build ./... && go vet ./...`；`go list -deps ./internal/project | grep gofer/internal/` 不含入口层（G022）。

### T5 文档与 CHANGELOG

- 执行方：suga（gpt）。
- 文件：`skills/gofer-usage/references/` 中讲传输的文件（先 `grep -ln "xfer\|传输" skills/gofer-usage/references/` 确认）、`CHANGELOG.md`「未发布」。
- 动作：传输说明加一句：传输只能读写项目目录内的文件；经项目内链接时链接必须是相对路径且指向项目内，Windows 目录联接不支持。CHANGELOG 加「修复」两条（关键文件预览经链接读到白名单外文件；传输与 job 附带上传经链接到达项目外）。G046：不写 issue id、本机路径。
- 验证：`sh scripts/check-private-names.sh`（`PRIVATE_NAMES_FILE` 指向主 checkout 的 `.private-names`）。

### T6 真实验收

- 执行方：Claude。
- 环境：worktree 构建的 Linux 二进制，容器内临时 server（临时端口、`GOFER_CONFIG_DIR=tmp/i72n-impl/conf`，清除 `GOFER_*` 连接变量，只按自记 pid 停）；临时项目 `tmp/i72n-impl/proj/`，内含 `README.md -> .env`、`out -> ../outside`（目录链接）、`ok -> docs`（根内相对链接）。
- 动作：`GET /v1/projects/<p>/file?path=README.md` → 404；`gofer tool xfer` get `out/secret.txt` 失败、get `ok/a.md` 成功；put 到 `out/x.txt` 失败且 `outside/` 无新文件；put 到 `ok/new.md` 成功。记录 `tmp/i72n-impl/acceptance.md`。
- 主机 Windows 上同样的 put/get 由 T7 的 gofer-verify（vet-darwin-windows 只编译）不覆盖；Windows 行为以 `tmp/i72n-probe` 实测为准，在验收记录中注明。

### T7 反向验证、全量与收尾

- 执行方：Claude。
- 反向（临时副本里改，不提交）：`OpenInRoot` 换成 `os.Open(filepath.Join(root, rel))`，get 出根用例必须失败；`PlaceInRoot` 换成直接 `os.MkdirAll` + `os.OpenFile`，put 出根用例必须失败；去掉 `ReadKeyFile` 复核，`README.md -> .env` 用例必须失败。输出 `tmp/i72n-impl/reverse.md`。
- `git diff --stat main...` 查无关删除；gofer-verify 四路通过；合并（`--no-ff`）；删 worktree 与分支；设计稿补实施记录；计划 COMPLETE 写不超过 8 行总结；关 `gofer-i72n`。

## 回滚与恢复

单独回退合并提交即可；无 schema、配置、协议变化。worker 与 server 分别升级后各自生效。

## 人工 Gate

- 本计划批准后开始 T0。
- push 与发版不在本计划内（随下一批，Q3=A）。

## 可追溯性

| 设计项 | 任务 | 验证 |
|---|---|---|
| `ReadKeyFile` 复核（D1） | T1 | T1 用例、T6、T7 反向 |
| 传输与上传走 `os.Root`（D2） | T2 T3 T4 | T2–T4 用例、T6、T7 反向 |
| 非目标不动（D3） | T0 | T7 diff 检查 |
| 绝对链接与联接限制写入说明（Q2） | T5 | T5 私有名检查 |

## 完成 Gate 与剩余工作

完成 Gate：T0–T7 通过；反向验证写入 `tmp/i72n-impl/reverse.md`；gofer-verify 全绿；合并并清理；设计稿实施记录；关 issue。

剩余工作：随下一批发版时 push 与发版。
