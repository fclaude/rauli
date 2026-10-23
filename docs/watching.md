# Watching

The live view is a web page that shows the browser an agent is using. It shows the tab as a picture that updates as the page does, lists what the agent has done, shows the actions that are waiting for a person to approve, and lets you take the tab over and click and type yourself. You open it in a browser of your own.

There are two ways to start it. `rauli watch` starts a browser and the view and nothing else, which is what you want for signing in by hand. `rauli serve --watch` serves the tools to a program as usual and also serves the view, so you can look in on an agent while it works.

```bash
rauli watch --profile ~/.local/share/rauli/profile https://example.com
```

```text
Live view: http://127.0.0.1:39491/?token=_CGhUaBkqqkummFH-EVXTVOHQFqGz0He
Press Ctrl-C to stop.
```

With `serve --watch` the same line goes to standard error, since standard output belongs to the protocol. An address given to `watch` is opened first, if the rules allow it.

## What the page has

The page is laid out as headings and a few controls, and the whole of it can be used from the keyboard:

- Control has the Take control and Give back buttons, and a field for sending text to the page.
- Browser shows the current page's title and address, and the tab as a picture.
- Waiting for approval lists each action that a rule asked about, with the element, the reason and the buttons to approve or refuse.
- What the agent did is a log of the tool calls, newest at the bottom. Typed text shows as a length, never as the text.
- Developer tools gives the address the browser's own tools can attach to.

While you have control, clicks, scrolling and keys on the picture go to the page. Shift+Escape moves keyboard focus out of the picture, so it never traps you.

## Taking control

Choose Take control and the agent's calls wait. They don't fail and they don't interleave with what you do. They run when you choose Give back, and one that has waited longer than its time limit ends with an error saying a person is using the browser, so the agent can try again later. A forgotten hold can't hang an agent forever.

What you do isn't in the log, because the tools don't see it. Element references the agent got before your turn stop working after it, since a reference names a node in one document. The agent gets an error and takes a new snapshot, and it never clicks something else by accident. Taking over doesn't pause what the page does by itself, such as timers and open connections.

## When a page needs a person

Raulí never tries to defeat a CAPTCHA or a bot check. There's no solver in it, and nothing in it pretends to be a person to get past a test meant for one. When a page needs a person, whether for a check like that, a code sent to a phone or a decision that's yours to make, the server tells the agent to explain what the page needs and ask you to take over the tab, and Take control in the live view is how you take it. Do what the page asks, give the tab back, and the agent carries on from where you left it.

## Approvals

When a rule asks for approval and no other way of answering has been chosen, the live view is who is asked while it's running. The question appears under Waiting for approval, and the agent's call waits for your answer. A request nobody answers within the approval timeout is refused. Without the view, the rules refuse such an action and say how to choose a way of asking. The [Rules](rules.md#who-is-asked) page has the modes.

## Why it is safe to leave open

The view is a window into a browser that may be signed in to real accounts, so it's built to be hard to reach by accident and by a hostile page.

- It listens on this machine only. `--listen` and the `[watch]` table accept a loopback address and refuse any other: `the live view listens on loopback addresses only, not "0.0.0.0"`.
- Every request needs the random token in the printed address. The first visit sets it as a cookie that scripts can't read and other sites can't send, and the address redirects to a clean one. A request without it is refused with a 401. I checked both.
- The `Host` header must name this machine, which stops a page on another site from reaching the view through DNS rebinding. The picture's connection must come from the view's own page.
- The page loads nothing from anywhere else, and every response says so in a content security policy.

Treat the address like a password. Anyone who has it and can reach your loopback interface can take control of a browser that may be signed in.

## From Go

A program that embeds Raulí gets the same thing from the session. `Hold` takes the browser for a person and `Release` gives it back, `Subscribe` delivers an event for every tool call, and the live view is a handler built on those. The events record typed text by length only. A reader that falls behind loses events and never slows the browser.
