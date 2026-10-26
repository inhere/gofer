# CFG-16 runner 声明可用外部工具 设计

- issue：gofer-vni6
- 路线图：CFG-16（N4，idea #6「runner 声明可用工具」）
- 状态：已实施（0.140，见文末「实施记录」）

## 一、问题

agent 在某台 worker（或 server 本机）上跑任务时，不知道这台机器装了哪些外部工具（ffmpeg、docker、kubectl、某个内部 CLI、PLC 调试工具……），只能自己 `which` 乱试，或靠人在项目 / agent 级 rule 里手写「这台机器有 X」。worker 注册时也只上报 labels / agents，web 和 CLI 看不到一台 runner 能干什么。

目标：

1. runner 在自己的配置里声明可用工具；
2. 派发到这台 runner 的 agent job，prompt 里自动带上一段「本机可用工具」；
3. worker 注册时把声明上报，`gofer worker show`、`GET /v1/runners`、web Runners 页可见；
4. job 进程环境里有 `GOFER_WORKER_ID`，脚本 / agent 能知道自己跑在哪台 worker 上。

不做：按工具自动选 worker（`--need-tool`）、自动探测版本号上报。声明是给 agent 看的说明，不是调度约束；需要调度约束时继续用 labels / projects。

## 二、配置

worker 在 `worker.yaml`、server 本机在 `config.yaml` 顶层，同一个形状：

```yaml
tools:
  - name: ffmpeg                 # 必填，工具名（agent 看到的名字）
    desc: 音视频转码，支持 h264/hevc 硬件编码   # 可选，一句话用途 / 注意事项
    bin: ffmpeg                  # 可选，可执行文件名或绝对路径；缺省 = name
  - name: plc-sim
    desc: 西门子 PLC 仿真器，端口 102；只读调试用
    bin: D:\tools\plcsim\plcsim.exe
```

- `name` 必填、唯一（大小写不敏感），只允许 `[A-Za-z0-9._-]`，长 ≤ 64。
- `desc` ≤ 200 字符；`bin` 只做说明与 doctor 检查，不改 PATH、不代 agent 调用。
- 总数 ≤ 50 条，渲染出的段落总长 ≤ 4 KiB（超了加载报错，指出最长的几条），防止 prompt 被撑大。
- server 的 `tools` 只作用于 server 本机 runner（`local`）；它是全局配置，不进项目瘦配置。
- worker 侧 `tools` 随 worker 配置热重载（与 labels 同一条 Caps 路径）；server 侧随 `serve reload`。不进 web 配置编辑（本地机器事实，由该机器维护）。

检查：

- `gofer config validate`（server / worker 两种都查）：格式错误报错；`bin` 在本机 `exec.LookPath` 找不到时**警告**（声明可能是给别的环境的，不阻断）。
- `gofer worker doctor`：新增一行 `tools`，列出声明数与找不到的 `bin`。

## 三、注入 prompt

由**执行 job 的机器**渲染（与挂载技能清单同一位置、同一原则：只有这台机器知道自己装了什么）：

- server 本机 runner：server 的 `Submit` 在本地执行分支渲染，用 `config.yaml` 的 `tools`；
- worker：hub 不渲染，worker 自己的 `Submit` 渲染，用 `worker.yaml` 的 `tools`（`workerConfigToConfig` 映射到运行配置的 `Tools`）；
- peer-http 等其他远程 runner：不渲染。

段落位置：规则段之后、技能清单之后、任务正文之前（规则是框架，技能和工具是内容）。形如：

```markdown
## 本机可用工具（gofer 注入，按需使用）

本任务运行在 worker `w-office` 上，以下工具已安装，可直接调用：

- `ffmpeg` — 音视频转码，支持 h264/hevc 硬件编码
- `plc-sim`（`D:\tools\plcsim\plcsim.exe`）— 西门子 PLC 仿真器，端口 102；只读调试用
```

不注入的情况：

- `exec` agent（命令不是给模型读的）；
- 续接 job（`job resume`，会话里已经有过这段）；
- 请求带 `no_rules`（`--no-rules`）——沿用「不要任何 gofer 注入」的既有开关，不新增 flag；
- 该 runner 没声明任何工具。

和规则 / 技能一样，渲染结果只用于本次运行，`request_json` 仍存调用方原文，rerun 不会叠加。

## 四、上报与展示

- `wsproto.Register` 与 `wsproto.Caps` 加可选字段 `tools`（`[{name, desc, bin}]`，`omitempty`）。G032：可选字段不升协议版本，旧 server 忽略未知字段。
- hub `WorkerSnapshot` 带上 tools；`UpdateCaps` 整体替换（与 labels 一致）。
- `GET /v1/runners` 的 worker 视图与本机 runner 视图（`localCapabilities`，取 server `tools`）加 `tools`；`GET /v1/workers/{id}` 同。
- `gofer worker show` 加一行 `tools: ffmpeg, plc-sim`（`-v` 时带 desc）。
- web Runners 页：节点卡片在 labels 下面显示工具 chip，悬停显示 desc。

## 五、job 环境变量

worker 执行的 job 进程加 `GOFER_WORKER_ID=<worker-id>`（worker dispatch 构造请求时写进 `Env`，与 `GOFER_SERVER_ADDR` 同处）。server 本机 runner 的 job 不设（本机即 server，没有 worker id）。

## 六、测试

- config：解析、校验（重名、非法名、超长、超总量）、server / worker 两份。
- wsproto：Register / Caps 带 tools 往返；旧帧（无 tools）解码。
- wshub：快照带 tools，`UpdateCaps` 整体替换。
- job：本地执行注入段落（位置在规则与技能之后）；exec / resume / no_rules / 无声明不注入；hub→worker 分支 hub 不渲染。
- worker：dispatch 请求 Env 带 `GOFER_WORKER_ID`。
- httpapi：`/v1/runners` worker 与本机视图带 tools。
- commands：`worker show` 输出；`config validate` 对找不到的 bin 只警告。

## 七、文档

- skill：`references/worker-config.md`（结构总览加 `tools`、热重载列表）、`references/server-config.md`（顶层 `tools`）、`references/operations.md`（worker show 字段）；面向任意项目写（G046）。
- `config/worker.example.yaml`、`config/gofer.example.yaml` 加示例；`worker init` 模板加注释掉的 `tools:` 示例。
- CHANGELOG「新增」；路线图 CFG-16 移到已落地。

## 八、实施记录

按本设计全部落地，代码要点：

- 配置：`config.Tool{name,desc,bin}`（`internal/config/tools.go`），`Config.Tools`（顶层）与 `WorkerConfig.Tools`；`ValidateTools` 由 `config.validate`（server 加载 / reload / 控制台写入）与 `loadWorkerConfig`（worker 启动 / 热重载 / `config validate worker`）共用。渲染行 `config.ToolLine` 只有一份，4 KiB 上限量的就是注入的那几行。desc 上限按字符（rune）计。字段策略表登记 `tools: HotReload`（随 reload 生效、控制台不写）。
- 注入：`job/tools.go` 的 `toolsPromptSection`，在 `Submit` 里 `!remote` 时拼到技能清单后面，经 `insertSkillsManifest` 放在规则段之后、任务正文之前。跳过条件：exec agent、`ResumedFrom` / `ResumeSourceAgent`（续接与 exec 载体）、`NoRules`、未声明。worker id 只在 `CredentialExternal`（worker 派发重入）时从 `req.Env[GOFER_WORKER_ID]` 取，本机 job 自带的同名 env 不被采信。
- worker：`workerConfigToConfig` 映射 `Tools`；`workerCaps` 从**已应用的 cfg** 取 tools（不是 wc），保证上报与 job 实际看到的一致；`worker.Config.Tools` → 注册帧，热重载走既有 Caps 帧。派发时 `Env` 增加 `GOFER_WORKER_ID`（常量 `job.EnvWorkerID`）。
- 协议：`wsproto.ToolBrief`；`Register.Tools`、`Caps.Tools` 均 `omitempty`，不升协议版本。hub `WorkerSnapshot.Tools`（防御性拷贝），`UpdateCaps` 整体替换。
- 展示：`httpapi.WorkerStatus.Tools`、`workerView.Tools`、`capsView.Tools`（本机行取 server `tools`）；`gofer worker show` 加 `tools:` 行，`-v/--verbose` 每条一行带 bin 与 desc；web Runners 本机卡与 worker 卡显示工具 chip（悬停看 desc / bin）。
- 检查：`config validate`（server / worker）找不到 bin 打 WARN 不失败；`worker doctor` 新增 `tools` 行（未声明 / 全部找到 = PASS，有缺 = WARN）。`worker init` 生成的 worker.yaml 末尾附注释掉的 `tools:` 示例。

与设计的出入：

- `--no-rules` 要让 worker 也不渲染工具段，原有派发帧不带这个开关，于是新增可选字段 `wsproto.Dispatch.NoRules`（`no_rules,omitempty`，经 `runner.Forward.NoRules` 传递）。旧 worker 忽略它照样渲染——旧 worker 本来也不认识 `tools`，无影响。peer-http 不传（对端自己是 server，按自己的配置决定）。
- `Caps` 的文档约定「不用 omitempty」，`tools` 例外用了 `omitempty`：hub 对 Caps 一律整体替换，缺键解码为 nil = 没有工具，与空列表含义相同，不会丢信息。
- 交互（pty）job 的首条输入在规则注入前就已生成，与技能清单一样不含工具段；ACP 持续会话首轮 prompt 含。
- POLICY worker 在还没有任何策略时收到 SIGHUP，沿用既有「不重建配置」的处理，新 tools 要等下一次策略下发 / reload 才进入 job prompt；上报同样取已应用的配置，所以两者保持一致。

