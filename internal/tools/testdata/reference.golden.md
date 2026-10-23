### `back`

Go back one page in the tab's history.

| Argument | Type | What it is |
| --- | --- | --- |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `click`

Click an element, in the middle, with a real mouse click. If it loads a page, waits for it.

| Argument | Type | What it is |
| --- | --- | --- |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `double` | boolean | Double-click. |
| `right` | boolean | Use the right button. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `click_at`

Click a point in the visible page, in pixels from its top left corner. For things a snapshot does not list, such as a drawing area or a custom control.

| Argument | Type | What it is |
| --- | --- | --- |
| `x` | number, required | Pixels from the left. |
| `y` | number, required | Pixels from the top. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `console`

Read what the page has written to its console since the last time, and any dialogs it raised.

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

It takes no arguments.

### `credentials`

List the logins the keychain holds: their names and which fields each has (such as user, password, totp), and where each may be used. Never the values.

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

It takes no arguments.

### `eval`

Run a JavaScript expression in the page and return its value as JSON. A last resort, for reading something the snapshot does not show; rules may refuse it.

| Argument | Type | What it is |
| --- | --- | --- |
| `expression` | string, required | The expression. |

### `fill_credential`

Fill a field from the keychain, for example the user or password of a login, or a one-time code. The secret goes from the keychain straight into the page; you never see it. Use credentials to see what is available.

| Argument | Type | What it is |
| --- | --- | --- |
| `field` | string, required | Which part: user, password, totp and so on. |
| `handle` | string, required | The login's name. |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |
| `submit` | boolean | Press Enter afterwards. |

### `forward`

Go forward one page in the tab's history.

| Argument | Type | What it is |
| --- | --- | --- |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `hover`

Move the pointer over an element, which opens menus and tooltips.

| Argument | Type | What it is |
| --- | --- | --- |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `navigate`

Go to a web address in the current tab and wait for the page to load. Addresses without a scheme are taken as https. Returns a snapshot of the page.

| Argument | Type | What it is |
| --- | --- | --- |
| `url` | string, required | The address to open. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `press`

Press a key or a combination, such as Enter, Tab, Escape, ArrowDown, a single character, or Control+a.

| Argument | Type | What it is |
| --- | --- | --- |
| `key` | string, required | The key or combination. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `reload`

Load the current page again and wait for it to finish.

| Argument | Type | What it is |
| --- | --- | --- |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `resize`

Change the size of the page's viewport, in pixels.

| Argument | Type | What it is |
| --- | --- | --- |
| `height` | integer, required | Height. |
| `width` | integer, required | Width. |

### `screenshot`

Take a picture of the page, or of one element. It works without any window being open.

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

| Argument | Type | What it is |
| --- | --- | --- |
| `fullPage` | boolean | Capture the whole scrollable page rather than the visible part. |
| `jpeg` | boolean | Return a JPEG, which is smaller, instead of a PNG. |
| `ref` | string | Capture only this element. |

### `scroll`

Scroll the page. A positive dy scrolls down. With no arguments it scrolls down 600 pixels.

| Argument | Type | What it is |
| --- | --- | --- |
| `dx` | number | Pixels to the right. |
| `dy` | number | Pixels down. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `select`

Choose an option of a drop-down list, by its visible text or its value.

| Argument | Type | What it is |
| --- | --- | --- |
| `option` | string, required | The option's text or value. |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `snapshot`

Read the current page as text: headings, text, links with their addresses, and form controls. Every element you can act on has a reference such as e12, which the other tools take. References end when the page changes, so take a new snapshot after anything that loads a page.

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

It takes no arguments.

### `tabs`

List the open tabs, open a new one, switch to one, or close one. Tabs are numbered from 0; the one the other tools act on is marked with *.

| Argument | Type | What it is |
| --- | --- | --- |
| `action` | string | What to do. Default list. One of: list, new, select, close. |
| `index` | integer | The tab to select or close. Closing defaults to the current tab. |
| `url` | string | An address to open in the new tab. |

### `text`

Read the visible text of the page, in reading order, without the structure a snapshot gives. Useful for long articles.

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

It takes no arguments.

### `type`

Type text into a field. By default it adds to what is there; set replace to start over. Set submit to press Enter afterwards. Do not use this for passwords: use fill_credential, which keeps the secret out of the conversation.

| Argument | Type | What it is |
| --- | --- | --- |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `text` | string, required | What to type. |
| `replace` | boolean | Select the field's contents first, so the text replaces them. |
| `slowly` | boolean | Send each character as a key press, for pages that listen for keys. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |
| `submit` | boolean | Press Enter afterwards. |

### `upload`

Put files into a file input, as choosing them in the file dialog would.

| Argument | Type | What it is |
| --- | --- | --- |
| `paths` | list of string, required | Absolute paths of the files. |
| `ref` | string, required | Element reference from the latest snapshot, such as e12. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |

### `wait`

Wait for some text to appear on the page, or for a number of seconds (at most 30).

It only looks: it changes nothing in the page, so a policy can let it through where it would stop an action.

| Argument | Type | What it is |
| --- | --- | --- |
| `seconds` | number | Seconds to wait. |
| `snapshot` | boolean | Return a fresh snapshot afterwards. Default true. |
| `text` | string | Text to wait for. |
