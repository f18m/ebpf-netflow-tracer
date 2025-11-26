run:
	cat ../ebpf_netflow_tracer/output.trace |go run main.go

dot:
	cat ../ebpf_netflow_tracer/output.trace |go run main.go | dot -Tpng -o graph.png
