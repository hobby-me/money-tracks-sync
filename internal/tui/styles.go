package tui

import "github.com/charmbracelet/lipgloss"

// Палитра — ANSI-256, чтобы одинаково читалась в любом ssh-терминале.
// Оранжевые хоткеи и синяя рамка — как у k9s: глаз оператора уже обучен.
var (
	colKey    = lipgloss.Color("208")
	colTitle  = lipgloss.Color("45")
	colBorder = lipgloss.Color("31")
	colHead   = lipgloss.Color("75")
	colDim    = lipgloss.Color("243")
	colOK     = lipgloss.Color("42")
	colErr    = lipgloss.Color("203")
	colWarn   = lipgloss.Color("214")
	colText   = lipgloss.Color("252")
	colSelBg  = lipgloss.Color("24")
	colSelFg  = lipgloss.Color("255")
	colInk    = lipgloss.Color("232")
	colPlate  = lipgloss.Color("238")

	sKey        = lipgloss.NewStyle().Foreground(colKey).Bold(true)
	sLabel      = lipgloss.NewStyle().Foreground(colDim)
	sValue      = lipgloss.NewStyle().Foreground(colText)
	sTitle      = lipgloss.NewStyle().Foreground(colTitle).Bold(true)
	sBorder     = lipgloss.NewStyle().Foreground(colBorder)
	sHead       = lipgloss.NewStyle().Foreground(colHead).Bold(true)
	sRowDim     = lipgloss.NewStyle().Foreground(colDim)
	sRowSel     = lipgloss.NewStyle().Foreground(colSelFg).Background(colSelBg).Bold(true)
	sOK         = lipgloss.NewStyle().Foreground(colOK)
	sErr        = lipgloss.NewStyle().Foreground(colErr)
	sWarn       = lipgloss.NewStyle().Foreground(colWarn)
	sHot        = lipgloss.NewStyle().Foreground(colKey)
	sBarFill    = lipgloss.NewStyle().Foreground(colHead)
	sBarEmpty   = lipgloss.NewStyle().Foreground(colPlate)
	sCrumb      = lipgloss.NewStyle().Foreground(colInk).Background(colKey).Padding(0, 1)
	sCrumbDim   = lipgloss.NewStyle().Foreground(colText).Background(colPlate).Padding(0, 1)
	sModal      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colKey).Padding(0, 2)
	sModalDngr  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colErr).Padding(0, 2)
	sButton     = lipgloss.NewStyle().Foreground(colText).Background(colPlate).Padding(0, 2)
	sButtonSel  = lipgloss.NewStyle().Foreground(colInk).Background(colKey).Bold(true).Padding(0, 2)
	sButtonDng  = lipgloss.NewStyle().Foreground(colInk).Background(colErr).Bold(true).Padding(0, 2)
	sPromptBg   = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	sPromptIn   = lipgloss.NewStyle().Foreground(colSelFg).Background(lipgloss.Color("236")).Bold(true)
	sPromptWarn = lipgloss.NewStyle().Foreground(colInk).Background(colWarn).Padding(0, 1)
	sPaste      = lipgloss.NewStyle().Border(lipgloss.NormalBorder(), true, false).BorderForeground(colKey)
	sCode       = lipgloss.NewStyle().Foreground(colInk).Background(colWarn).Bold(true).Padding(0, 2)
)

// hint — «<x> действие» для шапки и модалок.
func hint(key, text string) string {
	return sKey.Render("<"+key+">") + " " + sValue.Render(text)
}
