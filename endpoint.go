package main

import (
	"fmt"
	"strconv"

	"github.com/golang/glog"
	"k8s.io/api/core/v1"
)

// Endpoint is a summary of kubernetes endpoint
type Endpoint struct {
	Name    string
	Address string
	Port    int32
	RefName string
	Tags    []string
}

// NewEndpoint allows to create Endpoint
func NewEndpoint(name, address string, port int32, refName string, tags []string) Endpoint {
	return Endpoint{name, address, port, refName, tags}
}

func (k2c *kube2consul) generateEntries(id int, endpoint *v1.Endpoints) ([]Endpoint, map[string][]Endpoint) {
	var (
		eps                 []Endpoint
		refName             string
		perServiceEndpoints = make(map[string][]Endpoint)
	)

	service := getService(endpoint)

	if service == nil {
		glog.V(2).Infof("[job: %d] %s ignoring endpoint %s no service available", id, endpoint.Name)
		return eps, perServiceEndpoints
	}

	initPerServiceEndpointsFromService(service, perServiceEndpoints)

	for _, subset := range endpoint.Subsets {
		for _, port := range subset.Ports {

			servicePort := strconv.Itoa((int)(port.Port))
			metadata, _ := serviceMetaData(endpoint, service, servicePort)

			ignore := mapDefault(metadata, "ignore", "")
			if ignore != "" {
				continue
			}

			serviceName := mapDefault(metadata, "name", "")
			if serviceName == "" {
				if opts.explicit {
					continue
				}
				serviceName = endpoint.Name
			}

			for _, addr := range subset.Addresses {
				if addr.TargetRef != nil {
					refName = addr.TargetRef.Name
				}
				newEndpoint := NewEndpoint(serviceName, addr.IP, port.Port, refName, tagsToArray(metadata["tags"]))
				eps = append(eps, newEndpoint)
				perServiceEndpoints[serviceName] = append(perServiceEndpoints[serviceName], newEndpoint)
			}
		}
	}

	return eps, perServiceEndpoints
}

func (k2c *kube2consul) updateEndpoints(id int, ep *v1.Endpoints) error {
	glog.V(2).Infof("[job: %d] %s >>>>>>>>>>>>>>>>>>>> updateEndpoints", id, ep.Name)
	endpoints, perServiceEndpoints := k2c.generateEntries(id, ep)

	glog.V(2).Infof("[job: %d] %s: perServiceEndpoints : %+v", id, ep.Name, perServiceEndpoints)
	for _, e := range endpoints {
		if err := k2c.registerEndpoint(id, e); err != nil {
			glog.V(2).Infof("[job: %d] %s <<<<<<<<<<<<<<<<<<<< updateEndpoints", id, ep.Name)
			return fmt.Errorf("Error updating endpoints %v: %v", ep.Name, err)
		}
	}

	for serviceName, e := range perServiceEndpoints {
		if err := k2c.removeDeletedEndpoints(id, serviceName, e); err != nil {
			glog.V(2).Infof("[job: %d] %s <<<<<<<<<<<<<<<<<<<< updateEndpoints", id, ep.Name)
			return fmt.Errorf("Error removing possible deleted endpoints: %v: %v", serviceName, err)
		}
	}
	glog.V(2).Infof("[job: %d] %s <<<<<<<<<<<<<<<<<<<< updateEndpoints", id, ep.Name)
	return nil
}
