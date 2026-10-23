#!/usr/bin/env python3
"""Assemble site/content from the repository's own documentation.

docs/*.md stays the canonical text (GitHub shows it), so the site is built from
it and nothing under site/content is checked in. Run it from anywhere, then
build with Zola and run the finishing step:

    python3 site/build.py && (cd site && zola build && python3 finish.py && zola check --skip-external-links)

Two markers in a page are filled in here:

    <!-- include: docs/examples/readpage/main.go go -->   a file from the repository, as a code block
    <!-- tools-reference -->                              the reference for every tool

The tool reference is not written by hand. It comes from the command that knows
the tools (`go run ./cmd/rauli tools --markdown`), reshaped here for the site,
and the build stops if a tool the command lists (`tools --json`) has no entry in
it, or an entry names a tool that is gone.

The script is deterministic: the same inputs give the same files, and it stops
with a message if a marker names something that isn't there.
"""
import json
import pathlib
import re
import shutil
import subprocess
import sys

SITE = pathlib.Path(__file__).resolve().parent
ROOT = SITE.parent
REPO = "https://github.com/fclaude/rauli/blob/main/"

# source, target in content/, label in the navigation, weight
PAGES = [
    ("docs/home.md", "_index.md", "Home", 1),
    ("docs/getting-started.md", "getting-started.md", "Getting started", 2),
    ("docs/command.md", "command.md", "The command", 3),
    ("docs/configuration.md", "configuration.md", "Configuration", 4),
    ("docs/tools.md", "tools.md", "Tools", 5),
    ("docs/rules.md", "rules.md", "Rules", 6),
    ("docs/keychain.md", "keychain.md", "Keychain", 7),
    ("docs/watching.md", "watching.md", "Watching", 8),
    ("docs/recipes.md", "recipes.md", "Recipes", 9),
    ("docs/security.md", "security.md", "Security model", 10),
    ("docs/architecture.md", "architecture.md", "Architecture", 11),
]
TARGETS = {pathlib.PurePosixPath(src).name: dst for src, dst, _, _ in PAGES}
TARGETS["README.md"] = "_index.md"

# The highlighter on the site knows these names; the repository's markdown uses
# the ones GitHub knows.
FENCE_LANGUAGES = {"sh": "bash"}

LINK = re.compile(r"(\]\()([^)\s]+)(\))")
FENCE = re.compile(r"^(\s*)(`{3,})([A-Za-z0-9_+-]*)")
INCLUDE_MARKER = re.compile(r"^<!-- include: (\S+) (\S+) -->$")
TOOLS_MARKER = "<!-- tools-reference -->"
TOOL_HEADING = re.compile(r"^### `([a-z_]+)`$", re.M)


class BuildError(Exception):
    pass


def go(*args):
    """Run a Go program from the repository root and return what it printed."""
    try:
        done = subprocess.run(["go", "run", *args], cwd=ROOT, capture_output=True, text=True, check=True)
    except FileNotFoundError:
        raise BuildError("the tool reference needs Go on PATH")
    except subprocess.CalledProcessError as err:
        raise BuildError("go run %s failed:\n%s" % (" ".join(args), err.stderr.strip()))
    return done.stdout


def tools_reference():
    """The reference Markdown for the site, checked against the tools the code has."""
    names = sorted(tool["name"] for tool in json.loads(go("./cmd/rauli", "tools", "--json")))
    if not names:
        raise BuildError("the command reports no tools")
    text = reshape_reference(go("./cmd/rauli", "tools", "--markdown"))
    check_reference(names, text)
    return text.rstrip("\n"), names


def split_row(line):
    """The cells of a Markdown table row, honouring \\| inside a cell."""
    cells = re.split(r"(?<!\\)\|", line.strip())
    return [c.strip() for c in cells[1:-1]]


def reshape_reference(text):
    """Fit the command's reference into the Tools page.

    The command prints a title, an introduction, a `## name` heading for each tool and
    a four-column table. The page has its own title and introduction, puts the tools
    one level lower, and folds "required" into the type column, which leaves three
    columns that fit a phone without sideways scrolling.
    """
    first = text.find("\n## ")
    if first < 0:
        raise BuildError("the command's reference has no tool headings")
    out = []
    for line in text[first + 1 :].split("\n"):
        m = re.match(r"^## (\S+)$", line)
        if m:
            out.append("### `%s`" % m.group(1))
            continue
        if line.startswith("|"):
            cells = split_row(line)
            if cells[:4] == ["Argument", "Type", "Required", "What it does"]:
                out.append("| Argument | Type | What it does |")
                continue
            if len(cells) == 4 and re.fullmatch(r"-+", cells[0]):
                out.append("| --- | --- | --- |")
                continue
            if len(cells) == 4 and cells[2] in ("yes", "no"):
                typ = cells[1] + (", required" if cells[2] == "yes" else "")
                out.append("| %s | %s | %s |" % (cells[0], typ, cells[3]))
                continue
            raise BuildError("a row of the command's reference has a shape this script does not know: " + line)
        out.append(line)
    return "\n".join(out)


def check_reference(names, text):
    """Stop the build when the reference and the tools in the code disagree."""
    documented = TOOL_HEADING.findall(text)
    missing = sorted(set(names) - set(documented))
    stale = sorted(set(documented) - set(names))
    if missing:
        raise BuildError("these tools have no entry in the reference: " + ", ".join(missing))
    if stale:
        raise BuildError("the reference documents tools that no longer exist: " + ", ".join(stale))


def rewrite(target, source):
    """A link written for a file in the repository, as a link on the site."""
    if re.match(r"[a-z]+:", target) or target.startswith("#"):
        return target
    path, _, frag = target.partition("#")
    frag = "#" + frag if frag else ""
    name = pathlib.PurePosixPath(path).name
    if name in TARGETS and path.endswith(".md"):
        return "@/" + TARGETS[name] + frag
    resolved = pathlib.PurePosixPath(source).parent / path
    parts = []
    for part in resolved.parts:
        if part == "..":
            parts.pop()
        elif part != ".":
            parts.append(part)
    return REPO + "/".join(parts) + frag


def fill_markers(lines, src, reference):
    out = []
    for line in lines:
        stripped = line.strip()
        if stripped == TOOLS_MARKER:
            out += reference.split("\n")
            continue
        m = INCLUDE_MARKER.match(stripped)
        if m:
            path = ROOT / m.group(1)
            if not path.is_file():
                raise BuildError("%s: cannot include %s" % (src, m.group(1)))
            lang = FENCE_LANGUAGES.get(m.group(2), m.group(2))
            out += ["```" + lang] + path.read_text(encoding="utf-8").rstrip("\n").split("\n") + ["```"]
            continue
        out.append(line)
    return out


def convert(src, dst, nav, weight, reference):
    text = (ROOT / src).read_text(encoding="utf-8")
    title = None
    body = []
    fence = None  # the backticks that opened the code block we are in
    for line in text.split("\n"):
        m = FENCE.match(line)
        if m and fence is None:
            fence = m.group(2)
            if m.group(3):
                line = m.group(1) + fence + FENCE_LANGUAGES.get(m.group(3), m.group(3))
        elif m and line.strip() == m.group(2) and len(m.group(2)) >= len(fence):
            fence = None
        elif fence is None and title is None and line.startswith("# "):
            title = line[2:].strip()
            continue
        body.append(line)
    if title is None:
        raise BuildError("%s has no # title" % src)
    body = fill_markers(body, src, reference)
    text = "\n".join(body).lstrip("\n")
    text = LINK.sub(lambda m: m.group(1) + rewrite(m.group(2), src) + m.group(3), text)
    front = ["+++", 'title = "%s"' % title.replace('"', '\\"'), "weight = %d" % weight]
    if dst == "_index.md":
        front += ['sort_by = "weight"', 'template = "index.html"']
    front += ["", "[extra]", 'nav = "%s"' % nav, "+++", ""]
    return "\n".join(front) + text


def main(check_only=False):
    reference, names = tools_reference()
    pages = {}
    for src, dst, nav, weight in PAGES:
        pages[dst] = convert(src, dst, nav, weight, reference)
    for dst, text in pages.items():
        for target in re.findall(r"\]\(@/([^)#\s]+)", text):
            if target not in pages:
                raise BuildError("%s links to @/%s, which is not a page of the site" % (dst, target))
    if check_only:
        print("site/build.py: %d pages assemble, %d tools documented, every marker resolves" % (len(pages), len(names)))
        return
    content = SITE / "content"
    if content.exists():
        shutil.rmtree(content)
    content.mkdir()
    for dst, text in pages.items():
        (content / dst).write_text(text, encoding="utf-8")


if __name__ == "__main__":
    try:
        main(check_only="--check" in sys.argv)
    except BuildError as err:
        sys.exit("site/build.py: " + str(err))
