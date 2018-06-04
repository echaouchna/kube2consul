package main

import (
	"fmt"
	"strconv"

	"k8s.io/client-go/pkg/api/v1"
)

// Endpoint is a summary of kubernetes endpoint
type Endpoint struct {
	Name    string
	Address string
	Port    int32
	RefName string
	Tags    []string
}

var (
	serviceNameSuffix = "NAME"
	servicePrefix     = "SERVICE"
	ignoreSuffix      = "IGNORE"
	separator         = "_"
)

// NewEndpoint allows to create Endpoint
func NewEndpoint(name, address string, port int32, refName string, tags []string) Endpoint {
	return Endpoint{name, address, port, refName, tags}
}

func (k2c *kube2consul) generateEntries(endpoint *v1.Endpoints) ([]Endpoint, map[string][]Endpoint) {
	var (
		eps                 []Endpoint
		refName             string
		perServiceEndpoints = make(map[string][]Endpoint)
	)

	for _, subset := range endpoint.Subsets {
		for _, port := range subset.Ports {
			servicePort := strconv.Itoa((int)(port.Port))
			metadata, _ := serviceMetaData(endpoint, servicePort)

			ignore := mapDefault(metadata, "ignore", "")
			if ignore != "" {
				continue
			}
			delete(metadata, "ignore")

			serviceName := mapDefault(metadata, "name", "")
			if serviceName == "" {
				if opts.explicit {
					continue
				}
				serviceName = endpoint.Name
			}
			delete(metadata, "name")

			labels := parseLabels(metadata)

			fmt.Println(serviceName)

			for _, addr := range subset.Addresses {
				if addr.TargetRef != nil {
					refName = addr.TargetRef.Name
				}
				newEndpoint := NewEndpoint(serviceName, addr.IP, port.Port, refName, labels)
				eps = append(eps, newEndpoint)
				perServiceEndpoints[serviceName] = append(perServiceEndpoints[serviceName], newEndpoint)
			}
		}
	}

	return eps, perServiceEndpoints
}

func (k2c *kube2consul) updateEndpoints(ep *v1.Endpoints) error {
	endpoints, perServiceEndpoints := k2c.generateEntries(ep)
	for _, e := range endpoints {
		if err := k2c.registerEndpoint(e); err != nil {
			return fmt.Errorf("Error updating endpoints %v: %v", ep.Name, err)
		}
	}

	for serviceName, e := range perServiceEndpoints {
		if err := k2c.removeDeletedEndpoints(serviceName, e); err != nil {
			return fmt.Errorf("Error removing possible deleted endpoints: %v: %v", serviceName, err)
		}
	}
	return nil
}
