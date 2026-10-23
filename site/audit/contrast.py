#!/usr/bin/env python3
"""Measure the contrast of every text and background pair on the site.

It reads the colours from site/static/style.css, in the four palettes the
stylesheet has (light, dark, and each with the browser's "more contrast"
setting on), and the colours Zola wrote into the code blocks of the built pages
(site/public). WCAG asks for 4.5:1 for text and 3:1 for focus rings and
borders. The script prints a table and exits with 1 if any pair is under its
threshold.

    python3 site/audit/contrast.py [BUILT_SITE_DIR]   # default: site/public
"""
import pathlib
import re
import sys

SITE = pathlib.Path(__file__).resolve().parent.parent
CSS = (SITE / "static" / "style.css").read_text(encoding="utf-8")


def luminance(hex_colour):
    h = hex_colour.lstrip("#")
    r, g, b = (int(h[i : i + 2], 16) / 255 for i in (0, 2, 4))

    def channel(c):
        return c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4

    return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b)


def ratio(a, b):
    la, lb = sorted((luminance(a), luminance(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)


def variables(block):
    return dict(re.findall(r"--([a-z-]+):\s*(#[0-9a-fA-F]{6})", block))


def palettes():
    """The stylesheet's variables for each of the four palettes."""
    light = variables(re.search(r":root \{(.*?)\n\}", CSS, re.S).group(1))
    dark = dict(light, **variables(re.search(r"prefers-color-scheme: dark\) \{\s*:root \{(.*?)\}", CSS, re.S).group(1)))
    more = variables(re.search(r"@media \(prefers-contrast: more\) \{\s*:root \{(.*?)\}", CSS, re.S).group(1))
    more_dark = variables(re.search(r"\(prefers-contrast: more\) and \(prefers-color-scheme: dark\) \{\s*:root \{(.*?)\}", CSS, re.S).group(1))
    return {
        "light": light,
        "dark": dark,
        "light, more contrast": dict(light, **more),
        "dark, more contrast": dict(dark, **more_dark),
    }


PAIRS = [
    ("Body text on the page", "fg", "bg", 4.5),
    ("Muted text on the page", "muted", "bg", 4.5),
    ("Links on the page", "accent", "bg", 4.5),
    ("Inline code on its background", "fg", "code-bg", 4.5),
    ("Muted text on the code background", "muted", "code-bg", 4.5),
    ("Links on the code background", "accent", "code-bg", 4.5),
    ("Skip link text on its button", "accent-text", "accent", 4.5),
    ("Focus ring on the page", "accent", "bg", 3.0),
    ("Borders on the page", "rule", "bg", 3.0),
]


def code_tokens():
    """The weakest code colour in each theme, from the built pages."""
    public = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else SITE / "public"
    pages = sorted(public.rglob("*.html"))
    if not pages:
        sys.exit("contrast.py: no built pages under %s; run zola build first" % public)
    worst = {"light": (99.0, ""), "dark": (99.0, "")}
    for page in pages:
        text = page.read_text(encoding="utf-8")
        # The block's own colours are the background; each span's are a token.
        bg = re.search(r"background-color: light-dark\((#[0-9A-Fa-f]{6}), (#[0-9A-Fa-f]{6})\)", text)
        if not bg:
            continue
        backgrounds = {"light": bg.group(1), "dark": bg.group(2)}
        for light, dark in re.findall(r'<span style="color: light-dark\((#[0-9A-Fa-f]{6}), (#[0-9A-Fa-f]{6})\)', text):
            for mode, colour in (("light", light), ("dark", dark)):
                r = ratio(colour, backgrounds[mode])
                if r < worst[mode][0]:
                    worst[mode] = (r, colour)
    return worst


def main():
    pal = palettes()
    failed = False
    names = list(pal)
    print("| Pair | " + " | ".join(names) + " |")
    print("| --- | " + " | ".join("---" for _ in names) + " |")
    for label, fg, bg, need in PAIRS:
        cells = []
        for n in names:
            r = ratio(pal[n][fg], pal[n][bg])
            if r < need:
                failed = True
            cells.append("%.2f" % r)
        print("| %s | %s |" % (label, " | ".join(cells)))
    worst = code_tokens()
    cells = ["%.2f" % worst["light"][0], "%.2f" % worst["dark"][0], "-", "-"]
    print("| Weakest token in a code block | %s |" % " | ".join(cells))
    if worst["light"][0] < 4.5 or worst["dark"][0] < 4.5:
        failed = True
    if failed:
        print("\nSome pair is under its threshold.", file=sys.stderr)
        sys.exit(1)


if __name__ == "__main__":
    main()
