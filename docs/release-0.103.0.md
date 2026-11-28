# Core 0.103.0: Markdown templates

Prepared 2026-11-28 by Claude (thread "Build collaborative forms and
workflows", slice 1), released with Company 0.96.77 and Slate Viewer 0.50.0;
Slate stays 0.26.2. Tier: Core (one person running their own Metatrash wants
a Meetings folder whose new files start as meeting notes).

1. **Settings**: `templates` (up to 20 `.md` files of the space) and
   `defaultTemplate` in a folder's `.metatrash.json`. `/Templates/x.md` is
   from the space root; other paths are from the folder that names them.
   Inherited, lockable; `defaultTemplate: ""` turns an inherited default off.
2. **Templates**: ordinary Markdown. `{{@today}}`, `{{@now}}`, `{{@name}}`,
   `{{@email}}` and `{{@title}}` are filled in; front matter `newName` names
   the new file and is dropped from it. Form templates are refused.
3. **Making files**: `POST .../from-template` `{template?, folder, name?,
   ifInState, today?, now?}` (one commit; the folder's default template
   without `template`, the template's newName without `name`, numbered when
   taken) and MCP `write` with `fromTemplate` instead of `text`.

Upgrade: nothing to apply; start 0.103.0. Rollback: 0.102.1 doesn't know
`templates` or `defaultTemplate`. It reads a settings file that has them as
no settings at all (that folder's locks and read-only files lapse) and won't
save it, so remove those keys before going back.

Tests: `TestTemplateSettingsChecked`, `TestResolveTemplate`,
`TestStripTemplateKeys`, `TestNewFilesFromTemplates`,
`TestNewFileFromTemplateOverREST`; all of `internal/service` passes without a
database.
