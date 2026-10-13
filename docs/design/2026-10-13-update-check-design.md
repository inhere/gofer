# 检查更新设计

- issue：gofer-6x2n
- 状态：实施中（2026-10-13）

## 一、目标

- web「设置 → 关于」能看到当前版本、最新版本，并手动「检查更新」。
- server 定时（默认每 2 小时）检查一次，有新版本时在关于页与日志里可见。
- 默认从 GitHub 的最新 release 查；也可以配置内部 mirror（`make build-all` 生成的 `latest.yaml` 与各平台二进制）。
- 只**检查**，不自动下载或升级；一键更新见 [一键更新设计](2026-10-13-update-apply-design.md)（gofer-29gi）。

## 二、配置

```yaml
server:
  update:
    enabled: true            # 定时检查；默认 true（false 只关定时，手动检查仍可用）
    interval: 2h             # 定时检查间隔，最短 10m
    proxy: ""                # 访问更新源用的代理；空 = 跟随 HTTPS_PROXY 等环境变量
    # 默认来源：GitHub 最新 release
    repo: inhere/gofer
    # 内部 mirror：配了 latest_url 就用 mirror
    latest_url: "http://mirror.example.com/tools/{name}/latest.yaml"
    url_template: "http://mirror.example.com/tools/{name}/{name}-{os}-{arch}{ext}"
```

- 占位符：`{name}`（`gofer`）、`{os}` / `{arch}`（server 自己的 GOOS / GOARCH）、`{ext}`（Windows 为 `.exe`，否则为空）、`{version}`（最新版本号，只在 `url_template` 里有意义）。
- `latest.yaml` 即 `make latest-yaml` 的产物：`name` / `version` / `released_at` / `description`。
- **只能改配置文件**：server 会按配置去请求这些地址并把响应展示出来，开放编辑就等于让调用方指挥 server 访问任意内网地址。改完 `gofer serve reload` 生效。

## 三、行为

- server 启动约 1 分钟后检查一次，之后按 `interval` 定时检查；结果缓存在内存里。
- GitHub：`GET https://api.github.com/repos/{repo}/releases/latest`，取 `tag_name`（去掉前缀 `v`）、`html_url`、`published_at`；下载链接取名字以 `{name}-{os}-{arch}` 开头的 asset。
- mirror：`GET latest_url`（YAML），下载链接按 `url_template` 展开——指向 `make build-all` 产出的裸二进制，不是压缩包（GitHub 的 release 资产才是压缩包）。
- 版本比较按 `X.Y.Z` 数字比较；当前版本是 `git describe` 形式的开发构建（`0.152.3-2-gabc`）时视为高于 `0.152.3`。
- 发现新版本（与上次检查结果相比第一次看到这个版本）时 serve 日志记 `update.available`。
- 失败（网络、解析）只记在结果的 `error` 里，不影响任何其他功能。

## 四、接口

- `GET /v1/update`：最近一次结果 `{current, latest, newer, source, checked_at, released_at, release_url, download_url, description, error}`；从未检查过时 `checked_at` 为 0。
- `POST /v1/update/check`：立即检查一次并返回结果（job 凭据不可调用）。

## 五、web

关于页在「服务端」下方加「更新」区块：最新版本、是否有新版本、检查时间、来源、下载链接 / release 页面、错误；按钮「检查更新」调用 `POST /v1/update/check`。
