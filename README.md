# Pocket48 Bot

基于 Go 语言的 **Pocket48 (口袋48)** 消息监控机器人，对接 [NapCat](https://github.com/NapNeko/NapCatQQ) (OneBot v11) QQ 机器人框架。

实时监控小偶像的口袋房间和微博动态，将消息转发到 QQ 群。

## 功能

### 📱 口袋48 房间监控
- **文本/图片/语音/视频** 消息实时转发
- 回复消息、翻牌（FlipCard）消息解析
- 直播开播通知（含封面图）
- 礼物消息（含年度青春盛典记分礼物）
- 上麦提醒
- **自适应轮询**：有消息 300ms 快速拉取，安静期 1s 保底
- **媒体缓存**：图片/语音/视频预下载到本地，加快 NapCat 处理

### 🐼 微博监控
- 微博超话动态监控
- 每日自动超话签到
- 超话数据日统计推送
- Cookie / App 抓包热更新

### ⚙️ 系统特性
- 多群分组订阅（不同群可以监控不同房间）
- COS 归档存储（消息自动归档）
- 优雅关闭通知

## 快速开始

### 前置条件

1. 已部署 [NapCatQQ](https://github.com/NapNeko/NapCatQQ) 并开启 WebSocket 服务（默认 `ws://127.0.0.1:3001`）
2. 一个口袋48账号（用于拉取房间消息）

### 配置

创建 `config.json`：

```json
{
  "NAPCAT_WS_URL": "ws://127.0.0.1:3001",
  "NAPCAT_ACCESS_TOKEN": "",
  "POCKET_USERNAME": "13800000000",
  "POCKET_PASSWORD": "your_password",
  "POCKET_TOKEN": "",
  "SUPER_ADMIN": 123456789,
  "ADMIN_QQ": [],
  "BOUND_GROUP_ID": 987654321,
  "COMMAND_PREFIX": "bot",
  "POLLING_INTERVAL": 1,
  "GROUP_SUBSCRIPTIONS": {
    "987654321": [67248386]
  },
  "LIVE_MONITORING": true,
  "WEIBO_COOKIE": "",
  "WEIBO_SUBSCRIPTIONS": {}
}
```

### 运行

```bash
# 编译
go build -o pocket48-bot ./cmd/bot

# 运行
./pocket48-bot
```

## 命令

所有命令默认前缀 `bot`（可在配置中修改）。

### 房间监控

| 命令 | 说明 |
| :--- | :--- |
| `bot on` / `bot off` | 全局开启/停止消息转发 |
| `bot search <名字>` | 搜索小偶像房间 |
| `bot monitor <房间ID>` | 添加房间监控 |
| `bot remove <房间ID>` | 移除房间监控 |
| `bot list` | 查看监控列表 |

### 微博

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo add <UID>` | 添加微博监控 |
| `bot weibo del <UID>` | 删除微博监控 |
| `bot weibo list` | 查看微博监控 |
| `bot weibo cookie set <Cookie>` | 更新 Cookie |
| `bot weibo cookie check` | 检查 Cookie 状态 |
| `bot weibo super sign` | 手动超话签到 |
| `bot weibo super auto on/off` | 自动签到开关 |

### 直播 & 礼物

| 命令 | 说明 |
| :--- | :--- |
| `bot live on/off` | 全局直播通知开关 |
| `bot gift on/off <房间ID>` | 指定房间礼物回复开关 |
| `bot score on/off <房间ID>` | 年度青春盛典记分开关 |

### 其他

| 命令 | 说明 |
| :--- | :--- |
| `bot status` | 运行状态 |
| `bot archive status` | 归档状态 |
| `bot archive retry` | 重试归档队列 |

## 配置项

| 字段 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `NAPCAT_WS_URL` | NapCat WebSocket 地址 | `ws://127.0.0.1:3001` |
| `NAPCAT_ACCESS_TOKEN` | NapCat 鉴权 Token | `""` |
| `POCKET_USERNAME` | 口袋48手机号 | `""` |
| `POCKET_PASSWORD` | 口袋48密码 | `""` |
| `POCKET_TOKEN` | 口袋48 Token（登录后自动填充） | `""` |
| `SUPER_ADMIN` | 超级管理员 QQ | `0` |
| `BOUND_GROUP_ID` | 默认绑定的 QQ 群 | `0` |
| `COMMAND_PREFIX` | 命令前缀 | `"bot"` |
| `POLLING_INTERVAL` | 轮询间隔（秒） | `1` |
| `LIVE_MONITORING` | 全局直播通知 | `false` |
| `WEIBO_COOKIE` | 微博 Web Cookie | `""` |

## 项目结构

```
├── cmd/bot/main.go          # 入口
├── internal/
│   ├── config/              # 配置管理
│   ├── logic/               # 核心逻辑
│   │   ├── bot.go           # Bot 主循环
│   │   ├── messages.go      # 消息轮询 & 转发
│   │   ├── media.go         # 媒体下载 & 缓存清理
│   │   ├── commands.go      # 命令处理
│   │   ├── cmd_handlers.go  # 命令处理器
│   │   ├── weibo.go         # 微博逻辑
│   │   └── utils.go         # 工具函数
│   ├── napcat/              # NapCat OneBot 客户端
│   ├── pocket48/            # Pocket48 API 客户端
│   ├── monitor/             # 微博监控
│   └── storage/             # 消息归档（COS）
├── storage/                 # 本地存储目录
└── config.json              # 配置文件
```

## License

MIT
