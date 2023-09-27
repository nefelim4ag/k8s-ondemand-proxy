package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	"gopkg.in/yaml.v3"
	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nefelim4ag/k8s-ondemand-proxy/pkg/tcpserver"
)

type config struct {
	Namespace   string        `yaml:"namespace"`
	Replicas    int           `yaml:"replicas"`
	Resource    string        `yaml:"resource"`
	IdleTimeout time.Duration `yaml:"idle-timeout"`
	Proxy       []proxyPair   `yaml:"proxy"`
}

type proxyPair struct {
	Local  string `yaml:"local"`
	Remote string `yaml:"remote"`
}

type globalState struct {
	lastServe atomic.Int64
	readyPods atomic.Int32
	replicas  atomic.Int32
	inOutPort map[int]*net.TCPAddr
	namespace string
	group     string
	name      string

	client *clientset.Clientset

	servers []tcpserver.Server
}

func (state *globalState) touch() {
	state.lastServe.Store(time.Now().Unix())
}

func buildConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig != "" {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}
		return cfg, nil
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

func (state *globalState) pipe(src *net.TCPConn, dst *net.TCPConn) {
	defer src.Close()
	defer dst.Close()
	buf := make([]byte, 1024)

	for {
		state.touch()
		n, err := dst.ReadFrom(src)
		if err != nil {
			return
		}
		// Use blocking IO
		if n == 0 {
			n, err := src.Read(buf)
			if err != nil {
				return
			}
			b := buf[:n]

			state.touch()
			n, err = dst.Write(b)
			if err != nil {
				return
			}
		}
	}
}

func addrToPort(address string) (int, error) {
	srvSplit := strings.Split(address, ":")
	portString := srvSplit[len(srvSplit)-1]
	localPort, err := strconv.ParseInt(portString, 10, 32)
	if err != nil {
		return 0, err
	}
	return int(localPort), nil
}

func (state *globalState) connectionHandler(clientConn *net.TCPConn, err error) {
	if err != nil {
		slog.Error(err.Error())
		return
	}
	defer clientConn.Close()

	localPort, err := addrToPort(clientConn.LocalAddr().String())
	if err != nil {
		slog.Error(err.Error())
		return
	}
	upsreamSrv := state.inOutPort[int(localPort)]

	state.touch()
	// Must be some sort of locking, or sync.Cond, but I'm too lazy.
	for state.readyPods.Load() == 0 {
		state.touch()
		time.Sleep(time.Second * 2)
	}

	var serverConn *net.TCPConn
	// Retry up to ~40s
	for i := 1; i < 9; i++ {
		state.touch()
		serverConn, err = net.DialTCP("tcp", nil, upsreamSrv)
		if err != nil {
			netErr := err.(*net.OpError)
			if netErr.Err.Error() == "connect: connection refused" {
				// There must be some pod info logic to guess what happens and why...
				// But we will just wait & retry
				slog.Error(err.Error(), "temporary", "maybe", "retry", true, "count", i)
				time.Sleep(time.Second * time.Duration(i))
			} else {
				return
			}
		}
	}
	// If retry doesn't help - die
	if err != nil {
		return
	}

	serverConn.SetKeepAlive(true)
	slog.Info("Handle connection", "client", clientConn.RemoteAddr().String(), "server", serverConn.RemoteAddr().String())

	// Handle connection close internal in pipe, close both ends in same time
	go state.pipe(clientConn, serverConn)
	state.pipe(serverConn, clientConn)
}

func (state *globalState) statusWatcher() {
	for {
		// Infinite retry
		state.watch()
		time.Sleep(time.Second)
	}
}

func (state *globalState) watch() {
	client := state.client
	timeout := int64(600)

	listOptions := metav1.ListOptions{
		FieldSelector:  fields.OneTermEqualSelector(metav1.ObjectNameField, state.name).String(),
		TimeoutSeconds: &timeout,
	}

	switch state.group {
	case "statefulset", "sts":
		statefulSetClient := client.AppsV1().StatefulSets(state.namespace)
		sts, err := statefulSetClient.Get(context.TODO(), state.name, metav1.GetOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
		state.readyPods.Store(sts.Status.ReadyReplicas)
		state.replicas.Store(sts.Status.Replicas)

		watcher, err := statefulSetClient.Watch(context.TODO(), listOptions)
		if err != nil {
			slog.Error(err.Error())
		}
		wch := watcher.ResultChan()
		defer watcher.Stop()
		for {
			select {
			case event, ok := <-wch:
				if !ok {
					// Channel closed - restart
					return
				}
				sts, ok := event.Object.(*v1.StatefulSet)
				if !ok {
					slog.Error("unexpected type in watch event")
					return
				}
				state.replicas.Store(sts.Status.Replicas)
				if sts.Status.ReadyReplicas != state.readyPods.Load() {
					slog.Info("Scale event", "old", state.readyPods.Load(), "new", sts.Status.ReadyReplicas)
					state.readyPods.Store(sts.Status.ReadyReplicas)
				}
			case <-time.After(11 * time.Minute):
				// deal with the issue where we get no events
				return
			}
		}
	case "deployment", "deploy":
		deploymentClient := client.AppsV1().Deployments(state.namespace)
		deploy, err := deploymentClient.Get(context.TODO(), state.name, metav1.GetOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
		state.readyPods.Store(deploy.Status.ReadyReplicas)
		state.replicas.Store(deploy.Status.Replicas)

		watcher, err := deploymentClient.Watch(context.TODO(), listOptions)
		if err != nil {
			slog.Error(err.Error())
		}
		wch := watcher.ResultChan()
		defer watcher.Stop()
		for {
			select {
			case event, ok := <-wch:
				if !ok {
					// Channel closed - restart
					return
				}
				deploy, ok := event.Object.(*v1.Deployment)
				if !ok {
					slog.Error("unexpected type in watch event")
					return
				}
				state.replicas.Store(deploy.Status.Replicas)
				if deploy.Status.ReadyReplicas != state.readyPods.Load() {
					slog.Info("Scale event", "old", state.readyPods.Load(), "new", deploy.Status.ReadyReplicas)
					state.readyPods.Store(deploy.Status.ReadyReplicas)
				}
			case <-time.After(11 * time.Minute):
				// deal with the issue where we get no events
				return
			}
		}
	default:
		slog.Error("Api group not supported", "api", state.group)
	}
}

func (state *globalState) podsScaler(timeout time.Duration, replicas int32) {
	for range time.NewTicker(time.Second).C {
		now := time.Now().Unix()
		timeoutSec := int64(timeout.Seconds())
		lastAccess := state.lastServe.Load()
		if (lastAccess + timeoutSec) < now {
			state.updateScale(0)
		} else {
			state.updateScale(replicas)
		}
	}
}

func (state *globalState) updateScale(replicas int32) {
	client := state.client
	if state.replicas.Load() == replicas {
		// NoOp, wait for pod start
		return
	}

	slog.Info("Trigger scale", "name", state.name, "replicas", replicas, "group", state.group, "namespace", state.namespace)
	switch state.group {
	case "statefulset", "sts":
		statefulSetClient := client.AppsV1().StatefulSets(state.namespace)
		scale, err := statefulSetClient.GetScale(context.TODO(), state.name, metav1.GetOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
		scale.Spec.Replicas = replicas
		scale, err = statefulSetClient.UpdateScale(context.TODO(), state.name, scale, metav1.UpdateOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
	case "deployment", "deploy":
		deploymentClient := client.AppsV1().Deployments(state.namespace)
		scale, err := deploymentClient.GetScale(context.TODO(), state.name, metav1.GetOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
		scale.Spec.Replicas = replicas
		scale, err = deploymentClient.UpdateScale(context.TODO(), state.name, scale, metav1.UpdateOptions{})
		if err != nil {
			slog.Error(err.Error())
			return
		}
	default:
		slog.Error("Api group not supported", "api", state.group)
	}
}

func main() {
	var kubeconfig string
	var configPath string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&configPath, "config", "", "Yaml config path")
	flag.Parse()

	programLevel := new(slog.LevelVar)
	logger := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(logger))

	f, err := os.ReadFile(configPath)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	c := config{}
	if err := yaml.Unmarshal(f, &c); err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	_Config, err := buildConfig(kubeconfig)
	if err != nil {
		slog.Error(err.Error())
		os.Exit(1)
	}

	if len(c.Proxy) == 0 {
		slog.Error("Can't empty proxy section in config")
		os.Exit(1)
	}

	client := clientset.NewForConfigOrDie(_Config)

	state := globalState{
		client:    client,
		namespace: c.Namespace,
	}
	state.inOutPort = make(map[int]*net.TCPAddr, len(c.Proxy))

	resourceArgs := strings.Split(c.Resource, "/")
	if len(resourceArgs) != 2 {
		slog.Error("Wrong resource name, must be statefulset/app or deployment/app", "parsed", resourceArgs)
		return
	}

	state.group = resourceArgs[0]
	state.name = resourceArgs[1]

	state.touch()
	go state.podsScaler(c.IdleTimeout, int32(c.Replicas))
	go state.statusWatcher()

	state.servers = make([]tcpserver.Server, len(c.Proxy))

	for k, p := range c.Proxy {
		addr, err := net.ResolveTCPAddr("tcp", p.Local)
		if err != nil {
			slog.Error("failed to resolve address", "address", p.Local, "error", err.Error())
			os.Exit(1)
		}

		localPort := addr.Port

		upsreamSrv, err := net.ResolveTCPAddr("tcp", p.Remote)
		state.inOutPort[int(localPort)] = upsreamSrv
		if err != nil {
			slog.Error("failed to resolve address", p.Remote, err.Error())
			os.Exit(1)
		}

		err = state.servers[k].ListenAndServe(addr, state.connectionHandler)
		if err != nil {
			slog.Error(err.Error())
			os.Exit(1)
		}
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	slog.Info("Shutting down server...")
	for k := range state.servers {
		state.servers[k].Stop()
	}
	slog.Info("Server stopped.")
}
