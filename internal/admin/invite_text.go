package admin

import (
	"time"

	"github.com/hobby-me/money-tracks-sync/internal/store"
)

// InviteText — готовое сообщение для того, кто подключает устройство:
// всё, что нужно ввести в приложении, одним блоком. Одинаково печатается
// CLI и показывается в TUI — скопировал, вставил в мессенджер.
func InviteText(acc store.Account, inv store.Invite, admin bool, serverURL string) []string {
	role := "обычное устройство"
	if admin {
		role = "администратор"
	}
	if serverURL == "" {
		serverURL = "(адрес сервера не задан: SYNK_PUBLIC_URL или --url)"
	}
	return []string{
		"Money Tracks — подключение устройства",
		"Сервер:  " + serverURL,
		"Учётка:  " + acc.Name,
		"Код:     " + inv.Code,
		"Роль:    " + role,
		"Код одноразовый, действует до " + inv.ExpiresAt.Local().Format("15:04") +
			" (" + localZone(inv.ExpiresAt) + "), около 10 минут.",
		"В приложении: Настройки → Синхронизация → Подключить устройство.",
	}
}

func localZone(t time.Time) string {
	name, _ := t.Local().Zone()
	return name
}
