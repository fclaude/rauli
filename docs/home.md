# Raulí

Raulí lets a program use a web browser the way a person does. It starts a Chromium-based browser, usually without a window, and drives it over the DevTools Protocol. A program can read a page as text, click, type, scroll and take screenshots, and the page sees real mouse and keyboard events, so sites behave as they do in the browser you already use.

It's named after the raulí, a Chilean beech and a close relative of the coigüe. [Coigüe](https://github.com/fclaude/coigue) is a small terminal editor and [Brote](https://github.com/fclaude/brote) is a small Scheme interpreter, and the three live next to each other on GitHub.

## What it does

A program asks for a page and gets it back as text, with a reference on each element it can act on. It then clicks, types, scrolls and takes screenshots through the [tools](tools.md). The `rauli` command serves them to any program that speaks the Model Context Protocol, and from a shell it can print a page or save a screenshot.

A rules engine decides which addresses the browser may reach and which actions need a person's approval. A keychain hands a login to the page without the program ever seeing it. A live view shows the browser while it works, and lets you take the tab over. Small Scheme programs called [recipes](recipes.md) can be offered to an agent as tools of their own. Raulí runs on Linux and macOS.

## Where to go next

[Getting started](getting-started.md) installs it and reads a page. The [command](command.md) and [configuration](configuration.md) pages say what it takes. A fresh install allows any website, so read [Rules](rules.md) before you point an agent at anything that matters, and the [security model](security.md) for exactly what the guards do and don't protect against. The [architecture](architecture.md) page shows how the layers fit.
