/**
 * Electron 主进程入口：只做接线，判断都在 controller / core / routing / tabs 里
 * （那几支在 Node 里可测，这一支不可测 —— 所以它必须薄）。
 *
 * 三处与「普通 Electron 应用」不同、且都不是审美选择的地方：
 *
 *  1. **代理设在 `session.defaultSession` 上，并且只指向本机 mixed 入站**。
 *     分流全部交给 sing-box（`routing.ts` 那一张表），Chromium 侧只 bypass 回环 ——
 *     否则「谁在分流」有两个答案，而角标一定会说谎。
 *  2. **内核崩了不撤代理**（controller 的 `degraded`）：撤掉就是静默直连。
 *  3. **下载被禁**：一个代理浏览器不该顺手成为一个下载器 —— v1 明确不做下载管理，
 *     `session.on('will-download')` 直接取消并提示，而不是留一个半成品。
 */
import { app, BrowserWindow, ipcMain, session, shell, type IpcMainInvokeEvent } from 'electron';
import { join } from 'node:path';
import { existsSync } from 'node:fs';
import { Api } from './api.ts';
import { Controller } from './controller.ts';
import { Core } from './core.ts';
import { Store } from './store.ts';
import { Tabs } from './tabs.ts';
import { hostOf } from './routing.ts';
import type { RuleInput } from './routing.ts';
import type { Snapshot } from '../shared/types.ts';

/**
 * 构建期兜底配置。**空值就是空值**：没有配置服务地址时界面显示「未配置」，
 * 不显示一个编出来的域名（AGENTS.md §3）。
 */
const API_BASE_URLS = (process.env['BP_API_BASE_URLS'] ?? 'https://api.babel.plus')
  .split(',')
  .map((s) => s.trim())
  .filter(Boolean);
const WEB_URL = process.env['BP_WEB_URL'] ?? 'https://web.babel.plus';
const HELP_URL = process.env['BP_HELP_URL'] ?? 'https://babel.plus/help';
const NEW_TAB_URL = process.env['BP_NEW_TAB_URL'] ?? 'https://www.google.com/';
/** chrome 界面的高度（标签条 + 地址栏 + 提示条余量）。与 renderer 的 CSS 必须一致。 */
const CHROME_HEIGHT = 96;

function coreBinary(): string | null {
  const key = `${process.platform}-${process.arch}`;
  const name = process.platform === 'win32' ? 'sing-box.exe' : 'sing-box';
  const candidates = [
    // 打包后：electron-builder 的 extraResources 把 vendor 放进 resources/
    join(process.resourcesPath ?? '', 'vendor', key, name),
    // 开发期：desktop/vendor/<key>/<bin>
    join(app.getAppPath(), 'vendor', key, name),
  ];
  return candidates.find((p) => p && existsSync(p)) ?? null;
}

let win: BrowserWindow | null = null;
let tabs: Tabs | null = null;
let controller: Controller | null = null;
let refreshTimer: NodeJS.Timeout | null = null;

const store = new Store(app.getPath('userData'));

function rules(): RuleInput {
  const prefs = controller?.prefs ?? store.current.prefs;
  return {
    mode: prefs.mode,
    alwaysProxy: prefs.alwaysProxy,
    neverProxy: prefs.neverProxy,
    controlPlaneHosts: [...API_BASE_URLS, WEB_URL, HELP_URL]
      .map((u) => hostOf(u) ?? '')
      .filter((h) => h.length > 0),
  };
}

function snapshot(): Snapshot {
  const t = tabs?.state ?? { tabs: [], activeTabId: null };
  return {
    version: app.getVersion(),
    signedIn: controller?.signedIn ?? false,
    connection: controller?.connection ?? {
      status: 'off',
      port: null,
      outbound: null,
      startedAt: null,
      lastError: null,
      restarts: 0,
    },
    subscription: controller?.subscriptionSummary ?? null,
    subscriptionFetchedAt: controller?.subscriptionFetchedAt ?? null,
    regions: controller?.regions ?? [],
    prefs: controller?.prefs ?? store.current.prefs,
    tabs: t.tabs,
    activeTabId: t.activeTabId,
    links: { webUrl: WEB_URL, helpUrl: HELP_URL },
    onboarding: !store.current.onboarded,
  };
}

function push(): void {
  win?.webContents.send('bp:snapshot', snapshot());
}

async function applyProxy(port: number): Promise<void> {
  await session.defaultSession.setProxy({
    proxyRules: `socks5://127.0.0.1:${port}`,
    // 只 bypass 回环：其余一律进入站，由 sing-box 分流（见文件头第 1 条）。
    proxyBypassRules: '<-loopback>',
  });
  await session.defaultSession.closeAllConnections();
  tabs?.recomputeRoutes();
}

async function clearProxy(): Promise<void> {
  await session.defaultSession.setProxy({ mode: 'direct' });
  await session.defaultSession.closeAllConnections();
  tabs?.recomputeRoutes();
}

function createWindow(): void {
  win = new BrowserWindow({
    width: 1180,
    height: 780,
    minWidth: 720,
    minHeight: 480,
    title: 'babel.plus',
    backgroundColor: '#E9EBEE',
    webPreferences: {
      preload: join(__dirname, 'preload.cjs'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
    },
  });
  void win.loadFile(join(__dirname, 'index.html'));

  tabs = new Tabs({
    window: win,
    chromeHeight: CHROME_HEIGHT,
    rules,
    onChange: push,
    newTabUrl: NEW_TAB_URL,
  });

  win.on('resize', () => tabs?.layout());
  win.on('closed', () => {
    win = null;
    tabs = null;
  });
}

function wireIpc(): void {
  const handlers: Record<string, (e: IpcMainInvokeEvent, ...args: never[]) => unknown> = {
    'bp:snapshot': () => snapshot(),
    'bp:sign-in': async (_e, payload: never) => {
      const { email, password } = payload as unknown as { email: string; password: string };
      await controller?.signIn(email, password);
      return snapshot();
    },
    'bp:sign-out': async () => {
      await controller?.signOut();
      return snapshot();
    },
    'bp:connect': async (_e, payload: never) => {
      const { outbound } = (payload as unknown as { outbound?: string | null }) ?? {};
      await controller?.connect(outbound === undefined ? undefined : outbound);
      return snapshot();
    },
    'bp:disconnect': async () => {
      await controller?.disconnect();
      return snapshot();
    },
    'bp:set-prefs': async (_e, payload: never) => {
      await controller?.setPrefs(payload as never);
      tabs?.recomputeRoutes();
      return snapshot();
    },
    'bp:route-host': async (_e, payload: never) => {
      const { host } = payload as unknown as { host: string };
      await controller?.routeHost(host);
      tabs?.recomputeRoutes();
      return snapshot();
    },
    'bp:tab': (_e, payload: never) => {
      const p = payload as unknown as { action: string; id?: number; url?: string };
      switch (p.action) {
        case 'create':
          tabs?.create(p.url);
          break;
        case 'select':
          if (p.id !== undefined) tabs?.select(p.id);
          break;
        case 'close':
          if (p.id !== undefined) tabs?.close(p.id);
          break;
        case 'navigate':
          if (p.id !== undefined && p.url) tabs?.navigate(p.id, p.url);
          break;
        case 'reload':
          if (p.id !== undefined) tabs?.reload(p.id);
          break;
        case 'back':
          if (p.id !== undefined) tabs?.goBack(p.id);
          break;
        case 'forward':
          if (p.id !== undefined) tabs?.goForward(p.id);
          break;
      }
      return snapshot();
    },
    'bp:open-external': async (_e, payload: never) => {
      const { url } = payload as unknown as { url: string };
      // 只放行我们自己的两个站点：一个代理浏览器把任意 URL 交给系统浏览器
      // 等于把用户从被保护的窗口里推出去，而他不会注意到。
      const allowed = [WEB_URL, HELP_URL].map((u) => hostOf(u));
      const host = hostOf(url);
      if (host && allowed.includes(host)) await shell.openExternal(url);
      return snapshot();
    },
  };
  for (const [channel, fn] of Object.entries(handlers)) {
    ipcMain.handle(channel, fn as never);
  }
}

app.whenReady().then(async () => {
  await store.load();

  const api = new Api({
    baseUrls: API_BASE_URLS,
    getToken: () => store.current.token,
    setToken: (token) => {
      void store.update({ token });
    },
  });

  const bin = coreBinary();
  const core = new Core({
    binary: bin ?? 'sing-box',
    onEvent: (e) => {
      controller?.handleCoreEvent(e);
      push();
    },
  });

  controller = new Controller({
    api,
    store,
    core,
    applyProxy,
    clearProxy,
    controlPlaneHosts: rules().controlPlaneHosts,
    onChange: push,
  });

  wireIpc();
  createWindow();

  // 自检：`BP_SMOKE=1 pnpm start` 会在窗口加载完成后到**渲染层里**读几个真实节点，
  // 打印结果再退出。没有它，「窗口起来了」与「界面渲染出来了」之间那道缝没人能看见 ——
  // 而白窗口正是 Electron 最常见的第一个 bug（CSP 挡掉脚本、preload 路径错、模块脚本被
  // file:// 的 CORS 拒掉，三种现象一模一样）。CI 与本机都能跑，不需要人盯着看。
  if (process.env['BP_SMOKE'] === '1') {
    win?.webContents.once('did-finish-load', async () => {
      try {
        const probe = await win?.webContents.executeJavaScript(
          `JSON.stringify({
             bridge: typeof window.bp === 'object',
             onboarding: !document.getElementById('onboarding').hidden,
             signInButton: !!document.getElementById('obsubmit'),
             pill: document.getElementById('pillquota').textContent,
             styled: getComputedStyle(document.getElementById('chrome')).height,
           })`,
        );
        process.stdout.write(`BP_SMOKE ${probe}\n`);
        const r = JSON.parse(String(probe)) as Record<string, unknown>;
        const ok = r['bridge'] === true && r['signInButton'] === true && r['styled'] === '96px';
        app.exit(ok ? 0 : 1);
      } catch (cause) {
        process.stdout.write(`BP_SMOKE failed: ${String(cause)}\n`);
        app.exit(1);
      }
    });
  }

  // 内核缺失是一个必须在界面上说出来的状态，不是一条日志。
  if (!bin) {
    controller.handleCoreEvent({
      type: 'failed',
      detail: '找不到随包内核 sing-box。开发机上跑一次 `pnpm core`。',
    });
  }

  // 下载：v1 不做（spec §4.4）。取消并说明，而不是留一个半成品的下载管理器。
  session.defaultSession.on('will-download', (event) => {
    event.preventDefault();
    win?.webContents.send('bp:notice', { kind: 'download-blocked' });
  });

  if (store.current.token && store.current.prefs.launchAtStart) {
    void controller.connect();
  } else if (store.current.token) {
    void controller.refreshSubscription();
  }

  // 配额每 5 分钟拉一次；用尽 / 到期时 controller 会主动断开。
  refreshTimer = setInterval(() => void controller?.tick(), 5 * 60_000);

  app.on('activate', () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

app.on('window-all-closed', () => {
  if (process.platform !== 'darwin') app.quit();
});

app.on('before-quit', () => {
  if (refreshTimer) clearInterval(refreshTimer);
  // 退出前一定要把内核收掉：留一个孤儿 sing-box 在后台监听回环端口，
  // 下次启动会撞端口，而现象是「今天打不开网页」。
  void controller?.disconnect();
});
