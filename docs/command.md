# The command

`rauli` is the program. Run it with no arguments, or with `help`, and it lists its commands. Run `rauli <command> -h` to see the options of one. Everything on this page was run on Linux with Chrome 156, and the output shown is what came back.

| Command | What it does |
| --- | --- |
| `doctor` | Finds the browser, starts it, and says whether that worked. |
| `snapshot` | Prints a page as text, with a reference on each element you can act on. |
| `shot` | Saves a picture of a page to a file. |
| `serve` | Offers the [tools](tools.md) to a program over standard input and output. |
| `watch` | Opens a live view of the browser, to look in and take over. |
| `run` | Runs a Scheme script, or one recipe from it, against a fresh browser. |
| `recipes` | Lists the recipes a set of files defines. |
| `keychain` | Manages the logins an agent may use. |
| `tools` | Lists the tools, as text, JSON or a Markdown reference. |
| `version` | Prints the version. |

Raulí runs on Linux and macOS. Windows is out of scope.

## What every browser command shares

`doctor`, `snapshot`, `shot`, `serve`, `watch` and `run` start a browser, and they take the same options. Each option has a matching setting in the [configuration file](configuration.md), and an option on the command line wins over the file.

| Option | What it does |
| --- | --- |
| `-config file` | Reads settings from this file. The default is `~/.config/rauli/config.toml`, and it's fine for that file not to exist. A file you name must exist. |
| `-headed` | Shows a real browser window. |
| `-profile directory` | Keeps cookies and logins in this directory between runs. The default is a throwaway profile. |
| `-proxy URL` | Sends the browser's traffic through this proxy. |
| `-download-dir directory` | Saves downloads here. The default is to refuse them. |
| `-timeout duration` | Gives up on a single tool call after this long. The default is 45 seconds. |
| `-window WxH` | Sets the size of the window and of new pages. The default is 1280x800. |

Two environment variables matter. `RAULI_CHROME` is the path of the browser to use, and it wins over the search described under [Getting started](getting-started.md). `RAULI_NO_SANDBOX` switches Chromium's sandbox off, which a container usually needs and nothing else does.

Exit status is 0 when the command worked, 1 when it ran and failed, and 2 when it was used wrongly: an unknown command, a missing address, an option with a bad value.

## doctor

```bash
rauli doctor
```

It finds the browser, starts it, reads its version and the address of its developer tools, and closes it. This is the first thing to run on a new machine.

```text
browser:     /usr/bin/google-chrome-stable
version:     156.0.0.0
user agent:  Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36
devtools:    http://127.0.0.1:43545
ok
```

The user agent line shows what pages are told. It says `Chrome`, where a headless browser would say `HeadlessChrome`. The [security model](security.md#the-user-agent) explains why.

When something is wrong it says what, and exits with 1:

```text
rauli doctor: RAULI_CHROME=/nonexistent: stat /nonexistent: no such file or directory
```

## snapshot

```bash
rauli snapshot https://example.com
```

It opens the page, waits for it to load, and prints the page as text. The references, such as `e1`, only mean something inside a session that goes on to act, so from a shell they're there to show you what an agent would see. [Getting started](getting-started.md#reading-a-snapshot) explains the format.

An address without a scheme is taken as `https`. Only `http` and `https` open:

```text
rauli snapshot: "file:///etc/hostname" is not a web address
```

## shot

```bash
rauli shot https://example.com page.png
```

It saves a picture of the page and says what it saved. No window opens. A PNG is the default, `-jpeg` saves a smaller JPEG, and `-full-page` captures the whole scrollable page rather than the part you'd see.

```text
Saved page.png. Screenshot of http://127.0.0.1:8765/ (1280x800)
```

## serve

```bash
rauli serve
```

It starts a browser and speaks the Model Context Protocol on standard input and output. A program that speaks it can list the tools and call them. Standard output carries protocol messages and nothing else, and anything Raulí has to say goes to standard error. The browser closes when the program closes standard input.

Most agent programs ask for a command to run, and this is it. A configuration file for a program that lists its servers in JSON looks like this:

```json
{
  "mcpServers": {
    "browser": {
      "command": "rauli",
      "args": ["serve", "--config", "/home/you/.config/rauli/config.toml"]
    }
  }
}
```

Calls run one at a time, in the order they arrive, because a browser tab is a single thing to act on. A program that sends several at once gets the answers in order. The protocol versions it speaks are `2024-11-05`, `2025-03-26` and `2025-06-18`.

With `-watch` it also serves the [live view](watching.md) and prints its address on standard error. `-listen` picks the address for that. `-recipe` offers the recipes in a file or folder as extra tools, and may be repeated; [Recipes](recipes.md) says what they are.

## watch

```bash
rauli watch --profile ~/.local/share/rauli/profile https://example.com
```

It starts a browser and a live view of it, and prints the address of the view. [Watching](watching.md) is about what you can do there. This is also how you sign in to a site by hand, so that a profile you keep is signed in the next time a program uses it. Press Ctrl-C to stop.

```text
Live view: http://127.0.0.1:39491/?token=_CGhUaBkqqkummFH-EVXTVOHQFqGz0He
Press Ctrl-C to stop.
```

## run

```bash
rauli run signup.scm --recipe signup --arg email=ana@example.com
```

It loads a Scheme file and runs it against a fresh browser, with the same rules and keychain as `serve`. With `-recipe` it runs that recipe. Without, it runs the procedure called `main` if the file defines one, and the file's only recipe otherwise. What the script returns goes to standard output. `-arg name=value` gives a recipe a parameter and may be repeated, and `-save-images` names a folder for the screenshots the run takes. [Recipes](recipes.md) is where to learn to write one.

## recipes

```bash
rauli recipes recipes/
rauli recipes --json recipes/signup.scm
```

It lists the recipes that load from the files and folders named, and from the paths in the configuration file. It starts no browser, so it works on any machine, and it shows what `serve` would offer as tools.

## keychain

```bash
rauli keychain add --origin https://mail.example.com work-mail user password
rauli keychain list
```

The subcommands are `add`, `list`, `show`, `remove` and `test`. The [Keychain](keychain.md) page walks through them with their output. Options go before the name, and `--config` can appear anywhere.

## tools

```bash
rauli tools
rauli tools --json
rauli tools --markdown
```

It lists the tools without starting a browser. With no option it prints one line for each. `--json` prints the full definitions with their argument schemas, and `--markdown` prints the reference that the [Tools](tools.md) page is built from.

## version

```bash
rauli version
```

```text
rauli v0.0.0-20261023023200-7b5e3e542599 (go1.26.10-X:nodwarf5)
```

It takes no arguments, and `rauli version -h` shows its usage.

## When something goes wrong

These are the messages I got when I made each mistake on purpose. The `snapshot` ones apply equally to `shot`.

| What happened | What you see |
| --- | --- |
| Nothing is listening at the address | `could not load http://127.0.0.1:8767/: net::ERR_CONNECTION_REFUSED` |
| The name doesn't resolve | `could not load http://no-such-host.invalid/: net::ERR_NAME_NOT_RESOLVED` |
| The port is one Chromium won't open | `could not load http://127.0.0.1:6000/: net::ERR_UNSAFE_PORT` |
| A setting is misspelt | `bad.toml:1: there is no setting "browzer"` |
| The window size can't be read | `--window: "100x100" is not a window size: write width and height in pixels, from 200 to 8000, such as 1280x800` |
| No address was given | `give one address to open, such as rauli snapshot https://example.com` |
| A named configuration file is missing | `configuration file: open nofile.toml: no such file or directory` |
| A rule refused the address | `not allowed: http://127.0.0.1:8766/ is not on the list of sites the rules allow` |

A timeout shorter than the browser needs to start ends with `Target.getTargets: context deadline exceeded`. That message is about the timeout, whatever it names.
