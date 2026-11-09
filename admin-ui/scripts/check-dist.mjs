// Fails the build if the output would violate the admin panel's CSP:
// no inline <script>, no <style> elements, no style="" attributes, no
// external URLs in index.html.
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const dist = new URL("../../server/internal/adminui/dist/", import.meta.url).pathname;
const html = readFileSync(join(dist, "index.html"), "utf8");
const problems = [];
for (const m of html.matchAll(/<script\b([^>]*)>/gi)) {
  if (!/\bsrc=/.test(m[1])) problems.push("inline <script> in index.html");
}
if (/<style\b/i.test(html)) problems.push("<style> element in index.html");
if (/\sstyle\s*=/i.test(html)) problems.push("style= attribute in index.html");
if (/(src|href)=["']?(https?:)?\/\//i.test(html)) problems.push("external URL in index.html");
const assets = readdirSync(join(dist, "assets"));
for (const f of assets.filter((f) => f.endsWith(".css"))) {
  const css = readFileSync(join(dist, "assets", f), "utf8");
  if (/url\((["']?)(https?:)?\/\//i.test(css)) problems.push(`external url() in ${f}`);
  if (/@import\s/i.test(css)) problems.push(`@import in ${f}`);
}
for (const f of assets.filter((f) => f.endsWith(".js"))) {
  const js = readFileSync(join(dist, "assets", f), "utf8");
  if (/https?:\/\/fonts\.(googleapis|gstatic)\.com/.test(js)) problems.push(`Google Fonts URL in ${f}`);
}
if (problems.length) {
  console.error("CSP check failed:\n  " + problems.join("\n  "));
  process.exit(1);
}
console.log(`CSP check ok (${assets.length} assets)`);
