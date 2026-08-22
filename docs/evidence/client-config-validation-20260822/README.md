# 用真实客户端校验订阅产出 · 2026-08-22

> 日期：2026-08-22 · 性质：**证据型核查** · 状态：**已完成（校验层面）**
> 事实基线：容器里跑 **mihomo v1.19.30**（`-t`）与 **sing-box v1.13.19**（`check`），
> 输入是 `api/internal/subgen` 的**真实产出**（不是手抄的示例）
> 关联：[api-contract §4.5](../../02-architecture/api-contract.md)、
> [ADR 0006 §12](../../05-adr/0006-api-stack.md)（人工加载那一步）、
> [roadmap B45 / B46](../../00-overview/roadmap.md)

---

## 1 · 这些证据证明什么、不证明什么

**证明**：这两个版本的客户端**认得**我们生成的配置结构 —— 能解析、能完成初始化、不报错。
以及一条更要紧的：**某些规则拿不到数据文件时会让整份配置被拒绝加载**（§2）。

**不证明**（这一节比结论重要）：

1. 🔴 **不证明字段名是对的。** `-t` / `check` 做的是**结构与语义校验**：
   认得的键才校验，**认不得的键默默忽略**。
   api-contract §4.5 点名「需实测」的那三处（vless `smux` 块字段名、`reality-opts` 键名、
   hysteria2 `obfs-password` 拼写）写错时，这两个命令**照样通过**。
   真正能证明字段名对的只有「连上去跑通流量」——
   **[ADR 0006 §12](../../05-adr/0006-api-stack.md) 的人工加载那一步仍然欠着。**
2. **不证明官方图形客户端的行为。** §4 明确测到 `sing-box check` 对**缺 `inbounds`**
   的配置是通过的，而 SFI / SFA 把 profile 当完整配置加载时缺 tun inbound 会「零流量」。
   **校验通过 ≠ 能用。**
3. **不证明其它版本。** 结论绑在 mihomo v1.19.30 / sing-box v1.13.19 上。
4. **不证明真实客户端的 geodata 情况。** §2 的断网实验是**容器**里的裸 mihomo。
   桌面版 Clash Verge Rev 很可能自带 `geoip.metadb` —— **本仓没有一手数据**（B46）。

原始输出：[validation.txt](validation.txt)。
被校验的配置：
[generated-clash-with-geoip.yaml](generated-clash-with-geoip.yaml)（§2 的 A / B / B2 用的就是它，**带** `GEOIP,CN`）、
[generated-clash-shipped.yaml](generated-clash-shipped.yaml)（§2 的 C，也是**最终发布的形态**）、
[generated-singbox.json](generated-singbox.json)（§4 的 D）。

---

## 2 · 🔴 头号发现：`GEOIP,CN` 拿不到数据库时，**整份配置被拒绝加载**

这一条直接推翻了本轮改动第一版里写下的理由。

| 场景 | 结果 |
|---|---|
| **A** 带 `GEOIP,CN` · 全新配置目录 · **断网** | 🔴 `can't download MMDB` → `configuration file test **failed**` |
| **B** 带 `GEOIP,CN` · 全新配置目录 · 可联网 | ✅ 下载 8.6 MB MMDB，2195 ms，通过 |
| **B2** 承接 B（MMDB 已缓存）· 再断网 | ✅ 7 ms，通过 |
| **C** **不带** `GEOIP,CN` · 全新配置目录 · 断网 | ✅ 7 ms，通过 |

第一版的注释写的是：

> 「`GEOIP,CN` 依赖 GeoIP 数据库。数据库缺失时该条规则匹配不上，于是落到 `MATCH` ——
> 降级为全局代理而不是全局直连。这个失败方向是安全的。」

**A 与 C 的对照说明这句话是错的。** 缺数据库不是「这条规则不匹配」，
而是 `rules[8] [GEOIP,CN,DIRECT] error` → **整份配置加载失败**。

而那个数据库来自 `github.com/MetaCubeX/meta-rules-dat/releases/download/latest/geoip.metadb` ——
**需要下载它的人恰恰是「人在大陆、刚装完客户端、还没有任何可用代理」的那一刻**。
B2 说明缓存过一次之后就没事了，所以风险**特指首次加载**。

**两种失败方向的代价不对称：**

| | 最坏情况 |
|---|---|
| 不带 `GEOIP,CN` | 国内流量也走节点 —— 慢一些、出口账单贵一些，**产品能用** |
| 带 `GEOIP,CN` 但下不到 | **整份订阅加载不了，产品完全不能用** |

首次加载必须选前者。**因此规则表里去掉了 `GEOIP,CN,DIRECT`**，
只留不依赖任何下载数据的私有网段规则 + `MATCH` 兜底，
并加了一条测试挡住「有人顺手把它加回来」。

> ⚠️ 这也意味着 [tutorials-spec](../../03-product/tutorials-spec.md) 排障表里
> 「国内网站变慢/打不开 → `GEOIP,CN` 的位置问题」这一条**目前对不上实现**。
> 要拿回国内直连（出口成本也在等它 —— 见同批的 `evidence/egress-billing-20260820/`，
> 该目录在 PR #12 里，两个 PR 都合入后链接才通），前置是 **B46**：
> 首推客户端是否自带 `geoip.metadb`。

---

## 3 · Clash / mihomo：结构通过

去掉 `GEOIP,CN` 之后的规则表在全新目录 + 断网下 7 ms 通过（§2 的 C）。
`proxies` / `proxy-groups` / `rules` 三段的结构、
`fallback` 与 `select` 两个组、`MATCH` 指向的组名，mihomo 全部接受。

**再说一次：这只证明 mihomo 认得这个结构，不证明 §1 第 1 条那三个字段名是对的。**

---

## 4 · sing-box：结构通过，且**缺 `inbounds` 照样通过**

| 项 | 结果 |
|---|---|
| **D** 三节点配置（含新加的 `route.final`，**无 `inbounds`**） | ✅ 通过 |
| **E** 伪节点配置（账号停用通知，单个 127.0.0.1:1 节点） | ✅ 通过 |

D 同时兑现两件事：

1. 新加的 `route.final` 是合法的、指向的 tag 存在。
2. 🔴 **`sing-box check` 对缺 `inbounds` 的配置是通过的** ——
   这正是 [ADR 0006 §5.1](../../05-adr/0006-api-stack.md) 那条「加分做法」
   （Go 侧 import sing-box 结构体做反序列化校验）**抓不到** roadmap B45 的原因。
   把那条加分做法做了也没用，B45 只能真机验证。

---

## 5 · 顺带解决：SS-2022 的客户端密码形态

`api/internal/handler/subscription.go` 的 `TODO(P1)`：
「shadowsocks2022 的客户端密码形态未实测，订阅暂不下发该节点」。

第一次跑 §4 的 D 时用了 uuid 形态的 32 字符密码，sing-box 直接报：

```
FATAL initialize outbound[3]: bad key length, required 16, got 24
```

换成 `openssl rand -base64 16` 产出的 24 字符 PSK（base64 解出正好 16 字节）后通过。

**结论**：`2022-blake3-aes-128-gcm` 的客户端密码必须是
**恰好 16 字节的 base64**（24 字符、以 `==` 结尾）。
32 字符的 hex/uuid 形态解出 24 字节，直接被拒。

⚠️ **仍不能据此把那个 TODO 关掉**：本条只证明了**客户端侧**要什么形态，
没有证明**节点侧**（v2node / Xray 的 SS-2022 入站）用的是同一个值、同一种编码。
`subscription.go` 跳过 SS-2022 的判断因此仍然成立 ——
但「猜不出形态」这半个理由现在没有了，剩下的是「节点侧与客户端侧是否同一个值」。

---

## 6 · 复现

```bash
# 生成待校验的配置（用仓库里的 subgen，不要手抄）
#   —— 临时写一个 cmd 调 subgen.Render(FormatClash/FormatSingbox, doc) 即可

# mihomo：-d 指一个**全新目录**，否则会命中上一次缓存的 geoip.metadb
docker run --rm --network none -v "$PWD/fresh":/root/.config/mihomo \
  metacubex/mihomo:latest -t -d /root/.config/mihomo

# sing-box
docker run --rm -v "$PWD":/cfg:ro \
  ghcr.io/sagernet/sing-box:latest check -c /cfg/config.json
```

> 🔴 **`-d` 必须指全新目录。** 第一次做这个实验时复用了同一个目录，
> 联网那次把 MMDB 下载进去了，于是后面的「断网」实验全部误判为通过 ——
> 差点得出与 §2 相反的结论。
>
> `ghcr.io` 从这台开发机拉得很慢（与
> [local-development §2.1](../../04-ops/local-development.md) 记的 `gcr.io` 情况类似），
> 首次拉镜像要等几分钟。
