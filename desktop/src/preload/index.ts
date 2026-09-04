/**
 * 预加载：把主进程的能力暴露成一个**窄接口**。
 *
 * 渲染层拿不到 `ipcRenderer`，只能调下面这几个方法 —— chrome 界面里如果哪天混进一段
 * 第三方脚本（不该发生，但边界要按会发生来划），它能做的事就只有这几件。
 */
import { contextBridge, ipcRenderer } from 'electron';

const api = {
  snapshot: () => ipcRenderer.invoke('bp:snapshot'),
  signIn: (email: string, password: string) => ipcRenderer.invoke('bp:sign-in', { email, password }),
  signOut: () => ipcRenderer.invoke('bp:sign-out'),
  connect: (outbound?: string | null) => ipcRenderer.invoke('bp:connect', { outbound }),
  disconnect: () => ipcRenderer.invoke('bp:disconnect'),
  setPrefs: (patch: unknown) => ipcRenderer.invoke('bp:set-prefs', patch),
  routeHost: (host: string) => ipcRenderer.invoke('bp:route-host', { host }),
  tab: (action: string, id?: number, url?: string) => ipcRenderer.invoke('bp:tab', { action, id, url }),
  openExternal: (url: string) => ipcRenderer.invoke('bp:open-external', { url }),
  onSnapshot: (cb: (snapshot: unknown) => void) => {
    ipcRenderer.on('bp:snapshot', (_e, s) => cb(s));
  },
  onNotice: (cb: (notice: unknown) => void) => {
    ipcRenderer.on('bp:notice', (_e, n) => cb(n));
  },
};

contextBridge.exposeInMainWorld('bp', api);

export type PreloadApi = typeof api;
