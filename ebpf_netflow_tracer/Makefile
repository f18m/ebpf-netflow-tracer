NS:=kube-system

list-probes:
	sudo bpftrace -l tracepoint:* | less

run:
	sudo bpftrace ./netflow_tracer.bt

# important: deploy the probe first and then the ncat/tcp-echo binaries otherwise
# the TCP connect/accept will be lost
apply:
	kubectl apply -f ./tracer.yaml
	sleep 4
	kubectl apply -f ./traffic-echo.yaml
	kubectl apply -f ./traffic-sink.yaml
	$(MAKE) show-logs

show-logs:
	kubectl logs -n $(NS) -l app=bpftrace  --tail=-1 -f

save-logs:
	kubectl logs -n $(NS) -l app=bpftrace  --tail=-1 >output.trace

delete:
	kubectl delete -f ./tracer.yaml
	kubectl delete -f ./traffic-echo.yaml
	kubectl delete -f ./traffic-sink.yaml

check-server:
	sudo nsenter -t $(shell pgrep tcp-echo) --net  -- netstat -lpn

check-client:
	sudo nsenter -t $(shell pgrep tcp-) --net  -- netstat -lpn


show-bpf-programs:
	sudo bpftool prog list

check-if-kprobes-still-attached:
	sudo bpftool prog list | grep tcp

check-if-jit-enabled:
	sysctl net.core.bpf_jit_enable
