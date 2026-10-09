import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Activity, Calendar, RefreshCw, Save, X } from 'lucide-react'
import { api } from '../api'
import { TargetSelector } from '../components/TargetSelector'

// 超话签到监测。
//
// 数据来自超话页面接口（只读，不消耗每日签到机会）。每intervalMinutes 分钟采一次
// 「今日签到」人数，一天下来就能看出「某个人突然多涨一截」——
// 这在一天只跑一次的超话日报里完全看不出来，而它是判断数据有没有水分的唯一窗口。
//
// ★★ 2026-10-08 口径统一：图上的偏离、异常列表、邮件告警**全部由后端算**。
//
//   历史上这份逻辑在三个地方各写了一遍：bot 用单步偏离、admin 用 30 分钟窗口、
//   前端用硬编码 400。三者不一致 ⇒「面板看到的异常 ≠ 邮件发出的异常」，
//   而且阈值 400/800 比真实波动（p99=70）高一个数量级，**告警从未触发过**。
//   现在后端 internal/signstat 是唯一实现，前端只负责渲染。
//   ⇒ 本文件里**不再有任何统计计算**，也没有 MIN_ANOMALY_JUMP 之类的常量。

interface Point {
  ts: number
  sign: number
}

interface Series {
  oid: string
  name: string
  points: Point[]
  /** 第一次 ≥10000 的时刻；0 = 从未破万 */
  crossoverAt: number
  /** 破万前的最后一个精确值 */
  crossoverFrom: number
}

interface DeviationPoint {
  ts: number
  oid: string
  name: string
  dev: number
  step: number
  median: number
  from: number
  to: number
}

/** 一个不参与偏移统计的成员（破万者）—— 曲线仍画到破万点为止 */
interface ExcludedMember {
  oid: string
  name: string
  crossoverAt: number
  from: number
}

/** 后端算好的一组偏离（图表直接渲染，不再前端算） */
interface GroupAnalysis {
  groupName: string
  stamps: number[]
  members: string[]
  names: Record<string, string>
  series: Record<string, DeviationPoint[]>
  bounds: { pos: number; neg: number }
  excluded: ExcludedMember[]
}

interface Anomaly {
  oid: string
  name: string
  groupName: string
  fromTs: number
  toTs: number
  from: number
  to: number
  totalDelta: number
  peakDelta: number
  peakTs: number
  /** 合并了多少个连续超阈值的采样点 */
  steps: number
  groupMedian: number
  threshold: number
  reason: string
  /** confirmed = 确定异常；suspected = 疑似（破万者速度异常） */
  severity: 'confirmed' | 'suspected'
  ratePerHour?: number
  baselinePerHour?: number
}

interface HourlyPoint {
  hour: number
  total: number
  per: Record<string, number>
}

/**
 * 异常时段明细：**与推送到飞书 / QQ / 邮箱的那张 PNG 是同一份数据**
 * （后端同一个 signstat.WindowDetail，前端只是换一种呈现）。
 *
 * ★ deltas 里的 null 不是 0：代表这一格**没有可信增量**
 *   （破万后只有模糊值 / 没采样 / 间隔过大 / 跨日归零）。
 *   填 0 会把「破万拿不到数」读成「这一小时没涨」，结论是反的。
 */
interface DetailRow {
  oid: string
  name: string
  deltas: (number | null)[]
  marks: string[]
  net: number | null
  fuzzy: boolean
}

interface GroupWindowDelta {
  name: string
  net: number
  members: { oid: string; name: string; net: number }[]
}

interface DetailWindow {
  oid: string
  name: string
  from: number
  to: number
  stamps: number[]
  rows: DetailRow[]
  groups: GroupWindowDelta[]
}

interface MonitorConfig {
  enabled: boolean
  intervalMinutes: number
  windowMinutes: number
  spikeAbsolute: number
  spikeRatio: number
  retentionHours: number
  groupKey: string
  dailyReportEnabled: boolean
  dailyReportHour: number
  dailyReportMinute: number
  anomalyReportEnabled: boolean
  emailReportEnabled: boolean
  emailTo: string
  crossoverSuspectRatio: number
  crossoverSuspectRate: number
  imageTargets: string[]
  alertTargets: string[]
}

interface Group {
  name: string
  members: string[]
}

interface Payload {
  series: Series[]
  analysis: GroupAnalysis[]
  anomalies: Anomaly[]
  hourly: HourlyPoint[]
  /** 每条异常对应一段明细；长度与 anomalies 一致、下标一一对应 */
  details: DetailWindow[]
  /** 明细窗口跨度（分钟），后端回传的真实值 */
  detailWindow: number
  range: { mode: 'hours' | 'date'; label: string; since: number; until: number }
  config: MonitorConfig
  groups?: Group[]
}

const COLORS = ['#2f6bff', '#e0484d', '#12a150', '#8a5cf5', '#f59e0b', '#0ea5e9', '#ec4899', '#64748b']

// 破万门槛：>= 10000 后接口只返回 1000 粒度模糊值，不再精确。
const CROSSOVER = 10000

function fmtTime(ts: number): string {
  const d = new Date(ts)
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function fmtDateTime(ts: number): string {
  const d = new Date(ts)
  return `${d.getMonth() + 1}/${d.getDate()} ${fmtTime(ts)}`
}

function fmtFullDate(ts: number): string {
  const d = new Date(ts)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

/** 本地时区的 YYYY-MM-DD（不要用 toISOString，那是 UTC，会差一天） */
function localDateStr(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

// pick 颜色：同一个超话在所有图里颜色一致，便于跨图对照。
function colorFor(name: string, all: string[]): string {
  const idx = all.indexOf(name)
  return COLORS[(idx >= 0 ? idx : all.length) % COLORS.length]
}

/** 向上取整到 1/2/3/5/10×10^n（每数量级只取这五个，别用 2.5） */
function niceBound(v: number): number {
  if (v <= 0) return 0
  const mag = Math.pow(10, Math.floor(Math.log10(v)))
  for (const m of [1, 2, 3, 5, 10]) {
    const step = m * mag
    if (step > 0 && v <= step) return step
  }
  return mag * 10
}

function nearestIndex(stamps: number[], ts: number): number {
  if (stamps.length === 0) return -1
  let lo = 0
  let hi = stamps.length - 1
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (stamps[mid] < ts) lo = mid + 1
    else hi = mid
  }
  if (lo > 0 && Math.abs(stamps[lo - 1] - ts) <= Math.abs(stamps[lo] - ts)) return lo - 1
  return lo
}

function clampTs(ts: number, lo: number, hi: number): number {
  return Math.min(Math.max(ts, lo), hi)
}

/**
 * 阶梯线（step-after）。
 *
 * ★ 必须画阶梯线而不是斜线：采样点之间**没有观测值**，
 *   斜线会暗示"中间有连续数据"。tooltip 同理只显示真实采样点。
 */
// ★ values 允许 null：表示该时刻**没有数据**（破万之后）。
//   必须断线，不能填 0 —— 填 0 会画成一条贴着零线的假直线，
//   看上去像「这人一直在中位数上」，实际是拿不到数据。
function stepPath(
  values: (number | null)[],
  stamps: number[],
  x: (ts: number) => number,
  y: (v: number) => number,
): string {
  if (values.length === 0) return ''
  const segs: string[] = []
  let cur: string[] = []
  let prev: number | null = null
  const flush = () => {
    if (cur.length > 1) segs.push(cur.join(' '))
    cur = []
  }
  for (let i = 0; i < values.length; i += 1) {
    const v = values[i]
    const px = x(stamps[i])
    if (v === null) {
      flush()
      prev = null
      continue
    }
    const py = y(v)
    if (prev === null) {
      flush()
      cur = [`M ${px.toFixed(1)} ${py.toFixed(1)}`]
    } else {
      cur.push(`L ${px.toFixed(1)} ${y(prev).toFixed(1)}`)
      cur.push(`L ${px.toFixed(1)} ${py.toFixed(1)}`)
    }
    prev = v
  }
  flush()
  return segs.join(' ')
}

/** 采样间隔中位数（分钟），用于「采样间隔约 N 分钟」这句说明 */
function intervalMinutesLabel(stamps: number[]): string {
  if (stamps.length < 2) return '—'
  const diffs: number[] = []
  for (let i = 1; i < stamps.length; i += 1) diffs.push((stamps[i] - stamps[i - 1]) / 60000)
  diffs.sort((a, b) => a - b)
  return String(Math.round(diffs[Math.floor(diffs.length / 2)]))
}

/** 把 YYYY-MM-DD 拆成月历网格（周一为首列） */
function monthGrid(year: number, month0: number): Array<{ date: string; day: number; inMonth: boolean }> {
  const first = new Date(year, month0, 1)
  // getDay(): 0=周日 → 转成周一为首列
  const lead = (first.getDay() + 6) % 7
  const cells: Array<{ date: string; day: number; inMonth: boolean }> = []
  for (let i = 0; i < lead; i += 1) cells.push({ date: '', day: 0, inMonth: false })
  const y = first.getFullYear()
  const m = first.getMonth()
  const daysInMonth = new Date(y, m + 1, 0).getDate()
  for (let d = 1; d <= daysInMonth; d += 1) {
    const dt = new Date(y, m, d)
    cells.push({ date: localDateStr(dt), day: d, inMonth: true })
  }
  while (cells.length % 7 !== 0) cells.push({ date: '', day: 0, inMonth: false })
  return cells
}

// ────────────────────────────────────────────────────────────────────────────
// 偏离图
// ────────────────────────────────────────────────────────────────────────────

/**
 * 一组的偏离图。**数据全部来自后端**（后端已算好 dev / bounds / 破万截断）。
 *
 * 前端不再算偏离、不再算异常、不再有阈值常量 —— 那些口径全部收敛到后端。
 */
function DeviationChart({
  analysis,
  allNames,
  height = 300,
  threshold,
  anomalyTs,
  domId,
}: {
  analysis: GroupAnalysis
  allNames: string[]
  height?: number
  threshold: number
  anomalyTs: number[]
  domId?: string
}) {
  const [hoverTs, setHoverTs] = useState<number | null>(null)
  const svgRef = useRef<SVGSVGElement | null>(null)
  const [solo, setSolo] = useState<string | null>(null)

  const width = 940
  const pad = { top: 18, right: 120, bottom: 32, left: 66 }

  const stamps = analysis.stamps
  // ★ 不能用 analysis.members —— 它只含「参与统计」的人，破万者不在里面，
  //   那样破万者就永远画不出来（用户报：图上没有破万的超话）。
  //   破万者在 series 里有破万前的点，照样要画。
  const live = Object.keys(analysis.series).filter((oid) => (analysis.series[oid]?.length ?? 0) > 0)

  if (live.length < 2) {
    return (
      <div className="monitor-card">
        <div className="monitor-card-head">
          <h3>{analysis.groupName}</h3>
        </div>
        <p className="monitor-empty">
          本组可用成员不足 2 人，无法做组内对比。
          {analysis.excluded.length > 0
            ? `已破万 ${analysis.excluded.length} 人（破万后拿不到精确值，不再参与偏移计算，但曲线仍画到破万点为止）。`
            : '该组无有效采样数据。'}
        </p>
      </div>
    )
  }

  const rows = live.map((oid) => ({ oid, name: analysis.names[oid] ?? oid, pts: analysis.series[oid] }))
  const minTs = stamps[0]
  const maxTs = stamps[stamps.length - 1]
  const span = Math.max(maxTs - minTs, 1)
  const innerW = width - pad.left - pad.right
  const innerH = height - pad.top - pad.bottom

  // ★ Y 轴正负**分别**取极值，正负共用同一 bound ⇒ 0 永远在正中。
  //   不能「只扫正向 + 强行对称」—— 负偏离大于正向时超出部分会被裁掉，图上是断线。
  const bound = Math.max(1, niceBound(Math.max(analysis.bounds.pos, analysis.bounds.neg)))
  const yTop = bound
  const yBot = -bound

  const x = (ts: number) => pad.left + ((ts - minTs) / span) * innerW
  const y = (v: number) => pad.top + ((yTop - v) / (yTop - yBot)) * innerH

  // 每个 oid 的偏离序列对齐到 stamps
  // ★ 缺失点返回 null（断线），不能 ?? 0：破万后没有数据，
  //   补 0 会画成贴着零线的假直线。
  const devOf = (oid: string): (number | null)[] => {
    const m = new Map<number, number>()
    analysis.series[oid]?.forEach((p) => m.set(p.ts, p.dev))
    return stamps.map((ts) => (m.has(ts) ? (m.get(ts) as number) : null))
  }

  const hoverIdx = hoverTs === null ? -1 : nearestIndex(stamps, hoverTs)
  const snappedTs = hoverIdx >= 0 ? stamps[hoverIdx] : null
  const tooltipX = snappedTs === null ? 0 : x(snappedTs)
  const cursorX = hoverTs === null ? 0 : x(clampTs(hoverTs, minTs, maxTs))
  const tooltipRows =
    hoverIdx >= 0 ? rows.map((r) => ({ ...r, dev: devOf(r.oid)[hoverIdx] })) : []

  const isDim = (name: string) => solo !== null && solo !== name
  const twoPerson = live.length === 2

  return (
    <div className="monitor-card" id={domId}>
      <svg
        ref={svgRef}
        viewBox={`0 0 ${width} ${height}`}
        className="monitor-chart"
        role="img"
        aria-label={`${analysis.groupName} 偏移图`}
        onMouseLeave={() => setHoverTs(null)}
      >
        {/* Y 轴网格 */}
        {[bound, bound / 2, -bound / 2, -bound].map((v) => (
          <g key={v}>
            <line x1={pad.left} y1={y(v)} x2={pad.left + innerW} y2={y(v)} stroke="#eef0f3" />
            <text x={pad.left - 8} y={y(v) + 4} className="monitor-axis" textAnchor="end">
              {v > 0 ? `+${Math.round(v)}` : Math.round(v)}
            </text>
          </g>
        ))}
        {/* 零线 */}
        <line
          x1={pad.left}
          y1={y(0)}
          x2={pad.left + innerW}
          y2={y(0)}
          stroke="#98a2b3"
          strokeWidth={1.4}
          strokeDasharray="5 4"
        />
        <text x={pad.left - 8} y={y(0) + 4} className="monitor-axis" textAnchor="end">
          0
        </text>

        {/* 异常时刻竖虚线（用后端给的异常时间，橙线=阈值线） */}
        {anomalyTs.map((ts) => (
          <line
            key={`an-${ts}`}
            x1={x(ts)}
            y1={pad.top}
            x2={x(ts)}
            y2={pad.top + innerH}
            stroke="#fda29b"
            strokeWidth={1.2}
            strokeDasharray="3 3"
          />
        ))}
        {/* 阈值线 */}
        {[-threshold, threshold].map((v) => (
          <line
            key={`th-${v}`}
            x1={pad.left}
            y1={y(v)}
            x2={pad.left + innerW}
            y2={y(v)}
            stroke="#f79009"
            strokeWidth={1}
            strokeDasharray="6 4"
            opacity={0.7}
          />
        ))}

        {/* 曲线 */}
        {rows.map((r) => (
          <path
            key={r.oid}
            d={stepPath(devOf(r.oid), stamps, x, y)}
            fill="none"
            stroke={colorFor(r.name, allNames)}
            strokeWidth={isDim(r.name) ? 1.2 : 2.4}
            opacity={isDim(r.name) ? 0.18 : 1}
          />
        ))}

        {/* X 轴刻度 —— 按标签实际像素宽算密度，长时间范围必须稀，否则压字 */}
        {Array.from({ length: 7 }, (_, k) => {
          const ts = minTs + (span * k) / 6
          return (
            <text key={k} x={x(ts)} y={height - 12} className="monitor-axis" textAnchor="middle">
              {span > 6 * 3600_000 ? fmtDateTime(ts) : fmtTime(ts)}
            </text>
          )
        })}

        {/* 图例：名字 + 该组实际峰值偏差（邮件里没tooltip，必须把数值写进图例） */}
        {rows.map((r, i) => {
          const active = solo === null || solo === r.name
          const devs = devOf(r.oid)
          let peak = 0
          devs.forEach((d) => {
            if (d !== null && Math.abs(d) > Math.abs(peak)) peak = d
          })
          const isAnom = Math.abs(peak) >= threshold
          const exc = analysis.excluded.find((e) => e.oid === r.oid)
          return (
            <g
              key={`lg-${r.oid}`}
              style={{ cursor: 'pointer', pointerEvents: 'all' }}
              onClick={() => setSolo(solo === r.name ? null : r.name)}
            >
              {/* ★ 点击热区用 fill="none" + pointerEvents="all"，
                  不能用 fill="transparent"（部分浏览器不算已绘制，事件穿透）*/}
              <rect
                x={pad.left + innerW + 8}
                y={pad.top + i * 26}
                width={108}
                height={24}
                fill="none"
                pointerEvents="all"
              />
              <rect
                x={pad.left + innerW + 12}
                y={pad.top + 4 + i * 26}
                width={10}
                height={10}
                fill={colorFor(r.name, allNames)}
                opacity={active ? 1 : 0.3}
              />
              <text
                x={pad.left + innerW + 27}
                y={pad.top + 13 + i * 26}
                fontSize={11}
                fill={active ? '#101828' : '#98a2b3'}
                fontWeight={solo === r.name ? 600 : 400}
              >
                {r.name.length > 9 ? r.name.slice(0, 9) : r.name}
              </text>
              <text
                x={pad.left + innerW + 12}
                y={pad.top + 23 + i * 26}
                fontSize={10}
                fill={exc ? '#b54708' : isAnom ? '#b42318' : '#98a2b3'}
              >
                {exc
                  ? `破万于${fmtDateTime(exc.crossoverAt)}`
                  : `峰值 ${peak > 0 ? '+' : ''}${Math.round(peak)}`}
              </text>
            </g>
          )
        })}

        {/* hover 热区 */}
        <rect
          x={pad.left}
          y={pad.top}
          width={innerW}
          height={innerH}
          fill="none"
          pointerEvents="all"
          onMouseMove={(e) => {
            const svg = svgRef.current
            if (!svg) return
            const rect = svg.getBoundingClientRect()
            const vbX = ((e.clientX - rect.left) / rect.width) * width
            const ratio = (vbX - pad.left) / innerW
            setHoverTs(minTs + Math.min(Math.max(ratio, 0), 1) * span)
          }}
        />
        {hoverIdx >= 0 ? (
          <>
            <line
              x1={cursorX}
              y1={pad.top}
              x2={cursorX}
              y2={pad.top + innerH}
              stroke="#c9d3e0"
              strokeWidth={1}
              strokeDasharray="3 3"
            />
            <line
              x1={tooltipX}
              y1={pad.top}
              x2={tooltipX}
              y2={pad.top + innerH}
              stroke="#2f6bff"
              strokeWidth={1.4}
              opacity={0.75}
            />
            {/* ★ 该时刻没有数据（破万之后）就不画点，别把 null 当 0 */}
            {tooltipRows.map((r) =>
              r.dev === null ? null : (
                <circle
                  key={`hv-${r.oid}`}
                  cx={tooltipX}
                  cy={y(r.dev)}
                  r={3.5}
                  fill={colorFor(r.name, allNames)}
                  opacity={isDim(r.name) ? 0.25 : 1}
                />
              ),
            )}
          </>
        ) : null}

        {/* tooltip：纯 SVG，不用 foreignObject（缩放定位不稳） */}
        {hoverIdx >= 0 ? (() => {
          const lineH = 15
          const boxW = 196
          const boxH = 24 + tooltipRows.length * lineH
          const bx = tooltipX + 12 + boxW > width ? tooltipX - 12 - boxW : tooltipX + 12
          const by = pad.top + 4
          return (
            <g pointerEvents="none">
              <rect x={bx} y={by} width={boxW} height={boxH} rx={7} fill="#ffffff" stroke="#d0d5dd" />
              <text x={bx + 9} y={by + 15} fontSize={11} fontWeight={600} fill="#101828">
                {fmtDateTime(snappedTs ?? 0)}
              </text>
              <text x={bx + boxW - 9} y={by + 15} fontSize={9} fill="#98a2b3" textAnchor="end">
                本步增量偏离
              </text>
              {tooltipRows.map((r, k) => {
                const ry = by + 30 + k * lineH
                const vc = r.dev === null ? '#98a2b3' : r.dev > 0 ? '#e0484d' : r.dev < 0 ? '#12a150' : '#98a2b3'
                return (
                  <g key={`tt-${r.oid}`}>
                    <rect x={bx + 9} y={ry - 8} width={8} height={8} fill={colorFor(r.name, allNames)} />
                    <text x={bx + 22} y={ry} fontSize={11} fill="#475467">
                      {r.name.length > 10 ? r.name.slice(0, 10) : r.name}
                    </text>
                    <text x={bx + boxW - 9} y={ry} fontSize={11} fontWeight={600} fill={vc} textAnchor="end">
                      {r.dev === null ? '无数据' : r.dev > 0 ? `+${r.dev}` : r.dev}
                    </text>
                  </g>
                )
              })}
            </g>
          )
        })() : null}
      </svg>
      {twoPerson ? (
        <p className="monitor-card-note">
          本组 2 人，按「两人差距」口径（两条线必定互为相反数）。
        </p>
      ) : null}
    </div>
  )
}

// ────────────────────────────────────────────────────────────────────────────
// 异常列表
// ────────────────────────────────────────────────────────────────────────────

/**
 * 异常检测列表。
 *
 * ★ 卡片**随异常条数自然变高，不做滚动容器**（2026-10-08 用户明确）：
 *   「就算不止这一条异常，也不可能有十几二十个异常吧？那你为啥还要做成
 *   滚动的？你就让这个卡片随着异常数量的增加而变长」。
 *   只有 1 条时还强行给个滚动区，反而滚一下就看不到内容了。
 *
 * 点一行 = 选中它，下面的「异常时段明细」和「分组增量柱状图」跟着切。
 */
function AnomalyList({
  anomalies,
  selected,
  onSelect,
}: {
  anomalies: Anomaly[]
  selected: number
  onSelect: (i: number) => void
}) {
  if (anomalies.length === 0) {
    return (
      <div className="monitor-card">
        <div className="monitor-card-head">
          <h3>异常检测</h3>
        </div>
        <p className="monitor-empty">所选范围内没有检出异常。</p>
      </div>
    )
  }
  return (
    <div className="monitor-card monitor-card-alert">
      <div className="monitor-card-head">
        <h3>异常检测</h3>
        <span className="monitor-sub">共 {anomalies.length} 条 · 连续异常已合并为一次 · 点一行看它的时段明细</span>
      </div>
      <div className="monitor-table-wrap">
        <table className="monitor-table">
          <thead>
            <tr>
              <th>程度</th>
              <th>超话</th>
              <th>时间</th>
              <th className="num">签到变化</th>
              <th className="num">累计增量</th>
              <th className="num">峰值偏离</th>
              <th className="reason">判据</th>
            </tr>
          </thead>
          <tbody>
            {anomalies.map((a, i) => (
              <tr
                key={`${a.oid}-${a.fromTs}`}
                className={`clickable${i === selected ? ' sel' : ''}`}
                onClick={() => onSelect(i)}
                title="查看这一条异常的时段明细"
              >
                <td>
                  {a.severity === 'suspected' ? (
                    <span className="tag tag-suspect">疑似</span>
                  ) : (
                    <span className="tag tag-alert">异常</span>
                  )}
                </td>
                <td>{a.name}</td>
                <td>
                  {fmtDateTime(a.fromTs)}
                  {a.steps > 1 ? ` ~ ${fmtTime(a.toTs)}` : ''}
                  {a.steps > 1 ? <span className="muted"> （{a.steps} 个采样点）</span> : null}
                </td>
                <td className="num">
                  {a.from.toLocaleString()} → {a.to.toLocaleString()}
                </td>
                <td className="num hot">
                  {a.totalDelta >= 0 ? `+${a.totalDelta.toLocaleString()}` : a.totalDelta.toLocaleString()}
                </td>
                <td className="num hot">
                  {a.peakDelta > 0 ? `+${a.peakDelta}` : a.peakDelta}
                </td>
                <td className="muted reason" title={reasonDetail(a)}>{reasonFor(a)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// ────────────────────────────────────────────────────────────────────────────
// 异常时段明细（表）+ 分组增量柱状图
//
// 用户 2026-10-08：「我选昨天，昨天不是检测到一个超话的异常数据嘛，这个时候
// 面板里面就应该有：① 时间粒度改成一小时；② 那一个小时内 8 个超话每 5 分钟
// 签到数的详细变化；③ 那 4 个分组在那个小时里的增量柱状图。」
//
// ★ 这里那张表 = 推到飞书 / QQ / 邮箱的 PNG 上的那张表（同一份后端数据）。
// ────────────────────────────────────────────────────────────────────────────

function AnomalyDetailTable({
  dw,
  anomaly,
  allNames,
  threshold,
}: {
  dw: DetailWindow
  anomaly: Anomaly
  allNames: string[]
  threshold: number
}) {
  // 异常者置顶；其余保持后端给的名字序（sort 稳定）
  const rows = [...dw.rows].sort((a, b) => {
    if (a.oid === dw.oid) return -1
    if (b.oid === dw.oid) return 1
    return 0
  })
  const hasCrossover = rows.some((r) => r.fuzzy)
  if (dw.stamps.length === 0) {
    return <p className="monitor-empty">该时段内没有采样数据。</p>
  }
  return (
    <div className="monitor-table-wrap">
      <table className="monitor-table detail-table">
        <thead>
          <tr>
            <th>超话</th>
            {dw.stamps.map((ts) => (
              <th key={ts} className="num">
                {fmtTime(ts)}
              </th>
            ))}
            <th className="num net">净增</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const isAnom = r.oid === dw.oid
            return (
              <tr key={r.oid} className={isAnom ? 'anomrow' : undefined}>
                <td>
                  <i className="dot" style={{ background: colorFor(r.name, allNames) }} />
                  {isAnom ? '▲ ' : ''}
                  {r.name}
                </td>
                {dw.stamps.map((ts, ci) => {
                  const d = r.deltas[ci]
                  const mark = r.marks ? r.marks[ci] : ''
                  // 破万后的步：数字是**已知的下界**，星号 = 还有额外增量未知。
                  // 不能只画 +0*，那会把看得见的 1000 也丢掉（2026-10-08 用户纠正）。
                  const star = mark === 'crossover' ? '*' : ''
                  if (d === null || d === undefined) {
                    return (
                      <td
                        key={ts}
                        className="num dim"
                        title={star ? '已破万：接口只给千粒度模糊值' : '该时刻无可信增量'}
                      >
                        {star ? '+0*' : '—'}
                      </td>
                    )
                  }
                  const inAnom =
                    isAnom && ts >= anomaly.fromTs && ts <= anomaly.toTs + 5 * 60_000 && d > 0
                  const cls = inAnom ? 'num anom' : threshold > 0 && d >= threshold ? 'num hot' : 'num'
                  return (
                    <td key={ts} className={cls} title={star ? '已破万：数字为已知下界，实际可能更多' : undefined}>
                      {d > 0 ? `+${d}` : d}
                      {star}
                    </td>
                  )
                })}
                <td className="num net">
                  {r.net === null ? '—' : r.net > 0 ? `+${r.net}` : r.net}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
      <p className="monitor-card-note">
        格 = 该采样点的 5 分钟增量；异常时刻在<b>最后一列</b>。
        {hasCrossover ? ' * = 已破万，数字是已确认的增量，还有额外增量拿不到（+0 不等于没涨）。' : ''}
        {' '}— = 该时刻无采样、间隔过大，或跨日归零那一步。
      </p>
    </div>
  )
}

/**
 * 增量柱状图：**只画异常所在的那一组**（2026-10-08 用户明确）。
 *
 *   「这个增量柱状图 有错误……只看有异常数据的那一组就行，不用全部的组都加上。
 *    而且你不用把他们这个时段的差值做对比，你把每 5 分钟的数据都做成一个柱子」
 *
 * 所以：X 轴 = 该时段内每个采样时刻（与明细表同一批列），
 *       每个时刻画 N 根柱（N = 该组成员数），柱高 = 那一刻的增量。
 *       Y 轴从 0 开始（增量没有负值，零轴压底，不要居中留半屏负空间）。
 *       时长跟着明细表的粒度选择走。
 */
/**
 * 异常检测表格里的「判据」列 —— **只放一句短的**。
 *
 * ★ 2026-10-08 用户投诉：「正压疑似异常的判据有点太长了，把这一行都整得
 *   特别长。你给我简单写一下呀，写这么长干什么？」
 *   之前这里直接渲染后端那个长 reason，还把速率/基线又重复一遍。
 *   现在：表格里一行短句，完整说明挪到 title 悬浮提示里。
 */
function reasonFor(a: Anomaly): string {
  if (a.severity === 'suspected') {
    const r = a.ratePerHour ? Math.round(a.ratePerHour).toLocaleString() : ''
    return r ? `疑似：${r} 人/小时，远超正常` : '疑似：破万后涨速异常'
  }
  return '同组离群（5 分钟单步）'
}

/** 完整的判据说明（表格单元格的 title 悬浮提示）。 */
function reasonDetail(a: Anomaly): string {
  return a.reason || reasonFor(a)
}

function GroupIncrementBars({ dw, allNames }: { dw: DetailWindow; allNames: string[] }) {
  const groups = dw.groups ?? []
  // 异常者落在哪一组就画哪一组；找不到（如疑似异常没组）退回第一组
  const grp =
    groups.find((g) => g.members.some((m) => m.oid === dw.oid)) ?? groups[0]
  if (!grp) {
    return <p className="monitor-empty">没有分组数据。</p>
  }
  const rows = grp.members
    .map((m) => dw.rows.find((r) => r.oid === m.oid))
    .filter((r): r is DetailRow => Boolean(r))
  if (rows.length === 0 || dw.stamps.length === 0) {
    return <p className="monitor-empty">该时段内没有采样数据。</p>
  }

  const width = 940
  const height = 250
  const pad = { top: 26, right: 16, bottom: 52, left: 60 }
  const innerW = width - pad.left - pad.right
  const innerH = height - pad.top - pad.bottom

  let maxV = 1
  rows.forEach((r) =>
    r.deltas.forEach((d) => {
      if (d !== null && d !== undefined && d > maxV) maxV = d
    }),
  )
  const bound = niceBound(maxV)
  const yZero = pad.top + innerH // ★ 零轴在底部
  const y = (v: number) => yZero - (v / bound) * innerH

  const stampW = innerW / dw.stamps.length
  const barW = Math.max(2, Math.min(20, (stampW * 0.72) / rows.length))
  const xMid = (si: number) => pad.left + stampW * (si + 0.5)

  // X 轴刻度：标签按实际宽度稀疏放置
  const labelEvery = Math.ceil((fmtTime(dw.stamps[0]).length * 7.2) / stampW) || 1

  return (
    <svg viewBox={`0 0 ${width} ${height}`} className="monitor-chart" role="img" aria-label="异常组增量柱状图">
      {/* 破万柱的斜纹填充：明确区别于「5 分钟真实增量」的实心柱 */}
      <defs>
        <pattern id="fuzzyHatch" width="5" height="5" patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
          <rect width="5" height="5" fill="#fff7ed" />
          <line x1="0" y1="0" x2="0" y2="5" stroke="#f79009" strokeWidth="2" />
        </pattern>
      </defs>
      {[bound, bound / 2, 0].map((v) => (
        <g key={v}>
          <line x1={pad.left} y1={y(v)} x2={pad.left + innerW} y2={y(v)} stroke={v === 0 ? '#98a2b3' : '#eef0f3'} />
          <text x={pad.left - 8} y={y(v) + 4} className="monitor-axis" textAnchor="end">
            {v === 0 ? '0' : `+${Math.round(v)}`}
          </text>
        </g>
      ))}

      {dw.stamps.map((ts, si) => (
        <g key={ts}>
          {rows.map((r, bi) => {
            const d = r.deltas[si]
            if (d === null || d === undefined) return null // 断采/跨日：没数据就不画
            // ★ 破万后的步照常画，但用**斜纹+虚线边**区分，并在那根柱子上标
            //   「非瞬时增长」（2026-10-08 用户要求）。
            //   破万后接口只给千粒度模糊值，那个 1000 是整段时间的累计，
            //   绝不是 5 分钟内真涨了 1000 —— 画成实心柱会严重误导。
            const isFuzzy = r.marks?.[si] === 'crossover'
            const x = xMid(si) - (rows.length * barW) / 2 + bi * barW
            const w = Math.max(1, barW - 1)
            const h = Math.max(1, yZero - y(d))
            return (
              <g key={r.oid}>
                <rect
                  x={x}
                  y={y(d)}
                  width={w}
                  height={h}
                  fill={isFuzzy ? 'url(#fuzzyHatch)' : colorFor(r.name, allNames)}
                  stroke={isFuzzy ? '#b54708' : 'none'}
                  strokeWidth={isFuzzy ? 1 : 0}
                  strokeDasharray={isFuzzy ? '3 2' : undefined}
                  opacity={r.oid === dw.oid ? 1 : 0.8}
                >
                  <title>
                    {isFuzzy
                      ? `${r.name} ${fmtTime(ts)} +${d}（已破万，非瞬时增长：这是这段时间的累计增量）`
                      : `${r.name} ${fmtTime(ts)} +${d}`}
                  </title>
                </rect>
                {isFuzzy && d >= 100 ? (
                  <text
                    x={x + w / 2}
                    y={y(d) - 5}
                    textAnchor="middle"
                    fontSize={9}
                    fill="#b54708"
                  >
                    非瞬时
                  </text>
                ) : null}
              </g>
            )
          })}
          {si % labelEvery === 0 ? (
            <text x={xMid(si)} y={height - 14} textAnchor="middle" fontSize={10} fill="#98a2b3">
              {fmtTime(ts)}
            </text>
          ) : null}
        </g>
      ))}

      {/* 图例 */}
      {rows.map((r, i) => (
        <g key={`lg-${r.oid}`} transform={`translate(${pad.left + i * 118}, ${height - 34})`}>
          <rect width={9} height={9} y={-8} fill={colorFor(r.name, allNames)} />
          <text x={14} y={0} fontSize={11} fill="#475467">
            {r.oid === dw.oid ? '▲ ' : ''}
            {r.name.length > 9 ? `${r.name.slice(0, 9)}…` : r.name}
          </text>
        </g>
      ))}
    </svg>
  )
}

/** 异常时段明细卡：粒度选择 + 明细表 + 分组增量柱状图 */
function AnomalyDetailCard({
  dw,
  anomaly,
  allNames,
  threshold,
  windowMin,
  onWindowChange,
}: {
  dw: DetailWindow
  anomaly: Anomaly
  allNames: string[]
  threshold: number
  windowMin: number
  onWindowChange: (m: number) => void
}) {
  return (
    <div className="monitor-card monitor-card-alert" id="anomaly-detail">
      <div className="monitor-card-head">
        <h3>异常时段明细 · {anomaly.name}</h3>
        <span className="monitor-sub">
          {fmtDateTime(dw.from)} — {fmtTime(dw.to)} · 异常时刻在最右一列
        </span>
      </div>

      <div className="detail-toolbar">
        <span className="detail-toolbar-label">时间粒度</span>
        {[30, 60, 120].map((m) => (
          <button
            key={m}
            type="button"
            className={`chip${windowMin === m ? ' active' : ''}`}
            onClick={() => onWindowChange(m)}
          >
            {m >= 60 ? `${m / 60} 小时` : `${m} 分钟`}
          </button>
        ))}
        <span className="detail-toolbar-hint">默认 1 小时；异常时刻始终是表的最后一列。</span>
      </div>

      <AnomalyDetailTable dw={dw} anomaly={anomaly} allNames={allNames} threshold={threshold} />

      <div className="monitor-card-head" style={{ marginTop: 18 }}>
        <h3>增量柱状图 · {(dw.groups ?? []).find((g) => g.members.some((m) => m.oid === dw.oid))?.name ?? '异常所在组'}</h3>
        <span className="monitor-sub">每 5 分钟一根柱，只画异常所在组 · 悬停看具体数值</span>
      </div>
      <GroupIncrementBars dw={dw} allNames={allNames} />
      {dw.rows.some((r) => r.marks?.some((m) => m === 'crossover')) ? (
        <p className="monitor-foot">
          <svg width="16" height="11" style={{ verticalAlign: '-1px', marginRight: 5 }}>
            <rect width="16" height="11" fill="url(#fuzzyHatchKey)" stroke="#b54708" strokeWidth={1} />
            <defs>
              <pattern id="fuzzyHatchKey" width="5" height="5" patternTransform="rotate(45)" patternUnits="userSpaceOnUse">
                <rect width="5" height="5" fill="#fff7ed" />
                <line x1="0" y1="0" x2="0" y2="5" stroke="#f79009" strokeWidth="2" />
              </pattern>
            </defs>
          </svg>
          <b>斜纹柱 = 非瞬时增长</b>：该超话已破万，接口只给千粒度模糊值，
          这根柱子代表的是<em>这段时间里一共涨了这么多</em>，不是这 5 分钟内真涨了这么多。
        </p>
      ) : null}
    </div>
  )
}

// ────────────────────────────────────────────────────────────────────────────
// 每小时汇总
// ────────────────────────────────────────────────────────────────────────────

/**
 * 每小时汇总（单元格 = 该小时净增量）。
 *
 * ★ 跨日归零那一步后端已剔除，所以这里不会出现「-10000」这种假暴跌。
 */
function HourlyTable({ hourly, allNames }: { hourly: HourlyPoint[]; allNames: string[] }) {
  if (hourly.length === 0) return null
  const names = allNames.filter((n) => hourly.some((h) => n in h.per))
  return (
    <div className="monitor-card" id="hourly-table">
      <div className="monitor-card-head">
        <h3>每小时汇总</h3>
        <span className="monitor-sub">单元格 = 该小时内净增签到数（已剔除跨日归零）</span>
      </div>
      <div className="monitor-table-wrap">
        <table className="monitor-table">
          <thead>
            <tr>
              <th>时间</th>
              {names.map((n) => (
                <th key={n} className="num">
                  <i className="dot" style={{ background: colorFor(n, allNames) }} /> {n}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {hourly.map((h) => (
              <tr key={h.hour}>
                <td>{fmtDateTime(h.hour)}</td>
                {names.map((n) => {
                  const v = h.per[n]
                  if (v === undefined) return <td key={n} className="num">—</td>
                  return (
                    <td key={n} className={Math.abs(v) > 200 ? 'num hot' : 'num'}>
                      {v >= 0 ? `+${v}` : v}
                    </td>
                  )
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// ────────────────────────────────────────────────────────────────────────────
// 日历弹窗
// ────────────────────────────────────────────────────────────────────────────

function CalendarPicker({
  value,
  onChange,
  onClose,
  minDate,
}: {
  value: string
  onChange: (d: string) => void
  onClose: () => void
  minDate: string
}) {
  const initial = value ? new Date(`${value}T00:00:00`) : new Date()
  const [cursor, setCursor] = useState({ y: initial.getFullYear(), m: initial.getMonth() })
  const cells = useMemo(() => monthGrid(cursor.y, cursor.m), [cursor])
  const today = localDateStr(new Date())

  return (
    <div className="cal-pop" role="dialog" aria-label="选择日期">
      <div className="cal-head">
        <button
          type="button"
          className="icon-button"
          onClick={() => setCursor({ y: cursor.m === 0 ? cursor.y - 1 : cursor.y, m: cursor.m === 0 ? 11 : cursor.m - 1 })}
          aria-label="上一月"
        >
          ‹
        </button>
        <b>
          {cursor.y} 年 {cursor.m + 1} 月
        </b>
        <button
          type="button"
          className="icon-button"
          onClick={() => setCursor({ y: cursor.m === 11 ? cursor.y + 1 : cursor.y, m: cursor.m === 11 ? 0 : cursor.m + 1 })}
          aria-label="下一月"
        >
          ›
        </button>
        <button type="button" className="icon-button cal-close" onClick={onClose} aria-label="关闭">
          <X size={14} />
        </button>
      </div>
      <div className="cal-week">
        {['一', '二', '三', '四', '五', '六', '日'].map((w) => (
          <span key={w}>{w}</span>
        ))}
      </div>
      <div className="cal-grid">
        {cells.map((c, i) => {
          if (!c.inMonth) return <span key={`e${i}`} />
          const disabled = c.date < minDate
          return (
            <button
              key={c.date}
              type="button"
              className={`cal-day${c.date === value ? ' active' : ''}${c.date === today ? ' today' : ''}`}
              disabled={disabled}
              title={disabled ? '没有更早的采样数据' : undefined}
              onClick={() => {
                onChange(c.date)
                onClose()
              }}
            >
              {c.day}
            </button>
          )
        })}
      </div>
      <div className="cal-foot">
        <button
          type="button"
          className="link-button"
          onClick={() => {
            onChange('')
            onClose()
          }}
        >
          改回「近 N 小时」
        </button>
        <button type="button" className="link-button" onClick={() => { onChange(today); onClose() }}>
          今天
        </button>
      </div>
    </div>
  )
}

// ────────────────────────────────────────────────────────────────────────────
// 主组件
// ────────────────────────────────────────────────────────────────────────────

export function SignMonitor({
  embedded = false,
  initialHours,
  initialDate,
  initialAnom,
}: { embedded?: boolean; initialHours?: number; initialDate?: string; initialAnom?: string } = {}) {
  const [data, setData] = useState<Payload | null>(null)
  const [hours, setHours] = useState(initialHours ?? 24)
  // ★ 空字符串 = 用 hours 模式；YYYY-MM-DD = 看那天全天
  const [date, setDate] = useState(initialDate ?? '')
  const [calOpen, setCalOpen] = useState(false)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [form, setForm] = useState<MonitorConfig | null>(null)
  // 异常明细的时间粒度（分钟）。★ 2026-10-08 用户：「不想要半个小时的了，直接要一个小时的」
  const [detailMin, setDetailMin] = useState(60)
  // 选中的那条异常（下标），下面的明细表与柱状图跟着它走
  const [selAnomaly, setSelAnomaly] = useState(0)

  // ★ 快照页可用 ?anom=<超话名|oid> 直接定位到某条异常（分享用）。
  useEffect(() => {
    if (!initialAnom || !data) return
    const i = data.anomalies.findIndex(
      (a) => a.name === initialAnom || a.oid === initialAnom,
    )
    if (i >= 0) setSelAnomaly(i)
  }, [initialAnom, data])

  const query = (date ? `?date=${date}` : `?hours=${hours}`) + `&detailWindow=${detailMin}`

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const payload = await api<Payload>(`weibo/sign-monitor${query}`)
      setData(payload)
      setForm(payload.config)
      if (payload.detailWindow > 0) {
        setDetailMin(payload.detailWindow)
      }
      // ★ 异常条数变了就把选中项夹回合法范围（否则切日期后会指向空数据）
      const n = (payload.anomalies ?? []).length
      setSelAnomaly((cur) => (n === 0 ? 0 : Math.min(cur, n - 1)))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '加载失败')
    } finally {
      setLoading(false)
    }
  }, [query])

  useEffect(() => {
    void load()
    const timer = window.setInterval(() => void load(), 60_000)
    return () => window.clearInterval(timer)
  }, [load])

  const allNames = useMemo(() => (data ? data.series.map((s) => s.name) : []), [data])

  // 采样间隔中位数：整个范围（跨组取一次，不要每组各写一遍）
  const sampleInfo = useMemo(() => {
    if (!data) return { minutes: '—', count: 0 }
    const set = new Set<number>()
    data.analysis.forEach((g) => g.stamps.forEach((ts) => set.add(ts)))
    const stamps = Array.from(set).sort((a, b) => a - b)
    return { minutes: intervalMinutesLabel(stamps), count: stamps.length }
  }, [data])

  // 异常 → 落在哪一组（给该组画竖虚线）
  const anomalyByGroup = useMemo(() => {
    const hit = new Map<string, number[]>()
    if (!data) return hit
    data.anomalies.forEach((a) => {
      if (!a.groupName) return
      hit.set(a.groupName, [...(hit.get(a.groupName) ?? []), a.fromTs])
    })
    return hit
  }, [data])

  async function save() {
    if (!form) return
    setSaving(true)
    setError('')
    try {
      await api('weibo/sign-monitor', { method: 'POST', body: JSON.stringify(form) })
      await load()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '保存失败')
    } finally {
      setSaving(false)
    }
  }

  // 采样保留起点：最早只能回溯 retentionHours
  const minDate = useMemo(() => {
    const hoursBack = data?.config.retentionHours ?? 72
    return localDateStr(new Date(Date.now() - hoursBack * 3600_000))
  }, [data])

  const cfg = form ?? data?.config ?? null

  return (
    <section className="monitor-page">
      <header className="monitor-head">
        <div>
          {embedded ? null : (
            <>
              <p className="eyebrow">WEIBO SUPER TOPIC</p>
              <h2>超话签到监测</h2>
            </>
          )}
          <p className="monitor-lead">
            每 {cfg?.intervalMinutes ?? 5} 分钟采一次「今日签到」人数。
            下方曲线是<b>单步增量相对同组中位数的偏离</b>——
            正常在 0 附近小幅波动，某一条突然拉高就是刷量。
          </p>
        </div>
        <div className="heading-actions">
          <div className="cal-anchor">
            <button
              type="button"
              className="field-select cal-trigger"
              onClick={() => setCalOpen((v) => !v)}
              title="选择日期"
            >
              <Calendar size={14} />
              <span>{date || `近 ${hours} 小时`}</span>
            </button>
            {calOpen ? (
              <CalendarPicker
                value={date || localDateStr(new Date())}
                minDate={minDate}
                onChange={(d) => {
                  setDate(d)
                  setHours(24)
                }}
                onClose={() => setCalOpen(false)}
              />
            ) : null}
          </div>
          <button className="icon-button" onClick={() => void load()} title="刷新" disabled={loading}>
            <RefreshCw size={16} />
          </button>
        </div>
      </header>

      {error ? <p className="inline-error">{error}</p> : null}

      {/* ★ 图的说明写在**顶部一次**，不在每张图下面重复三遍 */}
      <div className="monitor-card monitor-legend-card">
        <div className="monitor-card-head">
          <h3>怎么看这些图</h3>
          <span className="monitor-sub">
            采样间隔约 {sampleInfo.minutes} 分钟 · 共 {sampleInfo.count} 个真实采样点
          </span>
        </div>
        <ul className="monitor-legend-list">
          <li>
            <b>纵轴</b>：这一点上「比同组其他人多涨了多少」。先算每个人这一步的增量，
            再减去组内增量中位数。<b>0 = 和同组同步</b>；高于 0 = 比同伴多涨；低于 0 = 比同伴少涨。
          </li>
          <li>
            <b>橙色虚线</b>：异常阈值（当前 {cfg?.spikeAbsolute ?? 150} 人）。
            实测正常波动的 99 分位约 70，所以这个值能把真实刷量挑出来、又不会误报。
          </li>
          <li>
            <b>红色竖虚线</b>：检出的异常时刻。连续的多个采样点会<b>合并成一次</b>异常。
          </li>
          <li>
            <b>阶梯线</b>：数值只在采样时刻有效，鼠标会自动吸附到最近的采样点，
            <b>不会显示采样点之间的插值</b>。点击右侧名字可只看那一条线，再点一次取消。
          </li>
          <li>
            <b>每天 0 点前后签到数会归零</b>（实测 8 个超话同时跌 3000~10500），
            这是数据本身的按天重置、<b>不是异常</b>，已在统计里剔除。
          </li>
          <li>
            <b>破万</b>：签到过万后接口只给 1000 粒度的模糊值。
            破万者的<b>曲线仍画到破万点为止</b>，只是破万之后不再参与偏移计算与异常检测。
          </li>
        </ul>
      </div>

      <AnomalyList
        anomalies={data?.anomalies ?? []}
        selected={selAnomaly}
        onSelect={setSelAnomaly}
      />

      {/* ★ 选中那条异常的时段明细：8 个超话每 5 分钟的变化 + 分组增量柱状图。
          与推到飞书 / QQ / 邮箱的 PNG 是同一份后端数据。 */}
      {data && (data.anomalies?.length ?? 0) > 0 && data.details?.[selAnomaly] ? (
        <AnomalyDetailCard
          dw={data.details[selAnomaly]}
          anomaly={data.anomalies[selAnomaly]}
          allNames={allNames}
          threshold={cfg?.spikeAbsolute ?? 150}
          windowMin={detailMin}
          onWindowChange={setDetailMin}
        />
      ) : null}

      {(data?.analysis ?? []).map((g, gi) => (
        <DeviationChart
          key={g.groupName}
          domId={`dev-chart-${gi}`}
          analysis={g}
          allNames={allNames}
          threshold={cfg?.spikeAbsolute ?? 150}
          anomalyTs={anomalyByGroup.get(g.groupName) ?? []}
        />
      ))}

      {data && data.series.length > 0 ? <HourlyTable hourly={data.hourly} allNames={allNames} /> : null}

      {data && data.series.length === 0 ? (
        <div className="empty-state">
          {loading ? '加载中…' : '该时间范围内没有采样数据。监测启用后约一个间隔就会出现数据。'}
        </div>
      ) : null}

      {/* ── 异常监控配置 ── */}
      {cfg ? (
        <div className="monitor-card">
          <div className="monitor-card-head">
            <h3>异常监控配置</h3>
            <span className="monitor-sub">改完点保存，立即生效（无需重启）</span>
          </div>

          <div className="config-fields">
            <div className="config-row toggle-row">
              <span>
                <strong>启用监测</strong>
                <small>关闭后停止采样，已有数据保留。</small>
              </span>
              <label className="switch-box">
                <input
                  type="checkbox"
                  checked={cfg.enabled}
                  onChange={(e) => setForm({ ...cfg, enabled: e.target.checked })}
                />
                <i />
              </label>
            </div>

            <label className="config-row">
              <span>
                <strong>采样间隔（分钟）</strong>
                <small>默认 5 分钟。间隔越短越能看清尖峰，但也越频繁调接口。</small>
              </span>
              <input
                type="number"
                min={1}
                max={360}
                value={cfg.intervalMinutes}
                onChange={(e) => setForm({ ...cfg, intervalMinutes: Number(e.target.value) })}
              />
            </label>

            <label className="config-row">
              <span>
                <strong>异常阈值（人）</strong>
                <small>
                  单步增量偏离超过这个数即判异常。实测正常波动 99 分位约 70，建议 150；
                  调太高会漏报（历史上配成 800 时告警从未触发过）。
                </small>
              </span>
              <input
                type="number"
                min={10}
                max={100000}
                value={cfg.spikeAbsolute}
                onChange={(e) => setForm({ ...cfg, spikeAbsolute: Number(e.target.value) })}
              />
            </label>

            <label className="config-row">
              <span>
                <strong>破万疑似速度</strong>
                <small>
                  <b>主判据</b>：破万后整千跳变的绝对小时速度超过这个值就标「疑似」。
                  正常大约一小时几百，所以默认 1000。破万那段时间具体涨了多少拿不到，
                  只能按速度判断 —— 所以是「疑似」，不是确认异常。
                </small>
              </span>
              <input
                type="number"
                min={50}
                max={100000}
                step={50}
                value={cfg.crossoverSuspectRate}
                onChange={(e) => setForm({ ...cfg, crossoverSuspectRate: Number(e.target.value) })}
              />
            </label>
            <label className="config-row">
              <span>
                <strong>破万疑似倍数</strong>
                <small>
                  辅助判据：模糊期速度超过它破万前自身正常增速的
                  这个倍数也标「疑似」。默认 3。
                </small>
              </span>
              <input
                type="number"
                min={1}
                max={100}
                step={0.5}
                value={cfg.crossoverSuspectRatio}
                onChange={(e) => setForm({ ...cfg, crossoverSuspectRatio: Number(e.target.value) })}
              />
            </label>

            <div className="config-row toggle-row">
              <span>
                <strong>每日最终报表</strong>
                <small>
                  每天定时发送，<b>不管有没有异常都发</b>。内容是各组偏移图 + 每小时汇总。
                </small>
              </span>
              <label className="switch-box">
                <input
                  type="checkbox"
                  checked={cfg.dailyReportEnabled}
                  onChange={(e) => setForm({ ...cfg, dailyReportEnabled: e.target.checked })}
                />
                <i />
              </label>
            </div>

            <label className="config-row">
              <span>
                <strong>日报发送时刻</strong>
                <small>按北京时间。</small>
              </span>
              <span className="time-inputs">
                <input
                  type="number"
                  min={0}
                  max={23}
                  value={cfg.dailyReportHour}
                  onChange={(e) => setForm({ ...cfg, dailyReportHour: Number(e.target.value) })}
                />
                <span>:</span>
                <input
                  type="number"
                  min={0}
                  max={59}
                  value={cfg.dailyReportMinute}
                  onChange={(e) => setForm({ ...cfg, dailyReportMinute: Number(e.target.value) })}
                />
              </span>
            </label>

            <div className="config-row toggle-row">
              <span>
                <strong>异常时推送</strong>
                <small>
                  检出异常立刻推一次（附异常补充图）。与上面的日报开关<b>互相独立</b>——
                  日报每天都发，异常推送只在想告警时才发。
                </small>
              </span>
              <label className="switch-box">
                <input
                  type="checkbox"
                  checked={cfg.anomalyReportEnabled}
                  onChange={(e) => setForm({ ...cfg, anomalyReportEnabled: e.target.checked })}
                />
                <i />
              </label>
            </div>

            <div className="config-row toggle-row">
              <span>
                <strong>发送到邮箱</strong>
                <small>关闭后只发到下面选的去向。</small>
              </span>
              <label className="switch-box">
                <input
                  type="checkbox"
                  checked={cfg.emailReportEnabled}
                  onChange={(e) => setForm({ ...cfg, emailReportEnabled: e.target.checked })}
                />
                <i />
              </label>
            </div>

            <label className="config-row">
              <span>
                <strong>收件人</strong>
                <small>SMTP 服务器与账号在「Bot」分组里配置，这里填收件地址。</small>
              </span>
              <input
                type="text"
                placeholder="you@example.com"
                value={cfg.emailTo}
                onChange={(e) => setForm({ ...cfg, emailTo: e.target.value })}
              />
            </label>

            <div className="config-row">
              <span>
                <strong>图片去向（飞书 / QQ）</strong>
                <small>报表与异常补充图都是 PNG 图片，可多选。</small>
              </span>
              <TargetSelector
                value={cfg.imageTargets ?? []}
                onChange={(ids) => setForm({ ...cfg, imageTargets: ids })}
              />
            </div>

            <div className="config-row">
              <span>
                <strong>文字告警去向</strong>
                <small>异常发生时额外发一条纯文字摘要（很快，不带图）。</small>
              </span>
              <TargetSelector
                value={cfg.alertTargets ?? []}
                onChange={(ids) => setForm({ ...cfg, alertTargets: ids })}
              />
            </div>
          </div>

          <div className="monitor-controls">
            <button className="primary-button" onClick={() => void save()} disabled={saving}>
              <Save size={15} /> {saving ? '保存中…' : '保存配置'}
            </button>
            <span className="monitor-hint">
              分组「{cfg.groupKey || '八小妹'}」· 采样明细保留 {cfg.retentionHours ?? 72} 小时
            </span>
          </div>
        </div>
      ) : null}
    </section>
  )
}