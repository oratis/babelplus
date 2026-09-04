/**
 * 标签页管理：每个标签页一个 `WebContentsView`，外加两件扩展做不到的事（spec §4.1）。
 *
 *  ② **per-tab 路由归属**：角标说这一页走的是代理还是直连。它不是猜的 ——
 *    整份浏览器流量都交给本机 mixed 入站，由 sing-box 按 `routing.ts` 那一张表分流，
 *    而角标用的是**同一张表同一个函数**（`decide`）。一处生成两处使用，所以角标不会说谎。
 *  ③ **「这个站点在这里被屏蔽」提示条**：一个直连页面加载失败、且主机在被屏蔽清单里时出现。
 *
 * per-tab 字节数走 CDP（`webContents.debugger` 的 `Network.loadingFinished.encodedDataLength`）——
 * 这正是 MV3 扩展拿不到的东西（非阻塞 `webRequest` 只有响应头，没有实际传输字节）。
 * ⚠️ 代价：调试器被我们占着时，用户自己开 DevTools 会拿不到 Network 面板的完整数据。
 * 所以 `attachMetering` 是可关的，且用户打开 DevTools 时我们主动让出（见 `detachForDevTools`）。
 */
import { WebContentsView, type BrowserWindow } from 'electron';
import { decide, hostOf, looksBlocked, type RuleInput } from './routing.ts';
import type { TabRoute, TabState } from '../shared/types.ts';

export interface TabsDeps {
  readonly window: BrowserWindow;
  /** 上方 chrome 界面的高度（px），标签页视图从这里往下铺。 */
  readonly chromeHeight: number;
  readonly rules: () => RuleInput;
  readonly onChange: () => void;
  readonly newTabUrl: string;
  readonly attachMetering?: boolean;
}

interface Tab {
  readonly id: number;
  readonly view: WebContentsView;
  title: string;
  url: string;
  route: TabRoute;
  bytes: number;
  loading: boolean;
  blockedHost: string | null;
  metered: boolean;
}

export class Tabs {
  private readonly d: TabsDeps;
  private readonly tabs: Tab[] = [];
  private active: number | null = null;
  private seq = 0;

  constructor(deps: TabsDeps) {
    this.d = deps;
  }

  get state(): { tabs: TabState[]; activeTabId: number | null } {
    return {
      tabs: this.tabs.map((t) => ({
        id: t.id,
        title: t.title,
        url: t.url,
        route: t.route,
        bytes: t.bytes,
        loading: t.loading,
        blockedHost: t.blockedHost,
      })),
      activeTabId: this.active,
    };
  }

  private routeOf(url: string): TabRoute {
    const host = hostOf(url);
    if (!host) return 'direct';
    return decide(host, this.d.rules()).route;
  }

  create(url?: string): number {
    this.seq += 1;
    const id = this.seq;
    const view = new WebContentsView({
      webPreferences: {
        // 页面是任意第三方内容：三道隔离一个都不能少。
        contextIsolation: true,
        nodeIntegration: false,
        sandbox: true,
        // 每个标签页共用默认 session —— 代理设置是 session 级的，
        // 单独 partition 会让「设了代理但这个标签页没走」这种最难查的事成为可能。
        webviewTag: false,
      },
    });
    const target = url ?? this.d.newTabUrl;
    const tab: Tab = {
      id,
      view,
      title: target,
      url: target,
      route: this.routeOf(target),
      bytes: 0,
      loading: true,
      blockedHost: null,
      metered: false,
    };
    this.tabs.push(tab);
    this.wire(tab);
    this.d.window.contentView.addChildView(view);
    void view.webContents.loadURL(target);
    this.select(id);
    return id;
  }

  private wire(tab: Tab): void {
    const wc = tab.view.webContents;
    wc.on('page-title-updated', (_e, title) => {
      tab.title = title;
      this.d.onChange();
    });
    wc.on('did-start-loading', () => {
      tab.loading = true;
      tab.blockedHost = null;
      this.d.onChange();
    });
    wc.on('did-stop-loading', () => {
      tab.loading = false;
      this.d.onChange();
    });
    wc.on('did-navigate', (_e, url) => {
      tab.url = url;
      tab.route = this.routeOf(url);
      // 新导航 = 新的一页，字节归零；累计口径是「这一页」，不是「这个标签页历史总和」。
      tab.bytes = 0;
      this.d.onChange();
    });
    wc.on('did-fail-load', (_e, errorCode, errorDescription, validatedURL, isMainFrame) => {
      if (!isMainFrame) return;
      // -3 = ERR_ABORTED，用户自己点走或重定向打断，不是失败。
      if (errorCode === -3) return;
      tab.route = 'failed';
      const host = hostOf(validatedURL);
      // 只有「本来走直连」且「在被屏蔽清单里」才提示走代理 ——
      // 一个本来就走代理却失败的页面，问题不在路由，弹这个条只会误导。
      const wasDirect = host ? decide(host, this.d.rules()).route === 'direct' : false;
      tab.blockedHost = host && wasDirect && looksBlocked(host) ? host : null;
      tab.title = errorDescription || tab.title;
      this.d.onChange();
    });
    wc.setWindowOpenHandler(({ url }) => {
      // 新窗口一律变成新标签页：一个浏览器不该弹出没有地址栏的窗口。
      this.create(url);
      return { action: 'deny' };
    });
    wc.on('devtools-opened', () => this.detachForDevTools(tab));
    if (this.d.attachMetering !== false) this.attachMetering(tab);
  }

  /** CDP 计量。失败不影响浏览 —— 拿不到字节数只是少一个数字。 */
  private attachMetering(tab: Tab): void {
    const wc = tab.view.webContents;
    try {
      wc.debugger.attach('1.3');
      tab.metered = true;
    } catch {
      return;
    }
    wc.debugger.on('message', (_e, method, params) => {
      if (method !== 'Network.loadingFinished') return;
      const len = (params as { encodedDataLength?: unknown }).encodedDataLength;
      if (typeof len === 'number' && Number.isFinite(len) && len > 0) {
        tab.bytes += len;
        this.d.onChange();
      }
    });
    wc.debugger.sendCommand('Network.enable').catch(() => undefined);
  }

  private detachForDevTools(tab: Tab): void {
    if (!tab.metered) return;
    try {
      tab.view.webContents.debugger.detach();
    } catch {
      /* 已经断了 */
    }
    tab.metered = false;
    this.d.onChange();
  }

  select(id: number): void {
    const tab = this.tabs.find((t) => t.id === id);
    if (!tab) return;
    this.active = id;
    for (const t of this.tabs) t.view.setVisible(t.id === id);
    this.layout();
    this.d.onChange();
  }

  close(id: number): void {
    const idx = this.tabs.findIndex((t) => t.id === id);
    if (idx < 0) return;
    const [tab] = this.tabs.splice(idx, 1);
    if (!tab) return;
    if (tab.metered) {
      try {
        tab.view.webContents.debugger.detach();
      } catch {
        /* ignore */
      }
    }
    this.d.window.contentView.removeChildView(tab.view);
    tab.view.webContents.close();
    if (this.active === id) {
      const next = this.tabs[Math.min(idx, this.tabs.length - 1)];
      this.active = next?.id ?? null;
      if (next) this.select(next.id);
    }
    this.d.onChange();
  }

  navigate(id: number, url: string): void {
    const tab = this.tabs.find((t) => t.id === id);
    if (!tab) return;
    tab.blockedHost = null;
    void tab.view.webContents.loadURL(url);
  }

  reload(id: number): void {
    this.tabs.find((t) => t.id === id)?.view.webContents.reload();
  }

  goBack(id: number): void {
    const wc = this.tabs.find((t) => t.id === id)?.view.webContents;
    if (wc?.navigationHistory.canGoBack()) wc.navigationHistory.goBack();
  }

  goForward(id: number): void {
    const wc = this.tabs.find((t) => t.id === id)?.view.webContents;
    if (wc?.navigationHistory.canGoForward()) wc.navigationHistory.goForward();
  }

  /** 路由规则变了（改偏好、接受提示条）之后重算所有角标。 */
  recomputeRoutes(): void {
    for (const t of this.tabs) if (t.route !== 'failed') t.route = this.routeOf(t.url);
    this.d.onChange();
  }

  /** 窗口大小变化时重排当前标签页的视图。 */
  layout(): void {
    const bounds = this.d.window.getContentBounds();
    for (const t of this.tabs) {
      if (t.id !== this.active) continue;
      t.view.setBounds({
        x: 0,
        y: this.d.chromeHeight,
        width: bounds.width,
        height: Math.max(0, bounds.height - this.d.chromeHeight),
      });
    }
  }

  get count(): number {
    return this.tabs.length;
  }
}
