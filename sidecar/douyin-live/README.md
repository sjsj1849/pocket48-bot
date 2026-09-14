# 抖音直播分时轮询

北京时间 20:00（含）至 23:00（不含）检查未开播直播间每 30 秒一次，其他时间每 5 分钟一次。已有直播间 ID 的独立服务和无 ID 的浏览器主页发现均采用此规则。时段切换不会继承白天的长等待；已开播的直播监听、心跳和结束检查不变。作品轮询仍由 DOUYIN_POLL_SECONDS 控制。

浏览器发现使用独立 30 秒定时唤醒和每账号节流，并与其他主页扫描互斥；网络慢或扫描未完成时跳过重叠轮次，因此间隔不代表通知时延的保证。

独立直播服务基于 [douyinLive v2.2.1](https://github.com/jwwsjlm/douyinLive/tree/v2.2.1)，固定提交 `60823bae3f14ee799b608df2cfa457b8a722d76d`。`evening-schedule.patch` 是本项目维护的补丁；上游源代码及其许可证在构建时从上游获取。

构建安装：`GOPROXY=https://proxy.golang.org,direct ./scripts/build_douyin_live_sidecar.sh`（需要 Go 自动工具链下载及 GitHub 网络）。原始发布版安装脚本仍可用于恢复上游版本。

修改配置文件前先停止 BOT 和面板服务，避免退出时将内存旧配置写回；修改后启动两项服务。配置 `DOUYIN_LIVE_SIDECAR_CMD` 为：

```
env POCKET48_DOUYIN_LIVE_SCHEDULE=1 ./storage/bin/douyinLive --config ./deploy/douyin-live.yaml
```

`monitor.poll_interval: "30s"` 控制晚间间隔。未设置环境变量时，补丁保留上游固定间隔，便于独立使用和回退。`monitor.notify_interval` 仅为 WebSocket 状态心跳，不是请求抖音的轮询间隔。
