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

var canvasStyle = lipgloss.NewStyle().Padding(1, 2, 1, 2).MaxWidth(100)

type k8sStateChange struct{}

type Model struct {
	Nodes           []*corev1.Node
	informerFactory informers.SharedInformerFactory
	nodesInformer   cache.SharedIndexInformer
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
	nodesInformer := informerFactory.Core().V1().Nodes().Informer()
	model := &Model{
		informerFactory: informerFactory,
		nodesInformer:   nodesInformer,
		stopCh:          stopCh,
		k8sStateUpdate:  k8sStateUpdate,
	}
	model.nodesInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
		UpdateFunc: func(_, _ interface{}) { model.k8sStateUpdate <- struct{}{} },
		DeleteFunc: func(_ interface{}) { model.k8sStateUpdate <- struct{}{} },
	})
	informerFactory.Start(stopCh) // runs in backgrounds
	return model
}

func (m *Model) Init() tea.Cmd {
	return func() tea.Msg {
		m.informerFactory.WaitForCacheSync(m.stopCh)
		return k8sStateChange{}
	}
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
	nodeHeight := 10
	nodeWidth := 20
	padding := 1
	var boxRows [][]string
	row := -1
	if len(boxRows) == 0 || (len(boxRows[row])+1)*(nodeWidth+padding*2) >= canvasStyle.GetMaxWidth() {
		boxRows = append(boxRows, []string{})
		row++
	}
	nodes := m.nodesInformer.GetStore().List()
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].(*corev1.Node).CreationTimestamp.Unix() < nodes[j].(*corev1.Node).CreationTimestamp.Unix()
	})
	for _, obj := range nodes {
		node := obj.(*corev1.Node)
		boxRows[row] = append(boxRows[row], lipgloss.NewStyle().
			Align(lipgloss.Left).
			Foreground(lipgloss.Color("#000000")).
			Background(lipgloss.Color("#93aabc")).
			Margin(1).
			Padding(1).
			Height(nodeHeight).
			Width(nodeWidth).Render(node.Name))
	}
	rows := lo.Map(boxRows, func(row []string, _ int) string {
		return lipgloss.JoinHorizontal(lipgloss.Top, row...)
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
