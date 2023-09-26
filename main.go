package main

import (
	"flag"
	"net"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"log/slog"

	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"nefelim4ag/k8s-ondemand-proxy/tcpserver"
)

type globalState struct {
	lastServe atomic.Int64
}

func (state *globalState) touch() {
	state.lastServe.Store(time.Now().UnixMicro())
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

func (state *globalState) pipe(src *net.TCPConn, dst *net.TCPConn){
	defer src.Close()
	defer dst.Close()
	buf := make([]byte, 4 * 4096)

	for {
		state.touch()
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

func (state *globalState) connectionHandler(clientConn *net.TCPConn, err error) {
	if err != nil {
		slog.Error(err.Error())
		return
	}
	state.touch()



	addr, err := net.ResolveTCPAddr("tcp", "127.0.0.1:6000")
	if err != nil {
		slog.Error("failed to resolve address %s: %w", "127.0.0.1:6000", err.Error())
		return
	}

    serverConn, err := net.DialTCP("tcp", nil, addr)
    if err != nil {
        slog.Error(err.Error())
        return
    }

	// Handle connection close internal in pipe, close both ends in same time
	go state.pipe(clientConn, serverConn)
	state.pipe(serverConn, clientConn)
}

func main() {
	var kubeconfig string
	var leaseLockName string
	var leaseLockNamespace string

	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&leaseLockName, "lease-lock-name", "", "the lease lock resource name")
	flag.StringVar(&leaseLockNamespace, "lease-lock-namespace", "", "the lease lock resource namespace")
	flag.Parse()

	programLevel := new(slog.LevelVar)
	logger := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: programLevel})
	slog.SetDefault(slog.New(logger))

	if leaseLockName == "" {
		slog.Error("unable to get lease lock resource name (missing lease-lock-name flag).")
	}
	if leaseLockNamespace == "" {
		slog.Error("unable to get lease lock resource namespace (missing lease-lock-namespace flag).")
	}

	// leader election uses the Kubernetes API by writing to a
	// lock object, which can be a LeaseLock object (preferred),
	// a ConfigMap, or an Endpoints (deprecated) object.
	// Conflicting writes are detected and each client handles those actions
	// independently.
	config, err := buildConfig(kubeconfig)
	if err != nil {
		slog.Error(err.Error())
	}
	_ = clientset.NewForConfigOrDie(config)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	state := globalState{}

	srvInstance := tcpserver.Server{}
	err = srvInstance.ListenAndServe(":11211", state.connectionHandler)
	if err != nil {
		slog.Error(err.Error())
	}

	<-sigChan
	slog.Info("Shutting down server...")
	srvInstance.Stop()
	slog.Info("Server stopped.")
}
