package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// overlay — модальное окно поверх таблицы. update возвращает команду и флаг
// «закрыть». Пока модалка открыта, клавиши до таблицы не доходят.
type overlay interface {
	update(msg tea.KeyMsg, m *Model) (tea.Cmd, bool)
	render(m *Model, width int) string
}

func modalWidth(width, want int) int {
	max := width - 4
	if max < 30 {
		max = 30
	}
	if want > max {
		return max
	}
	return want
}

// ---- confirm: вопрос с двумя кнопками, danger подсвечивает необратимое ----

type confirmOverlay struct {
	title  string
	body   []string
	yes    string
	danger bool
	sel    int // 0 — да, 1 — нет
	onYes  func(m *Model) tea.Cmd
}

func (o *confirmOverlay) update(msg tea.KeyMsg, m *Model) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "n", "q":
		return nil, true
	case "y":
		return o.onYes(m), true
	case "left", "h", "tab", "right", "l", "shift+tab":
		o.sel = 1 - o.sel
	case "enter":
		if o.sel == 0 {
			return o.onYes(m), true
		}
		return nil, true
	}
	return nil, false
}

func (o *confirmOverlay) render(m *Model, width int) string {
	yes, no := sButton.Render(o.yes), sButton.Render("Отмена")
	if o.sel == 0 {
		if o.danger {
			yes = sButtonDng.Render(o.yes)
		} else {
			yes = sButtonSel.Render(o.yes)
		}
	} else {
		no = sButtonSel.Render("Отмена")
	}
	var b strings.Builder
	b.WriteString(sTitle.Render(o.title))
	b.WriteString("\n\n")
	for _, l := range o.body {
		b.WriteString(sValue.Render(l))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(yes + "  " + no)
	b.WriteString("\n\n")
	b.WriteString(sLabel.Render("y/enter — подтвердить · n/esc — отмена · tab — переключить"))
	box := sModal
	if o.danger {
		box = sModalDngr
	}
	return box.Width(modalWidth(width, 64)).Render(b.String())
}

// ---- input: одна строка текста ----

type inputOverlay struct {
	title    string
	hint     string
	in       textinput.Model
	onSubmit func(m *Model, value string) tea.Cmd
	// allowEmpty — пустой enter тоже отправляется (квота: пусто — глобальная).
	// По умолчанию пустой ввод игнорируется: имя учётки без текста бессмысленно.
	allowEmpty bool
}

func newInput(title, placeholder, hint string, onSubmit func(*Model, string) tea.Cmd) *inputOverlay {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = placeholder
	in.CharLimit = 64
	in.Width = 40
	in.Focus()
	return &inputOverlay{title: title, hint: hint, in: in, onSubmit: onSubmit}
}

func (o *inputOverlay) update(msg tea.KeyMsg, m *Model) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc":
		return nil, true
	case "enter":
		v := strings.TrimSpace(o.in.Value())
		if v == "" && !o.allowEmpty {
			return nil, false
		}
		return o.onSubmit(m, v), true
	}
	var cmd tea.Cmd
	o.in, cmd = o.in.Update(msg)
	return cmd, false
}

func (o *inputOverlay) render(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(o.title))
	b.WriteString("\n\n")
	b.WriteString(o.in.View())
	b.WriteString("\n\n")
	if o.hint != "" {
		b.WriteString(sLabel.Render(o.hint))
		b.WriteByte('\n')
	}
	b.WriteString(sLabel.Render("enter — ок · esc — отмена"))
	return sModal.Width(modalWidth(width, 60)).Render(b.String())
}

// ---- pick: выбор одного пункта из списка ----

type pickOverlay struct {
	title  string
	items  []string
	notes  []string // подпись к пункту, может быть nil
	cursor int
	onPick func(m *Model, i int) tea.Cmd
}

func (o *pickOverlay) update(msg tea.KeyMsg, m *Model) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "q":
		return nil, true
	case "up", "k":
		if o.cursor > 0 {
			o.cursor--
		}
	case "down", "j":
		if o.cursor < len(o.items)-1 {
			o.cursor++
		}
	case "enter":
		return o.onPick(m, o.cursor), true
	default:
		// Цифра — быстрый выбор пункта.
		if len(msg.String()) == 1 && msg.String()[0] >= '1' && msg.String()[0] <= '9' {
			i := int(msg.String()[0] - '1')
			if i < len(o.items) {
				return o.onPick(m, i), true
			}
		}
	}
	return nil, false
}

func (o *pickOverlay) render(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(o.title))
	b.WriteString("\n\n")
	for i, it := range o.items {
		num := sKey.Render("<" + string(rune('1'+i)) + ">")
		line := num + " " + it
		if i == o.cursor {
			line = num + " " + sRowSel.Render(" "+it+" ")
		}
		b.WriteString(line)
		if o.notes != nil && i < len(o.notes) && o.notes[i] != "" {
			b.WriteString("\n    " + sLabel.Render(o.notes[i]))
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(sLabel.Render("↑↓/цифра — выбрать · enter — ок · esc — отмена"))
	return sModal.Width(modalWidth(width, 70)).Render(b.String())
}

// ---- notice: информация для чтения (детали, помощь, код приглашения) ----

type noticeOverlay struct {
	title   string
	lines   []string
	code    string    // код приглашения — крупно, на плашке
	expires time.Time // для живого обратного отсчёта
	paste   []string  // блок для копирования: без рамки слева, с колонки 0
	wide    bool
}

// pasteable — модалка без боковых рамок, прижатая к левому краю: выделение
// строк мышью даёт ровно текст сообщения, без │ и ведущих пробелов.
func (o *noticeOverlay) pasteable() bool { return len(o.paste) > 0 }

func (o *noticeOverlay) update(msg tea.KeyMsg, m *Model) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "enter", "q", " ":
		return nil, true
	}
	return nil, false
}

func (o *noticeOverlay) render(m *Model, width int) string {
	var b strings.Builder
	b.WriteString(sTitle.Render(o.title))
	b.WriteString("\n\n")
	if o.code != "" {
		b.WriteString(sCode.Render(o.code))
		b.WriteString("\n")
		left := countdown(o.expires, m.now)
		if left == "0:00" {
			b.WriteString(sErr.Render("код истёк — выпустите новый"))
		} else {
			b.WriteString(sLabel.Render("истекает через ") + sWarn.Render(left) +
				sLabel.Render(" · одноразовый · в базе только хэш"))
		}
		b.WriteString("\n\n")
	}
	if o.pasteable() {
		b.WriteString(sLabel.Render("── скопируйте блок целиком и отправьте тому, кто подключает устройство ──"))
		b.WriteString("\n\n")
		for _, l := range o.paste {
			b.WriteString(sValue.Render(l))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	for _, l := range o.lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(sLabel.Render("esc — закрыть"))
	if o.pasteable() {
		return sPaste.Width(width).Render(b.String())
	}
	want := 72
	if o.wide {
		want = 100
	}
	return sModal.Width(modalWidth(width, want)).Render(b.String())
}

// kv — строка «метка  значение» для деталей.
func kv(label, value string) string {
	return sLabel.Render(padRight(label, 14)) + sValue.Render(value)
}

// centered размещает модалку по центру области таблицы.
func centered(width, height int, content string) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
}
