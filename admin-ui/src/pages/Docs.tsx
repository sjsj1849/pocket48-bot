import { BookOpen, Info, Layers, RefreshCw, Shield } from 'lucide-react'

type DocSection = {
  title: string
  tone: 'blue' | 'green' | 'amber' | 'slate'
  icon: typeof BookOpen
  body: string[]
}

const sections: DocSection[] = [
  {
    title: '配置页怎么用',
    tone: 'blue',
    icon: Layers,
    body: [
      '左侧分组：Bot / 口袋48 / 微博 / 抖音 / 小红书。每组上方是运行参数（开关、Cookie、轮询等），下方是该平台的订阅/列表。',
      '改完参数点右上角「保存更改」。能热重载的字段会写盘并 SIGHUP Bot；必须重启的字段会停启 Bot。带「需重启」标记的项属于后者。',
      '订阅（房间/UID/创作者/超话）的添加、编辑、删除始终走热重载，不会整进程重启。',
    ],
  },
  {
    title: '热重载 vs 需重启',
    tone: 'amber',
    icon: RefreshCw,
    body: [
      '热重载（无需重启）：默认群号、命令前缀、媒体方式、邮件 SMTP、超话签到/日报开关、日报渠道、轮询间隔、抖音 IM 群号、Cookie 文本、口袋 NIM/直播开关、各平台订阅列表等。',
      '需重启：NapCat 地址/Token、口袋账号密码/Token、浏览器侧卡总开关、抖音/小红书总开关。',
      '运行中请勿手改 config.json（Bot 的 cfg.Save 会覆盖）。请用面板或停服务再改。',
    ],
  },
  {
    title: 'Bot',
    tone: 'slate',
    icon: Shield,
    body: [
      'NapCat WebSocket / Token：连 QQ 客户端（llbot/OneBot）。',
      '默认通知群号：内部通知与未单独指定群的转发落点。',
      '媒体发送方式：local=本机下载再发；remote=直传链接。',
      '邮件告警：仅「需要人工处理」的异常；Boot/签到走 QQ；日报渠道可在微博页配置（默认仅邮件）。',
    ],
  },
  {
    title: '口袋48',
    tone: 'green',
    icon: Info,
    body: [
      '账号/Token：Token 可自动获取；密码登录视环境而定。',
      '房间订阅：用搜索按小偶像名找房间 ID，再加入目标 QQ 群。关闭/无消息的房间会注明。',
      '直播房间不会加入包间订阅列表。',
      'NIM：实时房间消息/弹幕；轮询兜底在实时不投递时补抓。开关可热重载（与 QQ 命令改 live 类似）。',
    ],
  },
  {
    title: '微博',
    tone: 'blue',
    icon: Layers,
    body: [
      'Cookie ≠ 密码：是登录态会话。推荐「浏览器」页扫码，侧卡自动维护 weibo.com / m.weibo.cn。',
      '动态 UID：监控发博转发到 QQ 群；展示优先昵称。群 0 多为历史误加，可用编辑改到绑定群。',
      '超话订阅：每个超话可独立选择自动签到、超话日报或两者；日报开启时可选择或新建分组。',
      '日报按分组展示；发送渠道：email（默认）/ qq / both。分组填显示名（如 X姐姐们）。',
    ],
  },
  {
    title: '抖音',
    tone: 'amber',
    icon: Info,
    body: [
      '两套独立能力：① 创作者订阅=作品/开播；② 群聊 IM=指定抖音群的群主发言 + 可选私信。',
      'IM 目标群号可多个（逗号分隔）。QQ 落点默认绑定群。',
      '肥家等群聊默认只转发群主；普通成员不会转发。',
      '登录/扫码在「浏览器」页完成；作品拉数走 Cookie+HTTP。',
    ],
  },
  {
    title: '小红书',
    tone: 'green',
    icon: BookOpen,
    body: [
      '创作者订阅：填主页链接 / xhslink / 内部 user_id，不要填「小红书号」。',
      '登录在「浏览器」页；验收以 notes 能拉到帖为准，不要只看 Cookie 有没有值。',
    ],
  },
  {
    title: '浏览器页',
    tone: 'slate',
    icon: Shield,
    body: [
      '统一扫码入口：微博 / 抖音 / 小红书登录态写在同一浏览器 profile。',
      '扫完回配置页看 Cookie/状态；抖音 IM 登录失效时也会在这里出码。',
    ],
  },
]

export function Docs() {
  return (
    <main className="page-content docs-page">
      <section className="page-heading">
        <div>
          <p className="eyebrow">HELP</p>
          <h1>配置说明</h1>
          <p className="docs-lead">各平台可配置项、热重载范围与常见填写方式。配置页本身只保留简短提示。</p>
        </div>
        <div className="docs-badge">
          <BookOpen size={16} />
          <span>手册</span>
        </div>
      </section>
      <div className="docs-stack">
        {sections.map((sec) => {
          const Icon = sec.icon
          return (
            <article className={`docs-card tone-${sec.tone}`} key={sec.title}>
              <header className="docs-card-head">
                <span className={`docs-icon tone-${sec.tone}`}>
                  <Icon size={16} />
                </span>
                <h2>{sec.title}</h2>
              </header>
              <ul>
                {sec.body.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
            </article>
          )
        })}
      </div>
    </main>
  )
}
