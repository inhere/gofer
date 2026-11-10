<!-- template_id: design; template_version: 1.2.0 -->
# 项目文件读写的符号链接边界设计

> 状态：Draft 0.1 / 已批准（2026-11-10，Q1–Q3 均按推荐项）
> issue：gofer-i72n

## 修订记录

| 版本 | 日期 | 作者 | 摘要 |
|---|---|---|---|
| 0.1 | 2026-11-10 | Claude | 初稿：核查结论与修复方案 |

需要用户：答复 Q1–Q3（或接受全部推荐项），并明确批准进入计划编写。批准设计不等于授权实施。

## 背景与目标

文档阅读功能（gofer-i1bd）审查时发现：项目内的符号链接可以绕过「按请求路径判断」的规则——请求名是 `README.md`，链接实际指向 `.env`。`ReadDoc` 已在解析后对真实路径复核。本 issue 核查其余按项目相对路径读写文件的入口，有问题的一并修。

目标：凡是 server / worker 代表调用方在项目里读写文件的入口，都满足两条——

1. **不出根**：读写的真实位置在项目执行根之内（中间目录或目标是指向根外的链接时拒绝）。
2. **规则看真实目标**：入口自带的文件名规则（白名单等）按解析后的真实文件判断，不只按请求名。

## 范围与非目标

范围（逐个核查过，见下节）：

- `GET /v1/projects/{key}/file`（`project.ReadKeyFile`，关键文件预览）。
- 文件传输 get / put：server 本机执行（`internal/xfer/local.go`）与 worker 执行（`internal/worker/xfer.go`）。
- job 提交时附带的上传落位（`internal/job/xfer.go:183`，`FetchUpload` 写入 job 工作目录或结果目录）。
- 本机附件目录落位（`internal/xfer/attachments.go:63`）。

非目标：

- job 的 `cwd`、`read_dirs`、会话恢复等「给 agent 定工作目录」的 `SafeJoin`（`submit.go`、`resume.go`、`session_recovery.go`）：agent 在目录里本来就能执行任意命令，链接出根不扩大它的能力，本次不改。
- 结果目录（`ResultBaseDir`）的解析。
- 设备文件、bind mount、`/proc` 等文件系统边界（`os.Root` 也不防，见下）。

## 已确认事实与规范

逐处核查（代码行以 main `85057d33` 为准）：

| 入口 | 现状 | 问题 |
|---|---|---|
| `ReadKeyFile`（`internal/project/browse.go:245`） | 白名单只看请求名的 basename；随后 `SafeJoinReal` 防出根 | **有**：项目内 `README.md -> .env` 不出根，`SafeJoinReal` 放行，读出 `.env` 全文；响应 `name` 还是真实文件名 `.env`。路由是 `Public()`（在项目范围内的任何调用方可读） |
| xfer get，server 本机（`xfer/local.go:121` → `runGet` 的 `os.Open`） | 字面 `SafeJoin`，`os.Open` 跟随链接 | **有**：`link -> /etc/xxx` 或中间目录链到根外，都能把根外文件传出 |
| xfer put，server 本机（`runPut`：`MkdirAll` + 写临时文件 + `Rename`） | 字面 `SafeJoin` | **有**：中间目录是指向根外的链接时，`MkdirAll` / 临时文件 / `Rename` 都落在根外。目标本身是链接时 `Rename` 替换的是链接本身，不越界 |
| xfer get / put，worker（`worker/xfer.go:131`，`xferSend` 的 `os.Open`、`xferFetch` 的 `MkdirAll`/`OpenFile`/`Rename`） | 同上 | **有**，同上 |
| xfer 提交前校验（`httpapi/xfer_handler.go:628`） | 字面 `SafeJoin`，只做早期 400 | 无：真正的边界在执行机上（远程项目的文件 server 本来读不到），保持为早期校验 |
| job 附带上传（`job/xfer.go:183` → `FetchUpload`） | 字面 `SafeJoin`，写入 job 工作目录或结果目录 | **有**（写方向），同 put |
| 本机附件目录（`xfer/attachments.go:63`） | 字面 `SafeJoin`，根是 `<config-dir>/run/attachments` | 风险低：目录由 gofer 自建，调用方无法在里面造链接；仍按同一方式改，保持「同一边界」的说法成立 |

权限与风险：

- xfer 需要 `xfer.write`（危险级、不可授予会话、种入）。持有者通常也能提交 job、本来就能在执行机上跑命令，所以 xfer 出根**不是越权**，但它破坏了代码注释里「传输的边界与 job `--cwd` 相同、不会到达项目外」的承诺，也让「只给传输、不给执行」的调用方（例如受限的 CLI 用户或第三方集成）能读写根外文件。按缺陷修。
- `ReadKeyFile` 是 `Public()` 路由，是这批里唯一会让**低权限调用方**读到敏感文件的入口，优先级最高。

Go 标准库 `os.Root`（Go 1.24 引入，1.25 补齐 `MkdirAll` / `Rename` 等；本仓库 `go 1.26`）：

- 方法只能访问根之下的文件；**跟随链接，但链接不得指向根外**，且**链接目标不得是绝对路径**（即使绝对路径落在根内）。
- 在多数平台上以目录句柄实现，逐级打开，**没有「先检查、后打开」的竞态**。`SafeJoinReal` 是 `EvalSymlinks` 后再 `Open`，两步之间链接可被替换。
- 不防设备文件、bind mount、`/proc`。Windows 上不允许保留设备名（`NUL`、`COM1`）。
- 本机 Go 1.25 `go doc os.Root` 已确认上述方法与说明。
- Windows 实测（2026-11-10，主机 go1.25.10，`tmp/i72n-probe/`）：指向根外的目录联接与相对符号链接，`Open` / `MkdirAll` 都报 `path escapes from parent`，根外未创建任何东西；**指向根内的目录联接同样被拒**（联接总以绝对路径存储），指向根内的相对符号链接可用，指向根内的绝对符号链接被拒。

## 总体方案

### 1. `ReadKeyFile`：真实目标复核（与 `ReadDoc` 一致）

`SafeJoinReal` 之后，取真实路径相对真实根的 basename，再判一次 `keyFileAllowlist`；不通过返回与「不存在」相同的错误（404），不暴露真实名。响应 `name` 改回取请求名的 basename（它已经过白名单）。

不改为 `os.Root`：这里必须知道真实目标叫什么，`os.Root` 不返回解析结果；与 `ReadDoc` 保持同一写法，便于以后两处一起维护。

### 2. 传输与上传落位：改用 `os.Root`

在 `internal/project` 新增一个小的公共入口（例如 `OpenProjectRoot(execRoot) (*os.Root, error)` 加上 get / put 两个辅助函数），把读写都放到 `os.Root` 上：

- get：`root.Open(rel)`，之后照旧 `Stat` 判目录、拷贝、算 sha256。
- put：`root.MkdirAll(dir)` → `root.OpenFile(tmp, O_CREATE|O_WRONLY|O_TRUNC)` → 写入与校验 → `root.Rename(tmp, rel)`；失败时 `root.Remove(tmp)`。
- server 本机（`xfer/local.go`）、worker（`worker/xfer.go`）、job 附带上传（`FetchUpload` 的落位）、本机附件目录，全部改走这组辅助函数，不再各写一份 `MkdirAll` / `Rename`。
- 现有 `SafeJoin` 预检保留（它负责 `..`、绝对路径等字面规则和早期报错）；`os.Root` 负责链接边界。
- put 返回给 hub 的「绝对落位路径」（协议 v25）仍由 `SafeJoin` 结果给出，语义不变。

出根时返回明确的错误（如 `path escapes project via symlink`），不带解析后的路径。

## 架构

- `internal/project`：新增 `root.go`（`os.Root` 辅助）；`browse.go` 的 `ReadKeyFile` 加复核。数据层，不依赖入口层（G022）。
- `internal/xfer`、`internal/worker`、`internal/job`：调用新辅助函数，删掉各自的 `MkdirAll` / 临时文件 / `Rename` 片段（行为保持：临时文件名、校验顺序、错误语义不变）。
- 入口层（`httpapi`）不变，只是错误映射沿用现有分支。

## 关键流程

xfer get（server 本机）：`resolve` 做字面 `SafeJoin` 预检 → 打开项目 `os.Root` → `root.Open(rel)`（链接出根在此失败）→ 判目录 → 写入暂存区。

xfer put：字面预检 → 打开 `os.Root` → `MkdirAll(父目录)` → 写临时文件 → 校验 size / sha256 → `Rename` 到目标。任何一步出根都失败，不留临时文件。

## 安全、数据、运维与回滚

- 行为变化 1：项目内**以绝对路径写的**链接（`ln -s /srv/proj/docs/a.md a.md`）将不能经传输读写（`os.Root` 规定）。Windows 上**所有目录联接**都属此类（即使指向根内），经联接的传输路径会失败并明确报错。相对符号链接照常。见 Q2。
- 行为变化 2：`/file` 预览不再能经链接读到白名单外的文件。
- 无 schema、协议或配置变化；worker 与 server 各自独立生效（旧 worker 仍是旧行为，升级 worker 后生效）。
- 回滚：单独回退提交即可，无数据迁移。
- 测试：每个入口各补「目标链接出根」「中间目录链接出根」「根内链接可用」三类用例；Windows 上建链接需要权限，测试按现有惯例在不能建链接时 `t.Skip` 并注明。反向验证：把 `os.Root` 换回直接 `os.Open` / `os.Rename`，出根用例必须失败。

## 决策

- D1：`ReadKeyFile` 用「解析后复核」（同 `ReadDoc`），不用 `os.Root`。
- D2：传输与上传落位统一走 `internal/project` 的 `os.Root` 辅助函数。
- D3：job 工作目录类的 `SafeJoin` 不在本次范围。

## 待确认事项

- **Q1 修复范围**：(A，推荐) `ReadKeyFile` + 传输 get/put（server 与 worker）+ job 附带上传 + 附件目录一次改完；(B) 只修 `ReadKeyFile`（唯一的低权限入口），传输另排。推荐 A：传输几处是同一段代码的重复，抽成一组辅助函数改一次，比分两次改更省、也消掉重复实现。
- **Q2 绝对路径链接**：`os.Root` 拒绝绝对路径的链接，即使它指向根内。(A，推荐) 接受这一限制，在 skill 的传输说明里写一句「项目内的绝对路径链接不能经传输读写」；(B) 不用 `os.Root`，改用「`EvalSymlinks` 后判根」（允许根内绝对链接，但有检查与打开之间的竞态）。推荐 A：根内绝对链接在项目里少见，而竞态恰好是这类边界最常见的绕过方式。
- **Q3 版本节奏**：(A，推荐) 合并后随下一批一起发版，不单独发；(B) 作为安全修复单独尽快发版。推荐 A：最危险的 `/file` 只能读到项目内的文件（`.env` 这类），且需要有人在项目里放了指向它的链接；传输需要危险级权限。若你认为 `/file` 那一项要尽快上线，选 B 时我只把 `ReadKeyFile` 拆出来先发。

## 结论与人工计划 Gate

- 核查结论：issue 里列的三处中，xfer 的读写（server 本机与 worker）确有出根问题；`xfer_handler.go:628` 只是早期校验，不是边界。另发现 `ReadKeyFile` 的白名单可被项目内链接绕过（低权限可达，优先级最高），以及 job 附带上传的写方向同样问题。
- 计划 Gate：Q1–Q3 定案并批准后写实施计划。计划第一步实测 Windows 上 `os.Root` 对目录联接的行为，以及现有测试里建链接的惯例。

## 已定事项（2026-11-10）

用户（web）原话：「按推荐」。Q1 选 A（四处一次改完）、Q2 选 A（接受 `os.Root` 对绝对链接的限制，含 Windows 目录联接，skill 传输说明写明）、Q3 选 A（随下一批发版）。批准授权进入实施计划，不授权实施。批准后实测发现 Windows 根内联接也被拒，已补入「已确认事实」与「行为变化 1」，并在回复中向用户说明。

