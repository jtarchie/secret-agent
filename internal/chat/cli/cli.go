// Package cli is a bubbletea-based terminal chat transport.
package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/jtarchie/secret-agent/internal/chat"
)

type Transport struct {
	markdown bool
}

type Option func(*Transport)

func WithMarkdown(on bool) Option {
	return func(t *Transport) { t.markdown = on }
}

func New(opts ...Option) *Transport {
	t := &Transport{markdown: true}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

func (t *Transport) Run(ctx context.Context, botName string, newHandler chat.HandlerFactory) error {
	_, err := tea.NewProgram(
		newModel(ctx, botName, newHandler("local"), t.markdown),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	).Run()
	return err
}

type (
	chunkMsg      chat.Chunk
	streamDoneMsg struct{}
)

type keyMap struct {
	Send        key.Binding
	Newline     key.Binding
	HistoryPrev key.Binding
	HistoryNext key.Binding
	Cancel      key.Binding
	Quit        key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Send, k.Newline, k.HistoryPrev, k.HistoryNext, k.Cancel, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

var keys = keyMap{
	Send:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Newline:     key.NewBinding(key.WithKeys("alt+enter"), key.WithHelp("alt+enter", "newline")),
	HistoryPrev: key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "prev")),
	HistoryNext: key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "next")),
	Cancel:      key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "cancel/quit")),
	Quit:        key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "quit")),
}

type model struct {
	ctx      context.Context
	botName  string
	handler  chat.Handler
	history  []string
	viewport viewport.Model
	input    textarea.Model
	waiting  bool
	width    int
	height   int

	stream       <-chan chat.Chunk
	cancel       context.CancelFunc
	canceled     bool
	replyIdx     int
	replyBuf     strings.Builder
	spinner      spinner.Model
	inputHist    []string
	inputHistIdx int
	help         help.Model
	lastReply    string
	markdown     bool
	renderer     *glamour.TermRenderer

	userStyle   lipgloss.Style
	botStyle    lipgloss.Style
	errorStyle  lipgloss.Style
	statusStyle lipgloss.Style
}

func newModel(ctx context.Context, botName string, h chat.Handler, markdown bool) *model {
	ta := textarea.New()
	ta.Placeholder = "Type a message (enter to send, alt+enter for newline)..."
	ta.Focus()
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false
	ta.SetHeight(3)

	vp := viewport.New(80, 20)

	statusStyle := lipgloss.NewStyle().Faint(true)

	sp := spinner.New(spinner.WithSpinner(spinner.MiniDot))
	sp.Style = statusStyle

	hp := help.New()

	return &model{
		ctx:         ctx,
		botName:     botName,
		handler:     h,
		viewport:    vp,
		input:       ta,
		replyIdx:    -1,
		spinner:     sp,
		help:        hp,
		markdown:    markdown,
		userStyle:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		botStyle:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5")),
		errorStyle:  lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),
		statusStyle: statusStyle,
	}
}

func (m *model) Init() tea.Cmd {
	return textarea.Blink
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputHeight := 3
		helpHeight := 1
		m.viewport.Width = msg.Width
		m.viewport.Height = msg.Height - inputHeight - helpHeight - 1
		m.input.SetWidth(msg.Width)
		m.input.SetHeight(inputHeight)
		m.help.Width = msg.Width
		m.rebuildRenderer()
		m.refreshViewport()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.waiting && m.cancel != nil {
				m.cancel()
				m.canceled = true
				return m, nil
			}
			return m, tea.Quit
		case tea.KeyEsc:
			return m, tea.Quit
		case tea.KeyUp:
			if m.waiting || !m.inputIsSingleLine() {
				break
			}
			if m.inputHistIdx > 0 {
				m.inputHistIdx--
				m.input.SetValue(m.inputHist[m.inputHistIdx])
				m.input.CursorEnd()
			}
			return m, nil
		case tea.KeyDown:
			if m.waiting || !m.inputIsSingleLine() {
				break
			}
			if m.inputHistIdx < len(m.inputHist) {
				m.inputHistIdx++
				if m.inputHistIdx == len(m.inputHist) {
					m.input.SetValue("")
				} else {
					m.input.SetValue(m.inputHist[m.inputHistIdx])
					m.input.CursorEnd()
				}
			}
			return m, nil
		case tea.KeyEnter:
			if m.waiting {
				return m, nil
			}
			if msg.Alt {
				m.input.InsertRune('\n')
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.Reset()
			m.pushInputHist(text)
			if strings.HasPrefix(text, "/") {
				return m.runSlash(text)
			}
			m.appendLine(m.userStyle.Render("you") + ": " + text)
			m.appendLine(m.thinkingLine())
			m.replyIdx = len(m.history) - 1
			m.replyBuf.Reset()
			m.canceled = false
			sendCtx, cancel := context.WithCancel(m.ctx)
			m.cancel = cancel
			m.stream = m.handler(sendCtx, text)
			m.waiting = true
			return m, tea.Batch(waitForChunk(m.stream), m.spinner.Tick)
		}

	case spinner.TickMsg:
		if !m.waiting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.replyBuf.Len() == 0 && m.replyIdx >= 0 && m.replyIdx < len(m.history) {
			m.history[m.replyIdx] = m.thinkingLine()
			m.refreshViewport()
		}
		return m, cmd

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
		if m.replyBuf.Len() == 0 && m.replyIdx >= 0 && m.replyIdx < len(m.history) {
			m.history = append(m.history[:m.replyIdx], m.history[m.replyIdx+1:]...)
		}
		if m.replyBuf.Len() > 0 {
			m.lastReply = m.replyBuf.String()
			if rendered, ok := m.renderMarkdown(m.lastReply); ok && m.replyIdx >= 0 && m.replyIdx < len(m.history) {
				m.history[m.replyIdx] = m.botStyle.Render(m.botName) + ":\n" + rendered
			}
		}
		if m.canceled {
			m.history = append(m.history, m.statusStyle.Render("(canceled)"))
		}
		m.history = append(m.history, "")
		m.refreshViewport()
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		m.waiting = false
		m.stream = nil
		m.replyIdx = -1
		m.replyBuf.Reset()
		m.canceled = false
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) View() string {
	return fmt.Sprintf("%s\n%s\n%s", m.viewport.View(), m.input.View(), m.help.View(keys))
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

func (m *model) rebuildRenderer() {
	if !m.markdown || m.width <= 0 {
		return
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(m.width),
	)
	if err != nil {
		return
	}
	m.renderer = r
}

func (m *model) renderMarkdown(s string) (string, bool) {
	if !m.markdown || m.renderer == nil {
		return "", false
	}
	out, err := m.renderer.Render(s)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func (m *model) runSlash(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "/quit", "/exit":
		return m, tea.Quit
	case "/clear":
		m.history = nil
		m.refreshViewport()
		return m, nil
	case "/help":
		m.appendLine(m.statusStyle.Render("commands: /clear, /copy, /help, /quit"))
		m.appendLine(m.statusStyle.Render(m.help.FullHelpView(keys.FullHelp())))
		m.appendLine("")
		return m, nil
	case "/copy":
		if m.lastReply == "" {
			m.appendLine(m.errorStyle.Render("error") + ": no reply to copy yet")
			return m, nil
		}
		if err := clipboard.WriteAll(m.lastReply); err != nil {
			m.appendLine(m.errorStyle.Render("error") + ": " + err.Error())
			return m, nil
		}
		m.appendLine(m.statusStyle.Render("(copied last reply to clipboard)"))
		return m, nil
	default:
		m.appendLine(m.errorStyle.Render("error") + ": unknown command " + cmd)
		return m, nil
	}
}

func (m *model) inputIsSingleLine() bool {
	return !strings.Contains(m.input.Value(), "\n")
}

func (m *model) pushInputHist(text string) {
	if n := len(m.inputHist); n == 0 || m.inputHist[n-1] != text {
		m.inputHist = append(m.inputHist, text)
	}
	m.inputHistIdx = len(m.inputHist)
}

func (m *model) thinkingLine() string {
	return m.statusStyle.Render(m.spinner.View() + " thinking")
}

func (m *model) appendLine(s string) {
	m.history = append(m.history, s)
	m.refreshViewport()
}

func (m *model) refreshViewport() {
	m.viewport.SetContent(strings.Join(m.history, "\n"))
	m.viewport.GotoBottom()
}
