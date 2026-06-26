# Pocket48 Bot

基于 Go 的 **口袋48** 消息监控机器人，对接 [NapCat](https://github.com/NapNeko/NapCatQQ) (OneBot v11) QQ 机器人框架。

实时监控小偶像的口袋房间消息和微博动态，将消息转发到 QQ 群。

## 功能

### 📱 口袋48 房间监控
- **文本/图片/语音/视频** 消息实时转发
- 回复消息、翻牌（FlipCard）消息解析
- 直播开播通知（含封面图）
- 礼物消息（含年度青春盛典记分礼物）
- 上麦提醒
- **自适应轮询**：有消息 300ms 快速拉取，安静期 1s 保底
- **媒体缓存**：图片/语音/视频预下载到本地，加快 NapCat 发送

### 🐼 微博监控
- **主页微博**动态监控
- **超话发帖**监控（指定 uid 在指定超话的发帖）
- **超话签到**（手动/自动每日签到）
- **超话签到人数**查询与日排行
- **三套认证**同时维护：AppAuth / weibo.com Cookie / m.weibo.cn Cookie

### 🖼️ 消息转发
- 图片自动下载并转为 Base64 发送（兼容 NapCat）
- 语音消息文件转发
- 视频消息文件转发
- 媒体缓存自动清理

### ⚙️ 系统特性
- 多群分组订阅（不同群监控不同房间）
- COS 归档存储（消息自动归档）
- 自适应轮询间隔

## 快速开始

### 1. 下载或编译

**方式一：下载 Release（推荐）**

从 [Releases](https://github.com/sjsj1849/pocket48-bot/releases) 下载对应平台的压缩包：

| 你的平台 | 下载哪个 |
| :--- | :--- |
| Linux 服务器 (x86_64) | `pocket48-bot-*-linux-amd64.gz` |
| Linux 服务器 (ARM) | `pocket48-bot-*-linux-arm64.gz` |
| Windows 电脑 | `pocket48-bot-*-windows-amd64.zip` |
| macOS Intel | `pocket48-bot-*-darwin-amd64.gz` |
| macOS M1/M2/M3 | `pocket48-bot-*-darwin-arm64.gz` |

解压后得到一个可执行文件：

```bash
# Linux / macOS
gunzip pocket48-bot-*.gz
chmod +x pocket48-bot-*
./pocket48-bot-*
```

```powershell
# Windows — 解压 zip 后双击或命令行运行
pocket48-bot-*.exe
```

**方式二：自行编译**

需要安装 Go 1.21+：

```bash
git clone git@github.com:sjsj1849/pocket48-bot.git
cd pocket48-bot
go build -o pocket48-bot ./cmd/bot
./pocket48-bot
```

### 2. 安装 NapCat

NapCat 是一个 QQ 机器人框架，负责接收和发送 QQ 消息。**装完必须配置反向 WebSocket 连接**，bot 才能通过 WebSocket 主动连上 NapCat。

**安装方式（选一种）：**

- **Linux 一键脚本**（推荐服务器用）：
  ```bash
  curl -o napcat.sh https://nclatest.znin.net/NapNeko/NapCat-Installer/raw/main/install.sh
  bash napcat.sh
  ```
  安装后 NapCat 默认会在 `~/.local/share/QQ/` 下

- **Docker 安装**：
  ```bash
  docker run -d \
    --name napcat \
    -p 6099:6099 \
    -p 3001:3001 \
    -v ~/.config/QQ:/app/.config/QQ \
    --restart always \
    mlikiowa/napcat-docker:latest
  ```
  端口说明：`6099`=WebUI管理面板，`3001`=反向WebSocket端口（给bot连接用）

- **Windows 图形界面**：从 [NapCat Releases](https://github.com/NapNeko/NapCatQQ/releases) 下载安装包，解压运行即可

### 3. 配置 NapCat 反向 WebSocket

NapCat 需要开启 **反向 WebSocket**（Reverse WebSocket），让机器人作为客户端主动连接 NapCat。

**方法一：WebUI 配置**
1. 启动 NapCat 后，浏览器打开 `http://<你的IP>:6099`
2. 进入「网络配置」→「反向 WebSocket 客户端」
3. 添加一个连接：
   - **名称**：`pocket48-bot`
   - **目标地址**：`ws://127.0.0.1:3001`
   - **Access Token**：（留空，除非配置了 `NAPCAT_ACCESS_TOKEN`）
4. 保存并重载

**方法二：直接编辑配置文件**
NapCat 的配置文件通常位于 `~/.config/QQ/` 或 NapCat 安装目录下的 `config/` 中，`onebot11.json` 或 `napcat_config.json`，添加：

```json
{
  "network": {
    "reverseWs": [
      {
        "name": "pocket48-bot",
        "url": "ws://127.0.0.1:3001",
        "accessToken": ""
      }
    ]
  }
}
```

### 4. 写配置文件

在程序同目录创建 `config.json`（最少配置）：

```json
{
  "NAPCAT_WS_URL": "ws://127.0.0.1:3001",
  "POCKET_USERNAME": "13800000000",
  "POCKET_PASSWORD": "your_password",
  "SUPER_ADMIN": 123456789,
  "BOUND_GROUP_ID": 987654321
}
```

| 字段 | 说明 |
| :--- | :--- |
| `NAPCAT_WS_URL` | NapCat 反向 WebSocket 地址（默认本机不用改） |
| `NAPCAT_ACCESS_TOKEN` | NapCat 鉴权 Token（如果设置了就填） |
| `SUPER_ADMIN` | **你的 QQ 号** |
| `BOUND_GROUP_ID` | **消息发到的目标 QQ 群号** |
| `COMMAND_PREFIX` | 命令前缀（默认 `bot`） |
| `POCKET_USERNAME` | 口袋48手机号 |
| `POCKET_PASSWORD` | 口袋48密码 |
| `POCKET_TOKEN` | （可选）直接填 Token 跳过密码登录 |

> ⚠️ **密码登录暂未实现**：口袋48 App 密码有加密/哈希方案，我们尚未破解，所以 `POCKET_PASSWORD` 直配方式暂时不可用。启动时如果密码认证失败会报 `password error`。请改用 SMS 短信登录：
> ```
> bot login sms <手机号>    # 发送验证码
> bot code <验证码>          # 输入验证码完成登录
> ```
> 登录成功后的 Token 会自动保存到 `config.json`，后续重启不需要重新登录。

其他配置项（全部可选，有默认值）：

| 字段 | 说明 | 默认值 |
| :--- | :--- | :--- |
| `COMMAND_PREFIX` | 命令前缀 | `"bot"` |
| `LIVE_MONITORING` | 全局直播通知 | `false` |
| `ADMIN_QQ` | 管理员 QQ 号列表 | `[]` |
| `GROUP_SUBSCRIPTIONS` | 群→房间监控列表 | `{}` |
| `WEIBO_COOKIE` | 微博认证（建议通过命令设置） | `""` |

### 5. 运行

```bash
# 如果用 Release 下载的，直接运行：
./pocket48-bot-*

# 如果用源码编译的：
./pocket48-bot
```

程序会自动读取同目录下的 `config.json`，启动后自动登录口袋48并开始轮询。

### 6. 添加房间监控

在绑定的 QQ 群里发（假设前缀为 `bot`）：

```
bot search 王奕
```

找到房间 ID 后：

```
bot monitor 67248386
```

### 7. 配置微博认证

微博监控需要认证。推荐使用 App 抓包一键导入：

```
bot weibo cookie import <粘贴抓包文本>
```

支持从 curl 命令、请求头文本、Fiddler/Charles 导出文本中自动提取 AppAuth（Authorization/gsid）。也支持直接设置 Cookie：

```
bot weibo cookie set "SCF=xxx; SUB=xxx; ..."
```

认证状态检查：

```
bot weibo cookie check
```

> ⚠️ **微博 Cookie 需要人工维护**：微博的 Cookie 和 AppAuth 都会不定期过期（通常几天到几周不等）。过期后 bot 会在群里发通知提醒。需要重新抓包导入：
> ```
> bot weibo cookie import <新的抓包文本>
> ```
> 建议在手机上用 Stream / HTTP Catcher 抓一次微博 App 请求，把请求头全文粘贴进来即可，不需要手动提取具体字段。

---

## 命令

所有命令默认前缀 `bot`（可在 `config.json` 中修改 `COMMAND_PREFIX`）。以下假设前缀为 `bot`。

### 📱 口袋48 房间

#### 监控控制

| 命令 | 说明 |
| :--- | :--- |
| `bot on` | 开启口袋房间消息转发 |
| `bot off` | 关闭口袋房间消息转发 |

#### 房间管理

| 命令 | 说明 |
| :--- | :--- |
| `bot search <名字>` | 搜索小偶像的房间 |
| `bot monitor <房间ID>` | 添加房间监控到本群 |
| `bot remove <房间ID>` | 从本群移除该房间监控 |
| `bot list [channels]` | 查看本群监控的房间列表（加 `channels` 显示频道名） |

#### 功能开关

| 命令 | 说明 |
| :--- | :--- |
| `bot live [on/off] [房间号]` | 直播通知开关。无参数→查看状态；只有 on/off→全局开关；有房间号→指定房间 |
| `bot gift <on/off> <房间号>` | 指定房间的礼物消息回复开关 |
| `bot score <on/off> <房间号>` | 指定房间的年度青春盛典记分监控开关 |

#### 账号管理

| 命令 | 说明 |
| :--- | :--- |
| `bot login <token>` | 直接设置口袋48 Token |
| `bot login sms <手机号>` | 发送短信验证码登录 |
| `bot login pwd <密码>` | 密码登录 |
| `bot code <验证码>` | 输入短信验证码完成登录 |
| `bot whoami` | 查看当前口袋48账号和 Token 状态 |
| `bot admin <add/remove> <QQ号>` | 管理管理员（仅超级管理员可用） |
| `bot bind` | 在群里执行，将该群绑定为机器人目标群 |

---

### 🐼 微博

#### 微博监控管理

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo add <UID> [at_all]` | 添加微博监控。加 `at_all` 参数则新微博 @全体成员 |
| `bot weibo del <UID>` | 删除指定 UID 的微博监控。省略 UID 则清空该群全部 |
| `bot weibo list` | 查看本群监控的 UID 列表 |

#### 微博认证

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo cookie check` | 检查三套认证（AppAuth / weibo.com / m.weibo.cn）状态 |
| `bot weibo cookie import <抓包文本>` | **推荐**：从 App 抓包（curl/请求头）一键导入认证，自动解析 AppAuth 和 Cookie |
| `bot weibo cookie set <Cookie>` | 直接设置 weibo.com Cookie（不推荐，建议用 import） |

> 📌 **三种认证说明**：
> - **AppAuth（最高优先级）**：微博 App 抓包获取的完整认证，所有功能可用（监控、签到、超话发帖监控等）
> - **m.weibo.cn Cookie（次之）**：仅可用于基础功能（监控主页微博、超话签到、签到人数查询），超话发帖监控（superpost）等需要 AppAuth 的功能不可用
> - **weibo.com Cookie（最低）**：同上，基础功能可用但有限制（部分接口可能返回不完整数据）
>
> 建议优先用 `bot weibo cookie import` 导入 App 抓包文本，一次获取三套认证。

#### 超话签到

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo super list` | 查看已配置的超话列表 |
| `bot weibo super add <oid> [名称]` | 添加超话（oid 是超话 ID，可指定中文名称便于识别） |
| `bot weibo super del <名称或oid>` | 删除超话 |
| `bot weibo super sign [all/名称]` | 手动签到。all→全部签到，指定名称→签到指定超话 |
| `bot weibo super auto <on/off>` | 开启/关闭每日自动签到（每天首次运行，不固定时间点。bot 每 15 分钟检查一次，当日未签到则触发，约凌晨 00:00~00:15 执行） |

> 超话 oid 获取方式：在微博 App 打开超话页面，URL 中的数字即为 oid。

#### 超话发帖监控

> ⚠️ **可用性未知**：该功能依赖 AppAuth 认证，尚未充分测试，暂不确定是否能正常工作。后续更新会完善。如你使用中发现任何问题，欢迎反馈。

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo superpost bind <uid> <oid> [名称]` | 监控指定 uid 在指定超话的发帖 |
| `bot weibo superpost unbind <uid> <oid>` | 删除对应监控 |
| `bot weibo superpost list` | 查看本群超话发帖监控列表 |
| `bot weibo superpost test <uid> <oid>` | 测试是否能获取到该 uid 在超话的最新帖子 |

#### 超话签到人数（日排行）

| 命令 | 说明 |
| :--- | :--- |
| `bot weibo super count` | 查询当前超话签到人数排行（显示涨跌） |
| `bot weibo super count enable <on/off>` | 开启/关闭超话签到人数监控功能 |
| `bot weibo super count list [-g 组名]` | 查看已绑定的超话签到人数列表（可选按分组筛选） |
| `bot weibo super count bind <oid> [名称] [-g 分组名]` | 绑定一个超话来追踪签到人数，可用 -g 指定分组 |
| `bot weibo super count unbind <名称或oid>` | 解绑超话签到人数追踪 |
| `bot weibo super count yesterday` | 查看昨日快照数据 |
| `bot weibo super count group list` | 列出所有分组 |
| `bot weibo super count group create <名称>` | 创建新分组（日报会按分组分别出报告） |
| `bot weibo super count group rename <旧名称> <新名称>` | 重命名分组（日报标题同步更新） |
| `bot weibo super count group del <名称>` | 删除分组（其下超话回到未分组，不影响数据） |

---

### 🎤 直播 & 礼物

| 命令 | 说明 |
| :--- | :--- |
| `bot live <on/off> [房间号]` | 直播通知开关 |
| `bot gift <on/off> <房间号>` | 礼物回复开关 |
| `bot score <on/off> <房间号>` | 年度青春盛典记分监控开关 |

### 📋 其他

| 命令 | 说明 |
| :--- | :--- |
| `bot status` | 运行状态、监控房间数、微博监控数 |
| `bot help [命令名]` | 显示帮助。加命令名显示该命令的详细用法 |
| `bot archive status` | 归档存储状态 |
| `bot archive retry` | 重试归档队列中失败的任务 |
| `bot test <live/weibo>` | 发送测试消息（直播通知/微博通知） |
| `bot welcome <on/off> <群号>` | 群欢迎消息开关 |
| `bot welcome add/del/list <群号> [内容]` | 管理欢迎消息内容 |

### ❓ 获取帮助

群内发送 `bot help` 查看分类命令列表。

查看具体命令用法（如想看 `weibo` 命令的详细说明）：
```
bot help weibo
```

---

## 项目结构

```
├── cmd/bot/main.go           # 入口
├── internal/
│   ├── config/               # 配置管理
│   ├── logic/                # 核心逻辑
│   │   ├── bot.go            # Bot 主循环
│   │   ├── messages.go       # 消息轮询 & 转发
│   │   ├── media.go          # 媒体下载 & 缓存清理
│   │   ├── commands.go       # 命令分发
│   │   ├── cmd_handlers.go   # 命令处理器
│   │   ├── weibo.go          # 微博逻辑（认证/签到/超话）
│   │   └── utils.go          # 工具函数
│   ├── napcat/               # NapCat OneBot v11 客户端
│   ├── pocket48/             # 口袋48 API 客户端
│   ├── monitor/              # 微博监控轮询
│   └── storage/              # 消息归档（COS）
├── storage/                  # 本地存储目录
└── config.json               # 配置文件
```

## License

MIT
