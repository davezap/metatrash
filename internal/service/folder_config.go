package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// A folder describes itself in a .metatrash.json file inside it: its purpose,
// its children and the services attached to it. The space root always has one;
// other folders may. It is an ordinary file that agents read and write, checked
// by the service on every write so a mistake is reported instead of stored.
const folderConfigName = ".metatrash.json"

const (
	maxFolderPurpose     = 2000
	maxFolderChildren    = 200
	maxFolderChildText   = 1000
	maxFolderServices    = 8
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
	// Reserved for folder actions; refused until they are implemented.
	Actions json.RawMessage `json:"actions,omitempty"`
}

type folderService struct {
	Type   string `json:"type"`
	Repo   string `json:"repo,omitempty"`
	Branch string `json:"branch,omitempty"`
	Push   string `json:"push,omitempty"`
	Pull   string `json:"pull,omitempty"`
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
	if c.Actions != nil {
		return c, folderConfigError("actions are not supported yet.")
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
	if len(c.Services) > maxFolderServices {
		return c, folderConfigError(fmt.Sprintf("services is limited to %d entries.", maxFolderServices))
	}
	kinds := map[string]bool{}
	for i := range c.Services {
		svc := &c.Services[i]
		switch svc.Type {
		case "github":
			if !githubRepoPattern.MatchString(svc.Repo) || strings.HasSuffix(svc.Repo, "/.") || strings.HasSuffix(svc.Repo, "/..") {
				return c, folderConfigError("github repo must be owner/name, for example dave-zap/website.")
			}
			if svc.Branch != "" && (!gitBranchPattern.MatchString(svc.Branch) || strings.Contains(svc.Branch, "..") || strings.Contains(svc.Branch, "//") || strings.HasSuffix(svc.Branch, "/") || strings.HasSuffix(svc.Branch, ".") || strings.HasSuffix(svc.Branch, ".lock")) {
				return c, folderConfigError("github branch is not a valid branch name.")
			}
			if svc.Push != "" && svc.Push != "agent" && svc.Push != "auto" && svc.Push != "review" {
				return c, folderConfigError(`github push must be "agent" (the default: only the push tool sends), "auto" or "review".`)
			}
			if svc.Pull != "" && svc.Pull != "auto" {
				return c, folderConfigError(`github pull must be "auto".`)
			}
		case "":
			return c, folderConfigError("each service needs a type.")
		default:
			return c, folderConfigError(fmt.Sprintf("unknown service type %q; supported: github.", svc.Type))
		}
		if kinds[svc.Type] {
			return c, folderConfigError(fmt.Sprintf("only one %s service per folder.", svc.Type))
		}
		kinds[svc.Type] = true
	}
	return c, nil
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

func (c folderConfig) has(kind string) bool {
	for _, svc := range c.Services {
		if svc.Type == kind {
			return true
		}
	}
	return false
}

// checkFolderConfig validates a configuration write against the rest of the
// space: services only in owned spaces, never at the root, and no folder of
// the same service kind inside or around another. Called under the write queue
// with the snapshot the write applies to.
func (r *repository) checkFolderConfig(ctx context.Context, files map[string]record, path, text string) error {
	c, err := parseFolderConfig(text)
	if err != nil {
		return err
	}
	if len(c.Services) == 0 {
		return nil
	}
	folder := configFolder(path)
	if folder == "" {
		return folderConfigError("services cannot be attached to the space root; use a folder.")
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
			if existing.has(svc.Type) {
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

// folderConfigStarter is the root configuration a new space starts with.
func folderConfigStarter(name string) string {
	purpose := fmt.Sprintf("Space %q. No purpose written yet: agents may rewrite this file to describe what the space is for and, under children, its top-level folders.", name)
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	_ = e.Encode(struct {
		Purpose  string            `json:"purpose"`
		Children map[string]string `json:"children"`
	}{purpose, map[string]string{}})
	return b.String()
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
		markServiceFolders(node.Children, folder, configs)
	}
}
