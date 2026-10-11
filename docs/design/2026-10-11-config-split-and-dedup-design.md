# server 配置拆文件 + 去重

- issue：gofer-arsj
- 状态：设计稿，待确认后实施
- 范围：server 的 `config.yaml`。`worker.yaml` 通常很短，不做拆文件；去重（二）里的 agent `extends` 两边共用，因为 agent 定义是同一个结构。

## 背景

一份实际使用中的 server 配置有 377 行，11 个顶层段，长度分布如下：

| 段 | 行数 | 占比 |
|---|---|---|
| agents | 156 | 41% |
| projects | 103 | 27% |
| server | 47 | 12% |
| supervisor | 24 | 6% |
| 其余 7 段 | 47 | 12% |

读起来吃力，主要有两个原因：

1. **两个大段把其余配置挤散了**：agents 与 projects 占了 2/3，想看 server、runners、supervisor 得翻过一百多行 agent 定义。
2. **大量内容和内置默认值重复**：
   - 10 个 `detect: {command: <同 command>, args: [--version]}`，和默认探测完全一样；
   - claude、codex、opencode 三个 agent 在配置里整条重写了内置模板，只是为了改其中一两个字段。

用户问过能不能改成 TOML。结论是不改，原因有三：

- 配置嵌套深，还有大量对象列表。TOML 要写一长串全路径表头（`[agents.claude-acp.acp.modes]`、`[[server.notification.webhooks]]`），反而更难读。
- 现在 web 配置页保存时会保留注释，只改动过的段；Go 的 TOML 库做不到带注释的往返写回。
- 加载、写回、模板、文档、skill 全都得改，代价远大于收益。

## 目标

- 主文件只留 server、storage、runners 这类「全局设置」，一屏能看完；大段放到各自的文件。
- 能一次性找出、并安全删除「删掉也不改变实际效果」的字段。
- agent 在内置模板基础上**只写差异**。
- 已有的保证不变：web 保存时保留注释、只改变化的段；热加载；`config validate`；保存不会把默认值写进文件。

## 非目标

- 不换格式（不做 TOML / JSON）。
- 同一个顶层段不允许跨文件合并（例如 projects 拆成每个项目一个文件）。写回时一个段必须只属于一个文件，第一版不做（见「六、后续」）。
- 不改 `.gofer.project.yaml` 项目瘦配置的机制。

---

## 一、拆文件：`include`

### 1. 写法

主文件顶层新增 `include`：一个路径列表，相对主文件所在目录，可以用 glob：

```yaml
# config.yaml（主文件）
include:
  - config.d/*.yaml

server:
  addr: 0.0.0.0:8767
  # …
runners:
  # …
```

```yaml
# config.d/agents.yaml
agents:
  claude:
    extends: builtin
    interactive_args: [--enable-auto-mode]
  # …
```

```yaml
# config.d/projects.yaml
projects:
  my-project:
    host_path: /work/my-project
    # …
```

### 2. 规则

- **片段文件只能包含顶层段**，写法和主文件里完全一样，任何已知顶层段都能放（agents、projects、supervisor、roles……）。
- **一个顶层段只能出现在一个文件里**。出现在两个文件时加载报错，并列出这两个文件。这条规则让「web 改了这一段该写回哪个文件」有唯一答案。
- 不允许嵌套：片段文件里不能再写 `include`。主文件以外的 `include` 报错。
- glob 按文件名排序后展开。没有匹配到文件不报错（给 `config.d/*.yaml` 这种约定留余地）；写明的具体文件不存在则报错。
- 任何顶层段都能放进片段文件（包括 server），没有「只能在主文件」的限制。`gofer config split`（见 5）默认只拆 agents、projects，建议保持这个习惯：全局设置留在主文件，一眼能看到。
- 片段文件与主文件放在同一个配置目录下即可，不新增权限要求。

### 3. 加载、校验、热加载

- `config.Load` 读完主文件后读 `include` 展开的片段，逐个解析，再合成一个 `Config`。合成之后的流程与现在一样：`ApplyDefaults`、`validate`、agent 解析、项目注入。
- `RejectRemovedKeys`（拒绝已删除的旧键）对**每个文件**都跑，错误信息带文件名。
- 加载后在 `Config` 上记一份「顶层段 → 来源文件」的映射（`cfg.sources`，不导出、不序列化），写回与展示都用它。
- 热加载（`serve reload` / web「重新加载」/ SIGHUP）走的就是 `config.Load`，自动覆盖片段文件；reload 回执的 `path` 仍是主文件，新增 `files` 列出实际读到的全部文件。
- `gofer config validate`、`gofer config info` 显示所有文件；`config info` 增加一行 `files: config.yaml, config.d/agents.yaml, …`。
- `gofer config edit [--key <段名>]`：不带 `--key` 打开主文件；带 `--key agents` 打开 agents 所在的文件。

### 4. 写回（web 配置页、`project add` 等）

现在的 `config.Save(path, cfg)` 是「按顶层段手术式保存」：只有内容变化的段从结构体重新渲染，其余段逐字保留原文（含注释）。拆文件后改为**按文件分组**执行同一算法：

1. 按 `cfg.sources` 把顶层段分到各自的文件；**新出现的段**（文件里原本没有）写进主文件。
2. 对每个文件只渲染它拥有的段，沿用现有的 `mergeTopKeys`：未变的段原文保留，变了的段重渲染。
3. 只写内容真的变了的文件；每个文件各自先写临时文件再改名替换（与现在相同）。
4. `include` 本身由人维护，程序只读不改（web 保存不会动它）。

web 的 `GET /v1/config` 增加 `sources: {"agents": "config.d/agents.yaml", …}`，配置页每个段的标题旁显示它所在的文件，避免「改了却找不到在哪个文件」。

### 5. 迁移命令 `gofer config split`

```bash
gofer config split                       # 默认把 agents、projects 拆到 config.d/<段名>.yaml
gofer config split --keys agents,projects,supervisor --dir config.d
gofer config split --dry-run             # 只打印将要生成的文件与主文件的变化
```

- **原文切片，不重新渲染**：复用写回里按顶层段切分原文的 `topBlocks`，段内的注释、顺序、空行原样搬走，段上方的注释跟着段走。
- 主文件里删掉这些段，并在文件开头（第一个顶层键之前、文件头注释之后）加上 `include: [config.d/*.yaml]`；已有 `include` 时追加条目（去重）。
- 动手前备份主文件（`config.yaml.bak-<时间>`）。目标文件已存在时拒绝执行，不覆盖。
- 自检：拆分前后各加载一次，比较**生效配置**（`CanonicalConfig` 之后的结果）。不一致就回滚并报错。正常情况下不可能不一致，这一步是保险。

---

## 二、去重

### 1. agent `extends: builtin`：只写和内置模板不同的部分

现在声明一个和内置模板同名的 agent，会**整条替换**模板，不做字段级合并。原因写在 `agent.Resolve` 的注释里：`interactive`、`no_raw_cmd` 是普通 bool，从结构体上分不清「没写」和「写了 false」，部分合并会悄悄把它们翻转。

新增一个显式开关解决这个问题：

```yaml
agents:
  claude:
    extends: builtin            # 以同名内置模板为底，下面只写差异
    interactive_args: [--enable-auto-mode]
    from_session_args: [--resume, "{{from_session}}", --fork-session]
  codex:
    extends: builtin
    rules: [house-rules]
```

- **「写没写」从 YAML 原文判断，而不是看零值**：加载时对带 `extends` 的 agent 额外解析一次它的原始映射，拿到「写了哪些键」。模板为底，写了的键整体覆盖（列表、映射都是整字段替换，不做元素级合并），没写的键沿用模板。bool 写了 `false` 就是 false。
- `extends` 只接受 `builtin`，并且要求同名的内置模板存在（目前有 claude、codex、opencode 和各个 `*-acp`）。值写错或没有同名模板时加载报错。以后如果要支持「以另一个已声明的 agent 为底」（`extends: claude`），语法上留了位置，本期不做。
- **不受探测结果控制**：带 `extends` 的 agent 是操作员声明的，和现在的声明式 agent 一样，探测失败也不会被移除（模板注入才看探测结果）。
- `GET /v1/agents` 和 web Agents 页显示 `extends: builtin`，并标出哪些字段来自模板。
- **写回只保存差异**：web 改了某个带 `extends` 的 agent 时，按字段比较「当前值」和「模板值」。只写出①原本就写在文件里的键，以及②和模板不同的键。这样模板里的值不会被「物化」进文件，模板以后升级时，这个 agent 也会跟着升级。
  - 这一步要绕开 YAML 的 `omitempty`（覆盖模板的 `false` / 空列表也要写出来），所以带 `extends` 的 agent 用「键 → 值」的映射来渲染，不直接序列化结构体。
- worker.yaml 的 agents 用同一套解析，同样支持 `extends`。

### 2. `gofer config lint`：找出删掉也不影响效果的字段

```bash
gofer config lint                 # 默认检查本机 server 配置（含 include 的所有文件）
gofer config lint --target worker --worker-config <path>
gofer config lint --fix           # 删除报告里标为「可安全删除」的字段
```

**判定方法**：逐个字段做「删掉它再算一遍」的对比，不维护一份单独的规则表。

1. 把配置按原始 YAML 解析成树，遍历声明的每个键（agents、projects 下逐层展开）。
2. 对每个候选键：在树上删掉它，重新解码成 `Config`，跑 `ApplyDefaults`，对 agent 再跑 `ResolveAgent` 用到的各项默认填充（会话、模型、只读、输出、投递、探测默认等）。
3. 与原配置的**生效结果**比较。完全相同，就报告「可安全删除」。

这样判定的结果与实际运行时用到的默认逻辑永远一致：默认逻辑改了，lint 自动跟着变。

**额外的两类提示**（只提示，`--fix` 不自动改）：

- **可以改写成 `extends: builtin`**：声明了与内置模板同名的 agent，又没用 `extends` 时，算出它与模板的差异，打印改写后的最小写法，并注明能省几行。
- **整条与模板相同**：删掉这条声明后会由内置模板注入，但注入要看探测结果（本机找不到这个 CLI 就不提供），所以只提示，不建议自动删除。

**报告格式**：`<文件>:<行> <键路径>  <原因>`，例如：

```text
config.d/agents.yaml:14  agents.claude.detect      与默认探测相同（claude --version）
config.d/agents.yaml:37  agents.jcode.session_resume_interactive  与默认续接参数相同
config.d/agents.yaml:5   agents.claude              可改写为 extends: builtin（13 行 → 4 行）：
                         claude:
                           extends: builtin
                           interactive_args: [--enable-auto-mode]
                           from_session_args: [--resume, "{{from_session}}", --fork-session]
config.yaml:61           projects.my-project.exchange_subdir  与 storage.default_exchange_subdir 相同
summary: 23 个字段可安全删除（约 61 行），3 个 agent 可改写为 extends
```

**`--fix` 怎么改文件**：按 YAML 语法树里该键的起止行，在**原文上删除这几行**，连同紧贴在它上方、只属于它的注释行；其余文本逐字不动（不重新渲染整段，避免段内其他注释丢失）。改之前先备份；改完重新加载并比较生效配置，不一致就回滚。

**探测相关的判定**：内置模板注入、探测默认值这类结论跟机器有关，lint 只在配置所在的机器上运行才有意义。对 server 配置，在 server 主机上跑 `gofer config lint`（容器里可以经主机 job 执行）。报告第一行写明「判定基于本机：…」。

### 3. 预期效果（按开头那份 377 行的配置估算）

| 处理 | 预计减少 |
|---|---|
| 10 个默认 detect 块 | 约 40 行 |
| claude、codex、opencode 改为 `extends: builtin` | 约 30 行 |
| 其他与默认相同的字段（会话参数、项目字段等） | 约 10–20 行 |
| 拆出 agents、projects | 主文件再少约 170 行 |

主文件约剩 120 行（server、storage、runners、supervisor 等），agents 文件约 80 行，projects 文件约 100 行。

---

## 三、文档

- 运维 runbook：配置拆分的写法与迁移步骤、`config lint` 的用法。
- gofer-usage skill `references/server-config.md`：`include`、`extends: builtin`、`config split` / `config lint`；`references/worker-config.md`：worker 的 agent 也能用 `extends`。
- `config/*.example.yaml` 与 `gofer init` 生成的 server 配置：内置模板覆盖示例改用 `extends: builtin`；生成的配置保持单文件，不默认拆分（新配置本来就短）。
- `docs/reference/http-api.md`：`GET /v1/config` 的 `sources`，`GET /v1/agents` 的 `extends`。

## 四、兼容

- 不写 `include`、不写 `extends` 的配置，行为与现在完全相同。
- 旧版 gofer 读到 `include` 或 `extends` 时，会把它当作未知键忽略：`include` 里的段读不到，带 `extends` 的 agent 只剩差异字段，基本不可用。所以 server、所有 worker 和 CLI 需要先升级到新版，再修改配置。CHANGELOG 写明这一点。

## 五、测试

- **加载**：
  - 主文件加片段合成的结果与等价单文件一致；glob 排序；
  - 同一段出现在两个文件报错；片段里写 `include` 报错；写明的文件不存在报错；
  - `RejectRemovedKeys` 在片段文件里也生效，且错误带文件名。
- **写回**：
  - 改 agents 只重写 agents 文件，主文件字节不变；新段写进主文件；
  - 片段文件里未变的段保留注释；只写变化的文件。
- **热加载**：改片段文件后 reload 生效，回执 `files` 正确。
- **split**：
  - 前后生效配置一致；注释随段搬走；目标文件已存在时拒绝；`--dry-run` 不写盘；
  - 已有 `include` 时追加条目、去重。
- **extends**：
  - 未写的字段继承模板；写了 `false` / `[]` 的覆盖模板；列表整体替换；
  - 值不是 `builtin`、或没有同名模板时报错；探测失败不移除；
  - web 改一个字段后写回只含差异，模板里的值不进文件；worker.yaml 同样生效。
- **lint**：
  - 默认 detect、与项目默认相同的字段被报告，与默认不同的不报告；
  - `--fix` 只删报告的行、其余逐字不变，前后生效配置一致，不一致时回滚；
  - extends 建议给出正确的最小写法。
- 真机：在 server 主机上对现有配置依次执行 `config lint`、`config lint --fix`、`config split`，每一步后 `config validate` + `serve reload`，并抽查 web 配置页保存一次。

## 六、后续（本期不做）

- 同一个段跨文件合并（例如每个项目一个文件 `projects.d/<key>.yaml`），需要先设计「新增条目写进哪个文件」的规则。
- `extends: <已声明的 agent>`：以另一个声明为底。
- web 配置页提供「拆分 / 去重」按钮（目前只在 CLI 做，避免远程改写主机文件结构）。

## 七、实施分期

1. T1 `include` 加载 + 来源映射 + 按文件写回 + reload 回执 `files`、`config info` / `config edit --key`。
2. T2 `gofer config split`。
3. T3 agent `extends: builtin`：加载合并、写回只保存差异、API 和 web 展示。
4. T4 `gofer config lint`（含 extends 建议）与 `--fix`。
5. T5 文档、示例配置、skill references、CHANGELOG。
6. T6 验证（Linux race、Windows 全量、web 三命令）+ 在 server 主机上对现有配置真机迁移，然后发版。

## 实施记录（2026-10-11）

- T1 include：`internal/config/include.go`
  - `decodeConfig` 合并片段，记录 `configSources`（段 → 文件）；`Save` 对拆分配置走 `saveSplit`，按文件分组执行同一套按段写回。
  - 对外：reload 回执 `files`、`config info` 文件列表、`config edit --key`、`GET /v1/config` 的 `sources`、配置页标注来源文件。
- T2 split：`internal/config/split.go`。按原文切片；第一个键上方的文件头注释留在主文件；已有 `include` 时追加条目。
- T3 extends：
  - `internal/config/extends.go`：`ApplyAgentExtends` 以 YAML 原文判断「写了哪些键」；`AgentConfig.MarshalYAML` 只写差异。
  - 内置模板由 agent 包在 `init` 中注册到 `config.BuiltinAgentTemplate`，做依赖倒置，避免 config 反向依赖 agent。
- T4 lint：新包 `internal/configlint`。
  - 用文本缩进扫描定位键与行范围。
  - 用 `config.LoadWithOverrides` 在内存里试删再加载。
  - 「生效配置」的口径：agent 走 `agent.ResolveAgent`，并补齐默认版本探测；项目子目录按生效值；忽略 `include` 与 `extends`，最后做 JSON 比较。
- 与设计的偏差：
  - lint 本期只检查 server 配置，没有做 `--target worker`。worker.yaml 的加载在 commands 包里，worker 配置一般也很短。
  - 新增「未知键」一类：删掉它确实不影响生效配置，但它更可能是写错了，所以只报告、不自动删除。
  - `--fix` 在逐个删除之后，还会去掉被删空的父键（如 `log:`）。
- 同批顺带完成：
  - omp 用量改为逐次请求累加（之前只取最后一次）。
  - 用量增加 `tool_calls`（gofer-z2c6）。
