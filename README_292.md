# Cockpit Tools 292 本地版

## 打开软件

本机已安装的版本：双击 `~/Applications/Cockpit Tools 292.app`。
重新构建的应用位于 `target/release/bundle/macos/Cockpit Tools 292.app`。

这个应用使用独立标识 `com.jlcodes.cockpit-tools.codex292`，默认数据目录为
`~/.antigravity_cockpit_dev`，可以与旧版同时打开。无需先停止旧 API。

## 查看票据

进入 **Codex → API 服务 → 服务总览 → 292 Codex 票据**。

- 本机新版 API：`http://localhost:1456/v1`，密钥从新版服务页获取。
- 本机取票代理：`socks5h://127.0.0.1:7890`，需要代理软件保持运行。
- 新旧服务可以共用取票代理，但同时运行的 API 服务需要不同端口。
- “292 缓存未过期”表示本地缓存符合条件；最终可用性仍以实际请求结果为准。
- 默认本地缓存时长为 3600 秒（1 小时），上游可能提前使票据失效。

## 把票据带到另一台电脑

两端都需要安装支持票据导入导出的 Cockpit 292 版本。文件格式与操作系统无关，
不需要把文件放进某个固定目录，也不要直接覆盖内部缓存文件。

1. 取票电脑进入 **Codex → API 服务 → 服务总览 → 292 Codex 票据**，点击 **导出票据**。
   导出当前 API 账号池内已缓存、未过期的票据，不发起新的取票请求。
2. 将生成的 `cockpit-292-时间.json` 文件传到自己的另一台电脑。
3. 接收电脑先导入或登录同一 ChatGPT 账号，并将账号加入本机 API 账号池。
   启动 API 服务，在票据面板启用 **292 票据**，配置相同模型，保存设置。
   如果只想使用导入的票据，勾选 **只使用导入票据（不自动取票）**，无需填写取票代理。
4. 点击 **导入票据**，选择传来的文件。结果会显示导入、保留已有票据、跳过的数量及原因。
   表格可查看来源、原取得时间和剩余时间。

导入通过 ChatGPT 账号 ID（以及文件中存在的邮箱）匹配已加入的账号，
不依赖另一台电脑的 Cockpit 本地账号 ID，也不会添加账号或替换登录凭证。
过期、账号不匹配、模型未启用和格式无效的票据会跳过；已有更新票据会保留。

**导入不会续期。** 例如 17:00 取得、18:00 到期的票据，17:40 导入后最多剩 20 分钟。
如果接收端的本地缓存时长更短，会使用更早的到期时间。
仅导入模式在票据到期后需要重新导入；普通模式继续按原设置自动取票。

文件含票据和用于匹配的账号标识，不含 OAuth 登录令牌或 API 密钥；票据本身仍属于敏感数据。
请只通过可信方式传给自己的设备。跨设备或不同出口下，上游是否接受仍需实际请求验证。
本机通过模拟两端不同本地账号 ID 验证了导入、注入和重启保留；尚未在另一台 Windows/Mac 上实测。

## 传输格式

文件版本为 `cockpit-codex-292/v1`。`tickets` 每项包含 `chatgptAccountId`、可选的 `email`、
`model`、`state`、`capturedAt` 和 `expiresAt`；时间戳均为 Unix 毫秒。
此文件不能直接使用 Sub2 的数据库或内部缓存格式。导入导出通过本机私有控制接口完成，
普通 API 密钥不能单独调用；票据原文不会进入前端状态或返回在导入结果中。

## 重新构建 macOS 应用

安装项目要求的 Node.js、Rust、Go 和 Xcode 命令行工具后，在项目目录运行：

```sh
VITE_COCKPIT_TOOLS_PROFILE=292 npm run tauri -- build --config src-tauri/tauri.292.conf.json --bundles app
```

这里构建 `.app`，无需制作 DMG。

## 本机整理后的目录

源码：`~/Projects/292-workspace/sources/cockpit-tools`。
发布包：`~/Projects/292-workspace/releases/cockpit`。
票据文件：`~/Projects/292-workspace/tickets`。
使用工作区 `tools/构建 Cockpit 292.command` 可加载已迁移的 Go / Rust 工具链重新构建。

已在本机界面验证导出 4 张、重复导入保留 4 张且不续期。Mac 文件选择窗口若 Return 没有打开文件，可选中文件后按 Command + ↓。

## Windows x64 安装包

当前本地 Windows 包：`~/Projects/292-workspace/releases/cockpit/windows-x64/Cockpit-Tools-292_1.3.57_windows-x64-setup.exe`。
目标为 `x86_64-pc-windows-msvc`，包含 Windows x64 版 292 sidecar；需在目标 Windows 10/11 设备实际验证。
通过工作区 `tools/构建 Cockpit 292 Windows.command` 可重建，结果在 `target/x86_64-pc-windows-msvc/release/bundle/nsis`。
本包未签名，后续使用手动安装更新。Windows ARM64 需另行构建。
