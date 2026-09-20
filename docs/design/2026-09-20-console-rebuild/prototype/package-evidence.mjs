import {
  readFileSync,
  writeFileSync,
  mkdirSync,
  copyFileSync,
  existsSync,
} from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import vm from "node:vm";

const sourceDir = path.dirname(fileURLToPath(import.meta.url));
const designDir = path.resolve(sourceDir, "..");
const repo = path.resolve(designDir, "../../..");
const output = path.join(repo, "output/playwright/console-rebuild");
const context = vm.createContext({});
vm.runInContext(
  readFileSync(path.join(sourceDir, "catalog.js"), "utf8") +
    ";globalThis.pages=UX_PAGES;",
  context,
);
const pages = context.pages;
const cli = readFileSync(path.join(output, "qa-cli-output.txt"), "utf8");
const matched = cli.match(/### Result\n([\s\S]*?)\n### Ran Playwright code/);
if (!matched)
  throw new Error(
    "Browser verification did not return a result; do not publish unverified artifacts.",
  );
const result = JSON.parse(matched[1]);
if (
  result.errors.length ||
  result.externalRequests.length ||
  result.pages.length !== pages.length ||
  [...result.interactions, ...result.responsive].some((r) => !r.passed)
)
  throw new Error("Browser verification failed.");
const imagesDir = path.join(designDir, "assets/screens");
mkdirSync(imagesDir, { recursive: true });
for (const [id] of pages) {
  const image = path.join(output, "screens", id + ".png");
  if (!existsSync(image)) throw new Error("Missing screenshot " + id);
  copyFileSync(image, path.join(imagesDir, id + ".png"));
}
for (const id of [
  "state-empty",
  "state-loading",
  "state-error",
  "state-offline",
  "state-denied",
  "state-missing",
  "responsive-1024",
  "responsive-390",
])
  copyFileSync(
    path.join(output, "screens", id + ".png"),
    path.join(imagesDir, id + ".png"),
  );
writeFileSync(
  path.join(designDir, "browser-validation.json"),
  JSON.stringify(result, null, 2) + "\n",
);
const sampleCLI = readFileSync(
  path.join(output, "sample-qa-cli-output.txt"),
  "utf8",
);
const sampleMatch = sampleCLI.match(
  /### Result\n([\s\S]*?)\n### Ran Playwright code/,
);
if (!sampleMatch) throw new Error("Missing sample validation result.");
const sampleResult = JSON.parse(sampleMatch[1]);
if (
  sampleResult.errors.length ||
  sampleResult.samples.length !== 5 ||
  sampleResult.samples.some((s) => !s.passed)
)
  throw new Error("Sample validation failed.");
writeFileSync(
  path.join(designDir, "sample-validation.json"),
  JSON.stringify(sampleResult, null, 2) + "\n",
);
let atlas =
  "# 全页面原型图册\n\n本图册由页面字典和 Chromium 实际截图生成。截图使用 1440px 宽，长页面保留完整高度。每张图对应单文件原型中的实际页面，不是文字占位。\n\n[返回方案总览](README.md) · [交互规格](03-interaction-spec.md) · [下载可点击原型](prototype.html)\n\n";
atlas +=
  "## 页面索引\n\n| ID | 页面 | 原型地址 | 主要职责 |\n|---|---|---|---|\n";
for (const [id, route, title, , desc] of pages)
  atlas += `| ${id} | [${title}](#${id.toLowerCase()}) | \`${route}\` | ${desc} |\n`;
for (const group of [...new Set(pages.map((p) => p[3]))]) {
  atlas += `\n## ${group}\n\n`;
  for (const [id, route, title, , desc] of pages.filter((p) => p[3] === group))
    atlas += `<a id="${id.toLowerCase()}"></a>\n\n### ${id} · ${title}\n\n地址：\`${route}\`。${desc}。\n\n![${id} ${title}](assets/screens/${id}.png)\n\n`;
}
atlas += "## 全局状态与响应式\n\n";
for (const [id, title] of [
  ["state-empty", "空数据"],
  ["state-loading", "加载中"],
  ["state-error", "请求失败"],
  ["state-offline", "断网"],
  ["state-denied", "无权限"],
  ["state-missing", "404"],
  ["responsive-1024", "1024px 布局"],
  ["responsive-390", "390px 布局"],
])
  atlas += `### ${title}\n\n![${title}](assets/screens/${id}.png)\n\n`;
writeFileSync(path.join(designDir, "06-page-atlas.md"), atlas.trimEnd() + '\n');
console.log(
  JSON.stringify({
    pages: pages.length,
    extraRoutes: result.extraRoutes.length,
    interactionChecks: result.interactions.length,
    responsiveChecks: result.responsive.length,
    images: pages.length + 8,
  }),
);
