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

// Billing (0.91.0): plans and Paddle subscriptions as the service last heard
// of them (webhooks and the daily check), and comping accounts. Nothing is
// limited by a plan yet.

func usersComp(ctx context.Context, db *service.AdminDB, who string, on bool) error {
	u, err := db.FindUser(ctx, who)
	if err != nil {
		return err
	}
	changed, err := db.SetComped(ctx, u, on, time.Now())
	if err != nil {
		return err
	}
	switch {
	case on && !changed:
		fmt.Printf("%s is already comped. Nothing changed.\n", u.Email)
	case on:
		fmt.Printf("%s is now comped: a full subscriber with no charge.\n", u.Email)
	case !changed:
		fmt.Printf("%s was not comped. Nothing changed.\n", u.Email)
	default:
		fmt.Printf("%s is no longer comped.\n", u.Email)
	}
	if b, err := db.Billing(ctx, u); err == nil {
		fmt.Printf("Plan: %s\n", planLine(b))
		if on && b.Subscription != nil && b.Subscription.Status != "canceled" {
			fmt.Printf("Its Paddle subscription %s (%s) still bills: cancel it in Paddle if it shouldn't.\n", b.Subscription.SubscriptionID, b.Subscription.Status)
		}
	}
	return nil
}

func billingCommand(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return fmt.Errorf("billing needs a subcommand: show or events")
	}
	name := "billing " + args[0]
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	accountsConfig := accountsConfigFlag(flags)
	asJSON := flags.Bool("json", false, "Print JSON instead of text")
	var outcome *string
	var limit *int
	switch args[0] {
	case "show":
	case "events":
		outcome = flags.String("outcome", "", "Only events with this outcome")
		limit = flags.Int("n", 50, "How many events, newest first (at most 1000)")
	default:
		printUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", name)
	}
	rest, err := parseArgs(flags, args[1:])
	if err != nil {
		return err
	}
	if args[0] == "show" && len(rest) > 1 || args[0] == "events" && len(rest) != 0 {
		return fmt.Errorf("usage: metatrash %s", commandUsage[name])
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := service.OpenAdminDB(ctx, *accountsConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	if args[0] == "events" {
		events, err := db.PaddleEvents(ctx, *outcome, *limit)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(events)
		}
		t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(t, "RECEIVED\tEVENT\tTYPE\tSUBSCRIPTION\tACCOUNT\tOUTCOME")
		for _, e := range events {
			fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\n", minute(e.ReceivedAt), e.EventID, e.EventType, orDash(e.SubscriptionID), orDash(e.UserID), e.Outcome)
		}
		t.Flush()
		fmt.Println(count(len(events), "event", "events"))
		return nil
	}
	if len(rest) == 1 {
		u, err := db.FindUser(ctx, rest[0])
		if err != nil {
			return err
		}
		b, err := db.Billing(ctx, u)
		if err != nil {
			return err
		}
		if *asJSON {
			return printJSON(b)
		}
		printBilling(b)
		return nil
	}
	list, err := db.BillingAccounts(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(list)
	}
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(t, "USERNAME\tEMAIL\tPLAN\tSTATUS\tSTORAGE\tPADDLE\tUPDATED")
	for _, b := range list {
		paddle, updated := "-", "-"
		if s := b.Subscription; s != nil {
			paddle, updated = s.SubscriptionID+" ("+s.Status+")", minute(s.UpdatedAt)
		} else if b.CompedAt != nil {
			updated = minute(*b.CompedAt)
		}
		fmt.Fprintf(t, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", orDash(b.Username), b.Email, b.Plan, b.Status, storageSize(b.StorageBytes), paddle, updated)
	}
	t.Flush()
	fmt.Println(count(len(list), "account", "accounts"))
	return nil
}

// planLine is an account's plan in a few words.
func planLine(b service.AdminBilling) string {
	line := b.Plan
	if b.Status != "active" {
		line += ", " + b.Status
	}
	if b.CompedAt != nil {
		line += " since " + day(*b.CompedAt)
	}
	if s := b.Subscription; s != nil && b.Plan == "trial" && !s.TrialEndsAt.IsZero() {
		line += " until " + day(s.TrialEndsAt)
	}
	if b.StorageBytes > 0 {
		line += ", " + storageSize(b.StorageBytes) + " storage"
	}
	return line
}

func printBilling(b service.AdminBilling) {
	t := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(t, "Email\t%s\n", b.Email)
	fmt.Fprintf(t, "Username\t%s\n", orDash(b.Username))
	fmt.Fprintf(t, "Plan\t%s\n", planLine(b))
	if s := b.Subscription; s != nil {
		fmt.Fprintf(t, "Subscription\t%s (%s)\n", s.SubscriptionID, s.Status)
		fmt.Fprintf(t, "Customer\t%s\n", s.CustomerID)
		fmt.Fprintf(t, "Extra storage\t%d GB\n", s.StorageGB)
		for _, row := range []struct {
			label string
			at    time.Time
		}{{"Trial ends", s.TrialEndsAt}, {"Period ends", s.PeriodEndsAt}, {"Next bill", s.NextBilledAt}, {"Cancelled", s.CanceledAt}} {
			if !row.at.IsZero() {
				fmt.Fprintf(t, "%s\t%s\n", row.label, minute(row.at))
			}
		}
		if s.ScheduledChange != "" {
			fmt.Fprintf(t, "Scheduled\t%s on %s\n", s.ScheduledChange, minute(s.ScheduledAt))
		}
		fmt.Fprintf(t, "Paddle updated\t%s\n", minute(s.PaddleUpdatedAt))
		fmt.Fprintf(t, "Stored\t%s\n", minute(s.UpdatedAt))
	} else {
		fmt.Fprintf(t, "Subscription\tnone\n")
	}
	t.Flush()
}
