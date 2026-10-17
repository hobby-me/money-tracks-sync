// Package tui — терминальный интерфейс оператора synk в духе k9s: шапка с
// состоянием сервера и хоткеями, таблица ресурсов, :-команды, модалки
// подтверждений. Все действия идут через internal/admin — тот же слой, что
// у CLI, поэтому новая операция появляется в обоих местах одним патчем.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/hobby-me/money-tracks-sync/internal/admin"
)

type Options struct {
	DataDir string
	Listen  string // адрес API сервера (healthz)
	Metrics string // адрес метрик (отдельный порт, только localhost)
	URL     string // публичный адрес для блока подключения (SYNK_PUBLIC_URL)
	Version string // версия бинаря TUI (в шапке)
	// QuotaBytes — глобальный потолок сервера (SYNK_ACCOUNT_QUOTA_MB): таблица
	// показывает действующую квоту учёток без своей. 0 — без лимита.
	QuotaBytes int64
}

// Run открывает данные и крутит интерфейс до выхода.
func Run(opts Options) error {
	adm, err := admin.Open(opts.DataDir)
	if err != nil {
		return err
	}
	adm.DefaultQuota = opts.QuotaBytes
	defer adm.Close()
	m := newModel(adm, opts)
	_, err = tea.NewProgram(m, tea.WithAltScreen()).Run()
	return err
}
