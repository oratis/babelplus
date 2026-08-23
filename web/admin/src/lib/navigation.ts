/**
 * 后台的路由跳转桥。
 *
 * 后台没有登录态 Context（用户面板那套三态在这里用不上：闸 2 的准入不在应用层，
 * 应用只知道自己那一半）。所以 401 的跳转必须由 API 层发起，而 API 层拿不到 `navigate` ——
 * 这座桥就是把它递进去的地方。挂不上时退化成整页跳转，见 `createNavigationBridge`。
 */
import { createNavigationBridge } from '@babelplus/shared';

export const navigation = createNavigationBridge();
