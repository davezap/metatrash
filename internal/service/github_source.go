package service

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Reading repositories as the Metatrash GitHub App: an installation token
// limited to one repository and read access, then the GitHub REST API. No
// clone: the branch head, the recursive tree, one tarball for the content
// and single blobs for anything the tarball leaves out or changes
// (export-ignore, export-subst). Every file is checked against its blob hash.

const (
	maxGitHubTreeBytes    = 32 << 20  // recursive tree response
	maxGitHubTarballBytes = 256 << 20 // compressed tarball read
	maxGitHubBlobFetches  = 200       // single blobs after the tarball
	githubTarballTimeout  = 2 * time.Minute
)

// blobHash is Git's object ID for a blob with this content (SHA-1 repository).
func blobHash(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// githubSourceFor returns a reader for repo ("owner/name") through the space
// owner's GitHub connection. Tests replace it with githubSourceHook.
func (s *Service) githubSourceFor(ctx context.Context, r *repository, repo string) (githubSource, error) {
	if s.githubSourceHook != nil {
		return s.githubSourceHook(ctx, r, repo)
	}
	return s.githubRepoClient(ctx, r, repo, "read")
}

// githubPusherFor returns a writer for repo, with a token that may write its
// contents. Tests replace it with githubPushHook.
func (s *Service) githubPusherFor(ctx context.Context, r *repository, repo string) (githubPusher, error) {
	if s.githubPushHook != nil {
		return s.githubPushHook(ctx, r, repo)
	}
	return s.githubRepoClient(ctx, r, repo, "write")
}

// githubRepoClient finds the space owner's installation for the repository
// owner and asks for a token limited to that repository and contents access
// ("read" or "write").
func (s *Service) githubRepoClient(ctx context.Context, r *repository, repo, access string) (*githubRepoReader, error) {
	if s.accounts == nil || s.accounts.github == nil || s.ownedDB == nil {
		return nil, problem(403, "forbidden", "GitHub is not enabled on this Metatrash server.")
	}
	if !r.owned || r.owner == "" {
		return nil, problem(403, "forbidden", "GitHub folders work only in your own spaces.")
	}
	owner, _, _ := strings.Cut(repo, "/")
	list, err := s.ownedDB.githubInstallations(ctx, r.owner)
	if err != nil {
		return nil, err
	}
	for _, inst := range list {
		if !strings.EqualFold(inst.AccountLogin, owner) {
			continue
		}
		if inst.Status != "active" {
			return nil, problem(403, "forbidden", fmt.Sprintf("The Metatrash GitHub app is suspended on %s. Unsuspend it on GitHub, then try again.", inst.AccountLogin))
		}
		client := newGitHubClient(s.accounts.github)
		token, err := client.installationToken(ctx, inst.InstallationID, repo, access)
		if err != nil {
			return nil, err
		}
		return &githubRepoReader{client: client, token: token, repo: repo}, nil
	}
	return nil, problem(403, "forbidden", fmt.Sprintf("The space owner has no GitHub connection for %s. On Your account, use Connect GitHub to install the Metatrash app on %s (or Link an existing installation), then try again.", owner, owner))
}

// installationToken asks for a token limited to one repository with read or
// write access to its contents. GitHub refuses (404/422) when the
// installation does not include the repository or lacks the permission.
func (c *githubClient) installationToken(ctx context.Context, installationID int64, repo, access string) (string, error) {
	_, name, _ := strings.Cut(repo, "/")
	jwt, err := c.settings.appJWT(time.Now())
	if err != nil {
		return "", err
	}
	body, _ := json.Marshal(map[string]any{"repositories": []string{name}, "permissions": map[string]string{"contents": access}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.settings.apiBase+"/app/installations/"+strconv.FormatInt(installationID, 10)+"/access_tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Token string `json:"token"`
	}
	status, err := c.do(req, 64*1024, &out)
	if status == http.StatusNotFound || status == http.StatusUnprocessableEntity {
		if access == "write" {
			return "", problem(403, "forbidden", fmt.Sprintf("The Metatrash GitHub app cannot write to %s. On GitHub, open the app's installation settings (Configure): add the repository under Repository access, and accept any pending permission request (Contents: read and write). Then push again.", repo))
		}
		return "", problem(403, "forbidden", fmt.Sprintf("The Metatrash GitHub app cannot reach %s. On GitHub, open the app's installation settings (Configure), add the repository under Repository access, then pull again.", repo))
	}
	if err != nil {
		return "", err
	}
	if out.Token == "" || len(out.Token) > 1024 {
		return "", errGitHubUnavailable
	}
	return out.Token, nil
}

// githubRepoReader reads one repository with an installation token.
type githubRepoReader struct {
	client *githubClient
	token  string
	repo   string
}

func (g *githubRepoReader) get(ctx context.Context, path string, limit int64, out any) (int, error) {
	return g.send(ctx, http.MethodGet, path, nil, limit, out)
}

// send makes one API call on the repository; body, when set, is sent as JSON.
func (g *githubRepoReader) send(ctx context.Context, method, path string, body any, limit int64, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.client.settings.apiBase+"/repos/"+g.repo+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return g.client.do(req, limit, out)
}

func escapeBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (g *githubRepoReader) branchHead(ctx context.Context, branch string) (string, string, error) {
	var out struct {
		Commit struct {
			SHA    string `json:"sha"`
			Commit struct {
				Tree struct {
					SHA string `json:"sha"`
				} `json:"tree"`
			} `json:"commit"`
		} `json:"commit"`
	}
	status, err := g.get(ctx, "/branches/"+escapeBranch(branch), 256*1024, &out)
	if status == http.StatusNotFound {
		var repo struct {
			DefaultBranch string `json:"default_branch"`
		}
		if s, err := g.get(ctx, "", 256*1024, &repo); err == nil && s == http.StatusOK && repo.DefaultBranch != "" {
			if repo.DefaultBranch == branch {
				return "", "", invalid(g.repo + " is empty (it has no " + branch + " branch yet); there is nothing to pull.")
			}
			return "", "", invalid(fmt.Sprintf("%s has no branch %q; its default branch is %q. Set \"branch\" in the folder's %s, then pull again.", g.repo, branch, repo.DefaultBranch, folderConfigName))
		}
		return "", "", problem(403, "forbidden", fmt.Sprintf("%s was not found, or the Metatrash GitHub app cannot reach it.", g.repo))
	}
	if status == http.StatusConflict {
		// An empty repository has no branches yet.
		return "", "", invalid(g.repo + " is empty; there is nothing to pull yet.")
	}
	if err != nil {
		return "", "", err
	}
	if !hashPattern.MatchString(out.Commit.SHA) || !hashPattern.MatchString(out.Commit.Commit.Tree.SHA) {
		return "", "", errGitHubUnavailable
	}
	return out.Commit.SHA, out.Commit.Commit.Tree.SHA, nil
}

func (g *githubRepoReader) tree(ctx context.Context, tree string) ([]githubTreeEntry, error) {
	var out struct {
		SHA       string            `json:"sha"`
		Tree      []githubTreeEntry `json:"tree"`
		Truncated bool              `json:"truncated"`
	}
	status, err := g.get(ctx, "/git/trees/"+tree+"?recursive=1", maxGitHubTreeBytes, &out)
	if err != nil {
		if status == 0 || status == http.StatusOK {
			return nil, problem(507, "storage_limit", g.repo+" is too large to pull into a space.")
		}
		return nil, err
	}
	if out.Truncated {
		return nil, problem(507, "storage_limit", g.repo+" is too large to pull into a space.")
	}
	return out.Tree, nil
}

// contents reads the files in want (path -> blob hash) at commit: first from
// the commit's tarball, then single blobs for any the tarball did not supply.
func (g *githubRepoReader) contents(ctx context.Context, commit string, want map[string]string) (map[string][]byte, error) {
	got := map[string][]byte{}
	// A failed tarball read is not fatal: the blob API is the fallback.
	_ = g.tarball(ctx, commit, want, got)
	missing := []string{}
	for p := range want {
		if _, ok := got[p]; !ok {
			missing = append(missing, p)
		}
	}
	if len(missing) > maxGitHubBlobFetches {
		return nil, errGitHubUnavailable
	}
	for _, p := range missing {
		var out struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if _, err := g.get(ctx, "/git/blobs/"+want[p], 4<<20, &out); err != nil {
			return nil, err
		}
		if out.Encoding != "base64" {
			return nil, errGitHubUnavailable
		}
		b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
		if err != nil || blobHash(b) != want[p] {
			return nil, errGitHubUnavailable
		}
		got[p] = b
	}
	return got, nil
}

// tarball streams the commit's archive and keeps the wanted files whose
// content matches their blob hash.
func (g *githubRepoReader) tarball(ctx context.Context, commit string, want map[string]string, got map[string][]byte) error {
	ctx, cancel := context.WithTimeout(ctx, githubTarballTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.client.settings.apiBase+"/repos/"+g.repo+"/tarball/"+commit, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("User-Agent", "metatrash")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	stream := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := stream.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusTemporaryRedirect {
		resp.Body.Close()
		location, err := url.Parse(resp.Header.Get("Location"))
		// The archive link carries its own short-lived credential; never send
		// the token there, and go only to GitHub's archive host.
		if err != nil || !(location.Scheme == "https" && location.Host == "codeload.github.com" || strings.HasPrefix(location.String(), g.client.settings.apiBase+"/")) {
			return errors.New("unexpected tarball location")
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "metatrash")
		if resp, err = stream.Do(req); err != nil {
			return err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("tarball unavailable")
	}
	zr, err := gzip.NewReader(io.LimitReader(resp.Body, maxGitHubTarballBytes))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		// Entries sit under one top folder named after the repository and commit.
		_, rel, ok := strings.Cut(h.Name, "/")
		hash, wanted := want[rel]
		if !ok || !wanted || h.Size > 4<<20 {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
		if err != nil {
			return err
		}
		if blobHash(b) == hash {
			got[rel] = b
		}
	}
}

// githubPusher writes to one repository through the Git Data API: blobs and
// trees, a commit, then a non-forced branch update.
type githubPusher interface {
	branchHead(ctx context.Context, branch string) (commit, tree string, err error)
	// createTree applies changes on top of base and returns the new tree.
	createTree(ctx context.Context, base string, changes []githubTreeChange) (string, error)
	// createCommit returns the new commit and the tree GitHub recorded in it.
	createCommit(ctx context.Context, c githubNewCommit) (commit, tree string, err error)
	// updateRef moves branch to commit without force. It returns
	// errGitHubMovedAhead when the branch no longer points at the parent.
	updateRef(ctx context.Context, branch, commit string) error
}

// githubTreeChange is one entry for createTree: Content set writes a file,
// Content nil deletes the path.
type githubTreeChange struct {
	Path    string
	Mode    string
	Content *string
}

type githubNewCommit struct {
	Message, Tree, Parent string
}

var errGitHubMovedAhead = errors.New("github branch moved")

func (g *githubRepoReader) createTree(ctx context.Context, base string, changes []githubTreeChange) (string, error) {
	entries := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		e := map[string]any{"path": c.Path, "mode": c.Mode, "type": "blob"}
		if c.Content != nil {
			e["content"] = *c.Content
		} else {
			e["sha"] = nil
		}
		entries = append(entries, e)
	}
	var out struct {
		SHA string `json:"sha"`
	}
	if _, err := g.send(ctx, http.MethodPost, "/git/trees", map[string]any{"base_tree": base, "tree": entries}, maxGitHubTreeBytes, &out); err != nil {
		return "", err
	}
	if !hashPattern.MatchString(out.SHA) {
		return "", errGitHubUnavailable
	}
	return out.SHA, nil
}

func (g *githubRepoReader) createCommit(ctx context.Context, c githubNewCommit) (string, string, error) {
	// No author or committer: GitHub records the app's bot and signs the
	// commit (Verified). It does not sign commits with a custom author or
	// committer. The Metatrash user is named in a Co-authored-by trailer.
	body := map[string]any{"message": c.Message, "tree": c.Tree, "parents": []string{c.Parent}}
	var out struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := g.send(ctx, http.MethodPost, "/git/commits", body, 1<<20, &out); err != nil {
		return "", "", err
	}
	if !hashPattern.MatchString(out.SHA) || !hashPattern.MatchString(out.Tree.SHA) {
		return "", "", errGitHubUnavailable
	}
	return out.SHA, out.Tree.SHA, nil
}

func (g *githubRepoReader) updateRef(ctx context.Context, branch, commit string) error {
	var out struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	status, err := g.send(ctx, http.MethodPatch, "/git/refs/heads/"+escapeBranch(branch), map[string]any{"sha": commit, "force": false}, 1<<20, &out)
	if status == http.StatusUnprocessableEntity || status == http.StatusConflict {
		return errGitHubMovedAhead
	}
	if err != nil {
		return err
	}
	if out.Object.SHA != commit {
		return errGitHubUnavailable
	}
	return nil
}
