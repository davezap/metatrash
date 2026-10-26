package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// A folder's settings are in a .metatrash.json file inside it: the services
// attached to it, its published version (see publish.go) and, from older
// spaces, a purpose and children (descriptions now belong in the folder's
// AGENTS.md). Every folder has one (see
// folder_files.go). Agents read and write it; the service checks every write
// so a mistake is reported instead of stored.
const folderConfigName = ".metatrash.json"

const (
	maxFolderPurpose     = 2000
	maxFolderChildren    = 200
	maxFolderChildText   = 1000
	maxFolderServices    = 8
	maxPublishedVersion  = 1000000
	maxAcknowledgeFiles  = 50
	maxPrintImage        = 2000
	githubDefaultBranch  = "main"
	githubDefaultPush    = "agent"
	githubDefaultPull    = "auto"
	folderConfigHintText = "See the space root " + folderConfigName + " or the tool descriptions for the format."
)

var (
	childNamePattern  = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._-]*/?$`)
	githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	gitBranchPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
)

type folderConfig struct {
	Purpose  *string           `json:"purpose,omitempty"`
	Children map[string]string `json:"children,omitempty"`
	Services []folderService   `json:"services,omitempty"`
	// Inherit takes every setting but services from the parent folder's
	// settings; the folder's own keys override them.
	Inherit *bool `json:"inherit,omitempty"`
	// Locked locks keys of this file and ReadOnly files of this folder (see
	// locks.go).
	Locked   lockMap `json:"locked,omitempty"`
	ReadOnly lockMap `json:"readOnly,omitempty"`
	// PublishedVersion is the folder's official version: its files as they
	// were when the version was set (see publish.go). Never inherited.
	PublishedVersion *int `json:"publishedVersion,omitempty"`
	// Actions run an agent when files arrive (see actions.go). Never
	// inherited; owned spaces only.
	Actions []folderAction `json:"actions,omitempty"`
	// Print is how the folder's documents print (Slate Viewer): Image is
	// the https address of an image put centred above the title of the
	// first page. Never in the files themselves. Inherited like purpose.
	Print *folderPrint `json:"print,omitempty"`
}

type folderPrint struct {
	Image string `json:"image,omitempty"`
}

// folderService is one entry of services. Each type uses its own fields
// (see serviceKinds) and refuses the others'.
type folderService struct {
	Type   string `json:"type"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Push   string `json:"push,omitempty"`
	Pull   string `json:"pull,omitempty"`
	// Files are the files an acknowledge service asks people to read, named
	// like readOnly entries; by default every .md file in the folder and its
	// subfolders other than AGENTS.md.
	Files []string `json:"files,omitempty"`
}

// serviceKind describes one service type: check validates its settings,
// root allows it on the space root, and nest allows folders with it inside
// one another.
type serviceKind struct {
	check      func(svc folderService) error
	root, nest bool
}

// serviceKinds are the services a folder can attach. A new service is one
// entry here plus the code that acts on it.
var serviceKinds = map[string]serviceKind{
	"github":      {check: checkGitHubService},
	"acknowledge": {check: checkAcknowledgeService, root: true, nest: true},
}

func checkGitHubService(svc folderService) error {
	if !githubRepoPattern.MatchString(svc.Repo) || strings.HasSuffix(svc.Repo, "/.") || strings.HasSuffix(svc.Repo, "/..") {
		return folderConfigError("github repo must be owner/name, for example dave-zap/website.")
	}
	if svc.Branch != "" && (!gitBranchPattern.MatchString(svc.Branch) || strings.Contains(svc.Branch, "..") || strings.Contains(svc.Branch, "//") || strings.HasSuffix(svc.Branch, "/") || strings.HasSuffix(svc.Branch, ".") || strings.HasSuffix(svc.Branch, ".lock")) {
		return folderConfigError("github branch is not a valid branch name.")
	}
	if svc.Push != "" && svc.Push != "agent" && svc.Push != "auto" && svc.Push != "review" {
		return folderConfigError(`github push must be "agent" (the default: only the push tool sends), "auto" or "review".`)
	}
	if svc.Pull != "" && svc.Pull != "auto" {
		return folderConfigError(`github pull must be "auto".`)
	}
	if svc.Files != nil {
		return folderConfigError("github takes no files.")
	}
	return nil
}

func checkAcknowledgeService(svc folderService) error {
	if svc.Repo != "" || svc.Branch != "" || svc.Push != "" || svc.Pull != "" {
		return folderConfigError("acknowledge takes only files (which files people must read; by default every .md file).")
	}
	if len(svc.Files) > maxAcknowledgeFiles {
		return folderConfigError(fmt.Sprintf("acknowledge files is limited to %d entries.", maxAcknowledgeFiles))
	}
	for _, entry := range svc.Files {
		if err := checkReadOnlyEntry(entry); err != nil {
			return folderConfigError(fmt.Sprintf("acknowledge files entry %q must be one file name or pattern in this folder, such as leave.md or *.md, or a subfolder ending in /, such as hr/.", entry))
		}
	}
	return nil
}

// isFolderConfig reports whether path names a folder configuration file.
func isFolderConfig(path string) bool {
	return path == folderConfigName || strings.HasSuffix(path, "/"+folderConfigName)
}

// configFolder returns the folder a configuration file describes: "" for the
// space root, otherwise a prefix ending in "/".
func configFolder(path string) string {
	return strings.TrimSuffix(path, folderConfigName)
}

func folderConfigError(message string) *Error {
	return invalid("Invalid " + folderConfigName + ": " + message + " " + folderConfigHintText)
}

// parseFolderConfig checks the shape of a configuration file on its own:
// strict JSON, known keys only, bounded text, known service types.
func parseFolderConfig(text string) (folderConfig, error) {
	var c folderConfig
	trimmed := bytes.TrimSpace([]byte(text))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return c, folderConfigError("expected a JSON object.")
	}
	if err := noDuplicateKeys(trimmed); err != nil {
		return c, folderConfigError(err.Error())
	}
	if err := strictJSON(trimmed, &c); err != nil {
		return c, folderConfigError("unknown key or wrong value type (" + jsonProblem(err) + ").")
	}
	if err := checkActions(c.Actions); err != nil {
		return c, err
	}
	if c.Purpose != nil && utf8.RuneCountInString(*c.Purpose) > maxFolderPurpose {
		return c, folderConfigError(fmt.Sprintf("purpose is limited to %d characters.", maxFolderPurpose))
	}
	if len(c.Children) > maxFolderChildren {
		return c, folderConfigError(fmt.Sprintf("children is limited to %d entries.", maxFolderChildren))
	}
	for name, text := range c.Children {
		if bare := strings.TrimSuffix(name, "/"); len(name) > 240 || !childNamePattern.MatchString(name) || bare == "." || bare == ".." || strings.EqualFold(strings.TrimRight(bare, "."), ".git") {
			return c, folderConfigError(fmt.Sprintf("children key %q must be one file or folder name (folders end in /).", name))
		}
		if utf8.RuneCountInString(text) > maxFolderChildText {
			return c, folderConfigError(fmt.Sprintf("children text is limited to %d characters.", maxFolderChildText))
		}
	}
	if err := checkLockLevels("locked", c.Locked); err != nil {
		return c, err
	}
	for key := range c.Locked {
		if !lockableKeys[key] {
			return c, folderConfigError(fmt.Sprintf("locked names keys of this file: purpose, children, services, inherit, readOnly, publishedVersion, actions, print or locked, not %q.", key))
		}
	}
	if c.Locked["locked"] == lockEveryone {
		return c, folderConfigError(`locked itself can be locked only for "agents": a person must always be able to lift a lock.`)
	}
	if err := checkLockLevels("readOnly", c.ReadOnly); err != nil {
		return c, err
	}
	for entry := range c.ReadOnly {
		if err := checkReadOnlyEntry(entry); err != nil {
			return c, err
		}
	}
	if v := c.PublishedVersion; v != nil && (*v < 1 || *v > maxPublishedVersion) {
		return c, folderConfigError(fmt.Sprintf("publishedVersion must be a whole number from 1 to %d.", maxPublishedVersion))
	}
	if c.Print != nil && c.Print.Image != "" && !printImageAddress(c.Print.Image) {
		return c, folderConfigError(fmt.Sprintf("print image must be an https address of an image, such as https://example.com/logo.png, of at most %d characters.", maxPrintImage))
	}
	if len(c.Services) > maxFolderServices {
		return c, folderConfigError(fmt.Sprintf("services is limited to %d entries.", maxFolderServices))
	}
	kinds := map[string]bool{}
	for i := range c.Services {
		svc := &c.Services[i]
		kind, known := serviceKinds[svc.Type]
		switch {
		case svc.Type == "":
			return c, folderConfigError("each service needs a type.")
		case !known:
			return c, folderConfigError(fmt.Sprintf("unknown service type %q; supported: acknowledge, github.", svc.Type))
		}
		if err := kind.check(*svc); err != nil {
			return c, err
		}
		if kinds[svc.Type] {
			return c, folderConfigError(fmt.Sprintf("only one %s service per folder.", svc.Type))
		}
		kinds[svc.Type] = true
	}
	return c, nil
}

// printImageAddress reports whether image is an https URL with a host and no
// credentials, as print.image needs.
func printImageAddress(image string) bool {
	u, err := url.Parse(image)
	return err == nil && len(image) <= maxPrintImage && u.Scheme == "https" && u.Host != "" && u.User == nil && !strings.ContainsAny(image, " \t\r\n")
}

// withDefaults fills the documented defaults of a GitHub service.
func (svc folderService) withDefaults() folderService {
	if svc.Type == "github" {
		if svc.Branch == "" {
			svc.Branch = githubDefaultBranch
		}
		if svc.Push == "" {
			svc.Push = githubDefaultPush
		}
		if svc.Pull == "" {
			svc.Pull = githubDefaultPull
		}
	}
	return svc
}

func (svc folderService) equal(other folderService) bool {
	return svc.Type == other.Type && svc.Repo == other.Repo && svc.Branch == other.Branch && svc.Push == other.Push && svc.Pull == other.Pull && slices.Equal(svc.Files, other.Files)
}

func (c folderConfig) has(kind string) bool {
	for _, svc := range c.Services {
		if svc.Type == kind {
			return true
		}
	}
	return false
}

// checkFolderConfig validates a configuration write against the rest of the
// space: services only in owned spaces, on the root only where the kind allows
// it, no folder of a non-nesting kind inside or around another, and a
// publishedVersion that never goes down. Called under the write queue with the
// snapshot the write applies to.
func (r *repository) checkFolderConfig(ctx context.Context, files map[string]record, path, text string) error {
	c, err := parseFolderConfig(text)
	if err != nil {
		return err
	}
	folder := configFolder(path)
	if folder == "" && c.Inherit != nil && *c.Inherit {
		return folderConfigError("the space root has no parent folder to inherit from.")
	}
	if f, ok := files[path]; ok && c.PublishedVersion != nil {
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return err
		}
		if old, err := parseFolderConfig(string(b)); err == nil && old.PublishedVersion != nil && *c.PublishedVersion < *old.PublishedVersion {
			return folderConfigError(fmt.Sprintf("publishedVersion only goes up: it is %d, so publish the next version as %d.", *old.PublishedVersion, *old.PublishedVersion+1))
		}
	}
	if len(c.Actions) > 0 && !r.owned {
		return folderConfigError("actions are available only in your own spaces (connected through /mcp/account).")
	}
	if len(c.Services) == 0 {
		return nil
	}
	for _, svc := range c.Services {
		if folder == "" && !serviceKinds[svc.Type].root {
			return folderConfigError(svc.Type + " services cannot be attached to the space root; use a folder.")
		}
	}
	if !r.owned {
		return folderConfigError("services are available only in your own spaces (connected through /mcp/account).")
	}
	for other, f := range files {
		if other == path || !isFolderConfig(other) {
			continue
		}
		otherFolder := configFolder(other)
		if !strings.HasPrefix(folder, otherFolder) && !strings.HasPrefix(otherFolder, folder) {
			continue
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return err
		}
		existing, err := parseFolderConfig(string(b))
		if err != nil {
			// Written before a rule tightened; it attaches nothing we can rely on.
			continue
		}
		for _, svc := range c.Services {
			if existing.has(svc.Type) && !serviceKinds[svc.Type].nest {
				// Repos never nest. Say where the clash is and what to do instead.
				if strings.HasPrefix(otherFolder, folder) {
					return folderConfigError(fmt.Sprintf("%s already has a %s service and %s folders cannot nest; choose a folder that does not contain it.", otherFolder, svc.Type, svc.Type))
				}
				return folderConfigError(fmt.Sprintf("%s is inside %s, which already has a %s service, and %s folders cannot nest; put this repo beside it instead, for example in dependencies/.", folder, otherFolder, svc.Type, svc.Type))
			}
		}
	}
	return nil
}

// githubFolderOf returns the GitHub folder that contains path, or "". Repos
// never nest, so there is at most one.
func githubFolderOf(configs map[string]folderConfig, path string) string {
	for folder, c := range configs {
		if folder != "" && strings.HasPrefix(path, folder) && c.has("github") {
			return folder
		}
	}
	return ""
}

// dotNameRule keeps names starting with a dot inside GitHub folders, where
// repositories need them (.gitignore, .github/). Run under the write queue on
// the snapshot the change applies to:
//   - dest, when set, is a file being created, replaced or moved to; a dot
//     name there needs a GitHub folder around it.
//   - configPath/configText describe a folder configuration being written
//     (configText set) or deleted (configText nil); removing a folder's GitHub
//     service is refused while the folder still holds dot names.
func (r *repository) dotNameRule(ctx context.Context, files map[string]record, dest, configPath string, configText *string) error {
	needDest := dest != "" && hasDotName(dest)
	removing := false
	if configPath != "" {
		if f, ok := files[configPath]; ok {
			b, err := r.blob(ctx, f.Blob)
			if err != nil {
				return err
			}
			old, err := parseFolderConfig(string(b))
			if err == nil && old.has("github") {
				removing = true
				if configText != nil {
					if updated, err := parseFolderConfig(*configText); err == nil && updated.has("github") {
						removing = false
					}
				}
			}
		}
	}
	if needDest {
		configs, err := r.folderConfigs(ctx, files)
		if err != nil {
			return err
		}
		// The configuration being written decides for its own folder.
		if configPath != "" && configText != nil {
			if c, err := parseFolderConfig(*configText); err == nil {
				configs[configFolder(configPath)] = c
			}
		}
		if githubFolderOf(configs, dest) == "" {
			return invalid(dest + ": names starting with a dot are allowed only inside a GitHub folder (a folder whose " + folderConfigName + " has a github service). Rename it, or keep it in a GitHub folder.")
		}
	}
	if removing {
		folder := configFolder(configPath)
		for p := range files {
			if strings.HasPrefix(p, folder) && hasDotName(p) {
				return invalid(folder + " holds " + p + "; names starting with a dot are allowed only in GitHub folders. Move or delete such files before removing the github service.")
			}
		}
	}
	return nil
}

// noDuplicateKeys rejects repeated object keys at any depth, which
// encoding/json would otherwise resolve silently to the last value.
func noDuplicateKeys(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func() error
	walk = func() error {
		t, err := d.Token()
		if err != nil {
			return fmt.Errorf("not valid JSON.")
		}
		switch t {
		case json.Delim('{'):
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return fmt.Errorf("not valid JSON.")
				}
				key, _ := k.(string)
				if seen[key] {
					return fmt.Errorf("duplicate key %q.", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
		case json.Delim('['):
			for d.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = d.Token()
		}
		if err != nil {
			return fmt.Errorf("not valid JSON.")
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON object.")
	}
	return nil
}

// jsonProblem turns a decoding error into a short agent-facing reason.
func jsonProblem(err error) string {
	msg := err.Error()
	msg = strings.TrimPrefix(msg, "json: ")
	if len(msg) > 120 {
		msg = msg[:120]
	}
	return msg
}

// folderConfigs reads every valid .metatrash.json in a snapshot, keyed by the
// folder it describes. Files that no longer validate are skipped.
func (r *repository) folderConfigs(ctx context.Context, files map[string]record) (map[string]folderConfig, error) {
	configs := map[string]folderConfig{}
	for path, f := range files {
		if !isFolderConfig(path) {
			continue
		}
		b, err := r.blob(ctx, f.Blob)
		if err != nil {
			return nil, err
		}
		if c, err := parseFolderConfig(string(b)); err == nil {
			configs[configFolder(path)] = c
		}
	}
	return configs, nil
}

// markServiceFolders labels explorer folders that have services attached.
func markServiceFolders(nodes []*browserNode, prefix string, configs map[string]folderConfig) {
	for _, node := range nodes {
		if len(node.Children) == 0 {
			continue
		}
		folder := prefix + node.Name + "/"
		for _, svc := range configs[folder].Services {
			if svc.Type == "github" {
				node.Service = "GitHub " + svc.Repo
			}
		}
		if node.Service == "" && configs[folder].PublishedVersion != nil {
			node.Service = fmt.Sprintf("Published v%d", *configs[folder].PublishedVersion)
		}
		markServiceFolders(node.Children, folder, configs)
	}
}

// lockableKeys are the keys locked may name.
var lockableKeys = map[string]bool{"purpose": true, "children": true, "services": true, "inherit": true, "readOnly": true, "publishedVersion": true, "actions": true, "print": true, "locked": true}

// folderConfigInherit is the .metatrash.json a new folder below the root
// starts with: the parent folder's settings.
const folderConfigInherit = "{\"inherit\": true}\n"

// personWriteKey marks a request context whose writes come from a person:
// the built-in Metatrash Sync client (mtsync and Slate Viewer). Agents, space
// keys and every other app are held to locked settings.
type personWriteKey struct{}

func personWrite(ctx context.Context) bool {
	v, _ := ctx.Value(personWriteKey{}).(bool)
	return v
}

// configKeys splits a configuration text into its top-level keys, each value
// re-encoded so equal values compare equal however they were written. Text
// that is not a JSON object has no keys.
func configKeys(text string) map[string]string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return map[string]string{}
	}
	keys := make(map[string]string, len(raw))
	for k, v := range raw {
		b, _ := json.Marshal(v)
		keys[k] = string(b)
	}
	return keys
}

// configText returns the text of folder's .metatrash.json in files, or the
// text a missing one would start with.
func (r *repository) configText(ctx context.Context, files map[string]record, folder string) (string, error) {
	f, ok := files[folder+folderConfigName]
	if !ok {
		if folder == "" {
			return folderConfigEmpty, nil
		}
		return folderConfigInherit, nil
	}
	b, err := r.blob(ctx, f.Blob)
	return string(b), err
}

// parentFolder returns the folder that holds folder ("" holds the top level).
func parentFolder(folder string) string {
	return fileFolder(strings.TrimSuffix(folder, "/"))
}

// effectiveSettings returns folder's settings with inherited ones filled in,
// as top-level keys like configKeys. text, when set, stands for the folder's
// own file. Services, readOnly, publishedVersion, actions and inherit itself
// are never inherited.
func (r *repository) effectiveSettings(ctx context.Context, files map[string]record, folder string, text *string) (map[string]string, error) {
	own := ""
	if text != nil {
		own = *text
	} else {
		var err error
		if own, err = r.configText(ctx, files, folder); err != nil {
			return nil, err
		}
	}
	keys := configKeys(own)
	if folder == "" || keys["inherit"] != "true" {
		return keys, nil
	}
	parent, err := r.effectiveSettings(ctx, files, parentFolder(folder), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range parent {
		if _, set := keys[k]; !set && k != "services" && k != "readOnly" && k != "publishedVersion" && k != "actions" && k != "inherit" {
			keys[k] = v
		}
	}
	return keys, nil
}

// settingsJSON encodes settings from effectiveSettings as one JSON object.
func settingsJSON(settings map[string]string) json.RawMessage {
	raw := make(map[string]json.RawMessage, len(settings))
	for k, v := range settings {
		raw[k] = json.RawMessage(v)
	}
	b, _ := json.Marshal(raw)
	return b
}
