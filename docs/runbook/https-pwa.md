# HTTPS 入口与 Android PWA Runbook

## 生成证书

在 gofer 仓库外或 `tmp/` 下保存证书文件，使用临时配置显式运行命令：

```sh
gofer tool cert -c ./tmp/gofer.yaml --out-dir ./tmp/certs --hosts gofer.local,192.168.1.20
```

首次运行会生成 `ca.crt`、`ca.key`、`server.crt`、`server.key`；再次运行会复用
已有 CA，只重新签发服务器证书。不要把这些文件加入 Git；`server.key` 只给服务进程
读取。

## 启用双监听

```yaml
server:
  addr: 0.0.0.0:8765
  tls:
    addr: 0.0.0.0:9443
    cert_file: ./tmp/certs/server.crt
    key_file: ./tmp/certs/server.key
```

重启使用临时配置的 gofer serve 后，HTTP `:8765` 继续给 CLI/worker 使用，HTTPS
`:9443` 给浏览器使用。服务启动输出只包含监听地址，不包含私钥内容。

## Android 验证

1. 将 `ca.crt` 传到 Android。
2. 打开「设置 → 安全 → 加密与凭据 → 安装证书 → CA 证书」，选择该文件并完成系统确认。
3. Chrome 打开 `https://<服务器IP>:9443`，确认页面和登录正常。
4. Chrome 菜单选择「安装应用」，确认 PWA 以独立窗口打开。
5. 用 `http://<服务器IP>:8765` 验证旧 HTTP 入口仍可用。

若证书提示主机名不匹配，重新运行 `gofer tool cert --hosts`，把访问使用的 IP 或
主机名加入 SAN，再重启服务。不要通过关闭浏览器证书校验绕过问题。
