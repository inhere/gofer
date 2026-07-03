# Web 配置查看/编辑（WEB-04③）使用 Runbook

> 配套 design [`../design/2026-07-03-web-config-write-design.md`](../design/2026-07-03-web-config-write-design.md) / plan [`../plans/2026-07-03-web-config-write-plan.md`](../plans/2026-07-03-web-config-write-plan.md)。写层身份分级与 [交互应答 runbook](2026-07-02-web-interaction-answer-runbook.md) 同源（caller token + 可选能力位）。

## 是什么

功能分两处（IA 调整后）：

- **系统配置只读总览** —— 独立 **`⚙ 设置`** 入口（顶栏末尾/侧栏，与内容分组分隔）：`GET /v1/config` 展示全量受管配置——server / callers / agents / runners / storage / governance 等。**所有 secret 只显示「已配置 / 未配置」徽标**（`*_set`），token/密钥的值与 env 变量名一律不出网；`agents`/`roles` 的 `env` 只列 **key 名**（`env_keys`）不列值。**纯只读**。
- **项目编辑（写）** —— 在 **`Projects` 页**（舰队组，读+写）：新增 / 编辑 / 删除项目映射 + 准入字段（`allowed_agents` / `allowed_runners` / `allow_exec` / `default_agent` / `max_concurrent_jobs` / 路径）。经 `project.Registry` 落**全局** `~/.config/gofer/config.yaml`，**live 生效**（无需重启，后续 job 提交即用新映射/准入）。

## V1 范围与边界（重要）

**只有「projects 域」可写**。以下**只读**（要改走文件编辑 + 重启 serve）：

- `server`（addr/token/callers/workers/metrics/governance/runner_probe/notification）——这些是 httpapi 启动快照，改了 **SIGHUP 不生效、必须重启进程**，且 callers/token 涉 secret。
- `agents` / `runners` / `roles`——`agents[].env` 可能含明文 secret，编辑需 secret 写入策略（V1.1）。
- **任何 secret 值**都不能经 Web 录入/修改（V1 写请求无 secret 字段）。改 token 请编辑 config.yaml 或用 `token_env` 指向的环境变量。

准入字段（`allowed_agents`/`allowed_runners`/`allow_exec`）真源恒在**全局** config（不进项目 overlay，配置简化 design D2）；Web 编辑正是落全局，合规。

## 写鉴权：can_admin 能力位

项目 create/update/delete 端点走 `can_admin` 能力闸，**opt-in、默认关闭**（=任何认证 caller 可编辑项目，向后兼容）。要把配置编辑收敛到 admin token：

```yaml
server:
  governance:
    require_admin_capability: true    # 开闸：仅 can_admin:true 的 caller 可写配置/项目
  callers:
    - id: web-admin
      token_env: GOFER_ADMIN_TOKEN
      can_admin: true                 # 可编辑配置/项目
      can_answer: true                # （按需）也可应答交互
    - id: web-op
      token_env: GOFER_WEBOP_TOKEN
      can_answer: true                # 只应答交互，不能改配置（无 can_admin）
```

- 开闸后，无 `can_admin` 的 caller 调写端点返回 **403**（前端就地提示，不崩页）。
- **防锁死**：开闸但无任何 caller `can_admin: true` 时，serve **加载即 fail-fast** 报错。
- 闸与 `can_answer` 相互独立：一个 caller 可同时具备或分别具备两种能力。

## 端点

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/v1/config` | authed | 全量配置脱敏视图（只读，任何认证 caller 可看） |
| POST | `/v1/projects` | authed + can_admin | 新增项目（重名 409） |
| PUT | `/v1/projects/{key}` | authed + can_admin | 编辑项目（upsert） |
| DELETE | `/v1/projects/{key}` | authed + can_admin | 删除项目（未知 404） |

写请求非法（空 key/host_path、引用未定义 agent/runner）→ **400 不落盘**；文件系统路径不存在等作**非致命告警**（`warnings`）随响应返回（路径可后置创建）。项目写操作经 `slog` 审计（含 caller_id + action + project_key）。

## 安全要点（SR403/805 对齐）

- secret 值**永不出网**：视图 bool 化、env 只列 key 名、写请求无 secret 字段。
- 写端点在 authed `/v1` group（bearer→caller_id），非匿名。
- 准入编辑只落全局 Registry（D2），不触项目 overlay。
- 落盘前引用校验拒非法，避免写坏 config 致 serve 下次重启起不来。

## 冒烟验证

1. 用具 `can_admin` 的 token 登录控制台。
2. `⚙ 设置` 页总览：确认 server/caller/agent 的 token 只显示「已配置/未配置」，无明文；agent env 只见 key 名。
3. `Projects` 页新增一个项目（填 host_path + 选 default_agent/allowed_agents）→ 保存 → 列表出现 → `GET /v1/projects/{key}` 可见。
4. 编辑其准入（如开 allow_exec）→ 保存 live 生效。
5. 删除 → 二次确认 → 列表移除。
6. （开闸时）用无 `can_admin` 的 token → 写操作 403 就地提示。
