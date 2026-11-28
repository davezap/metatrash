package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"metatrash.com/metatrash/internal/filenames"
)

// Markdown templates (0.103.0). A folder's settings may list template files
// and name the one a plain new file starts from:
//
//	{"templates": ["/Templates/meeting.md", "decision.md"],
//	 "defaultTemplate": "/Templates/meeting.md"}
//
// A leading / is the space root; any other path is from the folder whose
// settings name it (../ climbs, never above the root). Both keys are
// inherited (inherit: true) and lockable like other settings.
//
// A template is an ordinary .md file. New files made from it start with its
// text, with {{@today}}, {{@now}}, {{@name}}, {{@email}} and {{@title}}
// filled in (the same @ names forms use for default answers; anything else is
// copied as written). Its front matter may say newName: "{{@today}} Meeting"
// for the new file's name; template-only keys are left out of the new file.
// Form templates (a form key) are filled in, not copied, so they are refused.
//
// The server makes the file in one commit: REST POST .../from-template (the
// Slate Viewer website), and MCP write with fromTemplate in place of text.

const (
	maxFolderTemplates   = 20
	maxTemplateEntry     = 240
	maxTemplateCopyTries = 100
)

// templateOnlyKeys are front matter keys of a template that its new files
// leave out.
var templateOnlyKeys = map[string]bool{"newName": true}

// templatePlaceholder matches {{@today}} and the other fill-ins, with spaces
// allowed inside the braces.
var templatePlaceholder = regexp.MustCompile(`\{\{\s*@(today|now|name|email|title)\s*\}\}`)

// templateValues are what a template's placeholders are filled with.
type templateValues struct {
	Today, Now, Name, Email, Title string
}

func (v templateValues) fill(text string) string {
	return templatePlaceholder.ReplaceAllStringFunc(text, func(m string) string {
		switch templatePlaceholder.FindStringSubmatch(m)[1] {
		case "today":
			return v.Today
		case "now":
			return v.Now
		case "name":
			return v.Name
		case "email":
			return v.Email
		}
		return v.Title
	})
}

// checkTemplateEntry checks one templates entry or defaultTemplate of a
// folder's settings: a .md file path, from the space root with a leading /,
// otherwise from the folder.
func checkTemplateEntry(entry string) error {
	refused := folderConfigError(fmt.Sprintf("template %q must be a Markdown file in this space: /Templates/meeting.md from the space root, or meeting.md or ../Templates/meeting.md from this folder.", entry))
	rest := strings.TrimPrefix(entry, "/")
	if rest == "" || len(entry) > maxTemplateEntry || !strings.HasSuffix(strings.ToLower(rest), ".md") || strings.ContainsAny(entry, "\\") {
		return refused
	}
	segments := strings.Split(rest, "/")
	for i, segment := range segments {
		if segment == ".." || (segment == "." && i < len(segments)-1) {
			if strings.HasPrefix(entry, "/") {
				return refused
			}
			continue
		}
		if !filenames.Segment(segment) || strings.HasPrefix(segment, ".") {
			return refused
		}
	}
	return nil
}

// checkTemplateSettings checks templates and defaultTemplate.
func checkTemplateSettings(c folderConfig) error {
	if len(c.Templates) > maxFolderTemplates {
		return folderConfigError(fmt.Sprintf("templates is limited to %d entries.", maxFolderTemplates))
	}
	seen := map[string]bool{}
	for _, entry := range c.Templates {
		if err := checkTemplateEntry(entry); err != nil {
			return err
		}
		if seen[entry] {
			return folderConfigError(fmt.Sprintf("templates lists %q twice.", entry))
		}
		seen[entry] = true
	}
	if c.DefaultTemplate != nil && *c.DefaultTemplate != "" {
		return checkTemplateEntry(*c.DefaultTemplate)
	}
	return nil
}

// resolveTemplate turns a templates entry written in folder ("" for the
// root, otherwise ending in /) into a path in the space, or "" when it
// climbs above the root.
func resolveTemplate(folder, entry string) string {
	if rest, ok := strings.CutPrefix(entry, "/"); ok {
		return rest
	}
	depth := 0
	for _, part := range strings.Split(strings.Trim(folder+entry, "/"), "/") {
		switch part {
		case "..":
			if depth--; depth < 0 {
				return ""
			}
		case ".", "":
		default:
			depth++
		}
	}
	return strings.TrimPrefix(path.Clean("/"+folder+entry), "/")
}

// folderTemplates are a folder's templates and default template as space
// paths, each resolved from the folder whose settings set it. def is "" when
// the folder has none (or turned an inherited one off with "").
type folderTemplates struct {
	Templates []string `json:"templates"`
	Default   string   `json:"defaultTemplate,omitempty"`
}

func (r *repository) templatesOf(ctx context.Context, files map[string]record, folder string) (folderTemplates, error) {
	var out folderTemplates
	haveList, haveDefault := false, false
	for f := folder; ; f = parentFolder(f) {
		text, err := r.configText(ctx, files, f)
		if err != nil {
			return out, err
		}
		var c folderConfig
		_ = json.Unmarshal([]byte(text), &c)
		if !haveList && c.Templates != nil {
			haveList = true
			out.Templates = []string{}
			for _, entry := range c.Templates {
				if p := resolveTemplate(f, entry); p != "" {
					out.Templates = append(out.Templates, p)
				}
			}
		}
		if !haveDefault && c.DefaultTemplate != nil {
			haveDefault = true
			if *c.DefaultTemplate != "" {
				out.Default = resolveTemplate(f, *c.DefaultTemplate)
			}
		}
		if f == "" || c.Inherit == nil || !*c.Inherit || haveList && haveDefault {
			break
		}
	}
	if out.Templates == nil {
		out.Templates = []string{}
	}
	return out, nil
}

// templateRequest makes a new file from a template. Template is a space path
// ("" = the folder's default template); Folder is where the file goes ("" for
// the root, otherwise ending in /); Name is the new file's name, or "" for
// the template's newName. Today and Now are the person's local date and
// time when their client sends them, as for forms.
type templateRequest struct {
	Template  string `json:"template,omitempty"`
	Folder    string `json:"folder"`
	Name      string `json:"name,omitempty"`
	IfInState string `json:"ifInState"`
	Today     string `json:"today,omitempty"`
	Now       string `json:"now,omitempty"`
}

// createFromTemplate makes a new file from a template in one commit.
func (s *Service) createFromTemplate(ctx context.Context, r *repository, space string, in templateRequest) (WriteResult, error) {
	if !hashPattern.MatchString(in.IfInState) {
		return WriteResult{}, invalid("ifInState must be a commit hash from read/list.")
	}
	if in.Folder != "" && (!strings.HasSuffix(in.Folder, "/") || !validPath(strings.TrimSuffix(in.Folder, "/"))) {
		return WriteResult{}, invalid("folder is a folder of this space ending in /, such as Meetings/, or \"\" for the top of the space.")
	}
	if in.Template != "" {
		in.Template = strings.TrimPrefix(in.Template, "/")
		if !validPath(in.Template) {
			return WriteResult{}, invalid("template is the path of a Markdown file in this space, such as Templates/meeting.md.")
		}
	}
	if in.Name != "" && (strings.Contains(in.Name, "/") || !validPath(in.Name)) {
		return WriteResult{}, invalid("name is one file name, such as Weekly meeting.md.")
	}
	var values templateValues
	values.Name, values.Email = s.templatePerson(ctx)
	now := s.now().UTC()
	values.Today, values.Now = s.templateClock(ctx, r, in.Today, in.Now, now)
	value, err := s.queued(ctx, r, func() (any, error) {
		_, files, err := r.snapshot(ctx, "")
		if err != nil {
			return nil, err
		}
		if in.Template == "" {
			t, err := r.templatesOf(ctx, files, in.Folder)
			if err != nil {
				return nil, err
			}
			if t.Default == "" {
				return nil, problem(409, "no_default_template", "This folder has no default template: name one with defaultTemplate in its "+folderConfigName+", or say which template to use.")
			}
			in.Template = t.Default
		}
		f, ok := files[in.Template]
		if !ok || !strings.HasSuffix(strings.ToLower(in.Template), ".md") {
			return nil, problem(404, "template_not_found", in.Template+" is not a Markdown file in this space; templates come from the same space.")
		}
		if f.Virtual != nil || f.Record != nil {
			return nil, invalid(in.Template + " is a virtual file and cannot be a template.")
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
		text := string(b)
		if isFormTemplate(text) {
			return nil, problem(400, "form_template", in.Template+" is a form: people fill it in and send it, rather than making files from it.")
		}
		keys, _ := splitFrontMatter(text)
		name := in.Name
		numbered := false
		if name == "" {
			pattern := strings.TrimSpace(keys["newName"])
			if pattern == "" {
				return nil, invalid(in.Template + " has no newName in its front matter, so give the new file a name.")
			}
			name = templateFileName(templateValues{Today: values.Today, Now: strings.ReplaceAll(values.Now, ":", "."), Name: values.Name, Email: values.Email}.fill(templatePlaceholder.ReplaceAllStringFunc(pattern, func(m string) string {
				if strings.Contains(m, "title") {
					return ""
				}
				return m
			})))
			if name == "" {
				return nil, invalid(in.Template + "'s newName gives no usable file name; give the new file a name.")
			}
			numbered = true
		}
		if path.Ext(name) == "" {
			name += ".md"
		}
		dest := in.Folder + name
		if numbered {
			base, ext := strings.TrimSuffix(name, path.Ext(name)), path.Ext(name)
			for n := 2; ; n++ {
				if _, taken := files[dest]; !taken {
					break
				}
				if n > maxTemplateCopyTries {
					return nil, problem(409, "destination_exists", "Too many files with that name already; give the new file a name.")
				}
				dest = fmt.Sprintf("%s%s %d%s", in.Folder, base, n, ext)
			}
		}
		if !validPath(dest) {
			return nil, invalid("Invalid path: " + dest + ".")
		}
		if _, taken := files[dest]; taken {
			return nil, problem(409, "destination_exists", dest+" already exists; choose another name.")
		}
		values.Title = strings.TrimSuffix(path.Base(dest), path.Ext(dest))
		out := values.fill(stripTemplateKeys(text))
		if !utf8.ValidString(out) || strings.ContainsRune(out, 0) {
			return nil, invalid("The new file's text must be UTF-8 without NUL.")
		}
		if int64(len(out)) > r.limits.MaxFileBytes {
			return nil, problem(413, "payload_too_large", "The new file would be larger than a file can be.")
		}
		return s.mutate(ctx, r, space, "write", Input{Path: dest, Text: &out, IfInState: in.IfInState, CreateOnly: true})
	})
	if err != nil {
		return WriteResult{}, err
	}
	v := value.(WriteResult)
	v.Hint = s.unpushedHint(ctx, r, v.NewState, v.File.Path)
	return v, nil
}

// templateFileName makes a newName's filled-in text safe as one file name:
// slashes, backslashes and colons become hyphens, control characters go,
// and spaces are trimmed.
func templateFileName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r == '/' || r == '\\' || r == ':':
			return '-'
		case r < ' ' || r == 0x7f:
			return -1
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	return strings.TrimSpace(name)
}

// stripTemplateKeys removes template-only keys (newName) from text's front
// matter, and the front matter itself when nothing else is left in it.
func stripTemplateKeys(text string) string {
	text = strings.TrimPrefix(text, "\ufeff")
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) < 2 || strings.TrimRight(lines[0], " \t") != "---" {
		return text
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if t := strings.TrimRight(lines[i], " \t"); t == "---" || t == "..." {
			end = i
			break
		}
	}
	if end < 0 {
		return text
	}
	kept := []string{}
	skipping := false
	for _, line := range lines[1:end] {
		if line != "" && line[0] != ' ' && line[0] != '\t' && line[0] != '#' {
			key, _, _ := strings.Cut(line, ":")
			skipping = templateOnlyKeys[strings.TrimSpace(key)]
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	rest := lines[end+1:]
	if strings.TrimSpace(strings.Join(kept, "")) == "" {
		// Nothing left: drop the block and one blank line after it.
		if len(rest) > 0 && strings.TrimSpace(rest[0]) == "" && len(rest) > 1 {
			rest = rest[1:]
		}
		return strings.Join(rest, nl)
	}
	out := append([]string{lines[0]}, kept...)
	out = append(out, lines[end])
	return strings.Join(append(out, rest...), nl)
}

// templatePerson is the name and email address a template's @name and @email
// take: the signed-in account's, or for a space key its author name and no
// address.
func (s *Service) templatePerson(ctx context.Context) (string, string) {
	if identity, ok := ctx.Value(mcpIdentityKey{}).(mcpIdentity); ok && identity.oauth != nil && s.ownedDB != nil {
		if name, email, err := s.personDetails(ctx, identity.oauth.UserID); err == nil {
			return name, email
		}
	}
	if a, ok := authorOf(ctx); ok {
		return a.Name, ""
	}
	return "", ""
}

// templateClock is the date and time @today and @now take: the person's own
// when their client sent them (within a day of now), otherwise the space's
// time zone (UTC when it has none), as forms do.
func (s *Service) templateClock(ctx context.Context, r *repository, today, sent string, now time.Time) (string, string) {
	local := now
	if r.id != "" {
		if name := s.dbSpaceZone(ctx, r.id); name != "" {
			if zone, err := time.LoadLocation(name); err == nil {
				local = now.In(zone)
			}
		}
	}
	day, clock := local.Format("2006-01-02"), local.Format("15:04")
	if d, err := time.Parse("2006-01-02", today); err == nil && validDate(today) && d.Sub(now.Truncate(24*time.Hour)).Abs() <= 24*time.Hour {
		day = today
	}
	if timePattern.MatchString(sent) {
		clock = sent
	}
	return day, clock
}

// serveFromTemplate handles POST .../from-template with {"template"?,
// "folder", "name"?, "ifInState", "today"?, "now"?}.
func (h *httpAdapter) serveFromTemplate(w http.ResponseWriter, r *http.Request, repo *repository, space string) {
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
		return
	}
	var body templateRequest
	if !validUnicodeJSON(b) || noDuplicateKeys(b) != nil || strictJSON(b, &body) != nil {
		sendError(w, invalid(`Send {"folder": "Meetings/", "ifInState": "<state>"}, with "template" (a .md file of this space; without it the folder's default template), "name" (without it the template's newName), and optionally "today" and "now" (the person's local date and time).`))
		return
	}
	value, err := h.service.createFromTemplate(r.Context(), repo, space, body)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 201, value)
}
