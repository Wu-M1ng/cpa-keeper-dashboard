# CLIProxyAPI Usage Keeper

项目仓库：<https://github.com/Wu-M1ng/cpa-keeper-dashboard>

轻量化的 CLIProxyAPI 原生用量插件。它接收 CLIProxyAPI 已完成的 `UsageRecord`，用有界内存队列异步批写 SQLite，并提供三个内嵌页面：总览、接口、设置。

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
.\scripts\build.ps1 -Version 1.4.7
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
      queue_size: 256
      batch_size: 64
      flush_interval_ms: 250
      retention_days: 30
      export_max_records: 50000
```

启动后验证：

```text
GET /v0/management/plugins
GET /v0/resource/plugins/usage-keeper/dashboard
```

管理中心需要显示 `registered: true` 与 `effective_enabled: true`，并在该插件的 `menus` 数组中看到：

```json
{
  "path": "/v0/resource/plugins/usage-keeper/dashboard",
  "menu": "用量 Keeper"
}
```

资源页面会出现在 CPA 管理中心的插件菜单中。更新 DLL/SO 后需要重启 CPA，或执行一次插件配置重载，让宿主重新调用 `management.register`。

## 三个页面

| 页面 | 内容 |
| --- | --- |
| 总览 | KPI、健康监测、请求/Token/费用趋势、队列状态、四个分布图、Token 构成、模型统计、请求明细、筛选、分页与 CSV 导出 |
| 接口 | API Key 统计、上游统计、Provider/Auth 上游详情 |
| 设置 | 模型价格、SQLite/WAL/队列状态、JSON 备份与恢复 |

## Dashboard 呈现契约

- 顶部导航固定为“总览 / 接口 / 设置”；分析与事件继续合并在总览页，所有现有 ID、筛选、分页、导出和详情抽屉保持可用。
- 总览按“指标带 -> 运行脉搏 -> 分析构成 -> 请求明细”分层呈现。趋势图仅绘制已加载的有用量点，横坐标按真实时间戳定位，悬停使用参考线和单帧更新。
- 分布图的百分比以当前范围全量请求为分母，Top 5 之外聚合为“其他”，避免把 Top 5 误报为全量占比；Token 和表格数值统一使用 K/M，完整数值保留在 title/Tooltip 中。
- 页面只读取 CPA 宿主的 `data-theme` / `data-cpa-theme`，不写入插件主题偏好。页面隐藏时停止刷新，页面可见且前端缓存过期后才按 `summary -> analysis -> events` 分阶段请求。
- CSS 为嵌入式单文件，不加载外部字体、图表库或图片；移动端请求明细使用 `data-label` 网格布局，避免页面级横向滚动。

## 低负载策略

`usage.handle` 的路径如下：

```text
UsageRecord -> compact event -> bounded channel -> return
                                      |
                                      v
                              one SQLite writer
```

队列默认容纳 256 条事件。队列满时在构造完整事件前记录 `dropped` 计数并立即返回；未刷新批次在进程突然退出时可能丢失，这是避免阻塞 API 完成路径的明确取舍。后台默认每 64 条或 250 ms 写一次。

Dashboard 仅在页面可见且 60 秒前端缓存失效时刷新。聚合 Management API 使用 4 秒、32 条目、4 MiB 上限的进程内缓存，并合并相同 Key 的并发查询。SQLite 最多 4 个连接，每个连接约 8 MiB 页缓存，临时排序写入临时文件。

## 发布

```powershell
.\scripts\build.ps1 -Version 1.4.7 -GoOS windows -GoArch amd64
```

推送语义化版本标签后，GitHub Actions 会自动构建 Windows/Linux/macOS 动态库，打包 zip，生成统一的 `checksums.txt`，并创建 GitHub Release：

```powershell
git tag v1.4.7
git push origin v1.4.7
```

也可在 GitHub Actions 手动运行 `release` 工作流并输入版本标签，例如 `v1.4.7`。工作流会检查远端 tag：不存在时在当前提交创建并推送，存在时直接复用；对应 GitHub Release 已存在时会更新同名发布资产。

发布 zip 根目录直接包含动态库。`registry.json` 是插件商店条目，仓库地址已指向本项目。
