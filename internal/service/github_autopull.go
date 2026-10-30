package service

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"
)

// Automatic pulls (0.69.0). When GitHub sends the app a push delivery for a
// branch, every GitHub folder that mirrors that repository and branch with
// pull "auto" (the default) is pulled, as the pull tool would, in the
// background. Only spaces whose owner linked the installation that sent it
// are looked at. A conflict, or any other refusal, stops that pull as it
// stops a pull an agent asks for, and is logged; the folder waits for the
// next push or a pull by hand. Pushes that arrive while a folder is being
// pulled make one more pull when it finishes.

// autoPullApp names automatic pulls in history.
const autoPullApp = "GitHub auto-pull"

// githubPushDelivery is the part of a push delivery automatic pulls read.
type githubPushDelivery struct {
	Ref        string `json:"ref"`
	Deleted    bool   `json:"deleted"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

// parseGitHubPush reads a push delivery: the repository, the branch and the
// installation, ok false for anything else (tags, a deleted branch).
func parseGitHubPush(body []byte) (repo, branch string, installation int64, ok bool) {
	var d githubPushDelivery
	if json.Unmarshal(body, &d) != nil || d.Deleted || d.Installation.ID <= 0 || !githubRepoPattern.MatchString(d.Repository.FullName) {
		return "", "", 0, false
	}
	branch, isBranch := strings.CutPrefix(d.Ref, "refs/heads/")
	if !isBranch || branch == "" {
		return "", "", 0, false
	}
	return d.Repository.FullName, branch, d.Installation.ID, true
}

// autoPulls tracks the folders being pulled automatically.
type autoPulls struct {
	mu sync.Mutex
	// again is set for a folder being pulled: true once another push came.
	again map[string]bool
	wg    sync.WaitGroup
}

// githubPushed starts the pulls a push delivery asks for. It returns at once;
// the work runs in the background.
func (s *Service) githubPushed(repo, branch string, installation int64) {
	s.autoPull.wg.Add(1)
	go func() {
		defer s.autoPull.wg.Done()
		ctx, cancel := s.backgroundContext(time.Minute)
		defer cancel()
		for _, f := range s.autoPullFolders(ctx, repo, branch, installation) {
			s.queueAutoPull(f.repo, f.folder)
		}
	}()
}

type autoPullFolder struct {
	repo   *repository
	folder string
}

// autoPullFolders finds the folders that mirror repo and branch with pull
// "auto", in spaces whose owner linked the installation.
func (s *Service) autoPullFolders(ctx context.Context, repo, branch string, installation int64) []autoPullFolder {
	s.ownedMu.RLock()
	repos := make([]*repository, 0, len(s.ownedRepos))
	for _, r := range s.ownedRepos {
		repos = append(repos, r)
	}
	s.ownedMu.RUnlock()
	linked := map[string]bool{}
	var out []autoPullFolder
	for _, r := range repos {
		owner := s.repositoryOwner(r)
		ok, seen := linked[owner]
		if !seen {
			ok = s.ownerLinkedInstallation(ctx, owner, installation)
			linked[owner] = ok
		}
		if !ok {
			continue
		}
		_, files, err := r.snapshot(ctx, "")
		if err != nil {
			log.Printf("github auto-pull: space %s: %v", r.id, err)
			continue
		}
		folders, err := r.githubFolders(ctx, files, nil)
		if err != nil {
			log.Printf("github auto-pull: space %s: %v", r.id, err)
			continue
		}
		for folder := range folders {
			svc, err := r.githubFolderService(ctx, files, folder)
			if err == nil && strings.EqualFold(svc.Repo, repo) && svc.Branch == branch && svc.Pull == "auto" {
				out = append(out, autoPullFolder{r, folder})
			}
		}
	}
	return out
}

// ownerLinkedInstallation reports whether the user linked this installation
// and it is active.
func (s *Service) ownerLinkedInstallation(ctx context.Context, userID string, installation int64) bool {
	if s.githubInstalledHook != nil {
		return s.githubInstalledHook(userID, installation)
	}
	if s.ownedDB == nil {
		return false
	}
	insts, err := s.ownedDB.githubInstallations(ctx, userID)
	if err != nil {
		log.Printf("github auto-pull: %v", err)
		return false
	}
	for _, inst := range insts {
		if inst.InstallationID == installation && inst.Status == "active" {
			return true
		}
	}
	return false
}

// queueAutoPull pulls the folder, or, while it is being pulled, asks for one
// more pull afterwards.
func (s *Service) queueAutoPull(r *repository, folder string) {
	key := r.path + "\x00" + folder
	p := &s.autoPull
	p.mu.Lock()
	if p.again == nil {
		p.again = map[string]bool{}
	}
	if _, running := p.again[key]; running {
		p.again[key] = true
		p.mu.Unlock()
		return
	}
	p.again[key] = false
	p.mu.Unlock()
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			s.runAutoPull(r, folder)
			p.mu.Lock()
			if p.again[key] {
				p.again[key] = false
				p.mu.Unlock()
				continue
			}
			delete(p.again, key)
			p.mu.Unlock()
			return
		}
	}()
}

// runAutoPull pulls one folder as the space's owner, through the app.
func (s *Service) runAutoPull(r *repository, folder string) {
	ctx, cancel := s.backgroundContext(githubPullTimeout)
	defer cancel()
	if s.ownedRepository(r.id) != r {
		return // the space was deleted meanwhile
	}
	owner := s.usernameOf(ctx, s.repositoryOwner(r))
	ctx = withAuthor(ctx, func() changeAuthor { return changeAuthor{Name: owner, App: autoPullApp, Kind: "agent"} })
	value, err := s.pull(ctx, r, r.id, folder)
	if err != nil {
		log.Printf("github auto-pull: space %s %s: %v", r.id, folder, errorText(err))
		return
	}
	if result := value.(PullResult); !result.UpToDate {
		log.Printf("github auto-pull: space %s %s: %s, %d created, %d updated, %d deleted", r.id, folder, short(result.Commit), result.Created, result.Updated, result.Deleted)
	}
}

// errorText is an error's message, without its code.
func errorText(err error) string {
	if p, ok := err.(*Error); ok {
		return p.Message
	}
	return err.Error()
}

// backgroundContext is a context for work the service does on its own,
// cancelled when the service stops.
func (s *Service) backgroundContext(limit time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}
