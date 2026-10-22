package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Acknowledgements (0.48.0). A folder whose .metatrash.json has an
// acknowledge service and a publishedVersion asks everyone in the space (its
// owner and active members) to read its published files. A person confirms a
// file in Slate Viewer or with mtsync ack; the account database keeps one row
// per person, file and published text, so a new version asks again only for
// files whose text changed. Reads are never committed to the space.
//
// GET  .../acknowledgements             the caller's published folders and files
// GET  .../acknowledgements?report=true who has read what (read-write access)
// POST .../acknowledgements             {"path", "version"}: the caller read it
//
// Only the OAuth REST resource offers them, and only a person (Metatrash Sync)
// can acknowledge.

// maxAcknowledgementRows bounds rows read for one answer.
const maxAcknowledgementRows = 20000

type ackRow struct {
	UserID, FileID, Blob, Path string
	Version                    int
	At                         int64
}

func (db *accountDatabase) checkAcknowledgementSchema(ctx context.Context) error {
	bad := fmt.Errorf("account schema v12 required (acknowledgements); follow docs/deployment.md")
	var count int
	if err := db.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'metatrash_acknowledgements' AND engine = 'InnoDB'").Scan(&count); err != nil || count != 1 {
		return bad
	}
	var columns sql.NullString
	var nonUnique, prefixes int
	if err := db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ','), COALESCE(SUM(non_unique), 0), COUNT(sub_part) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'metatrash_acknowledgements' AND index_name = 'PRIMARY'").Scan(&columns, &nonUnique, &prefixes); err != nil || columns.String != "space_id,user_id,file_id,blob_hash" || nonUnique != 0 || prefixes != 0 {
		return bad
	}
	rows, err := db.db.QueryContext(ctx, "SELECT space_id, user_id, file_id, blob_hash, version, path, acknowledged_at FROM metatrash_acknowledgements LIMIT 0")
	if err != nil {
		return bad
	}
	rows.Close()
	return nil
}

// recordAcknowledgement keeps the first time a person confirmed one text of
// a file; confirming it again changes nothing.
func (db *accountDatabase) recordAcknowledgement(ctx context.Context, spaceID string, row ackRow) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := db.db.ExecContext(ctx, "INSERT INTO metatrash_acknowledgements (space_id, user_id, file_id, blob_hash, version, path, acknowledged_at) VALUES (?, ?, ?, ?, ?, ?, ?)", spaceID, row.UserID, row.FileID, row.Blob, row.Version, row.Path, row.At)
	var duplicate *mysql.MySQLError
	if errors.As(err, &duplicate) && duplicate.Number == 1062 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot record acknowledgement")
	}
	return nil
}

// acknowledgements reads a space's rows, or one person's when userID is set.
func (db *accountDatabase) acknowledgements(ctx context.Context, spaceID, userID string) ([]ackRow, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	query := "SELECT user_id, file_id, blob_hash, version, path, acknowledged_at FROM metatrash_acknowledgements WHERE space_id = ?"
	args := []any{spaceID}
	if userID != "" {
		query += " AND user_id = ?"
		args = append(args, userID)
	}
	rows, err := db.db.QueryContext(ctx, query+fmt.Sprintf(" ORDER BY acknowledged_at LIMIT %d", maxAcknowledgementRows), args...)
	if err != nil {
		return nil, fmt.Errorf("cannot read acknowledgements")
	}
	defer rows.Close()
	var out []ackRow
	for rows.Next() {
		var a ackRow
		if err := rows.Scan(&a.UserID, &a.FileID, &a.Blob, &a.Version, &a.Path, &a.At); err != nil {
			return nil, fmt.Errorf("cannot read acknowledgements")
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read acknowledgements")
	}
	return out, nil
}

type spaceReader struct {
	UserID, Name string
}

// spaceReaders are the people asked to read: the owner, then active members.
func (db *accountDatabase) spaceReaders(ctx context.Context, spaceID string) ([]spaceReader, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := db.db.QueryContext(ctx, `SELECT u.user_id, COALESCE(u.username, ''), u.email, 0 FROM metatrash_spaces s JOIN metatrash_users u ON u.user_id = s.owner_user_id WHERE s.space_id = ?
UNION ALL SELECT u.user_id, COALESCE(u.username, ''), u.email, 1 FROM metatrash_memberships m JOIN metatrash_users u ON u.user_id = m.user_id WHERE m.space_id = ? AND m.status = 'active'
ORDER BY 4, 2, 1 LIMIT 1000`, spaceID, spaceID)
	if err != nil {
		return nil, fmt.Errorf("cannot read space members")
	}
	defer rows.Close()
	var out []spaceReader
	for rows.Next() {
		var r spaceReader
		var email string
		var member int
		if err := rows.Scan(&r.UserID, &r.Name, &email, &member); err != nil {
			return nil, fmt.Errorf("cannot read space members")
		}
		if r.Name == "" {
			r.Name = emailName(email)
		}
		out = append(out, r)
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("cannot read space members")
	}
	return out, nil
}

// policyAck is a person's newest confirmation of a file.
type policyAck struct {
	Version   int    `json:"version"`
	Timestamp string `json:"timestamp"`
	// Current is set when it confirmed the text now published.
	Current bool `json:"current"`
}

type policyFile struct {
	ID string `json:"id"`
	// Path is where the file is now; PublishedPath where it was in the
	// published version, for reading that text (read with revision).
	Path          string `json:"path"`
	PublishedPath string `json:"publishedPath"`
	// Blob is the Git blob hash of the published text.
	Blob string `json:"blob"`
	// Draft is set when the file has changed since it was published.
	Draft bool `json:"draft"`
	// Tracked is set when the folder's acknowledge service asks people to
	// read the file; Outstanding when the caller has not read this text yet.
	Tracked      bool       `json:"tracked"`
	Outstanding  bool       `json:"outstanding"`
	Acknowledged *policyAck `json:"acknowledged,omitempty"`
}

type policyFolder struct {
	Folder string `json:"folder"`
	publishPoint
	// Acknowledge is set when the folder asks people to read its files.
	Acknowledge bool         `json:"acknowledge"`
	Files       []policyFile `json:"files"`
}

type policiesResult struct {
	Space       string         `json:"space"`
	State       string         `json:"state"`
	Folders     []policyFolder `json:"folders"`
	Outstanding int            `json:"outstanding"`
}

// publishedFolders lists every folder with a publishedVersion at state and
// the files of its current published version that still exist.
func (r *repository) publishedFolders(ctx context.Context, state string, files map[string]record) ([]policyFolder, error) {
	configs, err := r.folderConfigs(ctx, files)
	if err != nil {
		return nil, err
	}
	current := make(map[string]string, len(files))
	for p, f := range files {
		current[f.ID] = p
	}
	folders := []policyFolder{}
	names := make([]string, 0, len(configs))
	for folder, c := range configs {
		if c.PublishedVersion != nil {
			names = append(names, folder)
		}
	}
	sort.Strings(names)
	for _, folder := range names {
		c := configs[folder]
		points, err := r.publishPoints(ctx, state, files, folder)
		if err != nil {
			return nil, err
		}
		var point *publishPoint
		for i := range points {
			if points[i].Version == *c.PublishedVersion {
				point = &points[i]
			}
		}
		if point == nil {
			continue
		}
		svc, ack := c.acknowledgeService()
		pf := policyFolder{Folder: folder, publishPoint: *point, Acknowledge: ack, Files: []policyFile{}}
		published, err := r.indexAt(ctx, point.Revision)
		if err != nil {
			return nil, err
		}
		pubConfigs := map[string]folderConfig{}
		for p, f := range published {
			if isFolderConfig(p) {
				if pc, err := r.cachedConfig(ctx, f.Blob); err == nil {
					pubConfigs[configFolder(p)] = pc
				}
			}
		}
		paths := make([]string, 0, len(published))
		for p := range published {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for _, p := range paths {
			f := published[p]
			if !strings.HasPrefix(p, point.folder) || isFolderFile(p) {
				continue
			}
			if owner, _, _ := publishingFolder(pubConfigs, p); owner != point.folder {
				continue
			}
			now, exists := current[f.ID]
			if !exists {
				continue
			}
			pf.Files = append(pf.Files, policyFile{ID: f.ID, Path: now, PublishedPath: p, Blob: f.Blob, Draft: files[now].Blob != f.Blob, Tracked: ack && svc.tracked(folder, now)})
		}
		folders = append(folders, pf)
	}
	return folders, nil
}

// ackIndex holds acknowledgement rows by person and file, oldest first.
type ackIndex map[string]map[string][]ackRow

func indexAcks(rows []ackRow) ackIndex {
	idx := ackIndex{}
	for _, a := range rows {
		if idx[a.UserID] == nil {
			idx[a.UserID] = map[string][]ackRow{}
		}
		idx[a.UserID][a.FileID] = append(idx[a.UserID][a.FileID], a)
	}
	return idx
}

// status is a person's state for one published file.
func (idx ackIndex) status(userID string, f policyFile) *policyAck {
	rows := idx[userID][f.ID]
	if len(rows) == 0 {
		return nil
	}
	newest := rows[len(rows)-1]
	ack := &policyAck{Version: newest.Version, Timestamp: time.Unix(newest.At, 0).UTC().Format(time.RFC3339)}
	for _, a := range rows {
		if a.Blob == f.Blob {
			ack.Current = true
			ack.Version, ack.Timestamp = a.Version, time.Unix(a.At, 0).UTC().Format(time.RFC3339)
		}
	}
	return ack
}

// policies answers GET .../acknowledgements for one person.
func (s *Service) policies(ctx context.Context, r *repository, space, userID string) (policiesResult, error) {
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return policiesResult{}, err
	}
	folders, err := r.publishedFolders(ctx, state, files)
	if err != nil {
		return policiesResult{}, err
	}
	result := policiesResult{Space: space, State: state, Folders: folders}
	var idx ackIndex
	for _, f := range folders {
		if f.Acknowledge && idx == nil {
			rows, err := s.ownedDB.acknowledgements(ctx, r.id, userID)
			if err != nil {
				return policiesResult{}, err
			}
			idx = indexAcks(rows)
		}
	}
	for i := range result.Folders {
		for j := range result.Folders[i].Files {
			f := &result.Folders[i].Files[j]
			if !f.Tracked {
				continue
			}
			f.Acknowledged = idx.status(userID, *f)
			f.Outstanding = f.Acknowledged == nil || !f.Acknowledged.Current
			if f.Outstanding {
				result.Outstanding++
			}
		}
	}
	return result, nil
}

type reportFile struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Folder  string `json:"folder"`
	Version int    `json:"version"`
}

type reportPerson struct {
	Name        string `json:"name"`
	Outstanding int    `json:"outstanding"`
	// Files holds one entry per report file, in the same order.
	Files []reportStatus `json:"files"`
}

type reportStatus struct {
	// Status is "read" (this text), "older" (an earlier text) or "unread".
	Status       string     `json:"status"`
	Acknowledged *policyAck `json:"acknowledged,omitempty"`
}

type reportResult struct {
	Space  string         `json:"space"`
	State  string         `json:"state"`
	Files  []reportFile   `json:"files"`
	People []reportPerson `json:"people"`
}

// policyReport answers GET .../acknowledgements?report=true.
func (s *Service) policyReport(ctx context.Context, r *repository, space string) (reportResult, error) {
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return reportResult{}, err
	}
	folders, err := r.publishedFolders(ctx, state, files)
	if err != nil {
		return reportResult{}, err
	}
	result := reportResult{Space: space, State: state, Files: []reportFile{}, People: []reportPerson{}}
	var tracked []policyFile
	for _, folder := range folders {
		for _, f := range folder.Files {
			if f.Tracked {
				tracked = append(tracked, f)
				result.Files = append(result.Files, reportFile{ID: f.ID, Path: f.Path, Folder: folder.Folder, Version: folder.Version})
			}
		}
	}
	readers, err := s.ownedDB.spaceReaders(ctx, r.id)
	if err != nil {
		return reportResult{}, err
	}
	var idx ackIndex
	if len(tracked) > 0 {
		rows, err := s.ownedDB.acknowledgements(ctx, r.id, "")
		if err != nil {
			return reportResult{}, err
		}
		idx = indexAcks(rows)
	}
	for _, person := range readers {
		row := reportPerson{Name: person.Name, Files: []reportStatus{}}
		for _, f := range tracked {
			ack := idx.status(person.UserID, f)
			st := reportStatus{Status: "unread", Acknowledged: ack}
			if ack != nil && ack.Current {
				st.Status = "read"
			} else {
				row.Outstanding++
				if ack != nil {
					st.Status = "older"
				}
			}
			row.Files = append(row.Files, st)
		}
		result.People = append(result.People, row)
	}
	return result, nil
}

// acknowledge records that the person read path's published text, version.
func (s *Service) acknowledge(ctx context.Context, r *repository, space, userID, path string, version int) (policyFile, error) {
	state, files, err := r.snapshot(ctx, "")
	if err != nil {
		return policyFile{}, err
	}
	if !validPath(path) {
		return policyFile{}, invalid("Invalid path.")
	}
	folders, err := r.publishedFolders(ctx, state, files)
	if err != nil {
		return policyFile{}, err
	}
	for _, folder := range folders {
		for _, f := range folder.Files {
			if f.Path != path {
				continue
			}
			if !f.Tracked {
				return policyFile{}, invalid(path + " is not a file its folder asks people to read (an acknowledge service in " + folder.Folder + folderConfigName + ").")
			}
			if version != folder.Version {
				return policyFile{}, problem(409, "conflict", fmt.Sprintf("%s is now at version %d, not %d; read the new version, then confirm it.", folder.Folder, folder.Version, version))
			}
			if err := s.ownedDB.recordAcknowledgement(ctx, r.id, ackRow{UserID: userID, FileID: f.ID, Blob: f.Blob, Path: path, Version: version, At: time.Now().UTC().Unix()}); err != nil {
				return policyFile{}, err
			}
			f.Acknowledged = &policyAck{Version: version, Timestamp: time.Now().UTC().Format(time.RFC3339), Current: true}
			return f, nil
		}
	}
	return policyFile{}, problem(404, "not_found", path+" is not in a published version of its folder.")
}

// serveAcknowledgements handles the acknowledgements resource after access
// was checked for reading; the report needs read-write access too.
func (h *httpAdapter) serveAcknowledgements(w http.ResponseWriter, r *http.Request, repo *repository, space string, access func(write bool) (*repository, string, error)) {
	identity, _ := r.Context().Value(mcpIdentityKey{}).(mcpIdentity)
	if identity.oauth == nil || repo.id == "" || h.service.ownedDB == nil {
		sendError(w, problem(404, "not_found", "Acknowledgements are kept only for your own and shared spaces."))
		return
	}
	userID := identity.oauth.UserID
	if r.Method == http.MethodGet {
		var value any
		var err error
		switch r.URL.RawQuery {
		case "":
			value, err = h.service.policies(r.Context(), repo, space, userID)
		case "report=true":
			if _, _, err = access(true); err == nil {
				value, err = h.service.policyReport(r.Context(), repo, space)
			}
		default:
			err = invalid("Unknown query parameter; the only one is report=true.")
		}
		if err != nil {
			sendError(w, err)
			return
		}
		sendJSON(w, 200, value)
		return
	}
	if r.URL.RawQuery != "" {
		sendError(w, invalid("Unknown query parameter."))
		return
	}
	if !personWrite(r.Context()) {
		sendError(w, problem(403, "forbidden", "Only a person can confirm reading a file, in Slate Viewer or with mtsync ack; agents cannot."))
		return
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		sendError(w, problem(415, "invalid_request", "Use application/json."))
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			sendError(w, problem(413, "payload_too_large", "Request exceeds the byte limit."))
		} else {
			sendError(w, invalid("Could not read body."))
		}
		return
	}
	var body struct {
		Path    string `json:"path"`
		Version int    `json:"version"`
	}
	if err := strictJSON(b, &body); err != nil || body.Path == "" || body.Version < 1 {
		sendError(w, invalid(`Send {"path": "...", "version": N}: the file and the published version you read.`))
		return
	}
	value, err := h.service.acknowledge(r.Context(), repo, space, userID, body.Path, body.Version)
	if err != nil {
		sendError(w, err)
		return
	}
	sendJSON(w, 200, value)
}
