package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Runs only with the existing disposable MariaDB test configuration/runtime grants.
func TestDatabaseAgentDiscovery(t *testing.T) {
	w := newOAuthWorld(t)
	store := dbJobStore{w.db.db}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var index string
	if err := w.db.db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index SEPARATOR ',') FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='metatrash_agent_jobs' AND index_name='metatrash_agent_jobs_queue_space'").Scan(&index); err != nil || index != "status,space_id,queued_at,job_id" {
		t.Fatalf("queue index missing or wrong: %q %v", index, err)
	}
	// A large old backlog and equal timestamps exercise both the predecessor
	// probe and deterministic per-space ordering. Other tests' queued rows are
	// left alone: collect our two spaces through the real paginated query.
	var oldest string
	for n := 0; n < 160; n++ {
		id, err := randomHex(16)
		if err != nil {
			t.Fatal(err)
		}
		if oldest == "" || id < oldest {
			oldest = id
		}
		j := agentJob{ID: id, SpaceID: w.ownedID, Folder: "queue/", Action: "test",
			File: "test.txt", Commit: strings.Repeat("a", 40), Status: jobQueued, QueuedAt: time.Unix(1, 0)}
		if err := store.add(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	id, err := randomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	other := agentJob{ID: id, SpaceID: w.joinID, Folder: "queue/", Action: "test",
		File: "test.txt", Commit: strings.Repeat("b", 40), Status: jobQueued, QueuedAt: time.Unix(2, 0)}
	if err := store.add(ctx, other); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		// Runtime grants deliberately cannot delete job history. Mark only this
		// fixture's jobs skipped so later worlds do not inherit a queued backlog.
		if _, err := w.db.db.ExecContext(cleanup, "UPDATE metatrash_agent_jobs SET status='skipped', finished_at=? WHERE folder='queue/' AND action='test' AND space_id IN (?,?)", time.Now().Unix(), w.ownedID, w.joinID); err != nil {
			t.Errorf("cleanup queue fixture: %v", err)
		}
	})
	collect := func(excluded []string) map[string]agentJob {
		t.Helper()
		found := map[string]agentJob{}
		after := ""
		for page := 0; page < 100; page++ {
			jobs, err := store.queued(ctx, after, excluded, jobDiscoveryPage)
			if err != nil {
				t.Fatal(err)
			}
			if len(jobs) == 0 {
				return found
			}
			for _, j := range jobs {
				if j.SpaceID <= after {
					t.Fatalf("cursor did not advance: %+v", j)
				}
				after = j.SpaceID
				if j.SpaceID == w.ownedID || j.SpaceID == w.joinID {
					found[j.SpaceID] = j
				}
			}
		}
		t.Fatal("disposable fixture exceeded traversal budget")
		return found
	}
	all := collect(nil)
	if all[w.ownedID].ID != oldest || all[w.joinID].ID != other.ID {
		t.Fatalf("wrong candidate heads: %+v", all)
	}
	eligible := collect([]string{w.ownedID})
	if _, hidden := eligible[w.ownedID]; hidden || eligible[w.joinID].ID != other.ID {
		t.Fatalf("busy backlog hid eligible space: %+v", eligible)
	}
	query, args, err := queuedJobQuery("", []string{w.ownedID}, jobDiscoveryPage)
	if err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := w.db.db.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON "+query, args...).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	t.Logf("queue discovery plan (review before promotion): %s", plan)
	if !strings.Contains(plan, "metatrash_agent_jobs_queue_space") {
		t.Fatal("queue-space index is absent from the explained access paths")
	}
	// Winning and lost claims still distinguish real execution admission.
	if ok, err := store.claim(ctx, other.ID, time.Now()); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, err := store.claim(ctx, other.ID, time.Now()); err != nil || ok {
		t.Fatalf("duplicate claim: %v %v", ok, err)
	}
	closed, stop := context.WithCancel(context.Background())
	stop()
	if _, err := store.queued(closed, "", nil, jobDiscoveryPage); err == nil {
		t.Fatal("cancelled query succeeded")
	}
}
