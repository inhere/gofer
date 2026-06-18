# TODO

- [x] 新增 AGENT_BRIDGE_CFG_DIR 自定义全局的配置目录(不设置默认还是 ~/.config/dev-agent-bridge/)
  - `config.ConfigDir()` 统一解析；`UserConfigPath()` 复用；config.yaml 与全局 .env 都落在该目录下。
- [x] 使用 goutil/envutil 支持先于配置加载 .env 文件
  - `config.LoadDotenv()` 在 main 启动最早期调用；先加载 `<cfg-dir>/.env` 再加载当前目录 `./.env`(后者覆盖前者)；已导出的 OS env 始终优先。
- [ ] 项目名(暂缓，沿用 dev-agent-bridge / AGENT_BRIDGE_ 前缀)：
  - Ferry （渡船 / 摆渡人） 含义：把 prompt/命令“摆渡”到目标项目目录里执行，再把日志/结果“摆渡”回来。完美替代“bridge”的动态感（bridge是静态的，ferry是主动往返的）
  - Convey（输送 / 传达） 含义：强调“输送任务 + 回传结果”的管道感，同时带有“传送指令”的语义，非常贴合 {项目, agent, prompt} 的提交模式。
- [ ] 支持远端机器运行作为客户端与server通信，暂定使用 ws 协议通信并保持连接
  - 客户端也有任务记录信息
  - 任务输出详情在客户端，server 如何读取？
  - 思考其他点 或者 问题？
