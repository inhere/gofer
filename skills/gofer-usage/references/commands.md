# gofer 命令参考（`job` 之外）

> 主 `SKILL.md` 详讲最常用的 `gofer job`。本文补齐其余命令，**按需查阅**——AI 要用 workflow / plan / schedule 等时读这里。
> 每个命令的**完整 flag** 用 `gofer <cmd> --help`（本文只给"是什么 + 常用法 + 何时用"）。
> 命令通过主机 server 执行；连接、project key、agent/runner 的通用规则见 `SKILL.md`。

## workflow（别名 `wf`）— job 链（有依赖的多步编排）

把多个 step 串成一条链，step 间可有依赖 / fan-out / join。**从文件提交**（title + `steps[]`）：

```bash
gofer workflow run <file.yaml> [-w]     # 从 yaml/json 提交; -w=轮询到终态并打印每步
gofer workflow list                     # 列 workflow(可带状态过滤)
gofer workflow show <id>                # 状态 + step 链
gofer workflow events <id>              # 生命周期事件时间线
gofer workflow cancel <id>              # 取消运行中的
gofer workflow export <id>              # 导出 spec(去密钥)可再 import, 默认 yaml(= run 格式)
```

- 文件格式：`.json` 按 json，其余按 yaml；顶层 `title` + `steps: [...]`。先 `export` 一个跑通的当模板最快。
- **何时用**：多步、有依赖/并发的编排（先 build → 再 test → 再 deploy）。**单发命令**用 `job`，别套 workflow。

## plan — job 分组计划（组织 + 跟踪，不是执行编排）

把相关 job 归到一个 plan 下、附带 todo 清单跟踪进度（**不**决定执行顺序，只做归类/看板）：

```bash
gofer plan create --title "<标题>" [--desc "<说明>"] [--plan-id <id>]
gofer plan attach <job-id> <plan-id>    # 把已有 job 挂到 plan
gofer plan list / show <id> / archive <id>
gofer plan add-todo <id> "<待办>" [--note "<备注>"]       # 加 todo(别名 todo-add)
gofer plan set-todo <todo-id> [--status doing|done|skipped|pending] [--note "<结果>"] [--append-note "<追加一行>"]
    # 生命周期推进：--status doing 自动记开始时间, done/skipped 记完结时间;
    # 裸调用=done, --undone=pending(旧用法兼容); --note 单独用只改备注;
    # --append-note 追加一行到现有备注(与 --note 互斥, 服务端原子追加)
gofer plan set-status <id> <status>
```

- **workflow vs plan**：workflow = **执行**依赖链（server 按链跑）；plan = **组织** view（把散 job + todo 归一起看）。

### 范式：长任务进度跟进（人不在电脑前也能看）

在任何 AI 会话里执行**多步骤长任务**（大改造 / 迁移 / 分阶段实施）时，把 plan 当进度看板用——每步经 CLI 或 gofer MCP 工具（`gofer_create_plan` / `gofer_add_todo` / `gofer_update_todo`）汇报，web 控制台 Plan 详情页（手机可开）就是实时进度页：

```bash
# 开工: 建计划, 每个步骤一个 todo
gofer plan create --title "xxx 改造实施"
gofer plan add-todo <plan-id> "步骤1: 数据模型迁移"
gofer plan add-todo <plan-id> "步骤2: API 扩展"
# 每步开始 / 完成时:
gofer plan set-todo <todo-id> --status doing
gofer plan set-todo <todo-id> --status done --note "迁移完成, 单测绿"
# 某步决定不做:
gofer plan set-todo <todo-id> --status skipped --note "原因..."
```

要点：note 写**结果/验收一句话**（不是过程流水，过程在 job logs）；跑长命令的步骤尽量用 `gofer job run` 执行并 `--plan <id>` 或 `plan attach` 挂进来，进度页可直接点进日志。

### 范式：决策点问人（gofer_ask_human）

大计划跑到**需要人拍板的分叉**时，agent 经 MCP 工具 `gofer_ask_human` **阻塞提问**；人在 web 铃铛 / Plan 详情页作答，答案从工具返回值流回，会话原地继续（设计 §C3）：

```
# agent 会话里(MCP 工具, 阻塞等待):
gofer_ask_human{plan_id, title, question, options: ["方案A","方案B"], timeout_sec: 1800}
# → 返回 {state:"answered", answer, answered_by} 或 {state:"expired"}

# 人这一侧(CLI 等价面, 不阻塞; web 更常用):
gofer plan ask --plan <id> --title "..." --question "..." [--option A --option B] [--timeout 30m]
gofer plan decisions --state OPEN
gofer plan answer <decision-id> --answer "方案A"
```

要点：

- **超时兜底在 agent 侧**：收到 `{state:"expired"}` 后按预案继续（执行推荐项），或把该步 todo 置 `skipped` + note 说明后跳过——**不无限阻塞、不原地重问**。
- `timeout_sec` 缺省 1800s（clamp `[2s, 24h]`）。按**宿主客户端的 tool 调用超时上限**设定：宿主若先杀调用，decision 留 OPEN、到期自动 EXPIRED，通道本身无错。
- **决策点串行提问是范式建议**（宿主客户端可能串行执行 tool call），**不是** MCP 连接限制——go-sdk 服务端并发执行 tool call，ask 阻塞不排队其他调用。

## session（别名 `sess`）— 终端会话中继（web ↔ 终端）

终端里的 Claude Code / Codex 会话经 hooks 登记到 server；会话的 **relay 开关**打开时，Stop hook 把 agent 最后一条消息发成一个 turn 并阻塞等待，人在 web「会话」页 / 铃铛 / CLI 作答，答案经 `decision: block` 注入同一会话继续（设计 SESS-01）。

```bash
gofer init hooks [--agent claude|codex|all] [--global] [--remove] [--force]
#   合并写 ./.claude/settings.json 与/或 ./.codex/hooks.json(--global 写 ~/); 幂等、只增删 `gofer hook` 自己的条目
#   Codex 另需 config.toml [features] hooks = true(旧名 codex_hooks), 且项目 .codex/ 需 trust
gofer session ls [-p <project>] [--state waiting_reply] [--all]   # 列会话(waiting_reply/needs_attention 置顶)
gofer session show <id>                 # 详情 + 最近 turn(id 可用前 8 位)
gofer session relay on|off [--session <id>]   # 省略 --session: 按当前目录反查(歧义时列出候选)
gofer session say <id> "<回复>"         # 答最新 OPEN turn; "/off" = 关中继放行
gofer session rm <id>                   # 移除登记(turn 保留)
gofer hook claude|codex [--wait N]      # hook 执行体(由 hooks 配置调用, 人不直接用); 日志 <config-dir>/run/hook.log
```

要点：

- 开关在 server、按会话；hook 每次 Stop 先查开关，关着零阻塞，server 不可达也直接放行，**永不卡死终端**。
- `UserPromptSubmit`（人在终端输入）自动把 relay 关掉；web 注入的回复虽也触发该事件，但带 `[gofer web 回复]` 前缀，hook 上报 injected，不会误关。
- Stop hook 等待期间终端显示 hook 运行中；人回到电脑想直接输入可按 Esc 取消。
- 硬边界：会话已停在空闲提示符时没有 hook 进程活着，web 拨开开关要等下一次 Stop；需终端输入一次。
- turn 复用决策通道：铃铛里「会话」标签条目可直接内联作答；`gofer plan decisions --state OPEN` 也能看到（kind=relay）。

## schedule（别名 `sch`）— 定时 job

```bash
gofer schedule add <...job请求...>      # 从一个 job 请求建定时计划
gofer schedule list / show <id>
gofer schedule enable <id> / disable <id>
gofer schedule run <id>                 # 立即跑一次(不等下次触发)
gofer schedule rm <id>
```

## project（别名 `p` / `proj`）

```bash
gofer project list [--remote]           # 不带=本地(按 GOFER_RUN_MODE 读 config.yaml/worker.yaml); --remote=server 实时 project
gofer project show <key>                # project 详情
gofer project validate <key>            # 校验路径/agent/runner(别名 check)
gofer project add / remove <key>        # 注册 / 移除
```

- POLICY 模式 worker 上 `gofer project list` 读 server 下发的策略缓存，列**映射后的本机路径**（见 `SKILL.md` §6）。

## config（别名 `cfg`）

```bash
gofer config info                       # 解析出的 config 路径 + 关键 ENV + 关键设置
gofer config show <project>             # 某 project overlay 合并后的有效 config
gofer config validate server|worker     # 校验 config(别名 check); worker 按模式给判据 + 校验 roots
gofer config edit                       # 用 $VISUAL/$EDITOR 打开解析出的 config
```

## init — 脚手架

```bash
gofer init [server|worker]              # 从内置 example 模板生成 config(默认 server)
gofer init -g worker                    # 写到用户全局 config 目录
gofer init [-g] skill                   # 装 gofer-usage skill: 默认写 ./.claude/skills 和 ./.agents/skills 两处; -g 写全局 ~/.claude+~/.agents; -o <dir> 单目标
```

`gofer init hooks [--agent claude|codex|all] [--global] [--remove]`：安装/卸载会话中继 hooks（见上文 session 节）。

## 运维向（AI 一般不直接用，了解即可）

- `serve` / `worker` / `presence`：起 server / worker、看在线状态。worker 配置见 [`worker-config.md`](worker-config.md)、server 配置见 [`server-config.md`](server-config.md)、加 project / 建 worker / 迁移分步见 [`setup-recipes.md`](setup-recipes.md)。
- `agent` / `mcp`：agent 定义探测、MCP 接入。
