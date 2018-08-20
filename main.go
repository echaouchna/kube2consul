package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coreos/pkg/flagutil"
	health "github.com/docker/go-healthcheck"
	concurrent "github.com/echaouchna/go-threadpool"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/vburenin/nsync"
	"k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	kcache "k8s.io/client-go/tools/cache"
)

var (
	opts                 cliOptions
	kube2consulVersion   string
	lock                 *consulapi.Lock
	lockCh               <-chan struct{}
	jobQueue             chan concurrent.Action
	resyncAlreadyRunning = false
	nmutex               = nsync.NewNamedMutex()
)

type arrayFlags []string

type kube2consul struct {
	consulCatalog  *consulapi.Catalog
	endpointsStore kcache.Store
}

type cliOptions struct {
	kubeAPI            string
	consulAPI          string
	consulToken        string
	resyncPeriod       int
	version            bool
	kubeConfig         string
	lock               bool
	lockKey            string
	noHealth           bool
	consulTag          string
	explicit           bool
	debug              bool
	excludedNamespaces arrayFlags
	jobNumber          int
}

// ActionType holds the name of the action to be executed
// add
// update
// delete
// remove dns garbage
type ActionType string

const (
	// AddOrUpdate add or update action name
	AddOrUpdate ActionType = "addOrUpdate"
	// Delete delete action name
	Delete ActionType = "delete"
	// UpdateService update service metadata
	UpdateService ActionType = "updateService"
)

func (actionType ActionType) value() string {
	return string(actionType)
}

func init() {
	flag.BoolVar(&opts.version, "version", false, "Prints kube2consul version")
	flag.IntVar(&opts.resyncPeriod, "resync-period", 30, "Resynchronization period in second")
	flag.StringVar(&opts.kubeAPI, "kubernetes-api", "", "Overrides apiserver address when used in cluster")
	flag.StringVar(&opts.consulAPI, "consul-api", "127.0.0.1:8500", "Consul API URL")
	flag.StringVar(&opts.consulToken, "consul-token", "", "Consul API token")
	flag.StringVar(&opts.kubeConfig, "kubeconfig", "", "Absolute path to the kubeconfig file")
	flag.BoolVar(&opts.lock, "lock", false, "Acquires a lock with consul to ensure that only one instance of kube2consul is running")
	flag.StringVar(&opts.lockKey, "lock-key", "locks/kube2consul/.lock", "Key used for locking")
	flag.BoolVar(&opts.noHealth, "no-health", false, "Disable endpoint /health on port 8080")
	flag.BoolVar(&opts.explicit, "explicit", false, "Only register containers which have SERVICE_NAME label set")
	flag.BoolVar(&opts.debug, "debug", false, "Enables debug log mode")
	flag.IntVar(&opts.jobNumber, "job-number", 0, "The number of parallel jobs")
	flag.StringVar(&opts.consulTag, "consul-tag", "kube2consul", "Tag setted on services to identify services managed by kube2consul in Consul")
	flag.Var(&opts.excludedNamespaces, "exclude-namespace", "Exclude a namespace")
}

func (i *arrayFlags) String() string {
	return "string array representation"
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func inSlice(value string, slice []string) bool {
	for _, s := range slice {
		if s == value {
			return true
		}
	}
	return false
}

func (k2c *kube2consul) resync(id int, kubeClient kubernetes.Interface) {
	epSet := make(map[string]bool)
	perServiceEndpoints := make(map[string][]Endpoint)

	glog.Infof("[job: %d] resync", id)

	glog.V(2).Infof("##> k8s services :")
	allEndpoints, err := k2c.getAllEndpoints(kubeClient)
	for _, ep := range allEndpoints.Items {
		if !stringInSlice(ep.Namespace, opts.excludedNamespaces) {
			_, perServiceEndpoints = k2c.generateEntries(id, &ep)
			for name := range perServiceEndpoints {
				glog.V(2).Infof("--> %s", name)
				epSet[name] = false
			}
		}
	}

	services, _, err := k2c.consulCatalog.Services(nil)
	if err != nil {
		glog.Errorf("[job: %d] Cannot remove DNS garbage: %v", id, err)
		return
	}

	glog.V(2).Infof("##> consul services :")
	for name, tags := range services {
		if !inSlice(opts.consulTag, tags) {
			continue
		}
		glog.V(2).Infof("--> %s", name)

		if _, ok := epSet[name]; !ok {
			err = k2c.removeDeletedEndpoints(id, name, []Endpoint{})
			if err != nil {
				glog.Errorf("[job: %d] Error removing DNS garbage: %v", id, err)
			}
		} else {
			epSet[name] = true
			for _, e := range perServiceEndpoints[name] {
				if err := k2c.registerEndpoint(id, e); err != nil {
					glog.Errorf("[job: %d] Error updating endpoints %v: %v", id, e.Name, err)
				}
			}
			err = k2c.removeDeletedEndpoints(id, name, perServiceEndpoints[name])
			if err != nil {
				glog.Errorf("[job: %d] Error removing DNS garbage: %v", id, err)
			}
		}
	}

	for name, found := range epSet {
		if !found {
			for _, e := range perServiceEndpoints[name] {
				if err := k2c.registerEndpoint(id, e); err != nil {
					glog.Errorf("[job: %d] Error updating endpoints %v: %v", id, e.Name, err)
				}
			}
		}
	}

	glog.Infof("[job: %d] resync done", id)
}

func consulCheck() error {
	_, err := newConsulClient(opts.consulAPI, opts.consulToken)
	if err != nil {
		return err
	}

	return nil
}

func kubernetesCheck() error {
	_, err := newKubeClient(opts.kubeAPI, opts.kubeConfig)
	if err != nil {
		return err
	}
	return nil
}

func initJobFunctions(k2c kube2consul) map[string]concurrent.JobFunc {
	actionJobs := make(map[string]concurrent.JobFunc)
	actionJobs[AddOrUpdate.value()] = func(id int, value interface{}) {
		if endpoints, ok := value.(*v1.Endpoints); ok {
			nmutex.Lock(endpoints.Name)
			defer nmutex.Unlock(endpoints.Name)
			if err := k2c.updateEndpoints(id, endpoints); err != nil {
				glog.Errorf("Error handling update event: %v", err)
			}
		}
	}

	actionJobs[Delete.value()] = func(id int, value interface{}) {
		if service, ok := value.(*v1.Service); ok {
			nmutex.Lock(service.Name)
			defer nmutex.Unlock(service.Name)
			perServiceEndpoints := make(map[string][]Endpoint)
			initPerServiceEndpointsFromService(service, perServiceEndpoints)
			k2c.removeDeletedServices(id, getStringKeysFromMap(perServiceEndpoints))
		}
	}

	actionJobs[UpdateService.value()] = func(id int, value interface{}) {
		if service, ok := value.(*v1.Service); ok {
			nmutex.Lock(service.Name)
			defer nmutex.Unlock(service.Name)
			endpoints := getEndpoints(service)
			if err := k2c.updateEndpoints(id, endpoints); err != nil {
				glog.Errorf("Error handling update event: %v", err)
			}
		}
	}
	return actionJobs
}

func main() {
	// parse flags
	flag.Parse()
	err := flagutil.SetFlagsFromEnv(flag.CommandLine, "K2C")
	if err != nil {
		glog.Fatalf("Cannot set flags from env: %v", err)
	}

	if opts.version {
		fmt.Println(kube2consulVersion)
		os.Exit(0)
	}

	glog.Info("Starting kube2consul ", kube2consulVersion)

	// create consul client
	consulClient, err := newConsulClient(opts.consulAPI, opts.consulToken)
	if err != nil {
		glog.Fatalf("Failed to create a consul client: %v", err)
	}

	// create kubernetes client
	kubeClient, err := newKubeClient(opts.kubeAPI, opts.kubeConfig)
	if err != nil {
		glog.Fatalf("Failed to create a kubernetes client: %v", err)
	}

	if !opts.noHealth {
		health.RegisterPeriodicThresholdFunc("consul", time.Second*5, 3, consulCheck)
		health.RegisterPeriodicThresholdFunc("kubernetes", time.Second*5, 3, kubernetesCheck)
		go func() {
			// create http server to expose health status
			r := mux.NewRouter()
			r.HandleFunc("/health", health.StatusHandler)
			srv := &http.Server{
				Handler:     r,
				Addr:        "0.0.0.0:8080",
				ReadTimeout: 15 * time.Second,
			}
			glog.Fatal(srv.ListenAndServe())
		}()
	}

	if opts.lock {
		glog.Info("Attempting to acquire lock")
		lock, err = consulClient.LockKey(opts.lockKey)
		if err != nil {
			glog.Fatalf("Lock setup failed :%v", err)
		}
		stopCh := make(chan struct{})
		lockCh, err = lock.Lock(stopCh)
		if err != nil {
			glog.Fatalf("Failed acquiring lock: %v", err)
		}
		glog.Info("Lock acquired")
	}

	jobQueue = make(chan concurrent.Action)
	defer close(jobQueue)

	k2c := kube2consul{
		consulCatalog: consulClient.Catalog(),
	}

	k2c.endpointsStore = k2c.watchEndpoints(kubeClient)

	_, _, stopWorkers := concurrent.RunWorkers(jobQueue, initJobFunctions(k2c), opts.jobNumber)

	defer stopWorkers()

	// Handle SIGINT and SIGTERM.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	for {
		select {
		case <-time.NewTicker(time.Duration(opts.resyncPeriod) * time.Second).C:
			if !resyncAlreadyRunning {
				resyncAlreadyRunning = true
				go func() {
					k2c.resync(0, kubeClient)
					resyncAlreadyRunning = false
				}()
			}
		case <-lockCh:
			glog.Fatalf("Lost lock, Exting")
		case sig := <-sigs:
			glog.Infof("Recieved signal: %v", sig)
			if opts.lock {
				// Release the lock before termination
				glog.Infof("Attempting to release lock")
				if err := lock.Unlock(); err != nil {
					glog.Errorf("Lock release failed : %s", err)
					os.Exit(1)
				}
				glog.Infof("Lock released")
				// Cleanup the lock if no longer in use
				glog.Infof("Cleaning lock entry")
				if err := lock.Destroy(); err != nil {
					if err != consulapi.ErrLockInUse {
						glog.Errorf("Lock cleanup failed: %s", err)
						os.Exit(1)
					} else {
						glog.Infof("Cleanup aborted, lock in use")
					}
				} else {
					glog.Infof("Cleanup succeeded")
				}
				glog.Infof("Exiting")
			}
			os.Exit(0)
		}
	}
}
