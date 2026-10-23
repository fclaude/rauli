#!/usr/bin/env python3
"""Make the built pages easier to use with a keyboard and a screen reader.

Zola writes plain <pre> and <table> elements, and either can be wider than a
phone. A box that scrolls sideways has to be reachable with Tab, or someone
without a mouse can't read its far end, and it needs a name that says what it
is. This step runs on the built site (site/public) and adds, to every page:

  - tabindex, role and an aria-label on each code block, named for its language
  - a scrollable, labelled wrapper around each table, a caption taken from the
    heading above it, and scope="col" on its header cells

It is idempotent: a page that was already finished is left alone.

    cd site && zola build && python3 finish.py
"""
import html
import pathlib
import re
import sys

PUBLIC = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else "public")

CODE_LABELS = {
    "shellscript": "Shell commands",
    "shellsession": "Terminal session",
    "plain": "Text",
    "go": "Go code",
    "html": "HTML",
    "toml": "TOML",
    "json": "JSON",
}
PRE = re.compile(r'<pre class="giallo"([^>]*)><code data-lang="([^"]*)">')
HEADING = re.compile(r"<h[1-6][^>]*>(.*?)</h[1-6]>", re.S)
TABLE = re.compile(r"<table>(.*?)</table>", re.S)


def label_for(lang):
    return CODE_LABELS.get(lang, lang.replace("-", " ").capitalize() + " code")


def strip_tags(text):
    return html.unescape(re.sub(r"<[^>]+>", "", text)).strip()


def finish(text):
    if "table-scroll" in text or 'role="region"' in text:
        return text
    # Two blocks with the same name would be two identical landmarks, so a name
    # used more than once on a page gets "1 of 3", "2 of 3" and so on.
    totals = {}
    for m in PRE.finditer(text):
        totals[label_for(m.group(2))] = totals.get(label_for(m.group(2)), 0) + 1
    seen = {}

    def code_block(m):
        label = label_for(m.group(2))
        if totals[label] > 1:
            seen[label] = seen.get(label, 0) + 1
            label = "%s, %d of %d" % (label, seen[label], totals[label])
        return '<pre class="giallo" tabindex="0" role="region" aria-label="%s"%s><code data-lang="%s">' % (
            label,
            m.group(1),
            m.group(2),
        )

    text = PRE.sub(code_block, text)
    out = []
    pos = 0
    for m in TABLE.finditer(text):
        headings = HEADING.findall(text[: m.start()])
        title = strip_tags(headings[-1]) if headings else "Table"
        body = re.sub(r"<th(?=[\s>])", '<th scope="col"', m.group(1))
        out.append(text[pos : m.start()])
        out.append(
            '<div class="table-scroll" role="region" tabindex="0" aria-label="%s, table">'
            "<table><caption class=\"visually-hidden\">%s</caption>%s</table></div>"
            % (html.escape(title, quote=True), html.escape(title), body)
        )
        pos = m.end()
    out.append(text[pos:])
    return "".join(out)


def main():
    pages = sorted(PUBLIC.rglob("*.html"))
    if not pages:
        sys.exit("site/finish.py: no pages under %s; run zola build first" % PUBLIC)
    for page in pages:
        before = page.read_text(encoding="utf-8")
        after = finish(before)
        if after != before:
            page.write_text(after, encoding="utf-8")


if __name__ == "__main__":
    main()
