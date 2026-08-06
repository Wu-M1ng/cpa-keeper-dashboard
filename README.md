# CLIProxyAPI Usage Keeper

项目仓库：<https://github.com/Wu-M1ng/cpa-keeper-dashboard>

轻量化的 CLIProxyAPI 原生用量插件。它接收 CLIProxyAPI 已完成的 `UsageRecord`，用有界内存队列异步批写 SQLite，并提供五个内嵌页面：总览、分析、接口、事件、设置。

## 设计边界

- 只声明 `usage_plugin` 和 `management_api`。
- 不启动独立 HTTP 服务，不轮询 CPA，不使用 Redis。
- `usage.handle` 只做 JSON 解码、脱敏和非阻塞入队，不执行磁盘 I/O。
- SQLite 使用 WAL、短事务和 rollup 表；总览/分析查询不扫描事件正文。
- API Key 仅保存缩略显示值和 HMAC 分组值，失败文本会截断并清理常见密钥格式。
- 不使用响应拦截或流式拦截，因此不会处理每个响应 Chunk。

## 构建前提

CLIProxyAPI 原生插件需要 CGO。Windows 使用 MinGW-w64，Linux 使用 `gcc`/`musl-gcc`，macOS 使用 Xcode Command Line Tools。

```powershell
cd go
$env:CGO_ENABLED = "1"
$env:GOSUMDB = "off" # 仅在本机代理无法访问 sum.golang.org 时使用
go mod download
go test ./...
go vet ./...
go build -buildmode=c-shared -o usage-keeper.dll .
```

Linux/macOS 将输出文件名改为 `usage-keeper.so` 或 `usage-keeper.dylib`。

也可以使用：

```powershell
.\scripts\build.ps1 -Version 0.1.0
```

## 安装

把动态库放入 CPA 插件目录，例如 Windows：

```text
<cpa-workdir>/plugins/windows/amd64/usage-keeper.dll
```

在 `config.yaml` 中开启：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    usage-keeper:
      enabled: true
      priority: 10
      storage_enabled: true
      storage_path: "data/usage-keeper.db"
      queue_size: 2048
      batch_size: 64
      flush_interval_ms: 250
      retention_days: 30
      export_max_records: 50000
```

启动后验证：

```text
GET /v0/management/plugins
GET /v0/resource/plugins/usage-keeper/
```

管理中心需要显示 `registered: true` 与 `effective_enabled: true`。资源页面会出现在 CPA 管理中心的插件菜单中。

## 五个页面

| 页面 | 内容 |
| --- | --- |
| 总览 | KPI、健康监测、请求/Token/费用趋势、队列状态 |
| 分析 | 模型、上游、API Key、来源分布、Token 构成、模型统计 |
| 接口 | API Key 统计、上游统计、Provider/Auth 上游详情 |
| 事件 | 请求明细、时间/模型/Provider/状态筛选、分页、CSV 导出 |
| 设置 | 模型价格、SQLite/WAL/队列状态、JSON 备份与恢复 |

## 低负载策略

`usage.handle` 的路径如下：

```text
UsageRecord -> compact event -> bounded channel -> return
                                      |
                                      v
                              one SQLite writer
```

队列满时记录 `dropped` 计数并立即返回；未刷新批次在进程突然退出时可能丢失，这是避免阻塞 API 完成路径的明确取舍。后台默认每 64 条或 250 ms 写一次。

## 发布

```powershell
.\scripts\build.ps1 -Version 0.1.0 -GoOS windows -GoArch amd64
```

推送语义化版本标签后，GitHub Actions 会自动构建 Windows/Linux/macOS 动态库，打包 zip，生成统一的 `checksums.txt`，并创建 GitHub Release：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

发布 zip 根目录直接包含动态库。`registry.json` 是插件商店条目，仓库地址已指向本项目。
