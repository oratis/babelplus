# babel.plus browser extension — Privacy policy

> 日期：2026-09-02 · 性质：**设计方案** · 状态：草稿（未托管、未经法务）
> 🔴 每一条都必须与实现逐字对上（spec §10）。改实现先改这里；改这里先改实现。
> Effective date: to be set when the extension is published.

## What the extension does

The babel.plus extension routes the traffic of the browser it is installed in through servers operated by babel.plus, using the proxy pass you bought on our website. It does nothing else.

## Data we process

**Account sign-in.** The email address and password you type into the extension are sent to the babel.plus API over HTTPS to obtain a session token. The token is stored in the extension's local storage on your device so you stay signed in. The password is never stored.

**Proxy credentials.** When you connect, the API returns short-lived credentials for the proxy servers. They are stored in the extension's *session* storage, which the browser clears when it closes. They are not written to disk by the extension.

**Quota.** Every five minutes while you are signed in, the extension asks the API how much of your pass has been used so it can show you. This number is produced by our servers from the traffic they carried for you; the extension does not measure it itself.

**Proxied traffic.** While connected, requests that the routing rules send through babel.plus pass through our servers, like any proxy. Our servers count bytes per account for billing. They do not store the content of what you visit. See the babel.plus service privacy policy for server-side retention.

**Preferences.** Routing mode, your "always route" and "never route" lists, the auto-connect switch and the last region are stored locally on your device. They are not sent to us.

**Diagnostics.** If you click "Copy diagnostics", a report is placed on your clipboard. It contains the extension version, the probe results per server (identified by number and region only), timestamps and the state of your quota. It contains no credentials, no server addresses, no email address and no page URLs. It is sent nowhere unless you paste it somewhere.

## Data we do not process

- We do not read, modify, record or transmit the content of the pages you visit. The extension has no content script.
- We do not log the URLs you visit. The extension's network listener only answers proxy-authentication challenges from our own servers and ignores everything else.
- We do not collect analytics, crash reports or usage telemetry from the extension.
- We do not sell, share or disclose any data to third parties for any purpose.

## Permissions

The extension asks for `proxy`, `webRequest`, `webRequestAuthProvider`, `storage`, `alarms` and access to all URLs. The all-URLs access exists solely so the browser lets the extension answer proxy-authentication challenges, which the browser attaches to whatever request is being proxied. A detailed justification for each permission is published with the store listing.

## Your choices

Sign out from the popup or the options page to delete the session token and proxy credentials from this browser and to clear the proxy settings. Uninstalling the extension removes all of its stored data.

## Contact

Support email and help site are shown in the extension's options page.
