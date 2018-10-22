package main

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
)

const (
	quantAddr = "0.0.0.0"
	quantPort = "8000"

	metricsAddr = "localhost"
	metricsPort = "8080"
)

type quantReqHandler struct {
}

func (h *quantReqHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	w.WriteHeader(http.StatusOK)

	quantifiedData := quantify(data)
	sort.Sort(quantifiedData)
	for _, req := range quantifiedData {
		fmt.Fprintf(w, "%s\n", req.String())
	}
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
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%s", quantAddr, quantPort),
		Handler: &quantReqHandler{},
	}

	if err := server.ListenAndServe(); err != nil {
		panic(err)
	}
}
