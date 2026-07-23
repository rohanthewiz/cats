package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/rohanthewiz/cats/internal/app"
)

// uiStage is which screen the manager is currently showing.
type uiStage int

const (
	stageList    uiStage = iota // the project+global todo list
	stageForm                   // add / edit a prompt
	stageConfirm                // confirm a delete / clear-completed
	stageTarget                 // pick where to drop the chosen prompt
	stageView                   // read-only view of a prompt's full body
)

// confirmKind distinguishes what the confirm stage is about to do.
type confirmKind int

const (
	confirmDelete    confirmKind = iota // delete the selected todo
	confirmClearDone                    // remove every done todo in both scopes
)

// formMode distinguishes adding a new todo from editing an existing one.
type formMode int

const (
	formAdd formMode = iota
	formEdit
)

// dropTargetKind is the two ways a prompt can land: into a brand-new Claude Code
// session, or into an already-running agent pane.
type dropTargetKind int

const (
	targetNewSession dropTargetKind = iota
	targetExistingPane
)

// dropTarget is one selectable destination in the target picker.
type dropTarget struct {
	kind    dropTargetKind
	pane    uint32 // for targetExistingPane
	agent   string // detected agent label, for existing panes
	command string // launch command, for targetNewSession ("claude", "codex", …)
	label   string
	desc    string
}

// dropMode is the per-drop submit choice.
type dropMode int

const (
	dropPaste dropMode = iota // type the prompt but don't press Enter
	dropRun                   // type the prompt and submit it
)

// todoRef identifies a todo by its scope and id, so the list can map a row back
// to the right store.
type todoRef struct {
	scope scope
	id    string
}

// pendingAction is the drop the user chose. The manager performs it without
// quitting — off the UI thread (see performDropCmd) — so the pane persists and
// you can drop more prompts in the same session.
type pendingAction struct {
	todo   Todo
	target dropTarget
	mode   dropMode
}

// dropResultMsg reports the outcome of an asynchronous drop back to the Update
// loop, so the manager can clear its "dropping…" state and show where the prompt
// landed (or why it failed) while staying open.
type dropResultMsg struct {
	desc     string  // human description of the destination, for the status line
	ref      todoRef // the dropped todo, so a successful run can auto-mark it done
	markDone bool    // mark ref done on success (a "run" drop starts the work)
	err      error
}

// model is the Bubble Tea state for the whole manager: a small stage machine
// over the list, the add/edit form, the delete confirm, and the target picker.
type model struct {
	ctx     RunContext
	project *store
	global  *store
	client  *catsClient // nil when the cats control socket is unavailable

	stage uiStage

	// List stage.
	list     fuzzyList
	rows     []todoRef // selectable row index -> todo
	hideDone bool      // fold completed todos out of the list

	// Form stage.
	formMode   formMode
	formScope  scope
	editID     string
	titleInput textinput.Model
	promptArea textarea.Model
	formFocus  int // 0 = title, 1 = prompt
	formErr    string

	// Confirm stage.
	confirmKind       confirmKind
	pendingDelete     todoRef
	pendingTitle      string
	pendingClearCount int

	// Target stage.
	dropTodo   todoRef
	targets    []dropTarget
	targetList fuzzyList

	// View stage.
	viewRef todoRef
	viewVP  viewport.Model

	width, height int

	status    string // transient message under the list
	statusErr bool

	dropping bool // a drop is in flight (off the UI thread); guards re-entry
	quitting bool
}

// newModel builds the initial manager state showing the todo list.
func newModel(ctx RunContext, project, global *store, client *catsClient) model {
	m := model{ctx: ctx, project: project, global: global, client: client, stage: stageList}
	m.list = newFuzzyList("Type to filter prompts…", nil)
	m.rebuildList()
	return m
}

// Init starts the cursor blinking.
func (m model) Init() tea.Cmd { return textinput.Blink }

// Update routes by stage; non-key messages flow to whatever input is active so
// cursors keep blinking and text keeps flowing.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.applySizes()
		return m, nil
	case dropResultMsg:
		m.dropping = false
		if msg.err != nil {
			m.setStatus("drop failed: "+msg.err.Error(), true)
			return m, nil
		}
		status := "dropped → " + msg.desc
		// A "run" drop starts the work, so close the todo out automatically. A
		// "paste" drop leaves it unsubmitted for the user to review, so it stays
		// open. setDone is idempotent and best effort — a save failure shouldn't
		// undo a successful drop, so we just skip the "marked done" note.
		if msg.markDone {
			if err := m.storeFor(msg.ref.scope).setDone(msg.ref.id, true); err == nil {
				status += " · marked done"
				m.rebuildList()
			}
		}
		m.setStatus(status, false)
		return m, nil
	case tea.KeyMsg:
		switch m.stage {
		case stageList:
			return m.updateList(msg)
		case stageForm:
			return m.updateForm(msg)
		case stageConfirm:
			return m.updateConfirm(msg)
		case stageTarget:
			return m.updateTarget(msg)
		case stageView:
			return m.updateView(msg)
		}
	}
	return m.forward(msg)
}

// forward passes a non-key message to the active input.
func (m model) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.stage {
	case stageList:
		cmd = m.list.editQuery(msg)
	case stageTarget:
		cmd = m.targetList.editQuery(msg)
	case stageForm:
		return m.forwardForm(msg)
	}
	return m, cmd
}

// --- List stage ---------------------------------------------------------------

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		// Clear an active filter first; quit only when there's nothing to clear.
		if strings.TrimSpace(m.list.input.Value()) != "" {
			m.list.input.SetValue("")
			m.list.filter()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "up", "ctrl+p":
		m.list.moveUp()
		return m, nil
	case "down", "ctrl+n":
		m.list.moveDown()
		return m, nil
	case "enter":
		return m.beginDrop()
	case "ctrl+a":
		return m.beginAdd()
	case "ctrl+e":
		return m.beginEdit()
	case "ctrl+x":
		return m.beginDelete()
	case "ctrl+t":
		return m.toggleSelected()
	case "ctrl+v":
		return m.beginView()
	case "ctrl+d":
		m.hideDone = !m.hideDone
		m.rebuildList()
		if m.hideDone {
			m.setStatus("hiding completed prompts", false)
		} else {
			m.setStatus("showing completed prompts", false)
		}
		return m, nil
	case "ctrl+w":
		return m.beginClearDone()
	case "ctrl+up":
		return m.moveSelected(-1)
	case "ctrl+down":
		return m.moveSelected(1)
	}
	cmd := m.list.editQuery(msg)
	return m, cmd
}

// selectedRef returns the highlighted todo's ref, and whether one is selected.
func (m model) selectedRef() (todoRef, bool) {
	idx := m.list.selectedIndex()
	if idx < 0 || idx >= len(m.rows) {
		return todoRef{}, false
	}
	return m.rows[idx], true
}

func (m *model) storeFor(s scope) *store {
	if s == scopeProject {
		return m.project
	}
	return m.global
}

func (m model) resolve(ref todoRef) (Todo, bool) {
	return m.storeFor(ref.scope).find(ref.id)
}

func (m model) toggleSelected() (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	td, _ := m.resolve(ref)
	if err := m.storeFor(ref.scope).toggle(ref.id); err != nil {
		m.setStatus("save failed: "+err.Error(), true)
		return m, nil
	}
	if td.Done {
		m.setStatus("reopened", false)
	} else {
		m.setStatus("marked done", false)
	}
	m.rebuildList()
	return m, nil
}

func (m *model) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

// rebuildList regenerates the list rows from the two stores. Project todos come
// first; within each scope open todos come before done ones (array order is the
// backlog's priority order), and done todos disappear entirely when hideDone is
// set. The "Project"/"Global" group headings only show when both groups have at
// least one visible todo.
func (m *model) rebuildList() {
	var items []listItem
	var rows []todoRef

	visible := func(s *store) int {
		n := 0
		for _, t := range s.todos {
			if !t.Done || !m.hideDone {
				n++
			}
		}
		return n
	}
	grouped := visible(m.project) > 0 && visible(m.global) > 0

	add := func(s *store) {
		appendTodo := func(t Todo) {
			ref := todoRef{scope: s.scope, id: t.ID}
			badge := "○"
			if t.Done {
				badge = checkStyle.Render("✓")
			}
			name := t.Title
			if name == "" {
				name = firstLine(t.Prompt, 60)
			}
			items = append(items, listItem{
				name: name,
				desc: firstLine(t.Prompt, 70),
				// Match against the whole prompt (flattened to one line), not
				// just the rendered first-line preview, so a filter can hit
				// text buried deep in a multi-line prompt.
				search:     strings.Join(strings.Fields(t.Title+" "+t.Prompt), " "),
				badge:      badge,
				strike:     t.Done,
				selectable: true,
				ref:        len(rows),
			})
			rows = append(rows, ref)
		}
		for _, t := range s.todos {
			if !t.Done {
				appendTodo(t)
			}
		}
		if m.hideDone {
			return
		}
		for _, t := range s.todos {
			if t.Done {
				appendTodo(t)
			}
		}
	}

	if grouped {
		items = append(items, listItem{name: "Project"})
	}
	add(m.project)
	if grouped {
		items = append(items, listItem{name: "Global"})
	}
	add(m.global)

	m.rows = rows
	// Swap in the new rows while keeping the existing query box and cursor
	// (setItems re-filters and clamps), so an add/edit/toggle doesn't disturb
	// what the user has typed or where they were.
	m.list.setItems(items)
}

// hiddenDoneCount is how many completed todos the hideDone fold is holding back,
// for the header note.
func (m model) hiddenDoneCount() int {
	n := 0
	for _, s := range []*store{m.project, m.global} {
		for _, t := range s.todos {
			if t.Done {
				n++
			}
		}
	}
	return n
}

// moveSelected shifts the highlighted todo one step up or down within its scope
// and done-state group, then keeps the highlight on it.
func (m model) moveSelected(delta int) (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	if err := m.storeFor(ref.scope).move(ref.id, delta); err != nil {
		m.setStatus("move failed: "+err.Error(), true)
		return m, nil
	}
	m.rebuildList()
	m.selectRow(ref)
	return m, nil
}

// selectRow parks the list cursor on the row showing ref, so a reorder or
// rebuild keeps the highlight on the same todo.
func (m *model) selectRow(ref todoRef) {
	for i, r := range m.rows {
		if r == ref {
			m.list.selectRef(i)
			return
		}
	}
}

// --- Add / Edit form ----------------------------------------------------------

func (m model) beginAdd() (tea.Model, tea.Cmd) {
	m.formMode = formAdd
	// Default to the project backlog when launched inside a project, else global.
	if m.project.available() {
		m.formScope = scopeProject
	} else {
		m.formScope = scopeGlobal
	}
	m.editID = ""
	m.titleInput, m.promptArea = m.newFormInputs("", "")
	m.formFocus = 1 // start in the prompt — that's the point of an entry
	m.titleInput.Blur()
	cmd := m.promptArea.Focus()
	m.formErr = ""
	m.stage = stageForm
	return m, cmd
}

func (m model) beginEdit() (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	return m.beginEditRef(ref)
}

// beginEditRef opens the edit form for a specific todo — from the list's
// highlight or from the view stage.
func (m model) beginEditRef(ref todoRef) (tea.Model, tea.Cmd) {
	td, ok := m.resolve(ref)
	if !ok {
		return m, nil
	}
	m.formMode = formEdit
	m.formScope = ref.scope
	m.editID = ref.id
	m.titleInput, m.promptArea = m.newFormInputs(td.Title, td.Prompt)
	m.formFocus = 1
	m.titleInput.Blur()
	cmd := m.promptArea.Focus()
	m.formErr = ""
	m.stage = stageForm
	return m, cmd
}

// newFormInputs builds the title field and prompt editor, sized to the screen
// and pre-filled with the given values.
func (m model) newFormInputs(title, prompt string) (textinput.Model, textarea.Model) {
	ti := textinput.New()
	ti.Placeholder = "Short title (optional — derived from the prompt if blank)"
	ti.Prompt = ""
	ti.CharLimit = 140
	ti.SetValue(title)

	ta := textarea.New()
	ta.Placeholder = "The prompt to hand Claude Code later…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetValue(prompt)

	w := m.width - 4
	if w < 20 {
		w = 60
	}
	ti.Width = w
	ta.SetWidth(w)
	h := m.height - 12
	if h < 4 {
		h = 8
	}
	ta.SetHeight(h)
	return ti, ta
}

func (m model) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.stage = stageList
		m.formErr = ""
		return m, nil
	case "tab", "shift+tab":
		return m.toggleFormFocus()
	case "ctrl+s":
		return m.saveForm()
	case "ctrl+g":
		if m.formMode == formAdd && m.project.available() {
			if m.formScope == scopeProject {
				m.formScope = scopeGlobal
			} else {
				m.formScope = scopeProject
			}
		}
		return m, nil
	}
	return m.forwardForm(msg)
}

func (m model) toggleFormFocus() (tea.Model, tea.Cmd) {
	if m.formFocus == 0 {
		m.formFocus = 1
		m.titleInput.Blur()
		return m, m.promptArea.Focus()
	}
	m.formFocus = 0
	m.promptArea.Blur()
	return m, m.titleInput.Focus()
}

func (m model) forwardForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.formFocus == 0 {
		m.titleInput, cmd = m.titleInput.Update(msg)
	} else {
		m.promptArea, cmd = m.promptArea.Update(msg)
	}
	return m, cmd
}

func (m model) saveForm() (tea.Model, tea.Cmd) {
	title := strings.TrimSpace(m.titleInput.Value())
	prompt := strings.TrimSpace(m.promptArea.Value())
	if prompt == "" {
		m.formErr = "The prompt can't be empty."
		return m, nil
	}
	if title == "" {
		title = firstLine(prompt, 60)
	}

	st := m.storeFor(m.formScope)
	if m.formMode == formAdd {
		err := st.add(Todo{ID: newID(), Title: title, Prompt: prompt, Created: time.Now()})
		if err != nil {
			m.formErr = "save failed: " + err.Error()
			return m, nil
		}
		m.setStatus("added to "+m.formScope.String()+" backlog", false)
	} else {
		err := st.update(Todo{ID: m.editID, Title: title, Prompt: prompt})
		if err != nil {
			m.formErr = "save failed: " + err.Error()
			return m, nil
		}
		m.setStatus("updated", false)
	}
	m.rebuildList()
	m.stage = stageList
	return m, nil
}

// --- Delete confirm -----------------------------------------------------------

func (m model) beginDelete() (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	td, _ := m.resolve(ref)
	m.confirmKind = confirmDelete
	m.pendingDelete = ref
	m.pendingTitle = firstNonEmpty(td.Title, firstLine(td.Prompt, 40))
	m.stage = stageConfirm
	return m, nil
}

// beginClearDone arms the confirm stage to remove every completed todo across
// both scopes, or reports there is nothing to clear.
func (m model) beginClearDone() (tea.Model, tea.Cmd) {
	count := m.hiddenDoneCount()
	if count == 0 {
		m.setStatus("no completed prompts to clear", false)
		return m, nil
	}
	m.confirmKind = confirmClearDone
	m.pendingClearCount = count
	m.stage = stageConfirm
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "y", "Y", "enter":
		if m.confirmKind == confirmClearDone {
			m.clearDone()
		} else {
			if err := m.storeFor(m.pendingDelete.scope).delete(m.pendingDelete.id); err != nil {
				m.setStatus("delete failed: "+err.Error(), true)
			} else {
				m.setStatus("deleted", false)
			}
		}
		m.rebuildList()
		m.stage = stageList
		return m, nil
	case "n", "N", "esc":
		m.stage = stageList
		return m, nil
	}
	return m, nil
}

// clearDone removes every completed todo from both stores and reports the total
// in the status line. A failure in one store doesn't stop the other; the first
// error wins the status.
func (m *model) clearDone() {
	removed := 0
	var firstErr error
	for _, s := range []*store{m.project, m.global} {
		n, err := s.clearDone()
		removed += n
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	switch {
	case firstErr != nil:
		m.setStatus("clear failed: "+firstErr.Error(), true)
	case removed == 1:
		m.setStatus("cleared 1 completed prompt", false)
	default:
		m.setStatus(fmt.Sprintf("cleared %d completed prompts", removed), false)
	}
}

// --- Target picker ------------------------------------------------------------

func (m model) beginDrop() (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	return m.startDrop(ref)
}

// startDrop opens the target picker for a specific todo — from the list's
// highlight or from the view stage. Guard failures land back on the list, where
// the status line is visible.
func (m model) startDrop(ref todoRef) (tea.Model, tea.Cmd) {
	if m.dropping {
		m.setStatus("a drop is still in progress…", false)
		m.stage = stageList
		return m, nil
	}
	if m.client == nil {
		m.setStatus("cats control socket unavailable — can't drop into a session", true)
		m.stage = stageList
		return m, nil
	}
	m.dropTodo = ref
	m.targets, m.targetList = m.buildTargets()
	m.stage = stageTarget
	return m, textinput.Blink
}

// buildTargets assembles the drop destinations: a new Claude session, a new
// session for every other agent currently running somewhere (if it's running,
// its command is installed), plus every agent pane cats reports (Claude panes
// first), excluding our own. Pane agent identity and cwd come straight from
// pane.list's runtime metadata; the workspace a pane lives in is the "w1"
// prefix of its handle, labeled via workspace.list.
func (m model) buildTargets() ([]dropTarget, fuzzyList) {
	wsLabel := firstNonEmpty(m.ctx.WorkspaceLabel, baseName(m.ctx.WorkDir), "the current workspace")
	targets := []dropTarget{{
		kind:    targetNewSession,
		command: "claude",
		label:   "＋ New Claude Code session",
		desc:    "open a new tab in " + wsLabel + " and launch claude",
	}}

	if m.client != nil {
		if panes, err := m.client.paneList(); err == nil {
			var agents []app.PaneInfo
			for _, p := range panes {
				if p.Agent == "" || isOwnPane(m.ctx, p) {
					continue
				}
				agents = append(agents, p)
			}
			sort.SliceStable(agents, func(i, j int) bool {
				return agents[i].Agent == "claude" && agents[j].Agent != "claude"
			})

			// One "new session" entry per distinct non-claude agent, launched
			// with the agent label cats detected as the command.
			seenAgent := map[string]bool{"claude": true}
			for _, p := range agents {
				if seenAgent[p.Agent] {
					continue
				}
				seenAgent[p.Agent] = true
				targets = append(targets, dropTarget{
					kind:    targetNewSession,
					command: p.Agent,
					label:   "＋ New " + p.Agent + " session",
					desc:    "open a new tab in " + wsLabel + " and launch " + p.Agent,
				})
			}

			wsLabels := map[string]string{}
			if labels, err := m.client.workspaceLabels(); err == nil {
				wsLabels = labels
			}
			for _, p := range agents {
				wsID := paneWorkspaceID(p)
				loc := firstNonEmpty(wsLabels[wsID], baseName(p.Cwd))
				here := ""
				if wsID != "" && wsID == m.ctx.WorkspaceID {
					here = " (this project)"
				}
				// Surface the agent's live state ("working", "idle", …) with its
				// location — picking an idle session over a busy one is usually
				// the point of the choice.
				desc := p.Cwd
				if p.AgentState != "" {
					desc = "[" + p.AgentState + "] " + desc
				}
				targets = append(targets, dropTarget{
					kind:  targetExistingPane,
					pane:  p.Pane,
					agent: p.Agent,
					label: fmt.Sprintf("%s · %s%s", p.Agent, firstNonEmpty(loc, "session"), here),
					desc:  desc,
				})
			}
		}
	}

	items := make([]listItem, len(targets))
	for i, t := range targets {
		items[i] = listItem{name: t.label, desc: t.desc, selectable: true, ref: i}
	}
	return targets, newFuzzyList("Filter targets…", items)
}

func (m model) updateTarget(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc":
		m.stage = stageList
		return m, nil
	case "up", "ctrl+p":
		m.targetList.moveUp()
		return m, nil
	case "down", "ctrl+n":
		m.targetList.moveDown()
		return m, nil
	case "enter":
		return m.chooseTarget(dropPaste)
	case "ctrl+r":
		return m.chooseTarget(dropRun)
	}
	cmd := m.targetList.editQuery(msg)
	return m, cmd
}

func (m model) chooseTarget(mode dropMode) (tea.Model, tea.Cmd) {
	if m.dropping {
		return m, nil
	}
	idx := m.targetList.selectedIndex()
	if idx < 0 || idx >= len(m.targets) {
		return m, nil
	}
	td, ok := m.resolve(m.dropTodo)
	if !ok {
		m.setStatus("could not find that prompt", true)
		m.stage = stageList
		return m, nil
	}
	target := m.targets[idx]
	// Perform the drop without quitting: the pane persists, so we return to the
	// list and run the (potentially slow) drop off the UI thread, reporting back
	// via dropResultMsg. A drop into a new session focuses the freshly-created
	// tab, leaving this manager alive in the background to reuse later.
	m.dropping = true
	m.stage = stageList
	m.setStatus("dropping into "+targetDesc(target)+"…", false)
	return m, m.performDropCmd(m.dropTodo, pendingAction{todo: td, target: target, mode: mode})
}

// performDropCmd runs the chosen drop in a goroutine (a tea.Cmd) so cats's
// pane creation and claude launch — which can take several seconds — don't
// freeze the manager. It reports the destination and any error back as a
// dropResultMsg. The client is captured by value; performDrop only makes
// short-lived, independent socket calls, so this is safe to run alongside the
// still-rendering UI.
func (m model) performDropCmd(ref todoRef, act pendingAction) tea.Cmd {
	client := m.client
	desc := targetDesc(act.target)
	markDone := act.mode == dropRun
	return func() tea.Msg {
		return dropResultMsg{desc: desc, ref: ref, markDone: markDone, err: performDrop(client, act)}
	}
}

// targetDesc is a short, human label for a drop destination, used in the
// "dropping…" / "dropped →" status lines.
func targetDesc(t dropTarget) string {
	if t.kind == targetNewSession {
		if t.command != "" && t.command != "claude" {
			return "new " + t.command + " session"
		}
		return "new Claude Code session"
	}
	return firstNonEmpty(t.agent, "session")
}

// --- Prompt view ----------------------------------------------------------------

// beginView opens the read-only view of the highlighted todo's full prompt —
// the way to read a long prompt without entering the edit form.
func (m model) beginView() (tea.Model, tea.Cmd) {
	ref, ok := m.selectedRef()
	if !ok {
		return m, nil
	}
	td, ok := m.resolve(ref)
	if !ok {
		return m, nil
	}
	m.viewRef = ref
	m.viewVP = viewport.New(m.viewWidth(), m.viewHeight())
	m.viewVP.SetContent(m.viewContent(td))
	m.stage = stageView
	return m, nil
}

func (m model) updateView(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "esc", "q":
		m.stage = stageList
		return m, nil
	case "enter":
		return m.startDrop(m.viewRef)
	case "ctrl+e":
		return m.beginEditRef(m.viewRef)
	}
	// Everything else (arrows, pgup/pgdn, mouse wheel) scrolls the body.
	var cmd tea.Cmd
	m.viewVP, cmd = m.viewVP.Update(msg)
	return m, cmd
}

// viewWidth and viewHeight size the view's scrollable body to the window,
// leaving room for the heading, meta line, and footer.
func (m model) viewWidth() int {
	if w := m.width - 4; w >= 20 {
		return w
	}
	return 76
}

func (m model) viewHeight() int {
	if h := m.height - 8; h >= 4 {
		return h
	}
	return 16
}

// viewContent renders the todo's full prompt wrapped to the view's width.
func (m model) viewContent(td Todo) string {
	return lipgloss.NewStyle().Width(m.viewWidth()).Render(td.Prompt)
}

// --- View ---------------------------------------------------------------------

func (m model) View() string {
	if m.quitting {
		return ""
	}
	switch m.stage {
	case stageForm:
		return m.viewForm()
	case stageConfirm:
		return m.viewConfirm()
	case stageTarget:
		return m.viewTarget()
	case stageView:
		return m.viewPrompt()
	default:
		return m.viewList()
	}
}

func (m model) viewList() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("📝 Cats Todo — prompt backlog"))
	scopeNote := "global only"
	if m.project.available() {
		scopeNote = firstNonEmpty(m.ctx.WorkspaceLabel, baseName(m.ctx.WorkDir), "project") + " + global"
	}
	b.WriteString("  ")
	b.WriteString(descStyle.Render(scopeNote))
	if m.hideDone {
		if hidden := m.hiddenDoneCount(); hidden > 0 {
			b.WriteString(descStyle.Render(fmt.Sprintf(" · %d done hidden", hidden)))
		}
	}
	b.WriteString("\n\n")

	b.WriteString(m.list.view("No prompts yet — press ctrl+a to add one."))

	if m.status != "" {
		b.WriteString("\n")
		st := okStyle
		if m.statusErr {
			st = errStyle
		}
		b.WriteString(st.Render("• " + m.status))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(footerStyle.Render("enter drop · ctrl+v view · ctrl+a add · ctrl+e edit · ctrl+t done · ctrl+x delete"))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("ctrl+↑/↓ move · ctrl+d hide/show done · ctrl+w clear done · esc quit"))
	return b.String()
}

func (m model) viewForm() string {
	var b strings.Builder
	heading := "New prompt — " + m.formScope.String() + " backlog"
	if m.formMode == formEdit {
		heading = "Edit prompt"
	}
	b.WriteString(titleStyle.Render(heading))
	b.WriteString("\n\n")

	b.WriteString(promptStyle.Render("Title"))
	b.WriteString("\n")
	b.WriteString(m.titleInput.View())
	b.WriteString("\n\n")

	b.WriteString(promptStyle.Render("Prompt"))
	b.WriteString("\n")
	b.WriteString(m.promptArea.View())
	b.WriteString("\n")

	if m.formErr != "" {
		b.WriteString(errStyle.Render(m.formErr))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	help := "tab switch field · ctrl+s save · esc cancel"
	if m.formMode == formAdd && m.project.available() {
		help = "tab switch field · ctrl+s save · ctrl+g toggle scope · esc cancel"
	}
	b.WriteString(footerStyle.Render(help))
	return b.String()
}

func (m model) viewConfirm() string {
	var b strings.Builder
	if m.confirmKind == confirmClearDone {
		b.WriteString(titleStyle.Render("Clear completed prompts?"))
		b.WriteString("\n\n")
		noun := "prompts"
		if m.pendingClearCount == 1 {
			noun = "prompt"
		}
		b.WriteString(nameStyle.Render(fmt.Sprintf("  delete %d completed %s across both backlogs", m.pendingClearCount, noun)))
		b.WriteString("\n\n")
		b.WriteString(footerStyle.Render("y clear · n / esc cancel"))
		return b.String()
	}
	b.WriteString(titleStyle.Render("Delete prompt?"))
	b.WriteString("\n\n")
	b.WriteString(nameStyle.Render("  " + truncate(m.pendingTitle, 70)))
	b.WriteString("\n\n")
	b.WriteString(footerStyle.Render("y delete · n / esc cancel"))
	return b.String()
}

// viewPrompt renders the read-only prompt view: heading, meta line, the full
// prompt body in a scrollable viewport, and a footer of actions.
func (m model) viewPrompt() string {
	td, ok := m.resolve(m.viewRef)
	if !ok {
		// Deleted from another pane while we were viewing it — fall back gently.
		return descStyle.Render("that prompt no longer exists — esc to go back")
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render("Prompt"))
	b.WriteString("  ")
	b.WriteString(nameSelStyle.Render(truncate(firstNonEmpty(td.Title, firstLine(td.Prompt, 60)), 60)))
	b.WriteString("\n")

	meta := m.viewRef.scope.String() + " backlog"
	if !td.Created.IsZero() {
		meta += " · added " + td.Created.Format("2006-01-02")
	}
	if td.Done {
		meta += " · " + checkStyle.Render("done")
	}
	b.WriteString(descStyle.Render(meta))
	b.WriteString("\n\n")

	b.WriteString(m.viewVP.View())
	b.WriteString("\n\n")
	b.WriteString(footerStyle.Render("↑/↓ scroll · enter drop · ctrl+e edit · esc back"))
	return b.String()
}

func (m model) viewTarget() string {
	var b strings.Builder
	td, _ := m.resolve(m.dropTodo)
	title := firstNonEmpty(td.Title, firstLine(td.Prompt, 50))
	b.WriteString(titleStyle.Render("Drop into…"))
	b.WriteString("  ")
	b.WriteString(descStyle.Render(truncate(title, 60)))
	b.WriteString("\n\n")
	b.WriteString(m.targetList.view("no agent sessions detected — pick New Claude Code session"))
	b.WriteString("\n")
	b.WriteString(footerStyle.Render("enter paste (don't submit) · ctrl+r drop & run · esc back"))
	return b.String()
}

// applySizes pushes the current window size into the inputs of the active stage
// so they wrap and scroll to fit. Only the live stage's components are touched:
// the form's textinput/textarea and the target picker are zero-value until their
// stage is entered (built in beginAdd/beginEdit/beginDrop), and calling a method
// like textarea.SetWidth on a zero-value model dereferences nil internal state.
func (m *model) applySizes() {
	w := m.width - 4
	if w < 20 {
		return
	}
	m.list.input.Width = w
	switch m.stage {
	case stageForm:
		m.titleInput.Width = w
		m.promptArea.SetWidth(w)
		if h := m.height - 12; h >= 4 {
			m.promptArea.SetHeight(h)
		}
	case stageTarget:
		m.targetList.input.Width = w
	case stageView:
		m.viewVP.Width = m.viewWidth()
		m.viewVP.Height = m.viewHeight()
		if td, ok := m.resolve(m.viewRef); ok {
			m.viewVP.SetContent(m.viewContent(td))
		}
	}
}

// baseName returns the last path element of p, or "" for an empty/"." path.
func baseName(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	b := filepath.Base(p)
	if b == "." || b == string(filepath.Separator) {
		return ""
	}
	return b
}
