API Server Metrics Quantifier
=============================

Small program that scrapes prometheus metrics for an OpenShift API Server
and quantifies the total amount of requests being made by different clients.

## Requirements / Setup

In order to use this program, you must have an OpenShift proxy serving traffic on port `8080`

```
oc proxy -p 8080
```

## Building

```
make
```

## Running

```
./bin/quant
```

You can view results by going to [http://localhost:8000]().
Results will consist of multiple sections, one section per client making requests to your API server.
Each section will include:
- The client's name
- Total number of requests made by that client so far
- Resources being requested by the client
- and a list HTTP verbs used by the client
