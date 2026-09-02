/**
 * service worker 入口。**所有事件监听器都在顶层同步注册** —— MV3 只会在 SW 启动时收集监听器，
 * 放进 `await` 之后注册的监听器在 SW 被回收再拉起时会丢，表现是「装上第一天正常，第二天代理凭据不再回填」。
 *
 * 这里只做接线；判断都在 controller / auth / probe 里，它们在 Node 里就能测。
 */
import { isRequest, SNAPSHOT_EVENT, type Response } from '../shared/messages.ts';
import type { Snapshot } from '../shared/types.ts';
import { createAuthListener, type AuthChallenge, type AuthDecision } from './auth.ts';
import { Controller, envFromImportMeta } from './controller.ts';
import { chromeProxyPort } from './proxy.ts';
import { chromeLocalStorage, chromeSessionStorage } from './storage.ts';

const manifest = chrome.runtime.getManifest();

const controller = new Controller({
  local: chromeLocalStorage(),
  session: chromeSessionStorage(),
  proxy: chromeProxyPort(),
  badge: {
    async set(text, color) {
      await chrome.action.setBadgeText({ text });
      await chrome.action.setBadgeBackgroundColor({ color });
    },
  },
  alarms: {
    // @types/chrome 把 AlarmCreateInfo 写成互斥联合，分两支写才过类型检查。
    create: async (name, info) => {
      if (info.periodInMinutes !== undefined) {
        await chrome.alarms.create(name, { periodInMinutes: info.periodInMinutes });
      } else {
        await chrome.alarms.create(name, { delayInMinutes: info.delayInMinutes ?? 1 });
      }
    },
    clear: async (name) => {
      await chrome.alarms.clear(name);
    },
  },
  env: envFromImportMeta(manifest.version, chrome.runtime.getURL('onboarding.html')),
  now: () => Date.now(),
  broadcast: (snapshot: Snapshot) => {
    // 没有页面打开时 sendMessage 会 reject（"Could not establish connection"），那是正常情况。
    chrome.runtime.sendMessage({ type: SNAPSHOT_EVENT, snapshot }).catch(() => undefined);
  },
  openUrl: async (url) => {
    await chrome.tabs.create({ url });
  },
  openOptions: () => chrome.runtime.openOptionsPage(),
  uiLanguage: () => chrome.i18n.getUILanguage(),
  userAgent: navigator.userAgent,
});

chrome.runtime.onMessage.addListener((message: unknown, _sender, sendResponse: (r: Response) => void) => {
  if (!isRequest(message)) return false;
  controller.handle(message).then(sendResponse, (cause: unknown) => {
    sendResponse({
      ok: false,
      error: { code: 'INTERNAL', message: cause instanceof Error ? cause.message : String(cause) },
    });
  });
  return true; // 异步回复
});

const authListener = createAuthListener({
  getCredentials: () => controller.getCredentials(),
  onRejected: () => void controller.onAuthRejected(),
});
chrome.webRequest.onAuthRequired.addListener(
  (details, asyncCallback) => {
    const challenge: AuthChallenge = {
      requestId: details.requestId,
      isProxy: details.isProxy,
      challenger: details.challenger,
    };
    if (!asyncCallback) return undefined;
    authListener(challenge, (decision: AuthDecision) => asyncCallback(decision));
    return undefined;
  },
  { urls: ['<all_urls>'] },
  ['asyncBlocking'],
);

chrome.alarms.onAlarm.addListener((alarm) => {
  void controller.handleAlarm(alarm.name);
});

chrome.runtime.onInstalled.addListener((details) => {
  if (details.reason === 'install') void controller.handle({ type: 'open', target: 'onboarding' });
});

chrome.runtime.onStartup.addListener(() => {
  void controller.onStartup();
});

chrome.proxy.onProxyError.addListener((details) => {
  controller.noteProxyError({ fatal: details.fatal, error: details.error });
});
