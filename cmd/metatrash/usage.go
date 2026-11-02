package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"metatrash.com/metatrash/internal/service"
)

// Storage (0.89.0): what owned spaces take up, as the service last measured
// it (after each write, and in full daily). Nothing is limited yet.

const usageNote = "Git counts every version of every text file; files are virtual files' content and text in the bucket, every version kept."

func usersUsage(ctx context.Context, db *service.AdminDB, rest []string, asJSON bool) error {
	var only *service.AdminUser
	if len(rest) == 1 {
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		only = &u
	}
	usage, err := db.UserUsage(ctx, only)
	if err != nil {
		return err
	}
	if only != nil {
		spaces, err := db.SpaceUsage(ctx, only.ID)
		if err != nil {
			return err
		}
		if asJSON {
			var one service.AdminUserUsage
			if len(usage) == 1 {
				one = usage[0]
			}
			return printJSON(struct {
				service.AdminUserUsage
				SpaceList []service.AdminSpaceUsage `json:"spaceList"`
			}{one, spaces})
		}
		if len(usage) == 1 {
			u := usage[0]
			fmt.Printf("%s (%s): %s in %s (Git %s, files %s)\n", orDash(u.Username), u.Email, storageSize(u.Bytes), count(u.Spaces, "space", "spaces"), storageSize(u.GitBytes), storageSize(u.FileBytes))
			if u.DeletedSpaces > 0 {
				fmt.Printf("Deleted spaces kept for 30 days: %s, %s (not in the total)\n", count(u.DeletedSpaces, "space", "spaces"), storageSize(u.DeletedBytes))
			}
		}
		printSpaceUsage(spaces, false)
		return nil
	}
	if asJSON {
		return printJSON(usage)
	}
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "USERNAME\tEMAIL\tSPACES\tTOTAL\tGIT\tFILES\tOBJECTS\tDELETED\tMEASURED")
	var total int64
	for _, u := range usage {
		spaces := fmt.Sprint(u.Spaces)
		if u.Unmeasured > 0 {
			spaces += fmt.Sprintf(" (%d not measured)", u.Unmeasured)
		}
		deleted := "-"
		if u.DeletedSpaces > 0 {
			deleted = storageSize(u.DeletedBytes)
		}
		measured := "-"
		if u.OldestMeasuredAt != nil {
			measured = ago(*u.OldestMeasuredAt)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", orDash(u.Username), u.Email, spaces, storageSize(u.Bytes), storageSize(u.GitBytes), storageSize(u.FileBytes), u.FileObjects, deleted, measured)
		total += u.Bytes
	}
	t.Flush()
	fmt.Printf("%s, %s in all. %s\n", count(len(usage), "account", "accounts"), storageSize(total), usageNote)
	return nil
}

func spacesUsage(args []string) error {
	flags := flag.NewFlagSet("spaces usage", flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	asJSON := flags.Bool("json", false, "Print JSON instead of a table")
	rest, err := parseArgs(flags, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: metatrash %s", commandUsage["spaces usage"])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := service.OpenAdminDB(ctx, *accountsConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	ownerID := ""
	if len(rest) == 1 {
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		ownerID = u.ID
	}
	spaces, err := db.SpaceUsage(ctx, ownerID)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(spaces)
	}
	printSpaceUsage(spaces, true)
	return nil
}

func printSpaceUsage(spaces []service.AdminSpaceUsage, summary bool) {
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "SPACE\tSTATE\tTOTAL\tGIT\tFILES\tOBJECTS\tGIT MEASURED\tFILES MEASURED")
	var total int64
	for _, s := range spaces {
		if !s.Measured {
			fmt.Fprintf(t, "%s/%s\t%s\tnot measured yet\t\t\t\t\t\n", s.Owner, s.Slug, s.State)
			continue
		}
		files := "never"
		if s.FilesMeasuredAt != nil {
			files = ago(*s.FilesMeasuredAt)
		}
		fmt.Fprintf(t, "%s/%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n", s.Owner, s.Slug, s.State, storageSize(s.Bytes), storageSize(s.GitBytes), storageSize(s.FileBytes), s.FileObjects, ago(*s.GitMeasuredAt), files)
		if s.State == "ready" {
			total += s.Bytes
		}
	}
	t.Flush()
	if summary {
		fmt.Printf("%s, %s in ready spaces (spaces.json spaces are not counted). %s\n", count(len(spaces), "owned space", "owned spaces"), storageSize(total), usageNote)
	} else {
		fmt.Println(usageNote)
	}
}

// storageSize is a byte count for people, in binary units (1 GB = 1024 MB).
func storageSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ago says how long ago t was, roughly.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
