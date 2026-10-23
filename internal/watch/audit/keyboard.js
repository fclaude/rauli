// A keyboard-only walk of the live view: the order Tab visits things, that every
// stop shows where focus is, that the buttons work from the keyboard, that the
// picture never traps the keyboard, and that the page reflows at 320 pixels and
// at twice the text size.
const { chromium } = require('playwright');
const URL = process.env.VIEW_URL;
let failed = 0;
const check = (ok, what) => { console.log((ok ? 'ok   ' : 'FAIL ') + what); if (!ok) failed++; };

async function focused(page) {
  return page.evaluate(() => {
    const e = document.activeElement;
    const cs = getComputedStyle(e);
    const name = e.getAttribute('aria-label') || e.textContent.trim().slice(0, 40) || e.id || e.tagName;
    return { tag: e.tagName, id: e.id, name, outline: cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) >= 2 };
  });
}

(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await page.goto(URL);
  await page.waitForFunction(() => +document.body.dataset.frames >= 1, null, { timeout: 20000 });

  // Tab order while watching: the skip link, Take control, the picture, then
  // the approval's two buttons and the log. Disabled controls are skipped.
  const seen = [];
  for (let i = 0; i < 6; i++) {
    await page.keyboard.press('Tab');
    const f = await focused(page);
    seen.push(f);
    check(f.outline, `focus is visible on "${f.name}"`);
  }
  const names = seen.map((f) => f.name);
  console.log('     order: ' + names.join(' > '));
  check(names[0].startsWith('Skip to'), 'the skip link comes first');
  check(names[1] === 'Take control', 'Take control comes next');
  check(seen[2].id === 'view', 'the picture is reachable by keyboard');
  check(names[3].startsWith('Approve') && names[4].startsWith('Deny'), 'the approval buttons follow, named for what they decide');
  check(seen[5].id === 'logbox', 'the log scrolls by keyboard');

  // Take control from the keyboard.
  await page.focus('#take');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.body.dataset.control === 'you');
  check(true, 'Enter on Take control takes control');
  check(await page.evaluate(() => !document.getElementById('give').disabled && !document.getElementById('say-text').disabled), 'Give back and the text box become available');

  // The picture forwards keys, so there must be a way out of it.
  await page.focus('#view');
  await page.keyboard.press('Shift+Escape');
  const out = await focused(page);
  check(out.id !== 'view', `Shift+Escape moves focus out of the picture (to "${out.name}")`);
  // While the picture forwards keys, Tab goes to the page, which is why the page
  // says how to get out. Focus stays on the picture until Shift+Escape.
  await page.focus('#view');
  await page.keyboard.press('Tab');
  check((await focused(page)).id === 'view', 'while in control Tab is forwarded to the page, and the page tells the person how to leave');
  check(await page.evaluate(() => document.getElementById('view-help').textContent.includes('Shift+Escape')), 'the instructions name Shift+Escape');

  // Give back from the keyboard.
  await page.focus('#give');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.body.dataset.control === 'agent');
  check(true, 'Enter on Give back gives control back');
  check((await focused(page)).id === 'take', 'focus moves to Take control after giving back');

  // Reflow at 320 pixels and at twice the text size.
  for (const width of [1280, 320]) {
    await page.setViewportSize({ width, height: 900 });
    await page.evaluate(() => { document.documentElement.style.fontSize = '200%'; });
    const over = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    check(over <= 0, `no sideways scrolling at ${width}px with text at 200% (overflow ${over}px)`);
    await page.evaluate(() => { document.documentElement.style.fontSize = ''; });
  }
  await page.setViewportSize({ width: 320, height: 900 });
  const small = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  check(small <= 0, `no sideways scrolling at 320px (overflow ${small}px)`);

  // The page has one level-one heading and no skipped levels.
  const heads = await page.evaluate(() => [...document.querySelectorAll('h1,h2,h3,h4,h5,h6')].map((h) => +h.tagName[1]));
  check(heads.filter((n) => n === 1).length === 1, 'exactly one h1');
  check(heads.every((n, i) => i === 0 || n - heads[i - 1] <= 1), 'no skipped heading levels');

  await browser.close();
  console.log(failed === 0 ? 'ALL KEYBOARD AND REFLOW CHECKS PASSED' : `${failed} CHECK(S) FAILED`);
  if (failed) process.exit(1);
})().catch((e) => { console.error(e); process.exit(1); });
