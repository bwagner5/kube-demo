package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/samber/lo"
	"golang.org/x/term"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var canvasStyle = lipgloss.NewStyle().Padding(1, 2, 1, 2)

var white = lipgloss.Color("#FFFFFF")
var black = lipgloss.Color("#000000")
var pink = lipgloss.Color("#F87575")
var teal = lipgloss.Color("#27CEBD")
var grey = lipgloss.Color("#6C7D89")

var nodeBorder = grey
var selectedNodeBorder = pink
var defaultPodBorder = teal

var nodeStyle = lipgloss.NewStyle().
	Align(lipgloss.Left).
	Foreground(white).
	Background(black).
	Border(lipgloss.HiddenBorder(), true).
	BorderBackground(nodeBorder).
	Margin(1).
	Padding(1).
	Height(10).
	Width(30)

var podStyle = lipgloss.NewStyle().
	Align(lipgloss.Bottom).
	Foreground(white).
	Background(black).
	Border(lipgloss.RoundedBorder(), true).
	BorderForeground(defaultPodBorder).
	Margin(0).
	Padding(0).
	Height(0).
	Width(1)

type keyMap map[string]key.Binding

var keyMappings = keyMap{
	"Move": key.NewBinding(
		key.WithKeys("up", "down", "left", "right"),
		key.WithHelp("↑/↓/←/→", "move"),
	),
	"Help": key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "toggle help"),
	),
	"Quit": key.NewBinding(
		key.WithKeys("q", "esc", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
}

// ShortHelp returns keybindings to be shown in the mini help view. It's part
// of the key.Map interface.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k["Move"], k["Quit"], k["Help"]}
}

// FullHelp returns keybindings for the expanded help view. It's part of the
// key.Map interface.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k["Move"], k["Help"], k["Quit"]},
	}
}

type k8sStateChange struct{}

type Model struct {
	Nodes           []*corev1.Node
	selectedNode    int
	selectedPod     int
	podSelection    bool
	details         bool
	informerFactory informers.SharedInformerFactory
	nodeInformer    cache.SharedIndexInformer
	podInformer     cache.SharedIndexInformer
	stopCh          chan struct{}
	k8sStateUpdate  chan struct{}
	help            help.Model
	viewport        viewport.Model
}

func New() *Model {
	config, err := clientcmd.BuildConfigFromFlags("", os.Getenv("KUBECONFIG"))
	if err != nil {
		log.Fatalf("could not initialize kubeconfig: %v", err)
	}
	kubeclient, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("could not initialize kube-client: %v", err)
	}
	informerFactory := informers.NewSharedInformerFactory(kubeclient, time.Minute*10)
	stopCh := make(chan struct{})
	k8sStateUpdate := make(chan struct{})
	nodeInformer := informerFactory.Core().V1().Nodes().Informer()
	podInformer := informerFactory.Core().V1().Pods().Informer()
	model := &Model{
		informerFactory: informerFactory,
		nodeInformer:    nodeInformer,
		podInformer:     podInformer,
		stopCh:          stopCh,
		k8sStateUpdate:  k8sStateUpdate,
		help:            help.New(),
		viewport:        viewport.New(0, 0),
	}
	model.nodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
		UpdateFunc: func(_, _ interface{}) { model.k8sStateUpdate <- struct{}{} },
		DeleteFunc: func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
	})
	model.podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
		UpdateFunc: func(_, _ interface{}) { model.k8sStateUpdate <- struct{}{} },
		DeleteFunc: func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
	})
	informerFactory.Start(stopCh) // runs in backgrounds
	return model
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg {
		m.informerFactory.WaitForCacheSync(m.stopCh)
		return k8sStateChange{}
	}, tea.EnterAltScreen)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			close(m.stopCh)
			return m, tea.Quit
		case "left", "right", "up", "down":
			m.selectedNode = m.moveCursor(msg)
		case "enter":
			m.details = !m.details
		case "?":
			m.help.ShowAll = !m.help.ShowAll
		}
	case k8sStateChange:
		return m, func() tea.Msg {
			select {
			case <-m.k8sStateUpdate:
				return k8sStateChange{}
			case <-m.stopCh:
				return nil
			}
		}
	}
	return m, nil
}

func (m *Model) moveCursor(key tea.KeyMsg) int {
	totalObjects := len(m.nodeInformer.GetStore().ListKeys())
	perRow := m.GetBoxesPerRow(canvasStyle, nodeStyle)
	switch key.String() {
	case "right":
		rowNum := m.selectedNode / perRow
		index := m.selectedNode + 1
		if index >= totalObjects {
			return index - index%perRow
		}
		return rowNum*perRow + index%perRow
	case "left":
		rowNum := m.selectedNode / perRow
		index := rowNum*perRow + mod((m.selectedNode-1), perRow)
		if index >= totalObjects {
			return totalObjects - 1
		}
		return index
	case "up":
		index := m.selectedNode - perRow
		col := mod(index, perRow)
		bottomRow := totalObjects / perRow
		if index < 0 {
			newPos := bottomRow*perRow + col
			if newPos >= totalObjects {
				return newPos - perRow
			}
			return bottomRow*perRow + col
		}
		return index
	case "down":
		index := m.selectedNode + perRow
		if index >= totalObjects {
			return index % perRow
		}
		return index
	}
	return 0
}

// mod perform the modulus calculation
// in go, the % operator is the remainder rather than the modulus
func mod(a, b int) int {
	return (a%b + b) % b
}

func (m *Model) View() string {
	physicalWidth, physicalHeight, _ := term.GetSize(int(os.Stdout.Fd()))
	if m.details {
		m.viewport.Height = physicalHeight
		m.viewport.Width = physicalWidth

		out, err := yaml.Marshal(m.getNodes()[m.selectedNode].Spec)
		if err == nil {
			m.viewport.SetContent(string(out))
		}
		if err != nil {
			panic(err)
		}
		return m.viewport.View()
	}
	canvasStyle = canvasStyle.MaxWidth(physicalWidth).Width(physicalWidth)
	var canvas strings.Builder
	canvas.WriteString(m.nodes())
	spaceToBottom := physicalHeight - strings.Count(canvas.String(), "\n")
	return canvasStyle.Render(canvas.String()+strings.Repeat("\n", spaceToBottom)) + "\n" + m.help.View(keyMappings)
}

func (m *Model) GetBoxesPerRow(container lipgloss.Style, subContainer lipgloss.Style) int {
	boxSize := subContainer.GetWidth() + subContainer.GetHorizontalMargins() + subContainer.GetHorizontalBorderSize()
	return int(float64(container.GetWidth()-container.GetHorizontalPadding()) / float64(boxSize))
}

func (m *Model) nodes() string {
	var boxRows [][]string
	row := -1
	perRow := m.GetBoxesPerRow(canvasStyle, nodeStyle)
	for i, node := range m.getNodes() {
		color := nodeStyle.GetBorderBottomBackground()
		if i == m.selectedNode {
			color = selectedNodeBorder
		}
		box := nodeStyle.Copy().BorderBackground(color).Render(
			lipgloss.JoinVertical(lipgloss.Left,
				node.Name,
				m.pods(node, nodeStyle),
			),
		)
		if i%int(perRow) == 0 {
			row++
			boxRows = append(boxRows, []string{})
		}
		boxRows[row] = append(boxRows[row], box)
	}
	rows := lo.Map(boxRows, func(row []string, _ int) string {
		return lipgloss.JoinHorizontal(lipgloss.Top, row...)
	})
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m *Model) getNodes() []*corev1.Node {
	nodes := m.nodeInformer.GetStore().List()
	sort.SliceStable(nodes, func(i, j int) bool {
		iCreated := nodes[i].(*corev1.Node).CreationTimestamp.Unix()
		jCreated := nodes[j].(*corev1.Node).CreationTimestamp.Unix()
		if iCreated == jCreated {
			return string(nodes[i].(*corev1.Node).UID) < string(nodes[j].(*corev1.Node).UID)
		}
		return iCreated < jCreated
	})
	var typedNodes []*corev1.Node
	for _, n := range nodes {
		typedNodes = append(typedNodes, n.(*corev1.Node))
	}
	return typedNodes
}

func (m *Model) pods(node *corev1.Node, nodeStyle lipgloss.Style) string {
	var boxRows [][]string
	pods := lo.Filter(m.podInformer.GetStore().List(), func(obj interface{}, _ int) bool {
		pod := obj.(*corev1.Pod)
		return pod.Spec.NodeName == node.Name
	})
	perRow := m.GetBoxesPerRow(nodeStyle, podStyle)
	sort.SliceStable(pods, func(i, j int) bool {
		iCreated := pods[i].(*corev1.Pod).CreationTimestamp.Unix()
		jCreated := pods[j].(*corev1.Pod).CreationTimestamp.Unix()
		if iCreated == jCreated {
			return string(pods[i].(*corev1.Pod).UID) < string(pods[j].(*corev1.Pod).UID)
		}
		return iCreated < jCreated
	})
	row := -1
	for i, obj := range pods {
		color := podStyle.GetBorderBottomForeground()
		if i%perRow == 0 {
			boxRows = append(boxRows, []string{})
			row++
		}
		pod := obj.(*corev1.Pod)
		for _, o := range pod.OwnerReferences {
			if o.Kind == "DaemonSet" {
				// color = yellow
			}
		}
		boxRows[row] = append(boxRows[row], podStyle.Copy().BorderForeground(color).Render(""))
	}
	rows := lo.Map(boxRows, func(row []string, _ int) string {
		return lipgloss.JoinHorizontal(lipgloss.Bottom, row...)
	})
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func main() {
	p := tea.NewProgram(New())
	if err := p.Start(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}
