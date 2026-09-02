# Store listing (English) — Chrome Web Store / Edge Add-ons

> 日期：2026-09-02 · 性质：**设计方案** · 状态：草稿（未提交）
> 红线：no "unblock", no "bypass", no streaming logos, no "eSIM", no "telecom" —— 见 [README.md](README.md) §0
> 关联：[privacy-policy.md](privacy-policy.md)

## 1 · Name / summary

**Name** (≤ 45 chars): `babel.plus — Private access for laptops in China`

**Short description** (≤ 132 chars):
`A consumer privacy proxy for travellers and residents in China. Routes this browser's traffic through your babel.plus pass.`

## 2 · Detailed description

babel.plus is a privacy proxy for people who bring a laptop to China — for work, for study, for a long stay. Buy a pass on our website, sign in here, and this browser's traffic travels through babel.plus. Nothing else on your computer is touched.

**What it does**
- One button. Sign in, pick a region, connect. The popup shows your exit IP so you can verify it yourself.
- Smart routing keeps Chinese sites direct, so maps, payments and local services stay fast. Switch to "Everything" if you prefer.
- A quota bar that tells the truth: how much of your pass is left, when it ends, and what this session used.
- Loud failures. If every server is unreachable you are told so and nothing silently falls back to a direct connection.
- Diagnostics you can paste into a support ticket — with no credentials, addresses or page URLs in them.

**What it does not do**
- It only routes this browser. Other apps (chat clients, editors, terminals) are not affected.
- It does not read, record or modify the pages you visit. There is no content script.
- It does not collect browsing data. See the privacy policy.
- It is not free. A pass is bought on our website; the extension never asks for payment details.

**Who it is for**
Travellers and residents whose phone plan does not cover a laptop or hotel Wi-Fi. If you are unsure whether babel.plus is for you, read the help site before buying — a 7-day pass is the cheapest way to find out.

## 3 · Permission justifications (paste into the "Privacy practices" tab)

**Single purpose**: Route this browser's traffic through the user's babel.plus proxy pass and show the state of that pass.

| Permission | Why |
|---|---|
| `proxy` | The product. Sets a PAC script that sends traffic through the user's babel.plus servers, and clears it on disconnect. |
| `webRequest` + `webRequestAuthProvider` | Only to answer `Proxy-Authenticate` challenges from babel.plus servers (`onAuthRequired` with `isProxy === true`). The listener ignores every other challenge. |
| Host permission `<all_urls>` | Required by Chrome for the proxy-authentication listener to see challenges: a proxy challenge is attached to the request being proxied, and that can be any site. The extension does not inject scripts, does not read request or response bodies (impossible with a non-blocking Manifest V3 listener anyway), and does not log URLs. |
| `storage` | Session token, routing preferences, the last known quota. Proxy credentials are kept in session storage and vanish when the browser closes. |
| `alarms` | Refresh the quota every 5 minutes and re-fetch the proxy configuration before it expires. |

**Remote code**: none. The PAC script is configuration data handed to Chrome's PAC evaluator via `chrome.proxy.settings`; it is generated inside the extension from a list of server addresses and routing rules fetched from our API. No JavaScript is fetched or evaluated in extension pages or the service worker.

**Data use disclosure**: the extension collects no personally identifiable information, no health, financial, authentication, personal communications, location, web history, user activity or website content data. The only data sent to our servers are the sign-in credentials the user types (to our API only), and the requests the user chooses to route through the proxy.

## 4 · Category / metadata

- Category: Productivity → Workflow & Planning (Chrome); Productivity (Edge)
- Language: English (primary), 简体中文 (secondary, UI only)
- Support: help site URL + support email (from the runtime config)
- Privacy policy URL: hosted copy of [privacy-policy.md](privacy-policy.md)
