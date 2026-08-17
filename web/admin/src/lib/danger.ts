/**
 * 危险操作清单 —— page-inventory §4.4 的 16 条，**逐字搬进代码**。
 *
 * 为什么放在代码里而不是只留在文档里：这 16 条的要求（二次确认 / 必填原因 / 输入确认串 /
 * 通知受影响用户 / 独立权限位）是**每个按钮都要实现一遍**的东西。
 * 放在一张表里，接线时可以由 `<DangerAction>` 统一读取，
 * 漏掉某一条就变成「这个 D 项没有对应的组件」这种可检查的事实，而不是评审时的记忆力问题。
 *
 * 规则：所有 D 项都写审计日志（含改前值 / 改后值）。
 *   confirmString —— 额外要求输入确认串（用户邮箱 / 节点名）
 *   notify        —— 额外要求自动通知受影响用户
 *   reason        —— 必填原因
 *   separatePerm  —— 独立权限位，默认不授予
 */
export interface DangerOp {
  readonly code: string;
  readonly title: string;
  /** 危害。写给要点这个按钮的人看，不是写给开发看。 */
  readonly harm: string;
  readonly reason?: boolean;
  readonly confirmString?: string;
  readonly notify?: boolean;
  readonly separatePerm?: boolean;
  /** 额外的、这一条独有的要求。 */
  readonly extra?: string;
}

export const DANGER: Readonly<Record<string, DangerOp>> = {
  D1: {
    code: 'D1',
    title: '改用户流量配额 / 到期时间',
    harm: '直接等于送钱，也是内部欺诈面',
    reason: true,
  },
  D2: {
    code: 'D2',
    title: '封禁 / 解封用户',
    harm: '用户 60 秒内断网（配置下发是 60s 轮询）',
    reason: true,
  },
  D3: {
    code: 'D3',
    title: '一键吊销用户全部订阅 token',
    harm: '用户所有设备立即失效，必然产生工单',
    confirmString: '用户邮箱',
    notify: true,
  },
  D4: {
    code: 'D4',
    title: '停用 / 删除节点',
    harm: '该节点所有在线用户掉线',
    confirmString: '节点名',
    extra: '确认框内必须显示当前在线人数',
  },
  D5: {
    code: 'D5',
    title: '轮换 / 吊销节点密钥',
    harm: '一步完成会让节点在下一次轮询时失联',
    extra: '强制两步：先发新密钥 → 确认节点已用新密钥上报 → 再撤旧的。UI 层禁止一步完成',
  },
  D6: {
    code: 'D6',
    title: '手工标记订单已支付',
    harm: '绕过支付校验，全系统最大的内部欺诈面',
    reason: true,
    confirmString: '订单号',
    separatePerm: true,
  },
  D7: {
    code: 'D7',
    title: '退款 / 作废订单',
    harm: '资金 + 已开通的配额需回收',
    reason: true,
    notify: true,
  },
  D8: {
    code: 'D8',
    title: '改套餐价格 / 下架套餐',
    harm: '影响待支付订单与续费',
    extra: '已生成订单的价格快照不可变',
  },
  D9: {
    code: 'D9',
    title: '改节点协议参数',
    harm: '参数写错 = 节点静默不可用（Xray 保留了静默别名，写错不报错）',
    extra: '保存前 JSON schema 校验 + 保留上一版可一键回滚',
  },
  D10: {
    code: 'D10',
    title: '调整用户余额',
    harm: '等于印钱',
    reason: true,
    confirmString: '用户邮箱',
    extra: '单次金额上限',
  },
  D11: {
    code: 'D11',
    title: '手工发放 / 作废佣金',
    harm: '资金',
    reason: true,
  },
  D11b: {
    code: 'D11b',
    title: '群发邮件',
    harm: '一次群发翻车会推高退信率；退信率 ≥ 5% 进入审查、≥ 10% 可能暂停发信',
    extra: '强制先发测试件 + 确认框显示收件人数 + 频率上限',
  },
  D12: {
    code: 'D12',
    title: '发布 / 置顶公告',
    harm: '公告兼域名广播位，写错域名会把用户导向错误地址',
    extra: '强制预览',
  },
  D13: {
    code: 'D13',
    title: '改系统配置 / 支付通道 / 域名池',
    harm: '全局生效',
    extra: '展示 diff',
  },
  D14: {
    code: 'D14',
    title: '导出用户数据 CSV',
    harm: '数据泄漏面',
    separatePerm: true,
    extra: '审计要记录：谁、何时、哪些字段、多少行',
  },
  D15: {
    code: 'D15',
    title: '删除管理员',
    harm: '后台失守 / 自锁',
    confirmString: '管理员邮箱',
    extra: '禁止删除最后一个管理员',
  },
  D16: {
    code: 'D16',
    title: '重置他人 TOTP',
    harm: '绕过第二因子',
    confirmString: '管理员邮箱',
    notify: true,
  },
};

export function dangerOps(codes: readonly string[]): DangerOp[] {
  return codes.flatMap((c) => {
    const op = DANGER[c];
    return op ? [op] : [];
  });
}
