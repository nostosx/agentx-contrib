import assert from "node:assert/strict";
import { pathToFileURL } from "node:url";

const { chromium, webkit } = await import(pathToFileURL(process.env.AGENTX_IT_PLAYWRIGHT).href);
const base = process.argv[2];
for (const [name, engine] of [["chromium", chromium], ["webkit", webkit]]) {
  const browser = await engine.launch({ headless: true });
  try {
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(`${base}/inspect/`);
    assert.match(await page.textContent("body"), /unauthorized/);
    await page.goto(`${base}/login`);
    await page.waitForFunction(() => document.querySelector("#status").textContent === "2 completed spans; 2 evaluations");
    assert.equal(await page.locator("#traces tr").count(), 2);
    await page.locator("#traces button").first().click();
    assert.match(await page.locator("#detail").textContent(), /onerror/);
    assert.equal(await page.evaluate(() => Boolean(window.injected)), false);
    assert.equal(await page.locator("#detail img").count(), 0);
    await page.selectOption("#baseline", "baseline");
    await page.selectOption("#candidate", "candidate");
    await page.click("#compare");
    await page.waitForFunction(() => document.querySelector("#comparison").textContent.includes("improved"));
    const comparison = JSON.parse(await page.locator("#comparison").textContent());
    assert.equal(comparison.pass_rate.baseline, 0);
    assert.equal(comparison.pass_rate.candidate, 1);
    const api = await page.evaluate(async () => (await fetch("./reports")).text());
    assert.equal(api.includes("PRIVATE_"), false);
    await page.click("#refresh");
    await page.waitForFunction(() => document.querySelector("#status").textContent === "2 completed spans; 2 evaluations");
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ browser: name, auth: true, spans: 2, evaluations: 2, comparison: true, xssBlocked: true, privateDataOmitted: true, pageErrors: errors }));
  } finally {
    await browser.close();
  }
}
