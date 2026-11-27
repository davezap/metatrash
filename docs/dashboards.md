# Dashboards

Core 0.99.0 (0.99.1 for the website home page and same-space `submitTo`; 0.99.2 for visitors), Slate 0.24.0,
Slate Viewer 0.44.0 (0.44.1), mtsync 0.99.0.

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

A space whose root has `dashboard.md` opens on it, in Slate Viewer and on the
website. The top folder's `home` setting (`.metatrash.json`, Properties →
General → Opens on) picks another file; without either a space opens on its
README.md.

## A whole dashboard

Three files at the top of a space. **dashboard.md**:

````markdown
# Team dashboard

```card
title: Open jobs
show: count
source: jobs.csv
where: state!=done
```

```card
title: Jobs by type
show: pie
source: jobs.csv
group: type
```

```card
title: Open jobs
size: wide
show: table
source: jobs.csv
where: state!=done
columns: job, type, assignee, due
sort: due
```

```card
title: Site notice
show: form
source: notice.md
```
````

**jobs.csv**:

```text
job,type,state,assignee,due,hours
Replace gutter,Repair,open,dave,2026-11-20,3
Paint fence,Maintenance,open,dave,2026-11-22,5
Fix door lock,Repair,done,dave,2026-11-15,1
Service heat pump,Service,open,dave,2026-11-25,2
Clean windows,Maintenance,new,,2026-11-28,4
Inspect roof,Inspection,open,dave,2026-11-19,2
Replace tap washer,Repair,new,,2026-11-21,1
Test smoke alarms,Inspection,done,dave,2026-11-14,1
```

**notice.md**:

```markdown
# Site safety notice

Please wear hi-vis on site from Monday 24 November. Press Acknowledge once you have read this.
```

The count shows 6, the pie four slices (Repair 3, Maintenance 2, Inspection
2, Service 1), the table the six open jobs soonest first, and the notice an
Acknowledge button. In Slate Viewer, New File keeps the extension you type, such as `.csv` (0.44.1).

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
| `source` | a folder ending in `/`, a `.csv` file, or a pattern such as `forms/*.md` (for `markdown` and `form`, a `.md` file); from the dashboard's folder, or from the space root with a leading `/` |
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

## Who sees what

A card shows only what the person looking may read. In a space shared on the
web (and the public space), visitors who are not signed in see its dashboards
too: queries answer, notices and forms show as not done, and acknowledging or
sending asks them to sign in (0.99.2). In a private space a signed-out query
is refused like reading a file. `show-if` only hides cards; it never grants
access.

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
- **A pattern** (Core 0.102.0): `forms/*.md`, `*.dash.md`, `guides/**/*.md`.
  Every file that matches is a row, of any type (a PDF too). `*` stands for
  any characters within a name, `?` for one, and `**` on its own for any
  number of folders (none included). Matching ignores case. Rows have
  `name`, `file` and `folder` (the file's folder), and a Markdown file's
  front matter as well, so `columns: title` shows a form's or guide's title.
  Folders' own `AGENTS.md` and settings, and anything under a folder whose
  name starts with a dot, are left out. With `show: list` each row is a link
  to its file, so a pattern makes a small table of contents, a list of other
  dashboards, or a list of forms to fill in:

  ````markdown
  ```card
  title: Forms
  show: list
  source: /forms/*.md
  columns: title
  ```
  ````

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
a source like any other. `review` and `submitTo` go in the form file, not in
the card:

```markdown
---
form: lock-up
title: Locking up
submitTo: done/
review: none
---
Locked? {{yesno name=locked required}}
```

`submitTo` (0.99.1): `/owner/space/folder/`, with the leading `/`, is a
space with the same owner (this one or another). Any other path is a folder
of the form's own space, from the form's folder: `done/`, `reports/done/`,
`../done/`. Before 0.99.1 a space was written `owner/space/folder/`; that
now means a folder of that name beside the form, so such forms need the `/`
added.

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
