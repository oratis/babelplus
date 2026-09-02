/**
 * 生成扩展图标（16 / 32 / 48 / 128 PNG）。
 *
 * 为什么自己写而不是放一张设计稿导出的 PNG：仓库里不该出现来历不明的二进制；
 * 这个脚本就是图标的**源**，改颜色改线宽重跑即可，产物可复现。
 * 图形照 client-products-spec §3.1：单色地球（圆环）+ 一条穿过的线；连接态的角标由
 * `chrome.action.setBadgeText` 加，不需要第二套图。
 *
 * 只依赖 Node 内置的 zlib（PNG = 签名 + IHDR + IDAT(deflate) + IEND，各块带 CRC32）。
 *
 * 用法：node scripts/gen-icons.mjs   → 写到 public/icons/
 */
import { deflateSync } from 'node:zlib';
import { mkdirSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const OUT = join(dirname(fileURLToPath(import.meta.url)), '..', 'public', 'icons');
const SIZES = [16, 32, 48, 128];
// 与 mockup 的 --accent 同色：#1B4D8F
const COLOR = [0x1b, 0x4d, 0x8f];

const CRC_TABLE = new Uint32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k += 1) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const t = Buffer.from(type, 'ascii');
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([t, data])));
  return Buffer.concat([len, t, data, crc]);
}
function png(size, rgba) {
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  ihdr[10] = 0;
  ihdr[11] = 0;
  ihdr[12] = 0;
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y += 1) {
    raw[y * (size * 4 + 1)] = 0; // filter: none
    rgba.copy(raw, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  }
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

/**
 * 几何：地球 = 外圈 + 一条竖向经线椭圆；「穿过的线」= 赤道从圆环左侧穿到右侧并伸出圆外。
 * 刻意不用斜线：斜线穿圆读起来是「禁止」标志，第一版就是这么画错的。
 */
function coverage(size, px, py) {
  const SS = 4; // 4×4 超采样抗锯齿
  const c = size / 2;
  const rOuter = size * 0.40;
  const stroke = Math.max(1.5, size * 0.10);
  const rInner = rOuter - stroke;
  const thin = Math.max(1, stroke * 0.55);
  let hit = 0;
  for (let i = 0; i < SS; i += 1) {
    for (let j = 0; j < SS; j += 1) {
      const x = px + (i + 0.5) / SS - c;
      const y = py + (j + 0.5) / SS - c;
      const d = Math.hypot(x, y);
      const inRing = d <= rOuter && d >= rInner;
      // 经线：竖向椭圆，半宽 0.42·rOuter，半高 = rInner（画在圆环内部）
      const ex = x / (rOuter * 0.42);
      const ey = y / rInner;
      const e = Math.hypot(ex, ey);
      const eStroke = thin / (rOuter * 0.42);
      const inMeridian = Math.abs(e - 1) <= eStroke / 2 && d <= rInner + 0.5;
      // 赤道：从圆环左边缘出发，穿过圆心，向右伸出到 0.49·size
      const inEquator = Math.abs(y) <= thin / 2 && x >= -rOuter && x <= size * 0.49;
      if (inRing || inMeridian || inEquator) hit += 1;
    }
  }
  return hit / (SS * SS);
}

mkdirSync(OUT, { recursive: true });
for (const size of SIZES) {
  const rgba = Buffer.alloc(size * size * 4);
  for (let y = 0; y < size; y += 1) {
    for (let x = 0; x < size; x += 1) {
      const a = coverage(size, x, y);
      const o = (y * size + x) * 4;
      rgba[o] = COLOR[0];
      rgba[o + 1] = COLOR[1];
      rgba[o + 2] = COLOR[2];
      rgba[o + 3] = Math.round(a * 255);
    }
  }
  const file = join(OUT, `icon${size}.png`);
  writeFileSync(file, png(size, rgba));
  console.log(`wrote ${file}`);
}
