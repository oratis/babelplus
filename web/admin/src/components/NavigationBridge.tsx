/**
 * 把 react-router 的 `navigate` 挂到 `lib/navigation.ts` 的桥上。
 * 渲染 `null`，只有副作用 —— 放在 `<Routes>` 旁边，任何路由下都挂着。
 */
import { useEffect } from 'react';
import { useNavigate } from 'react-router';
import { navigation } from '../lib/navigation.ts';

export function NavigationBridge() {
  const navigate = useNavigate();
  useEffect(() => {
    navigation.setNavigator((to, options) => navigate(to, { replace: options?.replace ?? false }));
    // 卸载时摘掉：留着一个指向已卸载树的 navigate，下一次 401 会静默失败。
    return () => navigation.setNavigator(null);
  }, [navigate]);
  return null;
}
