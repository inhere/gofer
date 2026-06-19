# P0 Spike：coder/websocket × gookit/rux `Accept` 验证（硬门）

> 主计划（契约真源）：[`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md)（§4 包布局 / §5 帧表 / §7 鉴权 / §9 跨阶段约束）。
> 设计依据：[`../../design/2026-06-17-ws-remote-worker-design.md`](../../design/2026-06-17-ws-remote-worker-design.md) §10（模块/依赖）、§13（部署/安全）、§17 评审 **#6**（本 spike 是 WP1 的**硬门**前置）/ #7（read deadline）。
> SSE 先例（证明 rux 上 Flush + 不二次写 header 可行）：[`../../../internal/httpapi/stream_handler.go`](../../../internal/httpapi/stream_handler.go)。

## 1. 修订记录

| 版本 | 日期 | 修改人 | 调整说明 |
|---|---|---|---|
| v0.1 | 2026-06-19 | Claude | 初版：P0 spike 计划。锁定 coder/websocket v1.8.15 pin、`Accept` options（origin/压缩/读上限）、rux 包装器二次写 header 验证点、register→result 单连回环冒烟用例归属。 |

## 2. 目标与范围

**唯一目标**：在写一行 WP1 业务代码前，用最小可运行证据证明下列五点同时成立——否则 WP1 选型/装配方式需重定（评审 #6）。

1. `github.com/coder/websocket` 的 `Accept(c.Resp, c.Req, opts)` 能**穿过 rux 的 `*responseWriter` 包装器**完成 WS 升级握手，返回一条活连接。
2. 一条连接上**双向 JSON 帧**收发正常（worker→hub、hub→worker）。
3. 升级/Hijack 后，rux 在 dispatch 链尾的 `defer ctx.writer.ensureWriteHeader()` **不二次写 status/body**（无 `superfluous WriteHeader` 警告、无 panic）。
4. **干净关闭**：`conn.Close(StatusNormalClosure, ...)` + ctx 取消能双向传播、读循环正常退出。
5. 在同一条连接上跑通**最小业务回环**：`register → registered → dispatch → result`（即 §5 帧表 WP1 子集的最小切片）。

**范围内**：1 个 rux 路由 + handler（hub 侧 `Accept`）+ 1 个 client（worker 侧 `Dial`）+ 1 个回环测试；4 项构建门（build/vet/test/-race）。

**范围外**：sink 生命周期/有序、背压/HOL、心跳/重连、per-worker token 绑定校验、多 worker、cancel/interaction/ping 帧——全部留给 WP1+（本 spike 只验"管子通不通"，不验业务正确性）。

## 3. 关键事实（已核验，实现者据此落地，勿再猜）

> 下列均已读源码确认，是本 spike 设计的依据：

- **rux `c.Resp` 是包装器，不是原始 writer**：`Context.Init` 内 `c.Resp = &c.writer`（`internal/core/context.go:48`），`c.writer` 是 `responseWriter`。`Accept` 拿到的是 `*responseWriter`。
- **包装器实现了 `Hijack()` 和 `Flush()`**：`internal/core/response_writer.go:46-55`。`Hijack()` 内 `if w.length < 0 { w.length = 0 }` 后转发到底层 `http.Hijacker`——**这一步把 `Written()` 翻为 true**（`Written() = length != noWritten(-1)`，见 `:28`）。
- **dispatch 链尾的二次写防护**：`Router.handle` 有 `defer ctx.writer.ensureWriteHeader()`（`internal/core/dispatch.go:102`）；`ensureWriteHeader` 被 `if !w.Written()` 守卫（`response_writer.go:59-67`）。**因此只要 Accept 路径触发过 Hijack（length 置 0），链尾 defer 即 no-op，不会二次写。** 这正是 SSE 先例靠"先写 `: open\n\n` 提交 status"达成的同款不变量（`stream_handler.go:106-113` 注释明说）。
- **coder/websocket v1.8.15 已在本机 module download cache**：`/workspace/go/pkg/mod/cache/download/github.com/coder/websocket/@v/v1.8.15.{info,mod}`，与设计 §10 引用一致。纯 Go / 无 cgo（保住"无 gcc 也能构建"属性）。
- **路由挂载点**：`buildRouter` 在 `internal/httpapi/server.go:126-167`，`/v1` group 用 `s.authMiddleware` 守卫（`:131-148`）。spike 路由先**裸挂**（不接 auth），鉴权绑定到 WP1。
- **现有 internal 包**：`internal/` 下已有 `httpapi/job/runner/...`，**尚无 `wshub`**（待 WP1 新建，§4）。

## 4. 步骤

### 步骤 0：构建环境（每个 shell 会话先执行）

```bash
export PATH=/path/to/ws-root/linux-env/sdk/gosdk/go1.25.10/bin:$PATH
cd /path/to/workspace/tools/gofer
go version   # 期望 go1.25.x
```

> 所有 `go build/vet/test` 均在 `tools/gofer` 目录下、且已 export 上述 PATH 后执行。

### 步骤 1：加依赖（pin v1.8.15）

```bash
go get github.com/coder/websocket@v1.8.15
go mod tidy
```

- 版本 pin = **v1.8.15**（与设计 §10 / download cache 一致，避免漂移）。
- 验证纯 Go / 无 cgo：`go list -deps github.com/coder/websocket` 不应引入 cgo 依赖；`go.mod` 新增一行 `require github.com/coder/websocket v1.8.15`。
- **决策落点**：此 pin 一旦本 spike PASS，即为 WP1 起的正式版本（§6）。

### 步骤 2：spike 文件归属与命名（KEEPABLE 冒烟）

本 spike **写成可保留的冒烟测试**（不是丢弃式脚本），作为后续 `wshub` 的回归基线：

- **新建包目录**：`internal/wshub/`（WP1 hub 侧落点，主计划 §4）。
- **spike 测试文件**：`internal/wshub/spike_test.go`
- **顶层测试函数名**：`TestSpike_AcceptLoopbackOverRux`
- **子测试（`t.Run`）**：`accept_handshake` / `register_result_loopback` / `no_double_write` / `flush_streaming` / `clean_close`
- **内部最小帧类型**：spike 内**本地声明**最小 envelope（`type frame struct{ Type, JobID string; ... }`），**不预先落 `wsproto`**——`wsproto` 全量帧表是 WP1 的事（§5），spike 只用 register/registered/dispatch/result 四种最小切片，避免 spike 阶段就锁死协议细节。

> 选 `internal/wshub/` 而非独立 `spike/` 包：测试随 WP1 hub 代码同目录沉淀，WP1 起手即有一个"管子通不通"的回归用例；若 WP1 发现需挪走再说。是否最终保留/晋升为 hub 正式冒烟，见 §6 决策项。

### 步骤 3：hub 侧 handler（穿过 rux 的 `Accept`）

伪代码 sketch（**仅勾勒形态，非最终实现**），落在 `spike_test.go` 内（或同包 `spike.go` helper）：

```go
// 路由：GET /v1/workers/connect —— spike 阶段裸挂，不接 authMiddleware
func spikeConnectHandler(c *rux.Context) {
    conn, err := websocket.Accept(c.Resp, c.Req, &websocket.AcceptOptions{
        // 关键 #b：worker 是非浏览器客户端，必须显式放开 origin，
        // 否则 coder/websocket 默认 origin 校验会拒绝握手。
        InsecureSkipVerify: true,                 // 或 OriginPatterns: []string{...}
        CompressionMode:    websocket.CompressionDisabled, // spike 关压缩，简化
    })
    if err != nil {
        return // Accept 失败已自行写过握手响应
    }
    defer conn.Close(websocket.StatusInternalError, "spike defer")

    ctx := c.Req.Context()
    conn.SetReadLimit(1 << 20) // 关键 #d 旁证：显式读上限（WP1 会调大/可配）

    // 1) 读 register
    var reg frame
    if err := wsjson.Read(ctx, conn, &reg); err != nil { return }
    // 2) 回 registered
    _ = wsjson.Write(ctx, conn, frame{Type: "registered" /*accepted:true*/})
    // 3) 发 dispatch
    _ = wsjson.Write(ctx, conn, frame{Type: "dispatch", JobID: "j1"})
    // 4) 读 result
    var res frame
    if err := wsjson.Read(ctx, conn, &res); err != nil { return }

    conn.Close(websocket.StatusNormalClosure, "done") // 关键 #e 干净关闭
}
```

挂载（测试内用 `httptest.NewServer(server.Handler())` 或直接 `rux.New()` + 该路由）：

```go
r := rux.New()
r.GET("/v1/workers/connect", spikeConnectHandler)
ts := httptest.NewServer(r)
defer ts.Close()
```

> 用真实 `httptest.NewServer`（而非 `httptest.NewRecorder`）——Recorder **不支持 Hijack**，无法验证握手（SSE 先例同理：`stream_handler.go:95-98` 对非 Flusher 直接 bail）。

### 步骤 4：worker 侧 client（`Dial`）

```go
wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/workers/connect"
conn, _, err := websocket.Dial(ctx, wsURL, nil) // spike 不带 token，鉴权留 WP1
// 1) 发 register
_ = wsjson.Write(ctx, conn, frame{Type: "register" /*worker_id:"laptop-01"*/})
// 2) 读 registered → 断言 accepted
// 3) 读 dispatch → 断言 job_id=="j1"
// 4) 回 result{job_id:"j1", status:"success", exit_code:0}
_ = wsjson.Write(ctx, conn, frame{Type: "result", JobID: "j1" /*status:success*/})
conn.Close(websocket.StatusNormalClosure, "client done")
```

### 步骤 5：跑门（按序）

```bash
go build ./...
go vet ./...
go test ./internal/wshub/ -run TestSpike_AcceptLoopbackOverRux -v
go test ./internal/wshub/ -run TestSpike_AcceptLoopbackOverRux -race -count=1
```

- 任一非绿 → 不进 WP1，回到 §3 关键事实逐条排查（最可能是 #b origin 或 #c 二次写）。
- `-race` 必须干净（hub handler 与 client 各跑一条 goroutine，存在并发读写）。

## 5. 风险与验证点（逐条转为 check）

> 每条都要在 spike 测试里有对应断言/观测；§7 表格回填即据此。

| # | 风险 | 验证方式（check） | 期望 |
|---|---|---|---|
| a | `Accept` 穿不过 rux 包装器（拿不到底层 Hijacker） | `websocket.Accept(c.Resp, c.Req, opts)` 返回 `err==nil` 且 `conn != nil` | 握手成功；包装器 `Hijack()` 透传到底层（`response_writer.go:50-55`） |
| b | **origin 默认校验拒绝非浏览器 worker** | 必须设 `InsecureSkipVerify:true`（或 `OriginPatterns`）；**额外跑一个反例**：不设时断言 `Accept` 返回 error / 握手 403 | 设了 → 握手 OK；不设 → 被拒（证明该 option 是必需项，写进 WP1） |
| c | **rux 链尾二次写 status/body**（`superfluous WriteHeader`） | 断言无 panic；捕获 `log`/`stderr` 无 `superfluous response.WriteHeader` 字样；handler 返回后底层连接已被 Hijack（`StatusCode()` 反映 101 而非 200 覆盖） | 无二次写。机理：Hijack 置 `length=0` → `Written()`=true → 链尾 `ensureWriteHeader` no-op（`dispatch.go:102` + `response_writer.go:59-67`） |
| d | Flush/streaming 在包装器上失效 | 复用 SSE 先例事实：包装器 `Flush()` 透传（`response_writer.go:46-48`）；spike 中帧能即时收到即旁证（WS 写本身不依赖应用层 Flush，但确认 `c.Resp` 实现 `http.Flusher` 仍成立） | Flush 可用（与 `stream_handler.go` 一致），无降级 |
| e | 干净关闭 / ctx 取消不传播 | client `Close(StatusNormalClosure)` 后 hub 侧 `wsjson.Read` 返回 close error；测试用带 timeout 的 ctx，超时取消时两侧读循环退出、`go test` 不挂死 | 双向感知关闭；测试在 deadline 内自然结束 |

## 6. 本 spike 锁定的决策（PASS 后即为 WP1 既定事实）

1. **库版本 pin**：`github.com/coder/websocket v1.8.15`（纯 Go / 无 cgo）。
2. **`Accept` options（WP1 起手默认）**：
   - origin 策略 = `InsecureSkipVerify: true`（worker 非浏览器、纯出站；生产由 `wss://` + per-worker token 兜安全，见主计划 §7/§9.3）。若后续要收紧改 `OriginPatterns`，在 WP1 决定，但 spike 已证明二者皆可放行握手。
   - 压缩 = `CompressionDisabled`（spike 关；WP1 视日志量再评估开启 `CompressionContextTakeover`）。
   - 读上限 = `SetReadLimit`（spike 取 1 MiB 占位；WP1 按 §9.2 背压/帧上限对齐 C4 的 `maxSSEFrameBytes` 调定）。
3. **冒烟用例去留**：spike 测试**保留**在 `internal/wshub/spike_test.go`，WP1 起手时晋升/扩展为 hub 的握手回归用例（不删；若 WP1 重组目录再迁移并在文件头留迁移记录）。
4. **协议落点时序**：spike **不**落 `wsproto`，只用本地最小 frame；`wsproto` 全量帧（§5 表）由 WP1 一次性定全（评审 #6：避免破坏性改协议）。

## 7. 验收 / 退出标准（→ WP1 硬门）

**全部满足才放行 WP1**（任一 FAIL 阻断）：

- [ ] `go get github.com/coder/websocket@v1.8.15` 成功，`go.mod` 已 pin，`go mod tidy` 干净。
- [ ] 握手 OK：`Accept` 穿过 rux 包装器返回活连接（check a）。
- [ ] origin option 必需性已证（设/不设的两路断言，check b）。
- [ ] **无二次写**：无 `superfluous WriteHeader`、无 panic（check c）。
- [ ] Flush/streaming 不受影响（check d，与 SSE 先例一致）。
- [ ] 干净关闭 + ctx 取消传播，测试不挂死（check e）。
- [ ] 完整业务回环跑通：`register → registered → dispatch → result`（§2.5）。
- [ ] `go build ./...` / `go vet ./...` 绿；`go test ... -race -count=1` 绿。

### 7.1 spike 结果（运行后回填，PASS 前不得开 WP1）

> 实施者跑完步骤 5 后回填本表；全 PASS 才在主计划 §10 勾选 P0 并进 WP1。

| 检查项 | 结果（PASS/FAIL） | 证据 / 备注 |
|---|---|---|
| 依赖 pin v1.8.15 + 纯 Go | _待回填_ | |
| a 握手穿过 rux 包装器 | _待回填_ | |
| b origin option 必需性（设/不设） | _待回填_ | |
| c 无二次写 / 无 panic | _待回填_ | |
| d Flush/streaming | _待回填_ | |
| e 干净关闭 / ctx 取消 | _待回填_ | |
| register→result 回环 | _待回填_ | |
| build / vet / test / -race | _待回填_ | |

**结论（回填）**：_PASS → 进 WP1 / FAIL → 阻断原因 + 处置_

---

> 完成后：在主计划 [`./2026-06-19-ws-worker-c6c7-plan.md`](./2026-06-19-ws-worker-c6c7-plan.md) §10 勾选「P0 Spike」并回填提交哈希（SR1201/SR1202）。
