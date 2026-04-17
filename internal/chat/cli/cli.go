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
		tea.WithMouseCellMotion(),
	).Run()
	return err
}

type (
	chunkMsg      chat.Chunk
	streamDoneMsg struct{}
)

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

	stream   <-chan chat.Chunk
	replyIdx int
	replyBuf strings.Builder

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
		replyIdx:    -1,
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
			m.appendLine(m.statusStyle.Render("…thinking"))
			m.replyIdx = len(m.history) - 1
			m.replyBuf.Reset()
			m.stream = m.handler(m.ctx, text)
			m.waiting = true
			return m, waitForChunk(m.stream)
		}

	case chunkMsg:
		if msg.Err != nil {
			m.history[m.replyIdx] = m.errorStyle.Render("error") + ": " + msg.Err.Error()
			m.refreshViewport()
			return m, waitForChunk(m.stream)
		}
		m.replyBuf.WriteString(msg.Delta)
		m.history[m.replyIdx] = m.botStyle.Render(m.botName) + ": " + m.replyBuf.String()
		m.refreshViewport()
		return m, waitForChunk(m.stream)

	case streamDoneMsg:
		if m.replyBuf.Len() == 0 && m.replyIdx >= 0 && m.replyIdx < len(m.history) &&
			strings.Contains(m.history[m.replyIdx], "…thinking") {
			m.history = append(m.history[:m.replyIdx], m.history[m.replyIdx+1:]...)
		}
		m.history = append(m.history, "")
		m.refreshViewport()
		m.waiting = false
		m.stream = nil
		m.replyIdx = -1
		m.replyBuf.Reset()
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) View() string {
	return fmt.Sprintf("%s\n%s", m.viewport.View(), m.input.View())
}

func waitForChunk(ch <-chan chat.Chunk) tea.Cmd {
	return func() tea.Msg {
		c, ok := <-ch
		if !ok {
			return streamDoneMsg{}
		}
		return chunkMsg(c)
	}
}

func (m *model) appendLine(s string) {
	m.history = append(m.history, s)
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	m.viewport.SetContent(strings.Join(m.history, "\n"))
	m.viewport.GotoBottom()
}
