# Configuration

The settings live in one TOML file, `~/.config/rauli/config.toml` unless `XDG_CONFIG_HOME` says otherwise, or the file named with `--config`. With no file everything has a default, and the defaults are described below. Relative paths in the file are taken from the file's own folder, so the result doesn't depend on where you run `rauli`, and `~/` means your home folder.

A key that Raulí doesn't know is an error that names the file and the line. I made that strict on purpose. A misspelt setting that was quietly ignored would leave you believing a rule was in force when it wasn't.

```text
bad.toml:1: there is no setting "browzer"
```

The file has five tables: `browser`, `rules`, `keychain`, `watch` and `recipes`. Every block of TOML on this page is read by Raulí's own parser when the tests run, so the examples can't drift from the code.

## A file to start from

This is a reasonable first file. It lets the agent visit one site, asks a person before anything that looks like a payment, and keeps a log of what the rules decided.

```toml
[browser]
profile = "~/.local/share/rauli/profile"

[rules.network]
allow = ["https://example.com", "https://*.example.com"]

[[rules.actions.rule]]
kind = "click"
element = "*pay*"
decision = "ask"
reason = "payments need a person"

[rules.audit]
file = "audit.jsonl"
```

## browser

| Key | Default | What it does |
| --- | --- | --- |
| `profile` | none | A folder that keeps cookies and logins between runs. With none, every run gets a new profile that is deleted when the browser closes. |
| `headed` | `false` | Shows a real browser window. |
| `window` | `"1280x800"` | The size of the window and of new pages, from 200 to 8000 pixels on each side. |
| `proxy` | none | A proxy URL for the browser's traffic. |
| `download_dir` | none | Where downloads are saved. With none, downloads are refused. |
| `timeout` | `"45s"` | How long one tool call may take. It takes a number and a unit, such as `"90s"` or `"2m"`. |

The default profile is throwaway. A profile you name is opt-in, either here or with `--profile`, and it holds your cookies. The agent must never read them, and no tool reads that folder. The [security model](security.md#the-profile-and-eval) says what the page's own scripts can still reach.

## rules

The rules are the subject of the [Rules](rules.md) page, which has the details and recipes. The keys are:

| Key | What it does |
| --- | --- |
| `network.allow` | The addresses the browser may reach. When the list isn't empty, everything else is refused. |
| `network.deny` | Addresses that are refused even when `allow` lists them. |
| `network.block_private` | Refuses localhost, private addresses and names ending in `.local`. An address in `allow` stays reachable. |
| `actions.default` | What happens to an action no rule matches: `allow`, `ask` or `deny`. |
| `actions.rule` | A list of rules for actions. The first one that matches decides. |
| `approval.mode` | How a person is asked: `deny`, `terminal` or `command`. |
| `approval.command` | The program for `command` mode, as a list. |
| `approval.timeout` | How long to wait for an answer. The default is two minutes. |
| `audit.file` | Where the audit log is written, one JSON object per line. With none, there's no log. |
| `audit.requests` | `denied` (the default) logs the requests the rules refused. `all` logs every request a page makes. |

## keychain

```toml
[keychain]
backend = "file"
path = "~/.config/rauli/keychain"
passphrase_env = "RAULI_PASSPHRASE"
```

| Key | What it does |
| --- | --- |
| `backend` | `keyring` uses the system's secret store. `file` uses an encrypted file. `command` fetches each secret from a program of yours. `memory` keeps logins until the program stops, and exists for tests. With no backend there's no keychain, and the `credentials` tool says so. |
| `service` | The name the logins are filed under in the system's secret store. |
| `path` | The encrypted file, for `file`. The default is `rauli/keychain` in your configuration folder. |
| `passphrase_env` | The environment variable that holds the passphrase, for `file`. |
| `passphrase_command` | A command that prints the passphrase, as a list, for `file`. |
| `command` | The program that prints a secret, as a list, for `command`. Raulí adds the login and the field as arguments. |
| `timeout` | How long that program may take, for `command`. The default is `"10s"`. |
| `handles` | The logins of the `command` backend, each with a `name`, its `fields`, and either `origins` or `anywhere = true`. |

`path`, `passphrase_env` and `passphrase_command` belong to the `file` backend, and `command`, `timeout` and `handles` to the `command` backend. Setting one for another backend is an error that names the key. With neither a variable nor a command, the file backend asks for the passphrase on the terminal. The [Keychain](keychain.md) page covers all of this.

## watch

```toml
[watch]
listen = "127.0.0.1"
port = 8765
```

| Key | Default | What it does |
| --- | --- | --- |
| `listen` | `127.0.0.1` | The address the live view listens on. It has to be a loopback address. |
| `port` | a free one | The port. |

`--listen` on the command line sets address and port together and wins over both. The view listens on this machine only, and [Watching](watching.md) says why.

## recipes

```toml
[recipes]
paths = ["recipes"]
deadline = "2m"
max_output = 100000
```

| Key | Default | What it does |
| --- | --- | --- |
| `paths` | none | Recipe files, or folders of them. A relative path is taken from the configuration file's folder. |
| `deadline` | `"2m"` | How long one run of a recipe may take. |
| `max_output` | `100000` | The most text a recipe may return, in characters. |

What a recipe is, and what it can touch, is on the [Recipes](recipes.md) page.
