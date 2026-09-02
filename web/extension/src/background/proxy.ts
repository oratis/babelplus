/**
 * `chrome.proxy.settings` 的最小封装。
 *
 * 只用 `pac_script` 模式，且 **`mandatory: true`**：PAC 脚本本身出错时不允许网络栈退回直连
 * （spec §3.3 规则 1 的另一半 —— 不只是候选串末位不放 DIRECT，脚本坏了也不能变成 DIRECT）。
 * scope 固定 `regular`：不碰无痕窗口的独立设置。
 *
 * `levelOfControl` 是连接前必须看一眼的东西：另一个扩展或企业策略占着代理设置时，
 * `set` 会静默成功但不生效 —— 用户看到「已连接」而流量根本没走我们。
 */
export type LevelOfControl =
  | 'not_controllable'
  | 'controlled_by_other_extensions'
  | 'controllable_by_this_extension'
  | 'controlled_by_this_extension';

export interface ProxyPort {
  setPac(pac: string): Promise<void>;
  clear(): Promise<void>;
  levelOfControl(): Promise<LevelOfControl>;
}

export function chromeProxyPort(): ProxyPort {
  return {
    async setPac(pac) {
      await chrome.proxy.settings.set({
        value: { mode: 'pac_script', pacScript: { data: pac, mandatory: true } },
        scope: 'regular',
      });
    },
    async clear() {
      await chrome.proxy.settings.clear({ scope: 'regular' });
    },
    async levelOfControl() {
      const result = await chrome.proxy.settings.get({});
      return result.levelOfControl as LevelOfControl;
    },
  };
}

export interface MemoryProxyPort extends ProxyPort {
  /** 当前生效的 PAC；`null` = 已清除。 */
  current: string | null;
  /** 每一次 set / clear 的记录，测试断言用。 */
  readonly history: ({ readonly op: 'set'; readonly pac: string } | { readonly op: 'clear' })[];
  control: LevelOfControl;
}

export function memoryProxyPort(control: LevelOfControl = 'controllable_by_this_extension'): MemoryProxyPort {
  const port: MemoryProxyPort = {
    current: null,
    history: [],
    control,
    async setPac(pac) {
      port.current = pac;
      port.history.push({ op: 'set', pac });
      if (port.control === 'controllable_by_this_extension') port.control = 'controlled_by_this_extension';
    },
    async clear() {
      port.current = null;
      port.history.push({ op: 'clear' });
      if (port.control === 'controlled_by_this_extension') port.control = 'controllable_by_this_extension';
    },
    async levelOfControl() {
      return port.control;
    },
  };
  return port;
}
