const { chromium } = require('playwright');
const fs = require('fs');
const AXE = fs.readFileSync(require.resolve('axe-core/axe.min.js'), 'utf8');
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const PAGES = ['/', '/getting-started/', '/tools/', '/rules/', '/keychain/', '/watching/', '/security/', '/architecture/', '/404.html'];
(async () => {
  const browser = await chromium.launch();
  let total = {violations: 0, serious: 0, incomplete: 0};
  for (const scheme of ['light', 'dark']) {
    for (const width of [1280, 320]) {
      const ctx = await browser.newContext({ viewport: { width, height: 800 }, colorScheme: scheme });
      const page = await ctx.newPage();
      for (const p of PAGES) {
        await page.goto(BASE + p);
        await page.addScriptTag({ content: AXE });
        const res = await page.evaluate(async () => await axe.run(document, {
          runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice'] },
        }));
        const bad = res.violations;
        total.violations += bad.length;
        total.serious += bad.filter(v => v.impact === 'serious' || v.impact === 'critical').length;
        total.incomplete += res.incomplete.length;
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        console.log(`${scheme.padEnd(5)} ${String(width).padEnd(4)} ${p.padEnd(12)} violations=${bad.length} incomplete=${res.incomplete.length} passes=${res.passes.length} pageOverflowPx=${overflow}`);
        for (const v of bad) console.log(`   ! ${v.impact} ${v.id}: ${v.help} (${v.nodes.length} nodes) e.g. ${v.nodes[0].target.join(' ')}`);
        for (const v of res.incomplete) console.log(`   ? incomplete ${v.id}: ${v.nodes.length} nodes e.g. ${v.nodes[0].target.join(' ')}`);
      }
      await ctx.close();
    }
  }
  console.log('TOTAL', JSON.stringify(total));
  await browser.close();
  if (total.violations > 0 || total.incomplete > 0) process.exit(1);
})();
