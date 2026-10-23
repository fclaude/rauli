# The Raulí site

The site is a small [Zola](https://www.getzola.org) project that GitHub Pages publishes at <https://fclaude.github.io/rauli/>. Its pages are the repository's own Markdown in `docs/`. Nothing under `site/content` is checked in, and `site/build.py` writes it every time. The Tools page is the exception to "the repository's own Markdown": the program that knows the tools writes it, so it can't describe a tool that isn't there or leave one out. The Getting started page includes the example program from `docs/examples`, which CI builds.

## Building it

You need Zola 0.23.6, Python 3 and Go:

```bash
python3 site/build.py
cd site
zola build
python3 finish.py
zola check --skip-external-links
```

`build.py` stops when a tool in the code has no entry in the reference, when an entry names a tool that is gone, and when an include or a link points at something that doesn't exist. `python3 -m unittest discover -s site` tests those checks. `finish.py` runs on the built pages. It lets you Tab to every code block and table that scrolls sideways, names each of them for a screen reader, gives tables a caption and `scope`, and numbers the code blocks of a kind when a page has more than one. `.github/workflows/pages.yml` does the same steps, with a pinned Zola, and publishes the result. For the first publish, the repository's Pages setting has to use "GitHub Actions" as its source.

## Checking accessibility

`bash site/audit/run.sh` builds the site for a local address, serves it and runs three checks. The first measures the contrast of every text and background pair, which is the table below. The second runs axe-core in Chromium (`npx playwright install chromium` the first time) on every page in light and dark, at 1280 and 320 pixels wide, with the WCAG 2.0, 2.1 and 2.2 A and AA rules plus axe's best-practice rules. It reports no violations, and nothing it can't decide either. The third presses Tab like a keyboard user would: the skip link comes first and moves focus to the content, every scrollable region can be reached, each page has one `h1` and no skipped heading levels, and the page doesn't scroll sideways at 320 pixels or with the text at 200%.

The runner refuses to start if something is already listening on its port (8099, or the `PORT` you set), because another server would answer in its place and the audit would be reading the wrong pages.

## Contrast

These are the measured ratios (WCAG asks for 4.5 for text and 3 for the focus ring and borders). `site/audit/contrast.py` prints them from the stylesheet and from the colours Zola wrote into the code blocks:

| Pair | Light | Dark | Light, more contrast | Dark, more contrast |
| --- | --- | --- | --- | --- |
| Body text on the page | 15.33 | 14.34 | 21.00 | 21.00 |
| Muted text on the page | 6.77 | 7.71 | 21.00 | 21.00 |
| Links on the page | 6.89 | 8.49 | 11.37 | 14.21 |
| Inline code on its background | 13.65 | 12.13 | 21.00 | 21.00 |
| Muted text on the code background | 6.02 | 6.53 | 21.00 | 21.00 |
| Links on the code background | 6.13 | 7.19 | 11.37 | 14.21 |
| Skip link text on its button | 7.31 | 8.90 | 11.37 | 14.21 |
| Focus ring on the page | 6.89 | 8.49 | 11.37 | 14.21 |
| Borders on the page | 3.55 | 3.40 | 21.00 | 21.00 |
| Weakest token in a code block | 5.04 | 9.23 | | |

The code colours come from Zola's `github-light-high-contrast` and `github-dark-high-contrast` themes, the pair that was measured to have the strongest weakest token for the sibling sites too.
