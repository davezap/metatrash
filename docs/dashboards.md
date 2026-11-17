# Dashboards

Core 0.99.0, Slate 0.24.0, Slate Viewer 0.44.0, mtsync 0.99.0.

A dashboard is an ordinary Markdown file with **cards** in it: headline
numbers, lists, tables, pie, bar and line charts, Markdown notices and forms.
Each card is a fenced block with the info string `card`. Text outside the
cards is normal Markdown, so a dashboard reads like any other page; cards
next to each other flow as a grid across the page and stack on a narrow
screen.

The dashboard holds the layout, not the data. A card names a **source** (a
folder of Markdown files, or a CSV file) and a small query over it. People,
agents and forms keep the sources up to date; every dashboard that reads a
source shows a saved change the next time it refreshes.

A space whose root has `dashboard.md` opens on it. A folder's `home` setting
(`.metatrash.json`, Properties → General) picks another file.

## A card

````markdown
```card
title: Jobs by type
size: wide
show: pie
source: jobs/
where: state!=done
group: type
```
````

| Key | Meaning |
| --- | --- |
| `title` | the card's heading |
| `size` | `normal` (default), `wide` (two columns) or `full` (the whole row) |
| `show` | `count`, `list`, `table`, `pie`, `bar`, `line`, `markdown` or `form` |
| `source` | a folder ending in `/` or a `.csv` file (for `markdown` and `form`, a `.md` file); from the dashboard's folder, or from the space root with a leading `/` |
| `where` | filters, separated by `;`; all must hold (below) |
| `group` | a field: one slice, bar or row per value |
| `measure` | `count` (default), or `sum`, `avg`, `min` or `max` followed by a field: `sum amount` |
| `sort` | a field, or `-field` for largest first; with `group`, `value`, `-value` (default) or `group` |
| `limit` | most rows or groups, 1 to 500 (default 50) |
| `columns` | the fields a `table` or `list` shows, separated by commas (a list shows the first) |
| `show-if` | when to show the card (below) |

A `markdown` card can hold its own text after a line `---` instead of a
`source`. A card that can't be read shows its text with what is wrong, and
the rest of the page still draws.

## Sources

- **A folder of Markdown files.** Each `.md` file directly in it is a row;
  its front matter keys are the fields (`state: open`). A list
  (`tags: [a, b]` or `- item` lines) gives several values. Every row also has
  `name` (the file name without `.md`) and `file` (its path), so a list or
  table can link to the file. A filled-in form copy also gives each of its
  answers as a field, so a folder of job forms is a source as it is; a
  copy's own `status`, `submittedBy` and `submittedAt` keep their meaning.
- **A CSV file.** The first line names the fields. Numbers may have commas
  (`"1,200"`).

Values are compared as numbers, `YYYY-MM-DD` dates or `HH:MM` times when
both sides read as one, otherwise as text, ignoring case. A source reads up
to 5000 rows.

## where

Like a form's `show-if`, separated by `;`:

| where | Rows whose field… |
| --- | --- |
| `assignee` | has a value |
| `!due` | has none |
| `state=open\|new` | is one of the values |
| `state!=done` | is anything but `done`, blank included |
| `hours>=2` | is 2 or more; also `>`, `<`, `<=` |
| `due<today` | is a date before today (the viewer's) |
| `assignee=@me` | is the signed-in person's username |

## show-if

A card's `show-if` takes the same operators, `;` between conditions, with
these values:

| Value | Is |
| --- | --- |
| `@count` | how many rows the card's query found |
| `@time` | the viewer's time, `HH:MM` |
| `@today` | the viewer's date |
| `@weekday` | `Mon` to `Sun` |
| `@me` | the viewer's username |
| `@done` | (form cards) the viewer has sent this form, or acknowledged this notice, as its text is now |

`show-if: @count>0` hides an empty overdue card; `show-if: @time>=17:00;
!@done` shows the lock-up form after five until it is sent. A condition only
hides a card: the data always comes with the viewer's own access to the
space.

## Forms and notices in cards

`show: form` with a `source` draws that file in the card:

- A file with fields is a form: the card has its fields and a Send button,
  and sending files a copy exactly as from the form's own page (its
  `submitTo`, the review, My Submissions).
- A file without fields is a **notice**: the card's button is
  **Acknowledge**, which records that the person read this text (no file is
  written). Changing the notice asks everyone again.

A form whose copies need no review says `review: none` in its front matter:
copies are filed with `status: complete`, nothing waits for a reviewer, and a
folder of them (a "Task complete" folder, in the same space if you like) is
a source like any other.

## For agents and programs

- MCP tool `query` (both `/mcp` and `/mcp/account`) takes a card's keys and
  answers like the card would: check a card before writing it into a
  dashboard.
- REST `POST /api/v1/spaces/{space}/query` (and under
  `/api/v1/account/spaces/{space}/` and the site API), body the card's keys
  plus `base` (the dashboard's path) and `today`.
- `GET .../notices?paths=a.md,b.md` and `POST .../notices`
  `{"path", "blob"}` on the account and site APIs.
- mtsync: `query DASHBOARD QUERY`, `notices FILE...`, `notice FILE BLOB`.
