const { chromium } = require('playwright');
const BASE = process.env.BASE || 'http://127.0.0.1:8099';
const PAGES = ['/', '/getting-started/', '/tools/', '/rules/', '/keychain/', '/watching/', '/security/', '/architecture/'];
const desc = () => { const e = document.activeElement; return e ? (e.tagName.toLowerCase() + (e.id ? '#' + e.id : '') + ':' + (e.getAttribute('aria-label') || e.textContent.trim().slice(0, 28))) : 'none'; };
(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1000, height: 700 } });
  const page = await ctx.newPage();
  let failures = 0;
  const ok = (cond, msg) => { if (!cond) { failures++; console.log('  FAIL', msg); } };
  for (const p of PAGES) {
    await page.goto(BASE + p);
    console.log(p);
    const hidden = await page.evaluate(() => { const r = document.querySelector('.skip-link').getBoundingClientRect(); return r.bottom <= 0; });
    ok(hidden, 'skip link should be off-screen until focused');
    await page.keyboard.press('Tab');
    const first = await page.evaluate(desc);
    const vis = await page.evaluate(() => { const r = document.activeElement.getBoundingClientRect(); return r.top >= 0 && r.bottom > 0 && r.right <= innerWidth; });
    ok(first.startsWith('a:Skip to the content'), 'first Tab is the skip link, got ' + first);
    ok(vis, 'skip link is visible when focused');
    const ring = await page.evaluate(() => { const s = getComputedStyle(document.activeElement); return s.outlineStyle !== 'none' || s.backgroundColor !== 'rgba(0, 0, 0, 0)'; });
    ok(ring, 'skip link shows focus');
    await page.keyboard.press('Tab');
    const second = await page.evaluate(desc);
    ok(second.startsWith('a:Raulí'), 'second Tab is the site title, got ' + second);
    const navCount = await page.evaluate(() => document.querySelectorAll('header nav a').length);
    const navOrder = [];
    for (let i = 0; i < navCount; i++) { await page.keyboard.press('Tab'); navOrder.push(await page.evaluate(desc)); }
    ok(navOrder.every(x => x.startsWith('a:')), 'nav items are links');
    // go back to the skip link and use it
    await page.goto(BASE + p);
    await page.keyboard.press('Tab');
    await page.keyboard.press('Enter');
    const afterSkip = await page.evaluate(desc);
    ok(afterSkip.startsWith('main#main'), 'Enter on skip link moves focus to main, got ' + afterSkip);
    await page.keyboard.press('Tab');
    const inMain = await page.evaluate(() => document.activeElement.closest('main') !== null);
    ok(inMain, 'next Tab after skip is inside main');
    // every code block and table region is reachable with Tab
    const regions = await page.evaluate(() => [...document.querySelectorAll('main [tabindex="0"]')].length);
    let reached = 0;
    await page.goto(BASE + p);
    for (let i = 0; i < 200; i++) {
      await page.keyboard.press('Tab');
      const isRegion = await page.evaluate(() => document.activeElement.matches('main [tabindex="0"]'));
      if (isRegion) reached++;
      const atEnd = await page.evaluate(() => document.activeElement === document.body);
      if (atEnd) break;
    }
    ok(reached === regions, `all ${regions} scrollable regions reachable by Tab, reached ${reached}`);
    // heading outline
    const outline = await page.evaluate(() => [...document.querySelectorAll('h1,h2,h3,h4,h5,h6')].map(h => +h.tagName[1]));
    let skip = false; for (let i = 1; i < outline.length; i++) if (outline[i] - outline[i - 1] > 1) skip = true;
    ok(outline.filter(x => x === 1).length === 1 && !skip, 'one h1 and no skipped levels: ' + outline.join(''));
    // landmarks
    const lm = await page.evaluate(() => ({ header: document.querySelectorAll('header').length, nav: document.querySelectorAll('nav').length, main: document.querySelectorAll('main').length, footer: document.querySelectorAll('footer').length, lang: document.documentElement.lang, current: document.querySelectorAll('[aria-current="page"]').length }));
    ok(lm.header === 1 && lm.nav === 1 && lm.main === 1 && lm.footer === 1 && lm.lang === 'en' && lm.current === 1, 'landmarks/lang/current page: ' + JSON.stringify(lm));
    console.log('  tab order:', ['skip', 'title', ...navOrder.map(x => x.replace(/^a:/, ''))].join(' > '), '| regions', regions, '| headings', outline.join(''));
  }
  // zoom: 200% text at 320 CSS px, and 400% at desktop (320 CSS px wide) for reflow
  for (const [w, fs] of [[320, '200%'], [1280, '200%'], [320, '100%']]) {
    const c2 = await browser.newContext({ viewport: { width: w, height: 700 } });
    const pg = await c2.newPage();
    for (const p of PAGES) {
      await pg.goto(BASE + p);
      await pg.addStyleTag({ content: `html{font-size:${fs} !important}` });
      const o = await pg.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      if (o > 0) { failures++; console.log(`  FAIL page scrolls sideways by ${o}px at ${w}px, text ${fs}, ${p}`); }
    }
    console.log(`reflow ok at ${w}px wide, text ${fs}`);
    await c2.close();
  }
  console.log(failures ? `FAILURES: ${failures}` : 'ALL KEYBOARD AND REFLOW CHECKS PASSED');
  await browser.close();
  if (failures) process.exit(1);
})();
