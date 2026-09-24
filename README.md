# CLIProxyAPI Usage Keeper

CLIProxyAPI 的轻量级用量记录与分析插件。插件接收 CLIProxyAPI 已完成的 `UsageRecord`，通过内存队列异步写入 SQLite，并提供概览、接口统计、请求明细、价格管理和数据备份功能。

## 功能

- 用量趋势：请求数、输入/输出 Token、缓存 Token、缓存命中率和费用。
- 运行状态：队列深度、写入数量、失败数量、数据库记录数和保留策略。
- 分析视图：按模型、Provider、客户端 Key 和来源查看请求分布。
- 请求明细：按时间、Provider、模型、状态和关键字筛选，并支持 CSV 导出。
- 上游详情：查看指定上游的模型分布、汇总指标和最近请求。
- 价格管理：按模型配置输入、输出、缓存读取、缓存写入和推理价格。
- 数据管理：导出和恢复完整备份，恢复前会校验备份完整性。
- 隐私保护：管理页面只显示脱敏后的客户端 Key、Provider 凭证和来源名称。
- 主题支持：支持浅色、深色主题，以及移动端布局和键盘操作。

## 设计边界

- 只声明 `usage_plugin` 和 `management_api` 能力。
- 不启动独立 HTTP 服务，不轮询 CLIProxyAPI，也不依赖 Redis。
- `usage.handle` 只负责 JSON 解码、脱敏和非阻塞入队，不执行磁盘 I/O。
- SQLite 使用 WAL、短事务和分钟级汇总表；明细查询使用毫秒级时间边界。
- 统计接口返回的客户端 Key 为匿名标识，可直接用于明细和 CSV 筛选。

## 构建要求

插件使用 CGO 构建：

- Windows：MinGW-w64
- Linux：`gcc` 或 `musl-gcc`
- macOS：Xcode Command Line Tools

需要 Go 1.24 或更高版本。

## 构建

在仓库根目录运行：

```powershell
.\scripts\build.ps1 -Version 1.6.4
```

指定目标平台：

```powershell
.\scripts\build.ps1 -Version 1.6.4 -GoOS windows -GoArch amd64
```

构建产物位于 `release/`，脚本会同时生成压缩包和 SHA-256 校验文件。

## 测试

```powershell
cd go
go test ./...
go vet ./...
node --check dashboard/app.js
node dashboard/app_test.cjs
```

## 配置

可参考 [config.example.yaml](config.example.yaml)。常用设置包括：

- `retention_days`：明细保留天数。
- `batch_size`：批量写入数量。
- `flush_interval_ms`：定时刷新间隔。
- `export_max_records`：备份和 CSV 导出的最大记录数。

配置更新与后台清理使用同一把锁，修改保留天数后不会继续使用旧值执行清理。

## 发布

插件当前版本为 **1.6.4**。GitHub Actions 支持手动输入 `vMAJOR.MINOR.PATCH` 格式的标签，例如 `v1.6.4`，并自动构建各平台压缩包和校验文件。

## 目录结构

```text
go/                 Go 插件、存储、查询和管理接口
go/dashboard/       内嵌的 HTML、CSS 和 JavaScript 页面
scripts/build.ps1   Windows 构建脚本
registry.json       插件注册信息
docs/               页面体验和设计说明
```

## 许可证

MIT
