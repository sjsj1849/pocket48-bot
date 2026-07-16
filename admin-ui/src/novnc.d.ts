declare module '@novnc/novnc' {
  export default class RFB {
    constructor(target: HTMLElement, url: string)
    scaleViewport: boolean
    resizeSession: boolean
    viewOnly: boolean
    disconnect(): void
    addEventListener(type: string, listener: (event: any) => void): void
  }
}
