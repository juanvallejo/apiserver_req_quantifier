package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

const (
	quantAddr = "0.0.0.0"
	quantPort = "8000"

	metricsAddr = "localhost"
	metricsPort = "8080"
)

type kubeconfig struct {
	CurrentContext string               `yaml:"current-context"`
	Contexts       []*kubeconfigContext `yaml:"contexts"`
	Clusters       []*kubeconfigCluster `yaml:"clusters"`
}

type kubeconfigCluster struct {
	Cluster *kubeconfigClusterInfo `yaml:"cluster"`
	Name    string                 `yaml:"name"`
}
type kubeconfigClusterInfo struct {
	Server string `yaml:"server"`
}

type kubeconfigContext struct {
	Context *kubeconfigContextInfo
	Name    string `yaml:"name"`
}
type kubeconfigContextInfo struct {
	Cluster string `yaml:"cluster"`
}

type uptimeResult struct {
	err    error
	result string
}

type quantReqHandler struct {
	kubeconfig string
}

func (h *quantReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// obtain master node uptime
	uptimeResultChan := make(chan *uptimeResult)
	go uptimeAsync(uptimeResultChan, h.kubeconfig)

	res, err := http.Get(fmt.Sprintf("http://%s:%s/metrics", metricsAddr, metricsPort))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "error: %v\n", err)
		return
	}

	data, err := ioutil.ReadAll(res.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "error: %v\n", err)
		return
	}

	w.WriteHeader(http.StatusPartialContent)

	quantifiedData := quantify(data)
	sort.Sort(quantifiedData)

	fmt.Fprintf(w, "[ %q ]\n", "INFO(not metrics): Master node uptime")

	// prepend uptime data (if any) to beginning of output
	select {
	case uptime := <-uptimeResultChan:
		if uptime.err != nil {
			fmt.Fprintf(w, "  - error fetching uptime info: %v\n", uptime.err)
			break
		}
		fmt.Fprintf(w, "  - %s\n", uptime.result)
	case <-time.After(500 * time.Millisecond):
		fmt.Fprintf(w, "  - %s\n", "timed out fetching uptime information")
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	fmt.Fprintln(w)

	for _, req := range quantifiedData {
		fmt.Fprintf(w, "%s\n", req.String())
	}
}

// uptimeAsync is meant to be called on a goroutine
// attempts to ssh into the master node and uptime system uptime information
func uptimeAsync(result chan *uptimeResult, configPath string) {
	configData, err := ioutil.ReadFile(configPath)
	if err != nil {
		result <- &uptimeResult{err: err}
		return
	}

	config := &kubeconfig{}
	if err := yaml.Unmarshal(configData, config); err != nil {
		result <- &uptimeResult{err: fmt.Errorf("error: unable to unmarshal provided KUBECONFIG: %v", err)}
		return
	}

	if len(config.CurrentContext) == 0 {
		result <- &uptimeResult{err: fmt.Errorf("invalid kubeconfig: empty current-context field")}
		return
	}
	if len(config.Contexts) == 0 {
		result <- &uptimeResult{err: fmt.Errorf("invalid kubeconfig: no contexts found")}
		return
	}
	if len(config.Clusters) == 0 {
		result <- &uptimeResult{err: fmt.Errorf("invalid kubeconfig: no clusters found")}
		return
	}

	var context *kubeconfigContext
	for _, ctx := range config.Contexts {
		if ctx.Name == config.CurrentContext {
			context = ctx
			break
		}
	}
	if context == nil {
		result <- &uptimeResult{err: fmt.Errorf("invalid kubeconfig: unable to find current context (%s) in provided list of contexts", config.CurrentContext)}
		return
	}

	clusterName := ""
	for _, cluster := range config.Clusters {
		if cluster.Name == context.Context.Cluster {
			clusterName = cluster.Cluster.Server
			break
		}
	}
	if len(clusterName) == 0 {
		result <- &uptimeResult{err: fmt.Errorf("invalid kubeconfig: unable to find current cluster (%s) in provided list of clusters", context.Context.Cluster)}
		return
	}

	hostSegs := strings.Split(clusterName, "://")
	if len(hostSegs) < 2 {
		result <- &uptimeResult{err: fmt.Errorf("malformed cluster hostname: expecting http(s)://host.name:port format, but got %s", clusterName)}
		return
	}
	hostSegs = strings.Split(hostSegs[1], ":")
	if len(hostSegs) == 0 {
		result <- &uptimeResult{err: fmt.Errorf("malformed cluster hostname: expecting http(s)://host.name:port format, but got %s", clusterName)}
		return
	}
	hostName := hostSegs[0]
	username := "core"
	stdout := bytes.NewBuffer(nil)
	stderr := bytes.NewBuffer(nil)

	cmd := exec.Command("/usr/bin/ssh", "-o", "UserKnownHostsFile=/dev/null", "-o", "StrictHostKeyChecking=no", fmt.Sprintf("%s@%s", username, hostName), "uptime", "--pretty")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		result <- &uptimeResult{err: fmt.Errorf("ssh error: %v: %v", err, stderr.String())}
		return
	}
	if len(stdout.String()) > 0 {
		result <- &uptimeResult{result: stdout.String()}
		return
	}
	if len(stderr.String()) > 0 {
		result <- &uptimeResult{err: fmt.Errorf("error: stderr: %s", stderr.String())}
		return
	}
	result <- &uptimeResult{err: fmt.Errorf("error: no output from command: %s", cmd.Args)}
}

type ApiserverReq struct {
	clientName    string
	totalReqCount int64
	resources     []string
	verbs         []string
}

func (r *ApiserverReq) String() string {
	s := fmt.Sprintf("[ %s ]\n", r.clientName)
	s += fmt.Sprintf("  - Total Requests: %v\n", r.totalReqCount)
	s += fmt.Sprintf("  - Resources: %v\n", r.resources)
	s += fmt.Sprintf("  - Verbs: %v\n", r.verbs)
	return s
}

type ApiserverReqList []*ApiserverReq

func (l ApiserverReqList) Swap(i, j int) {
	l[i], l[j] = l[j], l[i]
}

func (l ApiserverReqList) Less(i, j int) bool {
	return l[i].totalReqCount > l[j].totalReqCount
}

func (l ApiserverReqList) Len() int {
	return len(l)
}

// quantify receives prometheus metrics data as an array of bytes
// and measures apiserver_request_count
func quantify(data []byte) ApiserverReqList {
	// store total number of requests by client names
	reqs := map[string]*ApiserverReq{}
	shouldRecord := false
	idx := 0
	for _, line := range strings.Split(string(data), "\n") {
		idx++
		if strings.HasPrefix(line, "# TYPE") {
			continue
		}
		if strings.HasPrefix(line, "# HELP") {
			if shouldRecord {
				// if we have been recording, seeing this prefix means that
				// we have started a new section. No sense in continuing to record.
				break
			}
			shouldRecord = strings.HasPrefix(line, "# HELP apiserver_request_count")
			continue
		}
		if !shouldRecord {
			continue
		}

		req, err := parseLine(line)
		if req == nil || len(req.clientName) == 0 {
			fmt.Fprintf(os.Stderr, "error: malformed metrics line: %v: %v\n", line, err)
			continue
		}

		if seenReq, ok := reqs[req.clientName]; ok {
			seenReq.totalReqCount += req.totalReqCount
			seenReq.verbs = append(seenReq.verbs, req.verbs...)
			seenReq.resources = append(seenReq.resources, req.resources...)
			continue
		}
		reqs[req.clientName] = req
	}

	reqsList := []*ApiserverReq{}
	for _, v := range reqs {
		reqsList = append(reqsList, v)
	}
	return reqsList
}

func parseLine(line string) (*ApiserverReq, error) {
	req := &ApiserverReq{}
	objEnd := strings.Split(line, "}")
	if len(objEnd) == 0 {
		return nil, fmt.Errorf("missing metrics object delimiter '}'")
	}
	if len(objEnd) >= 2 {
		count, err := strconv.ParseInt(strings.TrimSpace(objEnd[1]), 10, 32)
		if err != nil {
			return nil, err
		}
		req.totalReqCount = count
	}
	objBegin := strings.Split(objEnd[0], "{")
	if len(objBegin) < 2 {
		return nil, fmt.Errorf("missing metrics object delimiter '{'")
	}
	fullObjFields := strings.Split(objBegin[1], ",")

	for _, field := range fullObjFields {
		segs := strings.Split(field, "=")
		if len(segs) < 2 {
			continue
		}
		key := segs[0]
		val := segs[1]
		switch key {
		case "client":
			req.clientName = val
		case "resource":
			req.resources = []string{val}
		case "verb":
			req.verbs = []string{val}
		}
	}

	return req, nil
}

func main() {
	handler := &quantReqHandler{}

	flag.StringVar(&handler.kubeconfig, "kubeconfig", "", "Absolute path to the kubeconfig generated by the OpenShift installer")
	if len(handler.kubeconfig) == 0 {
		handler.kubeconfig = os.Getenv("KUBECONFIG")
	}
	if len(handler.kubeconfig) == 0 {
		panic("A --kubeconfig location must be specified.")
	}

	flag.Parse()

	server := http.Server{
		Addr:    fmt.Sprintf("%s:%s", quantAddr, quantPort),
		Handler: handler,
	}

	fmt.Printf("Listening at %s on port %s...\n", quantAddr, quantPort)
	fmt.Printf("Scraping Prometheus metrics at %s on port %s...\n", metricsAddr, metricsPort)
	fmt.Printf("Using KUBECONFIG file: %s\n", handler.kubeconfig)

	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}
