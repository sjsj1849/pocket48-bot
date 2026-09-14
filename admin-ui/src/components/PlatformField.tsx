import type { ReactNode } from 'react'

export function PlatformField({ label, description, children }: { label: string; description?: string; children: ReactNode }) {
 return <label className="config-row"><span><strong>{label}</strong>{description && <small>{description}</small>}</span>{children}</label>
}
export function PlatformToggle({ label, description, checked, onChange }: { label: string; description?: string; checked: boolean; onChange: (checked: boolean) => void }) {
 return <label className="config-row toggle-row"><span><strong>{label}</strong>{description && <small>{description}</small>}</span><input type="checkbox" checked={checked} onChange={e=>onChange(e.target.checked)} /><i /></label>
}
export const platformSettingCounts = { Weverse: 14, X: 3 } as const
