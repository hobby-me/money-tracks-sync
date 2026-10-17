// synk — сервер синхронизации Money Tracks и CLI управления учётками.
//
//	synk serve   --data DIR --listen ADDR --metrics-listen ADDR [--quota-mb N]
//	synk tui     --data DIR [--listen ADDR] [--metrics-listen ADDR] [--url URL]   (то же: --tui)
//	synk status  --data DIR [--listen ADDR] [--metrics-listen ADDR]
//	synk account add --data DIR --name NAME [--bootstrap] [--quota-mb N] [--url URL]
//	synk account list --data DIR
//	synk account quota --data DIR --account ID|NAME (--mb N | --default)   (0 — без лимита)
//	synk account delete --data DIR --account ID|NAME [--force]   (папка → data/trash)
//	synk trash empty --data DIR
//	synk invite  --data DIR --account ID|NAME [--admin] [--url URL]
//	synk invite list   --data DIR [--account ID|NAME]
//	synk invite cancel --data DIR --hash CODE_HASH
//	synk invite purge  --data DIR
//	synk device list   --data DIR [--account ID|NAME]
//	synk device revoke --data DIR --id DEVICE_ID
//	synk device demote --data DIR --id DEVICE_ID
//	synk version
//
// Все операции над данными — в internal/admin; CLI и TUI лишь по-разному
// их показывают. Новая операция добавляется в admin и дальше выводится тут
// (строкой) и в internal/tui (клавишей).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/hobby-me/money-tracks-sync/internal/admin"
	"github.com/hobby-me/money-tracks-sync/internal/server"
	"github.com/hobby-me/money-tracks-sync/internal/store"
	"github.com/hobby-me/money-tracks-sync/internal/tui"
	"github.com/hobby-me/money-tracks-sync/internal/version"
)

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	args := os.Args[1:]
	// `synk --data DIR --tui` — флаг-переключатель для скрипта запуска на
	// сервере; эквивалентен подкоманде tui.
	if i := slices.Index(args, "--tui"); i >= 0 {
		args = append([]string{"tui"}, slices.Delete(slices.Clone(args), i, i+1)...)
	}
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "serve":
		cmdServe(args[1:])
	case "tui":
		cmdTUI(args[1:])
	case "status":
		cmdStatus(args[1:])
	case "account":
		cmdAccount(args[1:])
	case "invite":
		cmdInvite(args[1:])
	case "device":
		cmdDevice(args[1:])
	case "trash":
		cmdTrash(args[1:])
	case "version":
		fmt.Println(versionLine())
	default:
		usage()
	}
}

func versionLine() string {
	return "synk " + version.Version + " (" + version.GitCommit + ", " + version.BuildDate + ", " + version.GoVersion() + ")"
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: synk serve|tui|status|account|invite|device|trash|version ... (see main.go header or man synk)")
	os.Exit(2)
}

func dataFlag(fs *flag.FlagSet) *string {
	return fs.String("data", envOr("SYNK_DATA", "data"), "data directory")
}

func listenFlag(fs *flag.FlagSet) *string {
	return fs.String("listen", envOr("SYNK_LISTEN", "127.0.0.1:8090"), "API address (behind nginx)")
}

// metricsFlag — отдельный порт для /metrics: только localhost, в nginx не
// проксируется, поэтому финансовая статистика снаружи недостижима.
func metricsFlag(fs *flag.FlagSet) *string {
	return fs.String("metrics-listen", envOr("SYNK_METRICS_LISTEN", "127.0.0.1:8091"), "metrics address (localhost only)")
}

// envQuotaMB — глобальный потолок из SYNK_ACCOUNT_QUOTA_MB (synk.env);
// без него — store.DefaultQuotaBytes. 0 снимает ограничение.
func envQuotaMB() int64 {
	if v := os.Getenv("SYNK_ACCOUNT_QUOTA_MB"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return int64(server.DefaultQuotaBytes >> 20)
}

// quotaFlag — глобальный потолок флагом (serve, tui, status); у команд
// account флаг --quota-mb значит квоту самой учётки, там берётся envQuotaMB.
func quotaFlag(fs *flag.FlagSet) *int64 {
	return fs.Int64("quota-mb", envQuotaMB(), "global per-account storage quota, MB (0 = unlimited)")
}

// urlFlag — публичный адрес сервера для блока подключения. На сервере задан
// в /opt/synk/synk.env (SYNK_PUBLIC_URL), который читают systemd и synk-tui.
func urlFlag(fs *flag.FlagSet) *string {
	return fs.String("url", os.Getenv("SYNK_PUBLIC_URL"), "public server URL for invite messages")
}

// openAdmin открывает данные; quotaMB — глобальный потолок сервера, чтобы
// таблицы показывали действующую квоту.
func openAdmin(dataDir string, quotaMB int64) *admin.Admin {
	a, err := admin.Open(dataDir)
	if err != nil {
		log.Fatal(err)
	}
	a.DefaultQuota = quotaMB << 20
	return a
}

func fatal(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func cmdServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	data := dataFlag(fs)
	listen := listenFlag(fs)
	metrics := metricsFlag(fs)
	quotaMB := quotaFlag(fs)
	fs.Parse(args)

	if err := os.MkdirAll(*data, 0o700); err != nil {
		log.Fatal(err)
	}
	accounts, err := store.OpenAccounts(*data)
	fatal(err)
	defer accounts.Close()
	pool := store.NewPool(*data)
	defer pool.Close()

	app := server.New(accounts, pool)
	app.Quota = *quotaMB << 20
	srv := &http.Server{
		Addr:              *listen,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	msrv := &http.Server{
		Addr:              *metrics,
		Handler:           app.MetricsHandler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serve := func(s *http.Server, what string) {
		log.Printf("synk %s: %s on %s", version.Version, what, s.Addr)
		if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}
	go serve(srv, "api, data in "+*data)
	go serve(msrv, "metrics")
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	msrv.Shutdown(ctx)
}

func cmdTUI(args []string) {
	fs := flag.NewFlagSet("tui", flag.ExitOnError)
	data := dataFlag(fs)
	listen := listenFlag(fs)
	metrics := metricsFlag(fs)
	url := urlFlag(fs)
	quotaMB := quotaFlag(fs)
	fs.Parse(args)
	fatal(tui.Run(tui.Options{DataDir: *data, Listen: *listen, Metrics: *metrics, URL: *url,
		Version: version.Version, QuotaBytes: *quotaMB << 20}))
}

// cmdStatus — та же сводка, что в шапке TUI, одним экраном текста.
func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	data := dataFlag(fs)
	listen := listenFlag(fs)
	metrics := metricsFlag(fs)
	quotaMB := quotaFlag(fs)
	fs.Parse(args)
	a := openAdmin(*data, *quotaMB)
	defer a.Close()

	h := admin.Probe(context.Background(), *listen, *metrics)
	if h.Up {
		fmt.Printf("server %s: up %s  push=%d pull=%d photos=%d errors=%d\n", h.Listen, h.Version,
			int64(h.Counter("synk_ops_received_total")), int64(h.Counter("synk_ops_served_total")),
			int64(h.Counter("synk_photos_put_total")), int64(h.Counter("synk_errors_total")))
	} else {
		fmt.Printf("server %s: DOWN (%s)\n", h.Listen, h.Err)
	}
	if h.Up && !h.StartedAt.IsZero() {
		fmt.Printf("uptime %s  rss %s\n", h.Uptime(time.Now()).Truncate(time.Minute), humanBytes(h.RSSBytes))
	}
	ov, err := a.Overview()
	fatal(err)
	fmt.Printf("disk: data %s (accounts %s, trash %s) · free %s of %s\n",
		humanBytes(ov.DataBytes), humanBytes(ov.AccountsBytes()), humanBytes(ov.TrashBytes),
		humanBytes(ov.Disk.FreeBytes), humanBytes(ov.Disk.TotalBytes))
	fmt.Printf("idle accounts: 1m+ %d · 6m+ %d · 1y+ %d (deletable)\n",
		ov.IdleCounts[1], ov.IdleCounts[2], ov.IdleCounts[3])
	fmt.Printf("%-26s  %-16s  %7s  %7s  %8s  %8s  %8s  %7s  %-5s %s\n",
		"ACCOUNT", "NAME", "DEVICES", "INVITES", "OPS", "ROWS", "SIZE", "QUOTA", "IDLE", "LAST ACTIVITY")
	for _, acc := range ov.Accounts {
		fmt.Printf("%-26s  %-16s  %3d/%-3d  %7d  %8d  %8d  %8s  %7s  %-5s %s\n",
			acc.ID, acc.Name, acc.DevicesActive, acc.DevicesRevoked, acc.PendingInvites,
			acc.Stats.LatestSeq, acc.Stats.Rows, humanBytes(acc.Stats.DBBytes+acc.Stats.PhotoBytes),
			a.QuotaLabel(acc.Account), admin.IdleLabel(acc.Idle(ov.At)), fmtTime(acc.LastActivity()))
	}
	fmt.Println("quota: * — задана учётке, остальные — глобальная", humanBytes(a.DefaultQuota))
}

func cmdTrash(args []string) {
	if len(args) < 1 || args[0] != "empty" {
		usage()
	}
	fs := flag.NewFlagSet("trash empty", flag.ExitOnError)
	data := dataFlag(fs)
	fs.Parse(args[1:])
	a := openAdmin(*data, 0)
	defer a.Close()
	n, err := a.EmptyTrash()
	fatal(err)
	fmt.Printf("trash emptied, freed %s\n", humanBytes(n))
}

func cmdAccount(args []string) {
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("account add", flag.ExitOnError)
		data := dataFlag(fs)
		name := fs.String("name", "", "account name")
		bootstrap := fs.Bool("bootstrap", false, "also print an admin invite code")
		quotaMB := fs.Int64("quota-mb", -1, "this account's quota, MB (0 = unlimited; omit = global)")
		url := urlFlag(fs)
		fs.Parse(args[1:])
		if *name == "" {
			log.Fatal("--name required")
		}
		a := openAdmin(*data, envQuotaMB())
		defer a.Close()
		acc, inv, err := a.AddAccount(*name, *bootstrap)
		fatal(err)
		if *quotaMB >= 0 {
			q := *quotaMB << 20
			acc, err = a.SetQuota(acc.ID, &q)
			fatal(err)
		}
		fmt.Printf("account %s (%s)  quota %s\n", acc.ID, acc.Name, a.QuotaLabel(acc))
		if inv != nil {
			printInvite(acc, *inv, true, *url)
		}
	case "list":
		fs := flag.NewFlagSet("account list", flag.ExitOnError)
		data := dataFlag(fs)
		fs.Parse(args[1:])
		a := openAdmin(*data, envQuotaMB())
		defer a.Close()
		accs, err := a.Accounts()
		fatal(err)
		for _, acc := range accs {
			fmt.Printf("%s  %s  quota=%-8s %s\n", acc.ID, acc.CreatedAt.Format("2006-01-02"), a.QuotaLabel(acc.Account), acc.Name)
		}
	case "quota":
		fs := flag.NewFlagSet("account quota", flag.ExitOnError)
		data := dataFlag(fs)
		account := fs.String("account", "", "account id or name")
		mb := fs.Int64("mb", -1, "quota in MB (0 = unlimited)")
		useDefault := fs.Bool("default", false, "drop the per-account quota, use the global one")
		fs.Parse(args[1:])
		if *account == "" || (*mb < 0 && !*useDefault) {
			log.Fatal("--account and (--mb N | --default) required")
		}
		a := openAdmin(*data, envQuotaMB())
		defer a.Close()
		var q *int64
		if !*useDefault {
			v := *mb << 20
			q = &v
		}
		acc, err := a.SetQuota(*account, q)
		fatal(err)
		fmt.Printf("account %s (%s)  quota %s\n", acc.ID, acc.Name, a.QuotaLabel(acc))
	case "delete":
		fs := flag.NewFlagSet("account delete", flag.ExitOnError)
		data := dataFlag(fs)
		account := fs.String("account", "", "account id or name")
		force := fs.Bool("force", false, "delete even if active within the last year")
		fs.Parse(args[1:])
		if *account == "" {
			log.Fatal("--account required")
		}
		a := openAdmin(*data, 0)
		defer a.Close()
		acc, trash, err := a.DeleteAccount(*account, *force)
		if errors.Is(err, admin.ErrStillActive) {
			log.Fatalf("account %s (%s) was active less than a year ago; add --force to delete anyway", acc.Name, acc.ID)
		}
		fatal(err)
		fmt.Printf("deleted account %s (%s)\n", acc.ID, acc.Name)
		if trash != "" {
			fmt.Printf("data moved to %s — `synk trash empty` removes it for good\n", trash)
		}
	default:
		usage()
	}
}

func cmdInvite(args []string) {
	if len(args) > 0 && (args[0] == "list" || args[0] == "cancel" || args[0] == "purge") {
		cmdInviteSub(args[0], args[1:])
		return
	}
	fs := flag.NewFlagSet("invite", flag.ExitOnError)
	data := dataFlag(fs)
	account := fs.String("account", "", "account id or name")
	isAdmin := fs.Bool("admin", false, "grant admin to the registering device")
	url := urlFlag(fs)
	fs.Parse(args)
	if *account == "" {
		log.Fatal("--account required")
	}
	a := openAdmin(*data, 0)
	defer a.Close()
	acc, err := a.ResolveAccount(*account)
	fatal(err)
	inv, err := a.Invite(acc.ID, *isAdmin)
	fatal(err)
	printInvite(acc, inv, *isAdmin, *url)
}

func cmdInviteSub(sub string, args []string) {
	fs := flag.NewFlagSet("invite "+sub, flag.ExitOnError)
	data := dataFlag(fs)
	account := fs.String("account", "", "filter by account id or name")
	hash := fs.String("hash", "", "invite code hash (from list)")
	fs.Parse(args)
	a := openAdmin(*data, 0)
	defer a.Close()
	switch sub {
	case "list":
		invs, err := a.Invites(*account)
		fatal(err)
		now := time.Now()
		for _, inv := range invs {
			role := "device"
			if inv.GrantAdmin {
				role = "admin"
			}
			fmt.Printf("%s  acc=%s  %-7s  %-6s  expires %s\n",
				inv.CodeHash, inv.AccountName, inv.State(now), role, fmtTime(inv.ExpiresAt))
		}
	case "cancel":
		if *hash == "" {
			log.Fatal("--hash required")
		}
		fatal(a.CancelInvite(*hash))
		fmt.Println("done")
	case "purge":
		n, err := a.PurgeInvites(24 * time.Hour)
		fatal(err)
		fmt.Printf("purged %d\n", n)
	}
}

func cmdDevice(args []string) {
	if len(args) < 1 {
		usage()
	}
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("device list", flag.ExitOnError)
		data := dataFlag(fs)
		account := fs.String("account", "", "filter by account id or name")
		fs.Parse(args[1:])
		a := openAdmin(*data, 0)
		defer a.Close()
		devs, err := a.Devices(*account)
		fatal(err)
		for _, d := range devs {
			state := "active"
			if !d.Active() {
				state = "revoked"
			}
			role := ""
			if d.IsAdmin {
				role = " admin"
			}
			fmt.Printf("%s  acc=%s  %s%s  ops=%d  seen=%s  %s  %s\n",
				d.ID, d.AccountName, state, role, d.Ops, fmtTime(d.LastSeen),
				d.CreatedAt.Format("2006-01-02"), d.Name)
		}
	case "revoke", "demote":
		fs := flag.NewFlagSet("device "+args[0], flag.ExitOnError)
		data := dataFlag(fs)
		id := fs.String("id", "", "device id")
		fs.Parse(args[1:])
		if *id == "" {
			log.Fatal("--id required")
		}
		a := openAdmin(*data, 0)
		defer a.Close()
		if args[0] == "revoke" {
			fatal(a.RevokeDevice(*id))
		} else {
			fatal(a.DemoteDevice(*id))
		}
		fmt.Println("done")
	default:
		usage()
	}
}

// printInvite — тот же блок для копирования, что показывает TUI.
func printInvite(acc store.Account, inv store.Invite, isAdmin bool, url string) {
	fmt.Println()
	for _, line := range admin.InviteText(acc, inv, isAdmin, url) {
		fmt.Println(line)
	}
	fmt.Println()
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// humanBytes — холодный путь, но без fmt: та же форма, что в TUI.
func humanBytes(n int64) string {
	if n <= 0 {
		return "-"
	}
	units := []string{"B", "K", "M", "G", "T"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d%s", n, units[i])
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
