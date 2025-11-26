package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"regexp"
	"slices"
	"strconv"

	"github.com/emicklei/dot"
)

type Direction int

const (
	Remote2Local Direction = iota
	Local2Remote
)

// InputLine represents 1 line in the input of tcp_correlator, which is the output of tcp_tracer
type InputLine struct {
	Dir         Direction
	RemoteIP    string // TODO: use net.IP instead
	RemotePort  int
	LocalIP     string // TODO: use net.IP instead
	LocalPort   int
	ProcessID   int64
	ProcessName string
}

// NetworkEndpoint represents a generic IP:port pair, which is locally-relevant, i.e. is unique only within
// a particular container/POD assuming that IPs do not change over the container/POD lifetime
type NetworkEndpoint struct {
	IP   string
	Port int
}

// ProcessEndpoints collects all the endpoints (intended as IP:port pairs) that are exposed
// by the same process. This model assumes that:
//   - each Kube container exposes 1 network interface (no multi-networks via non-standard CNIs e.g. SRIOV)
//   - the network interface of each container has a single IP address assigned
//   - the IP address of the network interface of each container does not change during the lifetime of the PIDs
//     living inside the PODs and using the network
type ProcessEndpoints struct {
	ProcessID   int64
	ProcessName string
	LocalIP     string
	LocalPorts  []int
	DotNode     dot.Node
}

type ProcessEndpoint struct {
	PID  int64
	Port int
}

// Edge represents a uniquely-identified TCP connection between two processes
// (with the assumptions listed in ProcessEndpoints)
type Edge struct {
	Source ProcessEndpoint
	Dest   ProcessEndpoint
}

// Regex to parse lines
var regexLocalToRemote = regexp.MustCompile(`(.+):(\d+)<-(.+):(\d+)\|PID=(\d+) CMD=(.+)`)
var regexRemoteToLocal = regexp.MustCompile(`(.+):(\d+)->(.+):(\d+)\|PID=(\d+) CMD=(.+)`)

func parseLine(line string) (InputLine, error) {
	var ret InputLine
	var err error
	var matches []string

	if matches = regexLocalToRemote.FindStringSubmatch(line); len(matches) > 0 {
		// Parsed reverse direction
		ret.Dir = Local2Remote
	} else if matches = regexRemoteToLocal.FindStringSubmatch(line); len(matches) > 0 {
		// Parsed forward direction
		ret.Dir = Remote2Local
	} else {
		return InputLine{}, fmt.Errorf("skipping invalid line: %s", line)
	}

	ret.RemoteIP = matches[1]
	ret.RemotePort, err = strconv.Atoi(matches[2])
	if err != nil {
		return InputLine{}, fmt.Errorf("skipping invalid line: %s", line)
	}

	ret.LocalIP = matches[3]
	ret.LocalPort, err = strconv.Atoi(matches[4])
	if err != nil {
		return InputLine{}, fmt.Errorf("skipping invalid line: %s", line)
	}

	ret.ProcessID, err = strconv.ParseInt(matches[5], 10, 64)
	if err != nil {
		return InputLine{}, fmt.Errorf("skipping invalid line: %s", line)
	}

	ret.ProcessName = matches[6]

	return ret, nil
}

// IsValidLine checks if the local/remote IP addresses are worth showing in the DOT graph or not
// E.g. filters out anything that is on the 127.0.0.0/8 network
func IsValidLine(line InputLine) bool {
	if line.LocalPort == 0 || line.RemotePort == 0 {
		return false
	}

	localIP := net.ParseIP(line.LocalIP)
	remoteIP := net.ParseIP(line.RemoteIP)
	loopbackNet := net.IPNet{
		IP:   net.IPv4(127, 0, 0, 0),
		Mask: net.CIDRMask(8, 32),
	}
	if loopbackNet.Contains(localIP) || loopbackNet.Contains(remoteIP) {
		return false
	}

	if line.ProcessName == "k3s-server" {
		// k3s-server is SO chatty... skip any TCP connection landing or departing from it
		return false
	}

	return true
}

func createGraphFromStdin() (*dot.Graph, error) {
	// Create a new DOT graph
	graph := dot.NewGraph(dot.Directed)

	// Maps to store nodes and edges
	nodes := make(map[int64]ProcessEndpoints)         // PID -> Node
	knownEndpoints := make(map[NetworkEndpoint]int64) // Endpoint (IP:Port) -> PID
	edges := make(map[Edge]struct{})                  // Edge -> presence flag

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		parsedLine, err := parseLine(line)
		if err != nil {
			continue
		}

		// IP filter using net package
		if !IsValidLine(parsedLine) {
			//fmt.Printf("Skipping loopback IP line: %s\n", line)
			continue
		}

		// Create if the PID in this line is known or not
		n, pidIsKnown := nodes[parsedLine.ProcessID]
		if !pidIsKnown {
			// found a new process
			nodes[parsedLine.ProcessID] = ProcessEndpoints{
				ProcessID:   parsedLine.ProcessID,
				ProcessName: parsedLine.ProcessName,
				LocalIP:     parsedLine.LocalIP,
				LocalPorts:  []int{parsedLine.LocalPort},
				DotNode:     graph.Node(fmt.Sprintf("PID=%d\nName=%s\nIP=%s", parsedLine.ProcessID, parsedLine.ProcessName, parsedLine.LocalIP)),
			}
		} else {
			if n.LocalIP != parsedLine.LocalIP {
				panic(fmt.Sprintf("assumption not respected: %s %s", n.LocalIP, parsedLine.LocalIP))
			}
			if n.ProcessID != parsedLine.ProcessID {
				panic("logical bug??")
			}
			if n.ProcessName != parsedLine.ProcessName {
				panic("PID reuse??")
			}

			// should we enrich existing process?
			portIdx := slices.IndexFunc(n.LocalPorts, func(c int) bool { return c == parsedLine.LocalPort })
			if portIdx == -1 {
				// found a new exposed port
				n.LocalPorts = append(n.LocalPorts, parsedLine.LocalPort)
			} // else: port was already known... nothing to do

			// update map
			nodes[parsedLine.ProcessID] = n
		}

		// should we register the local endpoint to the local PID ?
		localEp := NetworkEndpoint{
			IP:   parsedLine.LocalIP,
			Port: parsedLine.LocalPort,
		}
		e, localEpIsKnown := knownEndpoints[localEp]
		if !localEpIsKnown {
			// Register the local endpoint in the list of known endpoints:
			knownEndpoints[localEp] = parsedLine.ProcessID
		} else {
			// already known... logical check:
			if e != parsedLine.ProcessID {
				panic("logical bug??")
			}
		}

		// If we know the PID listening on the remoteIP:remotePort endpoint,
		// we can draw an edge:
		remoteEp := NetworkEndpoint{
			IP:   parsedLine.RemoteIP,
			Port: parsedLine.RemotePort,
		}
		remotePID, isRemotePIDKnown := knownEndpoints[remoteEp]
		if isRemotePIDKnown {
			// we have all the info to build an edge
			edge := Edge{
				Source: ProcessEndpoint{
					PID:  parsedLine.ProcessID,
					Port: parsedLine.LocalPort,
				},
				Dest: ProcessEndpoint{
					PID:  remotePID,
					Port: parsedLine.RemotePort,
				},
			}
			if parsedLine.Dir == Remote2Local {
				// swap source/dest
				x := edge.Source
				edge.Source = edge.Dest
				edge.Dest = x
			}

			// is this edge a new one?
			if _, exists := edges[edge]; !exists {

				// this edge has not been drawn yet...
				sourceNode := nodes[edge.Source.PID]
				destNode := nodes[edge.Dest.PID]

				label := fmt.Sprintf("%s:%d->%s:%d", sourceNode.LocalIP, edge.Source.Port, destNode.LocalIP, edge.Dest.Port)
				sourceNode.DotNode.Edge(destNode.DotNode, label)
				edges[edge] = struct{}{}

				// register also the edge in the opposite direction

			}
		}
		//else:
		// due to the way the input feed is designed, we'll have a second chance
		// of drawing this edge later, typically in the next upcoming input line
		// which should normally contain local/remote endpoints swapped.
		// However it might happen that an edge does not get rendered because the
		// remote party never gets discovered (e.g. it's an endpoint of a node outside
		// kubernetes, e.g. in public internet, e.g. a remote image registry).
		// This case should be improved by drawing a node in the graph with IP:PORT populated and PID=?
	}

	// debug
	/*
		fmt.Printf("Found %d nodes:\n", len(nodes))
		for _, n := range nodes {
			fmt.Printf("%v\n", n)
		}
	*/

	return graph, nil
}

func main() {
	graph, err := createGraphFromStdin()
	if err != nil {
		panic(err) // TODO: exit gracefully instead of panicking
	}

	if _, err := os.Stdout.WriteString(graph.String()); err != nil {
		fmt.Printf("Error writing to stdout: %v\n", err)
	}
}
