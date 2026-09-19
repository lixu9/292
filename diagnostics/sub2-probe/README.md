# Sub2 / Cockpit 新票据对照

`main.go` 和 `codex292_diagnostic_bridge.go` 用来编译一次性 Sub2 诊断程序。它直接调用本地 Sub2 源码里的 `fireOpenAICodexTicketProbe`、身份头处理和 `repository.NewHTTPUpstream`，不连接数据库、不修改运行中的 Sub2 服务及其票据缓存。

`scripts/compare-292.py` 读取新版 Cockpit 的配置和测试账号凭证，在 Sub2 程序完成初始化后，同步发送开始信号并调用新版 Cockpit 的手动刷新接口。报告中的 `startSkewMs` 是四个请求开始时间的最大差值。只有本轮请求取得的新 292 才标记 `fresh292`；Cockpit 旧缓存的可用状态单独标记。

本机诊断程序：

```
~/Library/Caches/cockpit-292-build/sub2-codex292-diagnostic
```

本机源码副本：

```
~/Library/Caches/cockpit-292-build/sub2-probe-source
```

源码副本由用户最初下载的 Sub2 ZIP 解压，并覆盖当前本地已修改的源文件得到。原项目目录中的临时诊断文件已移除。重新编译时，把本目录两个 Go 文件分别放入副本的 `cmd/codex292-diagnostic/main.go` 和 `internal/service/codex292_diagnostic_bridge.go`，再运行 `go build -o ../sub2-codex292-diagnostic ./cmd/codex292-diagnostic`。

在 Cockpit 项目目录运行对照：

```sh
python3 scripts/compare-292.py \
  --account-id YOUR_COCKPIT_ACCOUNT_ID \
  --node-label node-2-round-1 \
  --sub2-probe ~/Library/Caches/cockpit-292-build/sub2-codex292-diagnostic \
  --output diagnostics/292-node-comparison.jsonl
```

每轮请求发出到完成期间保持代理节点不变。节点标签由操作者填写。脚本还会在取票前后，通过同一个专用代理访问 ChatGPT 域名的 CDN trace，记录出口 IP 的 SHA-256 截断标识和国家；不记录原始 IP。这能帮助核对出口变化，但如果代理每次连接都轮换 IP，前后观测不能证明四个取票连接共享同一出口。只允许账号池中保留所指定的一个测试账号。

报告只记录模型、HTTP 状态、票据长度、时间和缓存状态，不记录 OAuth 凭证或票据原文。Sub2 诊断请求收到的票据不写入旧服务；Cockpit 收到有效 292 时会按自身逻辑更新缓存。此测试比较 Sub2 原始取票代码与正在运行的新版 Cockpit；它不是对旧 Sub2 进程的强制刷新。
