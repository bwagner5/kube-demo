package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/samber/lo"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

var canvasStyle = lipgloss.NewStyle().Padding(1, 2, 1, 2)

type k8sStateChange struct{}

type Model struct {
	Nodes           []*corev1.Node
	informerFactory informers.SharedInformerFactory
	nodeInformer    cache.SharedIndexInformer
	podInformer     cache.SharedIndexInformer
	stopCh          chan struct{}
	k8sStateUpdate  chan struct{}
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

func (m *Model) View() string {
	physicalWidth, _, _ := term.GetSize(int(os.Stdout.Fd()))
	canvasStyle = canvasStyle.MaxWidth(physicalWidth)
	var canvas strings.Builder
	canvas.WriteString(m.nodes())
	return canvasStyle.Render(canvas.String())
}

func (m *Model) nodes() string {
	var boxRows [][]string
	nodes := m.nodeInformer.GetStore().List()
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].(*corev1.Node).CreationTimestamp.Unix() < nodes[j].(*corev1.Node).CreationTimestamp.Unix()
	})
	nodeStyle := lipgloss.NewStyle().
		Align(lipgloss.Left).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#000000")).
		Border(lipgloss.HiddenBorder(), true).
		BorderBackground(lipgloss.Color("#93aabc")).
		Margin(1).
		Padding(1).
		Height(20).
		Width(30)
	row := -1
	boxSize := nodeStyle.GetWidth() + nodeStyle.GetHorizontalMargins() + nodeStyle.GetHorizontalBorderSize()
	perRow := int(float64(canvasStyle.GetMaxWidth()) / float64(boxSize+canvasStyle.GetHorizontalPadding()))
	for i, obj := range nodes {
		node := obj.(*corev1.Node)
		box := nodeStyle.Render(
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

func (m *Model) pods(node *corev1.Node, nodeStyle lipgloss.Style) string {
	defaultColor := lipgloss.Color("#EE1111")
	dsColor := lipgloss.Color("#1111EE")
	color := defaultColor
	var boxRows [][]string
	pods := lo.Filter(m.podInformer.GetStore().List(), func(obj interface{}, _ int) bool {
		pod := obj.(*corev1.Pod)
		return pod.Spec.NodeName == node.Name
	})
	podStyle := lipgloss.NewStyle().
		Align(lipgloss.Bottom).
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(lipgloss.Color("#000000")).
		Border(lipgloss.NormalBorder(), true).
		BorderForeground(defaultColor).
		Margin(0).
		Padding(0).
		Height(1).
		Width(1)
	boxSize := podStyle.GetWidth() + podStyle.GetHorizontalMargins()
	perRow := int(float64(nodeStyle.GetWidth()) / float64(boxSize+nodeStyle.GetHorizontalPadding()))
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].(*corev1.Pod).CreationTimestamp.Unix() < pods[j].(*corev1.Pod).CreationTimestamp.Unix()
	})
	row := -1
	for i, obj := range pods {
		if i%perRow == 0 {
			boxRows = append(boxRows, []string{})
			row++
		}
		pod := obj.(*corev1.Pod)
		for _, o := range pod.OwnerReferences {
			if o.Kind == "DaemonSet" {
				color = dsColor
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
