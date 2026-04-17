// Package cli is a bubbletea-based terminal chat transport.
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/jtarchie/secret-agent/internal/chat"
)

type Transport struct{}

func New() *Transport { return &Transport{} }

func (t *Transport) Run(ctx context.Context, botName string, h chat.Handler) error {
	_, err := tea.NewProgram(
		newModel(ctx, botName, h),
		tea.WithAltScreen(),
	).Run()
	return err
}

type botReply struct {
	text string
	err  error
}

type model struct {
	ctx      context.Context
	botName  string
	handler  chat.Handler
	history  []string
	viewport viewport.Model
	input    textinput.Model
	waiting  bool
	width    int
	height   int

	userStyle   lipgloss.Style
	botStyle    lipgloss.Style
	errorStyle  lipgloss.Style
	statusStyle lipgloss.Style
}

func newModel(ctx context.Context, botName string, h chat.Handler) *model {
	ti := textinput.New()
	ti.Placeholder = "Type a message..."
	ti.Focus()
	ti.CharLimit = 4096

	vp := viewport.New(80, 20)

	return &model{
		ctx:         ctx,
		botName:     botName,
		handler:     h,
		viewport:    vp,
		input:       ti,
		userStyle:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		botStyle:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5")),
		errorStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),
		statusStyle: lipgloss.NewStyle().Faint(true),
	}
}

func (m *model) Init() tea.Cmd {
	return textinput.Blink
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - 3
		m.input.Width = msg.Width - 2
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyEnter:
			if m.waiting {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			m.appendLine(m.userStyle.Render("you") + ": " + text)
			m.waiting = true
			m.appendLine(m.statusStyle.Render("…thinking"))
			return m, m.askBot(text)
		}

	case botReply:
		m.dropLastLine()
		if msg.err != nil {
			m.appendLine(m.errorStyle.Render("error") + ": " + msg.err.Error())
		} else {
			m.appendLine(m.botStyle.Render(m.botName) + ": " + msg.text)
		}
		m.waiting = false
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) View() string {
	return fmt.Sprintf("%s\n%s", m.viewport.View(), m.input.View())
}

func (m *model) askBot(text string) tea.Cmd {
	return func() tea.Msg {
		reply, err := m.handler(m.ctx, text)
		return botReply{text: reply, err: err}
	}
}

func (m *model) appendLine(s string) {
	m.history = append(m.history, s)
	m.refreshViewport()
}

func (m *model) dropLastLine() {
	if len(m.history) == 0 {
		return
	}
	m.history = m.history[:len(m.history)-1]
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	m.viewport.SetContent(strings.Join(m.history, "\n"))
	m.viewport.GotoBottom()
}
