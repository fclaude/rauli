const { chromium } = require('playwright');
const fs = require('fs');
const AXE = fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
const URL = process.env.VIEW_URL;

async function waitFrames(page) {
  await page.waitForFunction(() => +document.body.dataset.frames >= 1, null, { timeout: 20000 });
}

(async () => {
  const browser = await chromium.launch();
  const total = { violations: 0, serious: 0, incomplete: 0 };
  for (const scheme of ['light', 'dark']) {
    for (const width of [1280, 320]) {
      for (const state of ['watching', 'in control']) {
        // axe-core has to be injected into the page, which the page's own policy forbids, so the audit
        // bypasses the policy. The Go tests load the page under the real policy.
        const ctx = await browser.newContext({ viewport: { width, height: 900 }, colorScheme: scheme, bypassCSP: true });
        const page = await ctx.newPage();
        await page.goto(URL);
        await waitFrames(page);
        if (state === 'in control') {
          await page.getByRole('button', { name: 'Take control' }).click();
          await page.waitForFunction(() => document.body.dataset.control === 'you');
        }
        await page.addScriptTag({ content: AXE });
        const res = await page.evaluate(async () => await axe.run(document, {
          runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'] },
        }));
        const bad = res.violations;
        total.violations += bad.length;
        total.serious += bad.filter((v) => v.impact === 'serious' || v.impact === 'critical').length;
        total.incomplete += res.incomplete.length;
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        console.log(`${scheme.padEnd(5)} ${String(width).padEnd(4)} ${state.padEnd(10)} violations=${bad.length} incomplete=${res.incomplete.length} passes=${res.passes.length} overflow=${overflow}px`);
        for (const v of bad) console.log(`   ! ${v.impact} ${v.id}: ${v.help} (${v.nodes.length} nodes) e.g. ${v.nodes[0].target.join(' ')}`);
        for (const v of res.incomplete) console.log(`   ? incomplete ${v.id}: ${v.nodes.length} nodes e.g. ${v.nodes[0].target.join(' ')}`);
        if (overflow > 0) total.violations += 1;
        await ctx.close();
      }
    }
  }
  console.log('TOTAL', JSON.stringify(total));
  await browser.close();
  if (total.violations > 0 || total.incomplete > 0) process.exit(1);
})().catch((e) => { console.error(e); process.exit(1); });
