# Forms

0.49.0. A form is a Markdown template with fields in it. People fill it in
with Slate Viewer (Fill In) or `mtsync submit`; Metatrash checks the answers
and files a filled-in copy in a review folder, usually in a private space
only reviewers belong to. The person who sends it needs no access there.

## A template

A template is an ordinary `.md` file. Its front matter makes it a form:

```markdown
---
form: leave-request
title: Leave request
submitTo: dave/hr-review/leave/
---

<!-- Fill this in at least two weeks ahead. -->

# Leave request

Full name: {{text name=full_name required max=80}}

First day: {{date name=start required min=today}}  Last day: {{date name=end required}}

Type of leave: {{choice name=type required}}
- ( ) Annual
- ( ) Sick
- ( ) Parental

Anything your manager should know:
{{textarea name=notes rows=4 max=2000}}

{{sign name=signature required}}
```

- `form` is the form's short name: lowercase letters, digits and hyphens, up
  to 48. It goes into each copy's file name.
- `title` is shown to people; it defaults to `form`.
- `submitTo` is `owner/space/folder/`: where copies are filed. The space
  must have the **same owner** as the template's space, so a template can
  never send answers to somebody else. Leave the folder out to file copies
  at the root. The folder must not be inside a GitHub folder.
- `<!-- comments -->` are notes for the person filling in: Slate shows them
  as hints in fill mode, and leaves them out of the filed copy.

A file whose front matter also has `submittedAt` is a filed copy, not a
template.

## Fields

A field is a `{{type name=… attribute…}}` token. Attributes are
`key=value`, `key="value with spaces"` (`\"` and `\\` inside quotes) or a
bare flag. Tokens inside code (fenced blocks or `` `spans` ``) and inside
comments are text, not fields.

Every field takes:

| Attribute | Meaning |
| --- | --- |
| `name` | required; letters, digits and `_`, starting with a letter, up to 64. The key of the answer, unique in the form |
| `label` | what to call the field in messages (defaults to the name) |
| `required` | the answer can't be blank |
| `placeholder` | grey hint text in the box |

| Type | Answer | Its own attributes |
| --- | --- | --- |
| `text` | one line | `min`, `max` (length; default max 200, at most 2000), `pattern` (a regular expression the whole answer must match) |
| `textarea` | several lines | `min`, `max` (default 5000, at most 20000), `rows` |
| `number` | a number | `min`, `max`, `step` (counted from `min`, or 0) |
| `date` | `YYYY-MM-DD` | `min`, `max` (a date, or `today`) |
| `time` | `HH:MM` | `min`, `max` |
| `email` | an email address | |
| `phone` | a phone number (digits, spaces, `+ ( ) - .`; 6 to 15 digits) | |
| `choice` | one of the options; with `multi`, a list of them | `multi`; with it, `min`, `max` (how many to pick) |
| `select` | one of the options, as a drop-down | |
| `yesno` | `Yes` or `No` | |
| `sign` | `"true"`: the copy records the signed-in name and the time | |

A `choice` or `select` takes its options from the list right below its line
(one blank line may come between). Write single choices as `- ( ) Annual`,
several as `- [ ] Laptop`, drop-downs as `- Auckland`; the box is optional.
Only one choice or select can start on a line. Outside a field, a task list
(`- [ ] …`) keeps its usual meaning.

Patterns use Go's regular expressions (RE2) on the server, which is the
check that counts. Slate checks them too where JavaScript reads them the same
way; keep to the common subset (`[A-Z]{2}[0-9]{4}`, `\d`, `|`, groups).

Limits: 200 fields, 100 options per field, 64 KB of answers.

## The filed copy

The server writes a new file into the `submitTo` folder, named
`<space slug>-<username>-<form>-<YYYY-MM-DDTHHMMZ>.md` (`-2`, `-3`… for more
in the same minute). Its front matter holds the answers for agents and
reports, and its text is the template with every field filled in and the
comments left out:

```markdown
---
form: leave-request
title: "Leave request"
template: "dave/team/hr/forms/leave-request.md"
templateRevision: 3f2a9c1…
submittedBy: "pat"
submittedAt: 2026-10-23T09:14:00Z
status: submitted
answers:
  full_name: "Pat"
  start: "2026-11-03"
  end: "2026-11-07"
  type: "Annual"
  notes: ""
  signature: "pat, 2026-10-23T09:14:00Z"
---
# Leave request

Full name: **Pat**

First day: **2026-11-03**  Last day: **2026-11-07**

Type of leave:
- [x] Annual
- [ ] Sick
- [ ] Parental
…
```

Answers are escaped so they can't add Markdown or fields; multi-line answers
are block quotes. The commit says `Form submission: <form>` and names the
person who sent it, like any change they make.

The person keeps a receipt with the copy (My Submissions in Slate Viewer,
`mtsync submissions`), kept in the account database.

## Sending

Slate Viewer: open the template, choose Fill In, answer, Send. Work in
progress is kept on that computer only.

mtsync: `mtsync submit FILE ANSWERS`, with the answers as JSON
(`{"full_name": "Pat", "type": "Annual", "signature": "true"}`).

REST: `POST /api/v1/account/spaces/{space}/submissions` (see the API
contract, Form submissions). Only a person can send a form; agents can't.

## Setting up a review space

1. Create a private space for reviewers, such as `hr-review`, and invite
   only them.
2. In the space everyone uses, write the template with
   `submitTo: <you>/hr-review/<folder>/`.
3. Everyone who can read the template can now send it; copies appear in the
   review space, and only its members can read them.
