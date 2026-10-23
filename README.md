# Raulí

Raulí is a browser that people and coding agents can both use. It will be Chromium, driven over the DevTools Protocol, so pages behave the way they do in the browser you already use.

Nothing is built yet. This repository is where it will live, next to [Coigüe](https://github.com/fclaude/coigue), a small terminal editor, and [Brote](https://github.com/fclaude/brote), the Scheme interpreter both of them can embed. The three are southern beeches and a shoot, which is how the names came about: raulí (*Nothofagus alpina*) is a Chilean tree and a close relative of the coigüe.

## What it should do

An agent can open pages, read them as text, click, type and take screenshots without a window ever appearing. Someone can look at the same browser whenever they want, and take over when a page needs a person.

The agent doesn't hold the secrets. Passwords and tokens stay in a keychain, and the browser fills them in itself, so the agent only says which login to use. Rules decide which sites it may visit and which actions need an approval first.

## Status

This file is all there is. The first thing to build is a small program that starts Chromium and gives an agent a page to read and a way to click.

## Licence

MIT. See `LICENSE`.
