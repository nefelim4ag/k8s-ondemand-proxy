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

	v1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nefelim4ag/k8s-ondemand-proxy/pkg/tcpserver"
)

type globalState struct {
	lastServe  atomic.Int64
	readyPods  atomic.Int32
	replicas   atomic.Int32
	inOutPort  map[int]*net.TCPAddr
	namespace  string
	group      string
	name       string

	client *clientset.Clientset
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

func addrToPort (address string) (int, error) {
	srvSplit := strings.Split(address, ":")
	portString := srvSplit[len(srvSplit) - 1]
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
	client := state.client
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
		listOptions := metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(metav1.ObjectNameField, sts.Name).String(),
		}
		watcher, err := statefulSetClient.Watch(context.TODO(), listOptions)
		if err != nil {
			slog.Error(err.Error())
		}
		wch := watcher.ResultChan()
		defer watcher.Stop()
		for event := range wch {
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
		listOptions := metav1.ListOptions{
			FieldSelector: fields.OneTermEqualSelector(metav1.ObjectNameField, deploy.Name).String(),
		}
		watcher, err := deploymentClient.Watch(context.TODO(), listOptions)
		if err != nil {
			slog.Error(err.Error())
		}
		wch := watcher.ResultChan()
		defer watcher.Stop()
		for event := range wch {
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
	var rawUpstreamServerAddr string
	var rawLocalServerAddr string
	var namespace string
	var resourceName string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&rawUpstreamServerAddr, "upstream", "", "Remote server address like dind.ci.svc.cluster.local:2375")
	flag.StringVar(&rawLocalServerAddr, "listen", "", "Local address listen to like :2375")
	flag.StringVar(&namespace, "namespace", "", "Kubernetes namespace to work with")
	flag.StringVar(&resourceName, "resource-name", "", "Kubernetes resource like deployment/app")
	idleTimeout := flag.Duration("idle-timeout", time.Minute*15, "Go Duration on last traffic activity before shutdown")
	replicas := flag.Int64("replicas", 1, "replica count on traffic & on cold startup")
	flag.Parse()

	programLevel := new(slog.LevelVar)
	logger := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(logger))

	config, err := buildConfig(kubeconfig)
	if err != nil {
		slog.Error(err.Error())
	}
	client := clientset.NewForConfigOrDie(config)

	state := globalState{
		client:    client,
		namespace: namespace,
	}
	state.inOutPort = make(map[int]*net.TCPAddr, 1)

	localPort, err := addrToPort(rawLocalServerAddr)
	if err != nil {
		slog.Error(err.Error())
		return
	}

	upsreamSrv, err := net.ResolveTCPAddr("tcp", rawUpstreamServerAddr)
	state.inOutPort[int(localPort)] = upsreamSrv
	if err != nil {
		slog.Error("failed to resolve address", rawUpstreamServerAddr, err.Error())
		return
	}

	resourceArgs := strings.Split(resourceName, "/")
	if len(resourceArgs) != 2 {
		slog.Error("Wrong resource name, must be statefulset/app or deployment/app", "parsed", resourceArgs)
		return
	}
	state.group = resourceArgs[0]
	state.name = resourceArgs[1]
	state.touch()
	go state.podsScaler(*idleTimeout, int32(*replicas))
	go state.statusWatcher()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	srvInstance := tcpserver.Server{}
	err = srvInstance.ListenAndServe(rawLocalServerAddr, state.connectionHandler)
	if err != nil {
		slog.Error(err.Error())
	}

	<-sigChan
	slog.Info("Shutting down server...")
	srvInstance.Stop()
	slog.Info("Server stopped.")
}
