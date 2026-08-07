# 架构说明

## CPA 插件边界

插件按 CLIProxyAPI v7 的原生 ABI v1 / JSON schema v2 工作，导出 `cliproxy_plugin_init`、`cliproxyPluginCall`、`cliproxyPluginFree` 和 `cliproxyPluginShutdown`。注册时只打开 `usage_plugin` 与 `management_api`。

参考：

- https://help.router-for.me/cn/plugin/development
- https://help.router-for.me/cn/plugin/usage-plugin
- https://help.router-for.me/cn/plugin/management-api

## 热路径

```text
CPA completed usage record
  -> ABI JSON decode
  -> compactUsageRecord
  -> bounded channel (default 256)
  -> return {}
```

磁盘写入、SQLite 聚合、保留清理和价格查询均在后台或 Management API 查询中完成。响应拦截和流式拦截不参与注册。

## 持久化

`usage_events` 是分页和导出的明细表，`usage_minute_rollups` 是趋势、分布和 KPI 的查询表。每个批次在一个短事务中同时写入事件和 rollup，避免统计与明细出现半批状态。

API Key 原文不落库。界面只展示缩略值，分组使用 HMAC-SHA256 截短值。失败文本仅清理控制字符并限制为 512 字节，不执行正则替换。

聚合查询使用 4 秒有界内存缓存并合并并发 miss。SQLite 最多打开 4 个连接，每个连接的页缓存约 8 MiB；临时表使用文件存储，新数据库页大小为 4096 字节。

## 页面路由

资源页面：

```text
/v0/resource/plugins/usage-keeper/dashboard
/v0/resource/plugins/usage-keeper/app.js
/v0/resource/plugins/usage-keeper/style.css
```

鉴权 Management API：

```text
/v0/management/plugins/usage-keeper/summary
/v0/management/plugins/usage-keeper/analysis
/v0/management/plugins/usage-keeper/interfaces
/v0/management/plugins/usage-keeper/upstream
/v0/management/plugins/usage-keeper/events
/v0/management/plugins/usage-keeper/events/export
/v0/management/plugins/usage-keeper/settings
/v0/management/plugins/usage-keeper/prices
/v0/management/plugins/usage-keeper/backup
/v0/management/plugins/usage-keeper/restore
```
