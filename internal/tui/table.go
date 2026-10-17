package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// column — колонка таблицы. flex — можно ужимать, когда не влезаем в ширину.
type column struct {
	title string
	flex  bool
	right bool
}

// row — строка с идентификатором, по которому выполняются действия.
// Ячейки могут нести свой цвет; dim перекрашивает всю строку в серый.
type row struct {
	id    string
	cells []string
	dim   bool
}

// table — своя минимальная таблица: заголовок капсом, курсор на всю ширину,
// автоширина колонок. bubbles/table здесь дал бы меньше контроля над
// раскладкой, чем нужно для k9s-подобного вида.
type table struct {
	cols   []column
	rows   []row
	cursor int
	offset int
	empty  string
}

const minFlexWidth = 6
const colGap = 2

// setRows подменяет данные, стараясь удержать курсор на той же записи.
func (t *table) setRows(rows []row) {
	prev := ""
	if t.cursor >= 0 && t.cursor < len(t.rows) {
		prev = t.rows[t.cursor].id
	}
	t.rows = rows
	if prev != "" {
		for i, r := range rows {
			if r.id == prev {
				t.cursor = i
				break
			}
		}
	}
	if t.cursor >= len(rows) {
		t.cursor = len(rows) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

func (t *table) selected() *row {
	if t.cursor < 0 || t.cursor >= len(t.rows) {
		return nil
	}
	return &t.rows[t.cursor]
}

func (t *table) move(delta int) {
	t.cursor += delta
	if t.cursor >= len(t.rows) {
		t.cursor = len(t.rows) - 1
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
}

func (t *table) first() { t.cursor = 0 }
func (t *table) last()  { t.move(len(t.rows)) }

// widths считает натуральную ширину колонок и ужимает flex-колонки, пока
// таблица не влезет. Ужимается всегда самая широкая — ID страдает первым.
func (t *table) widths(width int) []int {
	n := len(t.cols)
	w := make([]int, n)
	for i, c := range t.cols {
		w[i] = ansi.StringWidth(c.title)
	}
	for _, r := range t.rows {
		for i, cell := range r.cells {
			if i >= n {
				break
			}
			if cw := ansi.StringWidth(cell); cw > w[i] {
				w[i] = cw
			}
		}
	}
	total := colGap * (n - 1)
	for _, x := range w {
		total += x
	}
	over := total - width
	// Сначала ужимаем гибкие колонки до minFlexWidth, потом — любые до 3:
	// в узком терминале лучше «…» в ячейках, чем строка за краем экрана.
	for _, pass := range []struct {
		flexOnly bool
		min      int
	}{{true, minFlexWidth}, {false, 3}} {
		for over > 0 {
			widest := -1
			for i, c := range t.cols {
				if (c.flex || !pass.flexOnly) && w[i] > pass.min && (widest < 0 || w[i] > w[widest]) {
					widest = i
				}
			}
			if widest < 0 {
				break
			}
			w[widest]--
			over--
		}
	}
	return w
}

// render возвращает ровно height строк шириной width: заголовок + видимое
// окно строк, добитое пустотой.
func (t *table) render(width, height int) []string {
	if height < 1 {
		return nil
	}
	w := t.widths(width)
	out := make([]string, 0, height)

	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = strings.ToUpper(c.title)
	}
	out = append(out, sHead.Render(padRight(fit(t.line(titles, w), width), width)))

	visible := height - 1
	if visible < 1 {
		return out
	}
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+visible {
		t.offset = t.cursor - visible + 1
	}
	if t.offset < 0 {
		t.offset = 0
	}
	if len(t.rows) == 0 {
		msg := t.empty
		if msg == "" {
			msg = "пусто"
		}
		out = append(out, sRowDim.Render(padRight("  "+msg, width)))
	}
	for i := t.offset; i < len(t.rows) && i < t.offset+visible; i++ {
		r := t.rows[i]
		cells := r.cells
		if i == t.cursor || r.dim {
			cells = stripAll(cells)
		}
		line := padRight(fit(t.line(cells, w), width), width)
		switch {
		case i == t.cursor:
			line = sRowSel.Render(line)
		case r.dim:
			line = sRowDim.Render(line)
		}
		out = append(out, line)
	}
	for len(out) < height {
		out = append(out, strings.Repeat(" ", width))
	}
	return out
}

func (t *table) line(cells []string, w []int) string {
	var b strings.Builder
	for i, c := range t.cols {
		if i > 0 {
			b.WriteString(strings.Repeat(" ", colGap))
		}
		cell := ""
		if i < len(cells) {
			cell = fit(cells[i], w[i])
		}
		if c.right {
			b.WriteString(padLeft(cell, w[i]))
		} else {
			b.WriteString(padRight(cell, w[i]))
		}
	}
	return b.String()
}

func stripAll(cells []string) []string {
	out := make([]string, len(cells))
	for i, c := range cells {
		out[i] = ansi.Strip(c)
	}
	return out
}

// matches — фильтр подстрокой по всем ячейкам без учёта регистра и цвета.
func (r row) matches(q string) bool {
	if q == "" {
		return true
	}
	for _, c := range r.cells {
		if strings.Contains(strings.ToLower(ansi.Strip(c)), q) {
			return true
		}
	}
	return false
}
