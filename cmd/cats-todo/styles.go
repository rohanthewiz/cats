package main

import "github.com/charmbracelet/lipgloss"

// Palette — a small, cohesive set of styles for a clean dark-terminal look,
// shared by the fuzzyList component and the manager views.
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#11111B")).
			Background(lipgloss.Color("#7AA2F7")).
			Padding(0, 1)

	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7AA2F7")).Bold(true)
	countStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))

	nameStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5E7EB"))
	nameSelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF")).Bold(true)
	descStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	matchStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F2A900")).Bold(true)
	barStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#7AA2F7")).Bold(true)
	footerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	headingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9D7CD8")).Bold(true)

	// Todo-specific accents.
	doneStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563")).Strikethrough(true)
	checkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ECE6A")).Bold(true)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F7768E"))
	okStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ECE6A"))
)
