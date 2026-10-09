import type { ReactNode } from 'react'

export function PlatformField({ label, description, children }: { label: string; description?: string; children: ReactNode }) {
 return <label className="config-row"><span><strong>{label}</strong>{description && <small>{description}</small>}</span>{children}</label>
}

// 开关行：整行不可点，只有右侧开关本体可点，避免「点一下标题就切换开关」。
// 之前用 <label> 包住整行，label 会把点击转发给 checkbox，导致点文字也生效。
export function PlatformToggle({ label, description, checked, onChange }: { label: string; description?: string; checked: boolean; onChange: (checked: boolean) => void }) {
 return <div className="config-row toggle-row"><span><strong>{label}</strong>{description && <small>{description}</small>}</span><label className="switch-box"><input type="checkbox" checked={checked} onChange={e=>onChange(e.target.checked)} /><i /></label></div>
}

/**
 * 未启用平台的统一门面：一栏介绍 + 一栏开关。
 * 开关未打开时不渲染 children，避免用户在未启用的服务上误配。
 */
export function PlatformGate({ description, label, checked, onChange, children }: { description: string; label: string; checked: boolean; onChange: (checked: boolean) => void; children?: ReactNode }) {
 return <div className="platform-config platform-gate">
  <section className="platform-section">
   <p className="muted">{description}</p>
   <PlatformToggle label={label} checked={checked} onChange={onChange} />
  </section>
  {checked ? children : null}
 </div>
}

// B 站 / Weverse / X / Instagram / Melon 的设置不在 config.json 的 groups 里
// （各自存于 storage/<platform>/settings.json），在这里登记条目数，
// 否则左侧栏会回落到 0。数字对应各自面板的运行设置项数量。
export const platformSettingCounts = { Weverse: 17, X: 3, Instagram: 7, Melon: 8, Bilibili: 5 } as const
