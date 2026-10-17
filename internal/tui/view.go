package tui

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const headerLines = 4

func (m *Model) View() string {
	if m.width == 0 || m.height == 0 {
		return "synk: загрузка…"
	}
	header := m.renderHeader()
	footer := m.renderFooter()
	bodyH := m.height - headerLines - 1
	if bodyH < 3 {
		bodyH = 3
	}
	var body string
	if n, ok := m.overlay.(*noticeOverlay); ok && n.pasteable() {
		// Блок для копирования — с нулевой колонки, чтобы выделение строк
		// мышью не захватывало отступы.
		body = lipgloss.Place(m.width, bodyH, lipgloss.Left, lipgloss.Center, n.render(m, m.width))
	} else if m.overlay != nil {
		body = centered(m.width, bodyH, m.overlay.render(m, m.width))
	} else if m.view == viewDashboard {
		body = m.renderDashboard(bodyH)
	} else {
		body = m.renderTable(bodyH)
	}
	return header + "\n" + body + "\n" + footer
}

// renderHeader — как у k9s: слева состояние, дальше колонки хоткеев.
// Колонки отваливаются справа налево, когда терминал узкий.
func (m *Model) renderHeader() string {
	info := m.infoBlock()
	blocks := []string{info}
	used := lipgloss.Width(info)
	for _, col := range [][]string{m.navHints(), m.actionHints(), m.generalHints()} {
		block := strings.Join(col, "\n")
		w := lipgloss.Width(block) + 4
		if used+w > m.width {
			break
		}
		blocks = append(blocks, lipgloss.NewStyle().PaddingLeft(4).Render(block))
		used += w
	}
	out := lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
	// Ровно headerLines строк, каждая обрезана по ширине.
	lines := strings.Split(out, "\n")
	for len(lines) < headerLines {
		lines = append(lines, "")
	}
	lines = lines[:headerLines]
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) infoBlock() string {
	h := m.health
	var server string
	switch {
	case h.CheckedAt.IsZero():
		server = sLabel.Render("● проверяю…")
	case h.Up:
		server = sOK.Render("● up")
		if h.Version != "" {
			server += " " + sValue.Render(h.Version)
		}
	default:
		server = sErr.Render("● down") + " " + sLabel.Render(fit(h.Err, 40))
	}
	var traffic string
	if h.Up {
		traffic = sLabel.Render("push ") + sValue.Render(groupIntZero(int64(h.Counter("synk_ops_received_total")))) +
			sLabel.Render("  pull ") + sValue.Render(groupIntZero(int64(h.Counter("synk_ops_served_total")))) +
			sLabel.Render("  photos ") + sValue.Render(groupIntZero(int64(h.Counter("synk_photos_put_total"))))
		if e := int64(h.Counter("synk_errors_total")); e > 0 {
			traffic += sLabel.Render("  errors ") + sErr.Render(groupIntZero(e))
		}
	} else {
		traffic = sLabel.Render("метрики недоступны")
	}
	active, revoked, pending := 0, 0, 0
	for _, a := range m.snap.accounts {
		active += a.DevicesActive
		revoked += a.DevicesRevoked
		pending += a.PendingInvites
	}
	totals := sLabel.Render("accounts ") + sValue.Render(groupIntZero(int64(len(m.snap.accounts)))) +
		sLabel.Render("  devices ") + sValue.Render(groupIntZero(int64(active)))
	if revoked > 0 {
		totals += sLabel.Render(" +" + groupIntZero(int64(revoked)) + " revoked")
	}
	if pending > 0 {
		totals += sLabel.Render("  invites ") + sWarn.Render(groupIntZero(int64(pending))+" pending")
	}
	lines := []string{
		sTitle.Render("synk") + " " + sValue.Render(m.version) + "  " + sLabel.Render(m.adm.DataDir),
		sLabel.Render("api ") + sValue.Render(m.listen) + sLabel.Render("  metrics ") + sValue.Render(m.metrics) + " " + server,
		traffic,
		totals,
	}
	w := 0
	for _, l := range lines {
		if lw := lipgloss.Width(l); lw > w {
			w = lw
		}
	}
	for i, l := range lines {
		lines[i] = padRight(l, w)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) navHints() []string {
	tab := func(v view, key, name string) string {
		if v == m.view {
			return sKey.Render("<"+key+">") + " " + sTitle.Render(name)
		}
		return hint(key, name)
	}
	return []string{
		tab(viewAccounts, "1", "учётки"),
		tab(viewDevices, "2", "устройства"),
		tab(viewInvites, "3", "коды"),
		tab(viewDashboard, "4", "дашборд"),
	}
}

func (m *Model) actionHints() []string {
	switch m.view {
	case viewAccounts:
		return []string{hint("enter", "устройства"), hint("i", "код приглашения"), hint("a", "новая учётка"), hint("Q", "квота"), hint("d", "детали")}
	case viewDevices:
		return []string{hint("x", "отозвать"), hint("m", "снять админа"), hint("i", "код приглашения"), hint("d", "детали")}
	case viewInvites:
		return []string{hint("x", "отменить код"), hint("i", "код приглашения"), hint("p", "чистка старых"), hint(":", "команда")}
	default:
		return []string{hint("D", "удалить 1y+"), hint("t", "очистить trash"), hint("d", "детали"), hint(":", "команда")}
	}
}

func (m *Model) generalHints() []string {
	return []string{hint("/", "фильтр"), hint("r", "обновить"), hint("?", "помощь"), hint("q", "выход")}
}

// frame — рамка на всю ширину с заголовком в верхней линии; body добивается
// или обрезается до height-2 строк.
func (m *Model) frame(title string, body []string, height int) string {
	return frameW(title, body, m.width, height)
}

func frameW(title string, body []string, width, height int) string {
	inner := width - 4
	if inner < 10 {
		inner = 10
	}
	top := sBorder.Render("┌─") + title
	rest := width - lipgloss.Width(top) - 1
	if rest < 0 {
		rest = 0
	}
	top += sBorder.Render(strings.Repeat("─", rest) + "┐")
	bottom := sBorder.Render("└" + strings.Repeat("─", width-2) + "┘")
	side := sBorder.Render("│")
	var b strings.Builder
	b.WriteString(top)
	b.WriteByte('\n')
	for i := 0; i < height-2; i++ {
		l := ""
		if i < len(body) {
			l = body[i]
		}
		b.WriteString(side + " " + padRight(fit(l, inner), inner) + " " + side)
		b.WriteByte('\n')
	}
	b.WriteString(bottom)
	return b.String()
}

// bar — [████░░░░] заполнение доли frac шириной width.
func bar(frac float64, width int) string {
	if width < 2 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	fill := int(frac*float64(width) + 0.5)
	return sBarFill.Render(strings.Repeat("█", fill)) + sBarEmpty.Render(strings.Repeat("░", width-fill))
}

// rate — скорость счётчика между двумя опросами, в единицах в секунду.
func (m *Model) rate(cur, prev float64) string {
	dt := m.health.CheckedAt.Sub(m.prevHealth.CheckedAt).Seconds()
	if !m.prevHealth.Up || dt <= 0 || cur < prev {
		return dash
	}
	r := (cur - prev) / dt
	if r == 0 {
		return "0"
	}
	if r < 10 {
		return strconv.FormatFloat(r, 'f', 2, 64)
	}
	return strconv.FormatFloat(r, 'f', 0, 64)
}

// renderDashboard — вкладка 4: три панели (диск · сервер · застой), запросы
// по устройствам, ниже таблица учёток по застою с действиями D/i/d.
func (m *Model) renderDashboard(height int) string {
	ov := m.snap.overview
	h := m.health
	panelH := 7
	colW := m.width / 3
	lastW := m.width - 2*colW

	// Диск.
	used := ov.Disk.UsedBytes()
	frac := 0.0
	if ov.Disk.TotalBytes > 0 {
		frac = float64(used) / float64(ov.Disk.TotalBytes)
	}
	dataFrac := 0.0
	if ov.Disk.TotalBytes > 0 {
		dataFrac = float64(ov.DataBytes) / float64(ov.Disk.TotalBytes)
	}
	disk := []string{
		sLabel.Render("данные synk   ") + sValue.Render(humanBytes(ov.DataBytes)) +
			sLabel.Render("  учётки ") + sValue.Render(humanBytes(ov.AccountsBytes())) +
			sLabel.Render("  trash ") + sValue.Render(humanBytes(ov.TrashBytes)),
		sLabel.Render("диск занято   ") + sValue.Render(humanBytes(used)) + sLabel.Render(" из ") +
			sValue.Render(humanBytes(ov.Disk.TotalBytes)) + sLabel.Render("  свободно ") + sOK.Render(humanBytes(ov.Disk.FreeBytes)),
		bar(frac, colW-6) + " " + sValue.Render(strconv.Itoa(int(frac*100+0.5))+"%"),
		sLabel.Render("доля synk на диске ") + sValue.Render(strconv.FormatFloat(dataFrac*100, 'f', 2, 64)+"%"),
		sLabel.Render("<t> очистить trash"),
	}

	// Сервер.
	var srv []string
	switch {
	case h.CheckedAt.IsZero():
		srv = []string{sLabel.Render("● проверяю…")}
	case !h.Up:
		srv = []string{sErr.Render("● down"), sLabel.Render(fit(h.Err, colW-6)),
			sLabel.Render("api " + m.listen), sLabel.Render("metrics " + m.metrics)}
	default:
		total := h.Counter("synk_requests_total")
		errs := 0.0
		for _, v := range h.ErrorsByRoute() {
			errs += v
		}
		srv = []string{
			sOK.Render("● up") + " " + sValue.Render(h.Version) + sLabel.Render("  uptime ") + sValue.Render(shortDur(h.Uptime(m.now))) +
				sLabel.Render("  rss ") + sValue.Render(humanBytes(h.RSSBytes)),
			sLabel.Render("rps ") + sValue.Render(m.rate(total, m.prevHealth.Counter("synk_requests_total"))) +
				sLabel.Render("  запросов ") + sValue.Render(groupIntZero(int64(total))) +
				sLabel.Render("  4xx/5xx ") + warnIf(errs > 0, groupIntZero(int64(errs))) +
				sLabel.Render("  internal ") + warnIf(h.Counter("synk_errors_total") > 0, groupIntZero(int64(h.Counter("synk_errors_total")))),
			sLabel.Render("push ") + sValue.Render(groupIntZero(int64(h.Counter("synk_ops_received_total")))) +
				sLabel.Render(" ops  pull ") + sValue.Render(groupIntZero(int64(h.Counter("synk_ops_served_total")))) +
				sLabel.Render(" ops  photos ") + sValue.Render(groupIntZero(int64(h.Counter("synk_photos_put_total")))),
			sLabel.Render("api " + m.listen + "  metrics " + m.metrics),
			sLabel.Render("те же серии заберёт Prometheus с порта метрик"),
		}
	}

	// Застой.
	idle := []string{
		sLabel.Render("живые (< 1 мес)  ") + sOK.Render(groupIntZero(int64(ov.IdleCounts[0]))),
		sLabel.Render("молчат 1 мес+    ") + sWarn.Render(groupIntZero(int64(ov.IdleCounts[1]))),
		sLabel.Render("молчат 6 мес+    ") + sHot.Render(groupIntZero(int64(ov.IdleCounts[2]))),
		sLabel.Render("молчат 1 год+    ") + sErr.Render(groupIntZero(int64(ov.IdleCounts[3]))) + sLabel.Render("  ← можно удалить"),
		sLabel.Render("<D> удалить выбранную (только 1y+)"),
	}

	row1 := lipgloss.JoinHorizontal(lipgloss.Top,
		frameW(sTitle.Render(" диск "), disk, colW, panelH),
		frameW(sTitle.Render(" сервер "), srv, colW, panelH),
		frameW(sTitle.Render(" застой учёток "), idle, lastW, panelH))

	// Запросы по устройствам.
	devLines := m.deviceRequestLines()
	devH := len(devLines) + 2
	if devH > 8 {
		devH = 8
	}
	if devH < 3 {
		devH = 3
	}
	row2 := m.frame(sTitle.Render(" запросы по устройствам ")+sLabel.Render(" rps · всего · 5xx "), devLines, devH)

	tableH := height - panelH - devH
	if tableH < 4 {
		tableH = 4
	}
	t := m.tables[viewDashboard]
	title := sTitle.Render(" учётки по застою[" + groupIntZero(int64(len(t.rows))) + "] ")
	if m.filter != "" {
		title += sWarn.Render("/" + m.filter + " ")
	}
	row3 := m.frame(title, t.render(m.width-4, tableH-2), tableH)
	return row1 + "\n" + row2 + "\n" + row3
}

func warnIf(cond bool, s string) string {
	if cond {
		return sErr.Render(s)
	}
	return sValue.Render(s)
}

func shortDur(d time.Duration) string {
	if d <= 0 {
		return dash
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	switch {
	case days > 0:
		return strconv.Itoa(days) + "d" + strconv.Itoa(hours) + "h"
	case hours > 0:
		return strconv.Itoa(hours) + "h" + strconv.Itoa(mins) + "m"
	default:
		return strconv.Itoa(mins) + "m"
	}
}

// deviceRequestLines — строки «устройство (учётка)  rps  всего  5xx»,
// по убыванию запросов; "-" — запросы до авторизации (register, битые токены).
func (m *Model) deviceRequestLines() []string {
	if !m.health.Up {
		return []string{sLabel.Render("сервер недоступен — нет данных")}
	}
	cur := m.health.RequestsByDevice()
	prev := m.prevHealth.RequestsByDevice()
	errs := m.health.ErrorsByDevice()
	if len(cur) == 0 {
		return []string{sLabel.Render("запросов ещё не было")}
	}
	names := map[string]string{}
	for _, d := range m.snap.devices {
		names[d.ID] = d.Name + " (" + d.AccountName + ")"
	}
	ids := make([]string, 0, len(cur))
	for id := range cur {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return cur[ids[i]] > cur[ids[j]] })
	nameW := 0
	for _, id := range ids {
		if w := lipgloss.Width(deviceLabel(id, names)); w > nameW {
			nameW = w
		}
	}
	if nameW > 40 {
		nameW = 40
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		e := errs[id]
		out = append(out, padRight(fit(deviceLabel(id, names), nameW), nameW)+"  "+
			padLeft(m.rate(cur[id], prev[id]), 6)+"  "+padLeft(groupIntZero(int64(cur[id])), 8)+"  "+
			warnIf(e > 0, padLeft(groupIntZero(int64(e)), 5)))
	}
	return out
}

func deviceLabel(id string, names map[string]string) string {
	if id == "-" {
		return sLabel.Render("без авторизации (register, битые токены)")
	}
	if n, ok := names[id]; ok {
		return n
	}
	return sLabel.Render(shortID(id) + " (удалено)")
}

// renderTable — рамка с заголовком «view(scope)[n] /filter», внутри таблица.
func (m *Model) renderTable(height int) string {
	title := m.view.String()
	if m.scopeID != "" && m.view != viewAccounts {
		title += "(" + m.scopeName + ")"
	}
	t := m.table()
	title += "[" + groupIntZero(int64(len(t.rows))) + "]"
	styled := sTitle.Render(" " + title + " ")
	if m.filter != "" {
		styled += sWarn.Render("/" + m.filter + " ")
	}
	return m.frame(styled, t.render(m.width-4, height-2), height)
}

// renderFooter — крошки слева, статус/время обновления справа. В режиме
// команды или фильтра строка целиком отдаётся вводу.
// promptLine — строка ввода команды/фильтра: плашка, ввод, подсказка справа,
// вся строка на подложке, чтобы её нельзя было не заметить.
func (m *Model) promptLine(badge, input, help string) string {
	line := badge + " " + sPromptIn.Render(input)
	rest := m.width - lipgloss.Width(line) - 2
	if rest > 10 {
		line += "  " + sLabel.Render(fit(help, rest))
	}
	return sPromptBg.Render(padRight(ansi.Truncate(line, m.width, ""), m.width))
}

func (m *Model) renderFooter() string {
	if m.cmdMode {
		return m.promptLine(sCrumb.Render("команда"), m.cmdIn.View(),
			"accounts · devices [учётка] · invites [учётка] · invite <учётка> [admin] · add <имя> · purge · q   ·   enter — выполнить, esc — отмена")
	}
	if m.filtering {
		return m.promptLine(sPromptWarn.Render("фильтр"), m.filterIn.View(),
			"подстрока по всем колонкам   ·   enter — оставить, esc — сбросить")
	}
	crumbs := sCrumb.Render("synk")
	if m.scopeID != "" && m.view != viewAccounts {
		crumbs += " " + sCrumbDim.Render("accounts") + " " + sCrumbDim.Render(m.view.String()+"("+m.scopeName+")")
	} else {
		crumbs += " " + sCrumbDim.Render(m.view.String())
	}
	var right string
	switch {
	case m.loadErr != nil:
		right = sErr.Render("ошибка чтения: " + fit(m.loadErr.Error(), 60))
	case m.status != "" && m.statusErr:
		right = sErr.Render(m.status)
	case m.status != "":
		right = sOK.Render(m.status)
	case m.loading:
		right = sLabel.Render("⟳ обновляю…")
	case !m.snap.at.IsZero():
		right = sLabel.Render("обновлено " + m.snap.at.Local().Format("15:04:05"))
	}
	gap := m.width - lipgloss.Width(crumbs) - lipgloss.Width(right)
	if gap < 1 {
		return ansi.Truncate(crumbs+" "+right, m.width, "")
	}
	return crumbs + strings.Repeat(" ", gap) + right
}
