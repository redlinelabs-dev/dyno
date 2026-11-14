// Renders the raster icons in public/ from public/icon.svg with headless
// Chromium. Run after changing the SVG: `bun web/icons.ts`. Set CHROME to a
// Chromium binary; the default is Playwright's cached headless shell.
import { join } from "node:path";
import { chromium } from "playwright-core";

const pub = join(import.meta.dir, "public");
const svg = await Bun.file(join(pub, "icon.svg")).text();
// Home-screen icons are full-bleed squares: iOS and Android cut their own corners.
const square = svg.replace(/ rx="\d+"/, "");
const chrome =
  process.env.CHROME ??
  `${process.env.HOME}/.cache/ms-playwright/chromium_headless_shell-1208/chrome-headless-shell-linux64/chrome-headless-shell`;

const browser = await chromium.launch({ executablePath: chrome });
const page = await browser.newPage();
async function png(source: string, size: number): Promise<Uint8Array> {
  await page.setViewportSize({ width: size, height: size });
  await page.setContent(`<style>*{margin:0}svg{display:block;width:${size}px;height:${size}px}</style>${source}`);
  return page.screenshot({ omitBackground: true });
}

for (const [name, size] of [["apple-touch-icon.png", 180], ["icon-192.png", 192], ["icon-512.png", 512]] as const) {
  await Bun.write(join(pub, name), await png(square, size));
}

// favicon.ico for clients that ask for /favicon.ico regardless of <link>:
// an ICO directory whose entries are PNGs, which every current browser reads.
const sizes = [16, 32, 48];
const images = [];
for (const s of sizes) images.push(await png(svg, s));
const head = new DataView(new ArrayBuffer(6 + 16 * sizes.length));
head.setUint16(2, 1, true); // type: icon
head.setUint16(4, sizes.length, true);
let offset = head.byteLength;
sizes.forEach((s, i) => {
  const e = 6 + 16 * i;
  head.setUint8(e, s);
  head.setUint8(e + 1, s);
  head.setUint16(e + 4, 1, true); // colour planes
  head.setUint16(e + 6, 32, true); // bits per pixel
  head.setUint32(e + 8, images[i]!.byteLength, true);
  head.setUint32(e + 12, offset, true);
  offset += images[i]!.byteLength;
});
await Bun.write(join(pub, "favicon.ico"), new Blob([head, ...images]));
await browser.close();
